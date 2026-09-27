package integration_test

import (
	"bytes"
	"image/color"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// readAllTiles returns every tile of a PMTiles archive by position.
func readAllTiles(t *testing.T, path string) map[[3]int][]byte {
	t.Helper()
	r, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tiles := map[[3]int][]byte{}
	for z := 0; z <= int(r.Header().MaxZoom); z++ {
		for _, p := range r.TilesAtZoom(z) {
			data, err := r.ReadTile(p[0], p[1], p[2])
			if err != nil {
				t.Fatal(err)
			}
			tiles[p] = data
		}
	}
	return tiles
}

// TestTransformModes runs runTransform, which picks its mode like
// pmtransform, over an archive with transparent pixels and missing tiles.
func TestTransformModes(t *testing.T) {
	// 0..40°E, 20..40°N; the western half (0..20°E) is nodata, so z5
	// column 16 (0..11.25°E) has no tiles and column 17 is half transparent.
	tiffPath := writeSyntheticGeoTIFF(t, tiffWriterConfig{
		Width: 400, Height: 200, SamplesPerPixel: 3,
		OriginLon: 0, OriginLat: 40, PixelSizeDeg: 0.1, NoData: "0",
		PixelFunc: func(x, y, band int) uint16 {
			if x < 200 {
				return 0
			}
			return 100
		},
	})
	srcPath := runPipeline(t, pipelineConfig{InputPaths: []string{tiffPath}, Format: "png", MinZoom: 4, MaxZoom: 5})
	src := readAllTiles(t, srcPath)
	x16, y := coord.LonLatToTile(5, 30, 5)
	if x16 != 16 {
		t.Fatalf("test setup: lon 5 is in z5 column %d", x16)
	}
	if _, ok := src[[3]int{5, 16, y}]; ok {
		t.Fatal("test setup: the all-nodata tile exists in the source")
	}

	// sameSource reports whether out holds every source tile unchanged.
	sameSource := func(t *testing.T, out map[[3]int][]byte) {
		t.Helper()
		for p, data := range src {
			if !bytes.Equal(out[p], data) {
				t.Errorf("tile %v was not copied as-is", p)
			}
		}
	}

	t.Run("default flags copy every tile", func(t *testing.T) {
		out := readAllTiles(t, runTransform(t, transformConfig{InputPath: srcPath, MinZoom: -1, MaxZoom: -1}))
		sameSource(t, out)
		if len(out) != len(src) {
			t.Errorf("%d tiles, want the source's %d", len(out), len(src))
		}
	})

	t.Run("min zoom below the source adds levels", func(t *testing.T) {
		out := readAllTiles(t, runTransform(t, transformConfig{InputPath: srcPath, MinZoom: 2, MaxZoom: -1}))
		sameSource(t, out)
		for _, p := range [][3]int{{2, 2, 1}, {3, 4, 3}} {
			if _, ok := out[p]; !ok {
				t.Errorf("added level tile %v missing", p)
			}
		}
	})

	t.Run("fill-missing alone does not re-encode", func(t *testing.T) {
		blue := &color.RGBA{0, 0, 255, 255}
		outPath := runTransform(t, transformConfig{InputPath: srcPath, MinZoom: -1, MaxZoom: -1, FillMissing: blue})
		out := readAllTiles(t, outPath)
		sameSource(t, out)
		if _, ok := out[[3]int{5, 16, y}]; !ok {
			t.Fatal("the missing tile was not filled")
		}
		assertTilePixel(t, outPath, 5, 16, y, 128, 128, 0, 0, 255, 255, 0)
	})

	t.Run("nodata-color re-encodes", func(t *testing.T) {
		outPath := runTransform(t, transformConfig{InputPath: srcPath, MinZoom: -1, MaxZoom: -1,
			NodataColor: &color.RGBA{255, 0, 0, 255}})
		out := readAllTiles(t, outPath)
		if len(out) != len(src) {
			t.Errorf("%d tiles, want the source's %d (no fill)", len(out), len(src))
		}
		// 12°E: nodata in the half-transparent tile of column 17.
		px, py := coord.TilePixelCoords(12, 30, 5, 17, y, 256)
		assertTilePixel(t, outPath, 5, 17, y, int(px), int(py), 255, 0, 0, 255, 0)
	})
}
