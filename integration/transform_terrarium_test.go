package integration_test

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"path/filepath"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// TestTransformRebuildTerrariumBoundary rebuilds a synthetic terrarium
// archive whose elevations straddle a 256 m channel boundary — the case
// where per-channel RGBA downsampling corrupts values (averaging G=255 and
// G=0 gives ~127 instead of carrying into R). A checkerboard of 255.5 m and
// 256.5 m must downsample to 256 m, which only works in elevation space.
// It also asserts the "encoding" metadata survives the transform, since
// auto-detection depends on it.
func TestTransformRebuildTerrariumBoundary(t *testing.T) {
	const lo, hi = 255.5, 256.5

	// Build a z1 archive covering the world: 4 tiles of a lo/hi checkerboard.
	srcPath := filepath.Join(t.TempDir(), "terrarium-src.pmtiles")
	writer, err := pmtiles.NewWriter(srcPath, pmtiles.WriterOptions{
		MinZoom:    1,
		MaxZoom:    1,
		TileSize:   256,
		Bounds:     cog.Bounds{MinLon: -180, MinLat: -85, MaxLon: 180, MaxLat: 85},
		TileFormat: pmtiles.TileTypePNG,
		Encoding:   "terrarium",
	})
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			elev := lo
			if (x+y)%2 == 1 {
				elev = hi
			}
			img.SetRGBA(x, y, encode.ElevationToTerrarium(elev))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	for _, xy := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		if err := writer.WriteTile(1, xy[0], xy[1], buf.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Finalize(); err != nil {
		t.Fatal(err)
	}

	// Rebuild down to z0. runTransform auto-detects terrarium from metadata.
	outPath := runTransform(t, transformConfig{
		InputPath: srcPath,
		MinZoom:   0,
		MaxZoom:   1,
		Rebuild:   true,
	})

	// Every z0 pixel averages a 2x2 checkerboard block → exactly 256 m.
	got := terrariumElevationAt(t, outPath, 0, 0, 0)
	if math.Abs(got-256.0) > 1 {
		t.Errorf("rebuilt z0 elevation: got %.2f m, want 256.0 m ±1 (per-channel RGBA downsampling?)", got)
	}

	// The encoding marker must survive so downstream transforms keep working.
	r, err := pmtiles.OpenReader(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	meta, err := r.ReadMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if enc, _ := meta["encoding"].(string); enc != "terrarium" {
		t.Errorf(`output metadata "encoding": got %q, want "terrarium"`, enc)
	}
}
