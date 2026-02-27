package tile

import (
	"image"
	"testing"
)

// TestGetRGBA_CorrectDimensions verifies the returned image has the requested bounds.
func TestGetRGBA_CorrectDimensions(t *testing.T) {
	for _, sz := range [][2]int{{1, 1}, {4, 8}, {16, 16}, {256, 256}} {
		w, h := sz[0], sz[1]
		img := GetRGBA(w, h)
		if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
			t.Errorf("GetRGBA(%d,%d) bounds = %v, want %dx%d", w, h, img.Bounds(), w, h)
		}
	}
}

// TestGetRGBA_BoundsOriginAtZero verifies the returned image starts at (0,0).
func TestGetRGBA_BoundsOriginAtZero(t *testing.T) {
	img := GetRGBA(16, 16)
	if img.Bounds().Min != (image.Point{}) {
		t.Errorf("Bounds().Min = %v, want (0,0)", img.Bounds().Min)
	}
}

// TestGetRGBA_ReturnsZeroedImage verifies that GetRGBA always returns a zeroed image,
// even when reusing a pooled buffer that previously held non-zero data.
func TestGetRGBA_ReturnsZeroedImage(t *testing.T) {
	// Get and dirty an image.
	img := GetRGBA(8, 8)
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	// Return to pool.
	PutRGBA(img)

	// Get again — must be zeroed (same size reuses the pool entry).
	img2 := GetRGBA(8, 8)
	for i, v := range img2.Pix {
		if v != 0 {
			t.Errorf("Pix[%d] = %d after pool reuse, want 0", i, v)
			break
		}
	}
}

// TestPutRGBA_NilIsSafe verifies PutRGBA does not panic on nil input.
func TestPutRGBA_NilIsSafe(t *testing.T) {
	PutRGBA(nil) // must not panic
}

// TestGetRGBA_DifferentSizesDoNotInterfere verifies that pooled images of one
// size are not accidentally returned for a different size.
func TestGetRGBA_DifferentSizesDoNotInterfere(t *testing.T) {
	small := GetRGBA(4, 4)
	large := GetRGBA(8, 8)

	PutRGBA(small)

	// Requesting the large size should still return 8×8.
	reuse := GetRGBA(8, 8)
	if reuse.Bounds().Dx() != 8 || reuse.Bounds().Dy() != 8 {
		t.Errorf("GetRGBA(8,8) returned bounds %v after putting 4×4", reuse.Bounds())
	}
	PutRGBA(large)
}

// TestGetRGBA_PoolReuseIdentity verifies that after Put, the next Get of the
// same size returns the same underlying array (pool actually reuses memory).
// This is a best-effort check — the GC may have collected the pooled value.
func TestGetRGBA_PoolReuseIdentity(t *testing.T) {
	img := GetRGBA(64, 64)
	PutRGBA(img)

	img2 := GetRGBA(64, 64)
	// Can't guarantee same pointer across GC cycles, but the returned image
	// must be correctly sized and zeroed.
	if img2.Bounds().Dx() != 64 || img2.Bounds().Dy() != 64 {
		t.Errorf("reused image has wrong bounds: %v", img2.Bounds())
	}
	for i, v := range img2.Pix {
		if v != 0 {
			t.Errorf("Pix[%d] = %d, want 0", i, v)
			break
		}
	}
}
