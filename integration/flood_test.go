package integration_test

import (
	"image"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

// TestBuildFloodMaskEndToEnd builds a flood mask on a synthetic multi-tile
// GeoTIFF with a near-nodata border ring and an interior near-nodata speckle.
// After the (parallel) build, ReadTile must return the ring transparent and
// the speckle opaque — the core promise of --nodata-flood.
func TestBuildFloodMaskEndToEnd(t *testing.T) {
	const (
		W, H   = 520, 520 // 3×3 tiles at 256px, right/bottom partials
		ring   = 10       // border ring width in pixels
		spX    = 260      // interior speckle, disconnected from the ring
		spY    = 260
		fillPx = 128
	)
	path := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: W, Height: H,
		SamplesPerPixel: 3,
		OriginLon:       7.0, OriginLat: 47.0,
		PixelSizeDeg: 0.0001,
		PixelFunc: func(x, y, band int) uint16 {
			onRing := x < ring || y < ring || x >= W-ring || y >= H-ring
			if onRing || (x == spX && y == spY) {
				return 2 // near-nodata (within tolerance of 0)
			}
			return fillPx
		},
	})

	r, err := cog.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	r.SetBandConfig(cog.BandConfig{HasNodata: true, Nodata: 0, NodataTolerance: 4})

	if err := r.BuildFloodMask(4); err != nil {
		t.Fatalf("BuildFloodMask: %v", err)
	}

	alphaAt := func(px, py int) uint32 {
		tile, err := r.ReadTile(0, px/256, py/256)
		if err != nil {
			t.Fatalf("ReadTile for (%d,%d): %v", px, py, err)
		}
		rgba, ok := tile.(*image.RGBA)
		if !ok {
			t.Fatalf("ReadTile returned %T, want *image.RGBA", tile)
		}
		return uint32(rgba.Pix[(py%256)*rgba.Stride+(px%256)*4+3])
	}

	// Border ring: edge-reachable → transparent. Sample all four sides,
	// including the partial right/bottom tiles.
	ringSamples := [][2]int{{0, 0}, {5, 5}, {W - 1, 0}, {W - 3, H - 3}, {0, H - 1}, {300, 4}, {W - 2, 300}}
	for _, s := range ringSamples {
		if a := alphaAt(s[0], s[1]); a != 0 {
			t.Errorf("ring pixel (%d,%d): alpha=%d, want 0", s[0], s[1], a)
		}
	}
	// Interior speckle: within tolerance but not edge-reachable → opaque.
	if a := alphaAt(spX, spY); a == 0 {
		t.Errorf("interior speckle (%d,%d) was made transparent; flood fill should not reach it", spX, spY)
	}
	// Regular interior content: opaque.
	if a := alphaAt(100, 100); a == 0 {
		t.Errorf("interior pixel (100,100) was made transparent")
	}
}
