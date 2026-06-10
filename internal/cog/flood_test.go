package cog

import (
	"image"
	"sync"
	"testing"
)

// TestScanlineFloodReachable: a candidate set that's a U-shape touching the
// top edge should flood entirely; a separately-disconnected interior region
// (a 1-pixel speckle in the middle) must NOT be flooded.
func TestScanlineFloodReachable(t *testing.T) {
	W, H := 8, 8
	cand := newBitmap(W * H)
	// Top-edge connected "U" along row 0..2.
	for x := 0; x < W; x++ {
		cand.set(0*W + x) // row 0 — all candidates, all on the boundary
	}
	cand.set(1*W + 0) // left arm of U
	cand.set(2*W + 0)
	cand.set(1*W + W - 1) // right arm
	cand.set(2*W + W - 1)

	// Interior speckle, disconnected from the boundary.
	cand.set(4*W + 4)

	flood := newBitmap(W * H)
	scanlineFlood(W, H, cand, flood)

	// Top row + arms should all be flooded.
	mustReach := []int{0*W + 0, 0*W + 7, 1*W + 0, 2*W + 0, 1*W + 7, 2*W + 7}
	for _, idx := range mustReach {
		if !flood.get(idx) {
			t.Errorf("expected idx %d reached, was not", idx)
		}
	}
	// Interior speckle must NOT be reached.
	if flood.get(4*W + 4) {
		t.Errorf("interior speckle at (4,4) was flooded; should be isolated")
	}
	// Non-candidate cells (e.g. middle of row 3) must not be reached.
	if flood.get(3*W + 3) {
		t.Errorf("non-candidate cell (3,3) was flooded")
	}
}

// TestScanlineFloodFullBorder: a full ring of candidates around the boundary
// surrounding an interior of non-candidates. The ring should flood; the
// interior should stay empty.
func TestScanlineFloodFullBorder(t *testing.T) {
	W, H := 6, 6
	cand := newBitmap(W * H)
	for x := 0; x < W; x++ {
		cand.set(0*W + x)
		cand.set((H-1)*W + x)
	}
	for y := 0; y < H; y++ {
		cand.set(y*W + 0)
		cand.set(y*W + W - 1)
	}
	flood := newBitmap(W * H)
	scanlineFlood(W, H, cand, flood)

	for x := 0; x < W; x++ {
		if !flood.get(0*W+x) || !flood.get((H-1)*W+x) {
			t.Errorf("border row not flooded at x=%d", x)
		}
	}
	for y := 1; y < H-1; y++ {
		for x := 1; x < W-1; x++ {
			if flood.get(y*W + x) {
				t.Errorf("interior pixel (%d,%d) flooded but isn't a candidate", x, y)
			}
		}
	}
}

// TestBitmapSetGet covers the bit-packing helper for off-by-one issues at
// uint64 word boundaries.
func TestBitmapSetGet(t *testing.T) {
	for _, n := range []int{1, 63, 64, 65, 127, 128, 129, 1000} {
		bm := newBitmap(n)
		for i := 0; i < n; i++ {
			if bm.get(i) {
				t.Fatalf("n=%d: fresh bit %d already set", n, i)
			}
		}
		// Set every 3rd bit.
		for i := 0; i < n; i += 3 {
			bm.set(i)
		}
		for i := 0; i < n; i++ {
			want := i%3 == 0
			if got := bm.get(i); got != want {
				t.Errorf("n=%d, i=%d: got %v, want %v", n, i, got, want)
			}
		}
	}
}

// newOpaqueRGBA returns a w×h RGBA image with every pixel fully opaque white.
func newOpaqueRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

