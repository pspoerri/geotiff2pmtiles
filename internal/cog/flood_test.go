package cog

import "testing"

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
