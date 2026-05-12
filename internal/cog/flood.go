package cog

import (
	"fmt"
	"image"
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

// BuildFloodMask scans the source at the configured nodata tolerance, then
// flood-fills from the four image edges through near-nodata pixels. Only the
// connected component(s) of "border-like" pixels reachable from the COG
// boundary are recorded as transparent — interior dark pixels (forest canopy,
// text, shadows) that happen to fall within tolerance are left opaque.
//
// Memory: 2 packed bitmaps of (Width×Height) bits during construction; one
// stays on the Reader afterwards. For a 24081×18046 image that's ~54 MB
// resident.
//
// Must be called after SetBandConfig with HasNodata=true and before
// concurrent ReadTile traffic.
func (r *Reader) BuildFloodMask() error {
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

	candidate := newBitmap(W * H)
	flood := newBitmap(W * H)

	// Pass 1: decode each source tile and mark "near-nodata" pixels in
	// `candidate`. Decoding is done with HasNodata temporarily disabled so the
	// raw RGB values reach us untouched — we then apply the tolerance check
	// ourselves at the output (post-band-reorder) level.
	saved := r.bandCfg
	r.bandCfg.HasNodata = false
	r.floodMask = nil // make sure decode paths don't try to consult a partial mask

	nd := int(saved.Nodata)
	tol := int(saved.NodataTolerance)
	if tol < 0 {
		tol = 0
	}

	for tr := 0; tr < tilesDown; tr++ {
		for tc := 0; tc < tilesAcross; tc++ {
			img, err := r.ReadTile(0, tc, tr)
			if err != nil {
				r.bandCfg = saved
				return fmt.Errorf("BuildFloodMask: tile (%d,%d): %w", tc, tr, err)
			}
			rgba := toRGBA(img)
			pix := rgba.Pix
			yLim := th
			if tr*th+th > H {
				yLim = H - tr*th
			}
			xLim := tw
			if tc*tw+tw > W {
				xLim = W - tc*tw
			}
			for y := 0; y < yLim; y++ {
				py := tr*th + y
				rowOff := py * W
				rowPix := y * rgba.Stride
				for x := 0; x < xLim; x++ {
					i := rowPix + x*4
					if absDiff(int(pix[i+0]), nd) <= tol &&
						absDiff(int(pix[i+1]), nd) <= tol &&
						absDiff(int(pix[i+2]), nd) <= tol {
						candidate.set(rowOff + tc*tw + x)
					}
				}
			}
		}
	}

	r.bandCfg = saved

	// Pass 2: scanline flood fill from the four image edges through `candidate`.
	scanlineFlood(W, H, candidate, flood)

	r.floodMask = &flood
	r.floodMaskW = W
	r.floodMaskH = H
	return nil
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
func (r *Reader) applyFloodMaskRGBA(rgba *image.RGBA, tileX0, tileY0, tw, th int) {
	if r.floodMask == nil {
		return
	}
	W := r.floodMaskW
	H := r.floodMaskH
	pix := rgba.Pix
	for y := 0; y < th; y++ {
		py := tileY0 + y
		if py < 0 || py >= H {
			continue
		}
		rowOff := py * W
		rowPix := y * rgba.Stride
		for x := 0; x < tw; x++ {
			px := tileX0 + x
			if px < 0 || px >= W {
				continue
			}
			if r.floodMask.get(rowOff + px) {
				pix[rowPix+x*4+3] = 0
			}
		}
	}
}