// TestApplyFloodMaskRGBA exercises the word-at-a-time mask application,
// including bit runs that straddle uint64 word boundaries and tiles that
// extend past the right/bottom image edge (clamping).
func TestApplyFloodMaskRGBA(t *testing.T) {
	W, H := 100, 8 // W deliberately not a multiple of 64
	mask := newBitmap(W * H)
	// Row 2: bits 60..70 — crosses the word boundary at bit 64 of the row-2
	// bit range (absolute bits 260..270, word boundary at 256).
	for x := 60; x <= 70; x++ {
		mask.set(2*W + x)
	}
	// Row 5: single bit at the last column.
	mask.set(5*W + 99)

	r := &Reader{floodMask: &mask, floodMaskW: W, floodMaskH: H}

	// Tile of 70×8 at x offset 40: extends to x=110, past W=100 → clamped.
	tw, th := 70, 8
	rgba := newOpaqueRGBA(tw, th)
	r.applyFloodMaskRGBA(rgba, 40, 0, tw, th)

	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			px, py := 40+x, y
			wantTransparent := px < W && ((py == 2 && px >= 60 && px <= 70) || (py == 5 && px == 99))
			gotAlpha := rgba.Pix[y*rgba.Stride+x*4+3]
			if wantTransparent && gotAlpha != 0 {
				t.Errorf("pixel (%d,%d) [src (%d,%d)]: alpha=%d, want 0", x, y, px, py, gotAlpha)
			}
			if !wantTransparent && gotAlpha != 255 {
				t.Errorf("pixel (%d,%d) [src (%d,%d)]: alpha=%d, want 255", x, y, px, py, gotAlpha)
			}
		}
	}
}

// TestApplyFloodMaskRGBAScaled verifies that the level-0 mask carries through
// to an overview read via center-of-footprint sampling.
func TestApplyFloodMaskRGBAScaled(t *testing.T) {
	W, H := 8, 8 // level-0 dimensions
	mask := newBitmap(W * H)
	// Transparent left half of the source.
	for y := 0; y < H; y++ {
		for x := 0; x < W/2; x++ {
			mask.set(y*W + x)
		}
	}
	r := &Reader{floodMask: &mask, floodMaskW: W, floodMaskH: H}

	// 2:1 overview: 4×4. Overview pixel x maps to source center (2x+0.5)*2 ≈ 2x.
	levelW, levelH := 4, 4
	rgba := newOpaqueRGBA(levelW, levelH)
	r.applyFloodMaskRGBAScaled(rgba, levelW, levelH, 0, 0, levelW, levelH)

	for y := 0; y < levelH; y++ {
		for x := 0; x < levelW; x++ {
			gotAlpha := rgba.Pix[y*rgba.Stride+x*4+3]
			wantTransparent := x < levelW/2 // left half of overview ← left half of source
			if wantTransparent && gotAlpha != 0 {
				t.Errorf("overview pixel (%d,%d): alpha=%d, want 0", x, y, gotAlpha)
			}
			if !wantTransparent && gotAlpha != 255 {
				t.Errorf("overview pixel (%d,%d): alpha=%d, want 255", x, y, gotAlpha)
			}
		}
	}
}

// TestMarkTileCandidatesConcurrent marks two horizontally adjacent tiles from
// two goroutines. Their bit ranges share uint64 words (W=100 is not a multiple
// of 64), so this fails under -race unless the word merge is atomic.
func TestMarkTileCandidatesConcurrent(t *testing.T) {
	W, H := 100, 4
	tw := 50
	cand := newBitmap(W * H)

	mkTile := func(allNodata bool) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, tw, H))
		for i := 0; i < len(img.Pix); i += 4 {
			v := uint8(200)
			if allNodata {
				v = 0
			}
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 255
		}
		return img
	}

	var wg sync.WaitGroup
	for tc := 0; tc < 2; tc++ {
		wg.Add(1)
		go func(tc int) {
			defer wg.Done()
			markTileCandidates(cand, mkTile(true), W, tc*tw, 0, tw, H, 0, 2)
		}(tc)
	}
	wg.Wait()

	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if !cand.get(y*W + x) {
				t.Fatalf("candidate bit (%d,%d) not set", x, y)
			}
		}
	}
}

// TestMarkTileCandidatesTolerance checks the tolerance window and that
// non-matching pixels stay unset.
func TestMarkTileCandidatesTolerance(t *testing.T) {
	W, H := 16, 1
	cand := newBitmap(W * H)
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for x := 0; x < W; x++ {
		v := uint8(x) // 0..15
		i := x * 4
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 255
	}
	markTileCandidates(cand, img, W, 0, 0, W, H, 0, 5)
	for x := 0; x < W; x++ {
		want := x <= 5
		if got := cand.get(x); got != want {
			t.Errorf("x=%d (value %d, tol 5): candidate=%v, want %v", x, x, got, want)
		}
	}
}
