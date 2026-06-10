package cog

import (
	"fmt"
	"image"
	mathbits "math/bits"
	"runtime"
	"sync"
	"sync/atomic"
)

// bitmap is a 1-bit-per-element packed bitmap over [0, n).
type bitmap struct {
	bits []uint64
	n    int
}

func newBitmap(n int) bitmap {
	return bitmap{bits: make([]uint64, (n+63)>>6), n: n}
}

func (b bitmap) set(i int)      { b.bits[i>>6] |= 1 << (uint(i) & 63) }
func (b bitmap) get(i int) bool { return b.bits[i>>6]&(1<<(uint(i)&63)) != 0 }

// orWordAtomic ORs a pre-assembled 64-bit chunk into word w. Safe for
// concurrent use by multiple writers touching overlapping words.
func (b bitmap) orWordAtomic(w int, bits uint64) {
	if bits != 0 {
		atomic.OrUint64(&b.bits[w], bits)
	}
}

// BuildFloodMask scans the source at the configured nodata tolerance, then
// flood-fills from the four image edges through near-nodata pixels. Only the
// connected component(s) of "border-like" pixels reachable from the COG
// boundary are recorded as transparent — interior dark pixels (forest canopy,
// text, shadows) that happen to fall within tolerance are left opaque.
//
// Pass 1 (tile decode + tolerance matching, the dominant cost) runs on
// `workers` goroutines; workers <= 0 uses GOMAXPROCS.
//
// Memory: 2 packed bitmaps of (Width×Height) bits during construction; one
// stays on the Reader afterwards. For a 24081×18046 image that's ~54 MB
// resident (~108 MB peak during construction).
//
// Must be called after SetBandConfig with HasNodata=true and before
// concurrent ReadTile traffic.
func (r *Reader) BuildFloodMask(workers int) error {
	if !r.bandCfg.HasNodata {
		return fmt.Errorf("BuildFloodMask: BandConfig.HasNodata must be set")
	}
	ifd := &r.ifds[0]
	W := int(ifd.Width)
	H := int(ifd.Height)
	if W == 0 || H == 0 {
		return fmt.Errorf("BuildFloodMask: empty image (%dx%d)", W, H)
	}
	tw := int(ifd.TileWidth)
	th := int(ifd.TileHeight)
	tilesAcross := ifd.TilesAcross()
	tilesDown := ifd.TilesDown()
	nTiles := tilesAcross * tilesDown

	candidate := newBitmap(W * H)
	flood := newBitmap(W * H)

	// Pass 1: decode each source tile and mark "near-nodata" pixels in
	// `candidate`. Decoding is done with HasNodata temporarily disabled so the
	// raw RGB values reach us untouched — we then apply the tolerance check
	// ourselves at the output (post-band-reorder) level. The bandCfg mutation
	// happens before any worker starts and is restored after all have stopped,
	// so workers always observe HasNodata=false.
	saved := r.bandCfg
	r.bandCfg.HasNodata = false
	r.floodMask = nil // make sure decode paths don't try to consult a partial mask

	nd := int(saved.Nodata)
	tol := int(saved.NodataTolerance)
	if tol < 0 {
		tol = 0
	}

	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > nTiles {
		workers = nTiles
	}

	var (
		next     atomic.Int64
		failed   atomic.Bool
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !failed.Load() {
				n := int(next.Add(1)) - 1
				if n >= nTiles {
					return
				}
				tr := n / tilesAcross
				tc := n % tilesAcross
				img, err := r.ReadTile(0, tc, tr)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("BuildFloodMask: tile (%d,%d): %w", tc, tr, err)
					}
					errMu.Unlock()
					failed.Store(true)
					return
				}
				yLim := th
				if tr*th+th > H {
					yLim = H - tr*th
				}
				xLim := tw
				if tc*tw+tw > W {
					xLim = W - tc*tw
				}
				markTileCandidates(candidate, toRGBA(img), W, tc*tw, tr*th, xLim, yLim, nd, tol)
			}
		}()
	}
	wg.Wait()

	r.bandCfg = saved
	if firstErr != nil {
		return firstErr
	}

	// Pass 2: scanline flood fill from the four image edges through `candidate`.
	scanlineFlood(W, H, candidate, flood)

	r.floodMask = &flood
	r.floodMaskW = W
	r.floodMaskH = H
	return nil
}

// markTileCandidates sets the candidate bit for every pixel of the decoded
// tile whose RGB channels all fall within tolerance of the nodata value.
// (x0,y0) is the tile's top-left corner in source coordinates; (xLim,yLim)
// its valid extent (clipped at the image edge). Bits are assembled into
// word-local accumulators and merged with atomic OR, so concurrent workers
// may safely touch adjacent tiles that share boundary words.
func markTileCandidates(candidate bitmap, rgba *image.RGBA, W, x0, y0, xLim, yLim, nd, tol int) {
	pix := rgba.Pix
	for y := 0; y < yLim; y++ {
		rowBit := (y0+y)*W + x0
		rowPix := y * rgba.Stride
		var acc uint64
		accW := -1
		for x := 0; x < xLim; x++ {
			i := rowPix + x*4
			if absDiff(int(pix[i+0]), nd) <= tol &&
				absDiff(int(pix[i+1]), nd) <= tol &&
				absDiff(int(pix[i+2]), nd) <= tol {
				idx := rowBit + x
				w := idx >> 6
				if w != accW {
					if accW >= 0 {
						candidate.orWordAtomic(accW, acc)
					}
					accW = w
					acc = 0
				}
				acc |= 1 << (uint(idx) & 63)
			}
		}
		if accW >= 0 {
			candidate.orWordAtomic(accW, acc)
		}
	}
}

// scanlineFlood does a 4-connected scanline flood fill. Pixels in `flood` are
// set iff they are in `candidate` AND reachable through `candidate` from the
// image boundary. Memory: queue grows with the number of distinct horizontal
// runs touched, not the filled area — typically O(perimeter).
func scanlineFlood(W, H int, candidate, flood bitmap) {
	type seed struct{ x, y int }
	queue := make([]seed, 0, 1024)

	push := func(x, y int) {
		if x < 0 || x >= W || y < 0 || y >= H {
			return
		}
		idx := y*W + x
		if !candidate.get(idx) || flood.get(idx) {
			return
		}
		queue = append(queue, seed{x, y})
	}

	// Seed: every boundary pixel that is a candidate.
	for x := 0; x < W; x++ {
		push(x, 0)
		push(x, H-1)
	}
	for y := 0; y < H; y++ {
		push(0, y)
		push(W-1, y)
	}

	for len(queue) > 0 {
		// pop
		s := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x, y := s.x, s.y
		idx := y*W + x
		if flood.get(idx) || !candidate.get(idx) {
			continue
		}
		// extend left
		lx := x
		for lx > 0 {
			i := y*W + lx - 1
			if !candidate.get(i) || flood.get(i) {
				break
			}
			lx--
		}
		// extend right + mark
		rx := lx
		// helper: was previous neighbor in the run direction also filled?
		prevAbove := false
		prevBelow := false
		for rx < W {
			i := y*W + rx
			if !candidate.get(i) || flood.get(i) {
				break
			}
			flood.set(i)
			// check above
			if y > 0 {
				ai := (y-1)*W + rx
				if candidate.get(ai) && !flood.get(ai) {
					if !prevAbove {
						queue = append(queue, seed{rx, y - 1})
					}
					prevAbove = true
				} else {
					prevAbove = false
				}
			}
			// check below
			if y < H-1 {
				bi := (y+1)*W + rx
				if candidate.get(bi) && !flood.get(bi) {
					if !prevBelow {
						queue = append(queue, seed{rx, y + 1})
					}
					prevBelow = true
				} else {
					prevBelow = false
				}
			}
			rx++
		}
	}
}

// applyFloodMaskRGBA zeroes alpha (keeping RGB) for pixels whose corresponding
// bit in the source-level flood mask is set. tileX0/tileY0 are the top-left
// source coordinates of the tile; tw/th are its decoded size in pixels.
//
// The mask is consumed a 64-pixel word at a time: all-zero words (the common
// case on interior tiles) are skipped with a single load, and set bits are
// visited via trailing-zeros iteration rather than a per-pixel branch.
func (r *Reader) applyFloodMaskRGBA(rgba *image.RGBA, tileX0, tileY0, tw, th int) {
	if r.floodMask == nil {
		return
	}
	W := r.floodMaskW
	H := r.floodMaskH
	x0, x1 := 0, tw
	if tileX0 < 0 {
		x0 = -tileX0
	}
	if tileX0+tw > W {
		x1 = W - tileX0
	}
	y0, y1 := 0, th
	if tileY0 < 0 {
		y0 = -tileY0
	}
	if tileY0+th > H {
		y1 = H - tileY0
	}
	if x0 >= x1 || y0 >= y1 {
		return
	}
	words := r.floodMask.bits
	pix := rgba.Pix
	for y := y0; y < y1; y++ {
		rowBit := (tileY0+y)*W + tileX0
		rowPix := y * rgba.Stride
		for x := x0; x < x1; {
			idx := rowBit + x
			off := uint(idx) & 63
			chunk := words[idx>>6] >> off
			n := 64 - int(off)
			if n > x1-x {
				n = x1 - x
			}
			if chunk != 0 {
				if n < 64 {
					chunk &= (uint64(1) << uint(n)) - 1
				}
				for chunk != 0 {
					i := mathbits.TrailingZeros64(chunk)
					pix[rowPix+(x+i)*4+3] = 0
					chunk &= chunk - 1
				}
			}
			x += n
		}
	}
}

// applyFloodMaskRGBAScaled applies the level-0 flood mask to a tile decoded
// from an overview IFD of dimensions levelW×levelH. Each overview pixel is
// tested against the mask bit at the center of its level-0 footprint
// (nearest-neighbor), so transparency carries through to overview reads.
// tileX0/tileY0 are the tile's top-left coordinates in overview pixels.
func (r *Reader) applyFloodMaskRGBAScaled(rgba *image.RGBA, levelW, levelH, tileX0, tileY0, tw, th int) {
	if r.floodMask == nil || levelW <= 0 || levelH <= 0 {
		return
	}
	W := r.floodMaskW
	H := r.floodMaskH
	x0, x1 := 0, tw
	if tileX0 < 0 {
		x0 = -tileX0
	}
	if tileX0+tw > levelW {
		x1 = levelW - tileX0
	}
	y0, y1 := 0, th
	if tileY0 < 0 {
		y0 = -tileY0
	}
	if tileY0+th > levelH {
		y1 = levelH - tileY0
	}
	if x0 >= x1 || y0 >= y1 {
		return
	}
	mask := r.floodMask
	pix := rgba.Pix
	for y := y0; y < y1; y++ {
		sy := ((tileY0+y)*2 + 1) * H / (2 * levelH)
		if sy >= H {
			sy = H - 1
		}
		rowOff := sy * W
		rowPix := y * rgba.Stride
		for x := x0; x < x1; x++ {
			sx := ((tileX0+x)*2 + 1) * W / (2 * levelW)
			if sx >= W {
				sx = W - 1
			}
			if mask.get(rowOff + sx) {
				pix[rowPix+x*4+3] = 0
			}
		}
	}
}
