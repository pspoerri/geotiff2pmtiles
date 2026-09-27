package main

import (
	"image/color"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

func TestPassthroughMetadata(t *testing.T) {
	src := map[string]any{
		"name": "composite", "description": "d", "format": "webp", "type": "baselayer",
		"minzoom": "0", "maxzoom": "9", "bounds": "0,0,1,1", "center": "0,0,4",
		"attribution": "a", "encoding": "terrarium",
		"scenes":        []any{"S2A_1", "S2B_2"},
		"stretch":       map[string]any{"lo": 100.0, "hi": 3000.0},
		"vector_layers": []any{},
	}
	got := passthroughMetadata(src)
	for _, k := range []string{"scenes", "stretch", "vector_layers"} {
		if _, ok := got[k]; !ok {
			t.Errorf("key %q was dropped", k)
		}
	}
	for _, k := range derivedMetadataKeys {
		if _, ok := got[k]; ok {
			t.Errorf("derived key %q passed through; it would override the writer's value", k)
		}
	}
}

// Every key the writer derives must be in derivedMetadataKeys, or a stale
// source value passed through Extra would override the new one.
func TestDerivedMetadataKeysCoverTheWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "all.pmtiles")
	w, err := pmtiles.NewWriter(path, pmtiles.WriterOptions{
		Name: "n", Description: "d", Attribution: "a", Type: "overlay", Encoding: "terrarium",
		MinZoom: 1, MaxZoom: 2, TileSize: 256, TileFormat: pmtiles.TileTypePNG,
		Bounds: cog.Bounds{MinLon: 1, MinLat: 2, MaxLon: 3, MaxLat: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTile(1, 1, 0, []byte("tile")); err != nil {
		t.Fatal(err)
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	r, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	meta, err := r.ReadMetadata()
	if err != nil {
		t.Fatal(err)
	}
	for k := range meta {
		if !slices.Contains(derivedMetadataKeys, k) {
			t.Errorf("the writer derives %q, which derivedMetadataKeys lacks", k)
		}
	}
}

// writeArchive writes an archive with the given bounds and a tile at each
// of the given positions.
func writeArchive(t *testing.T, bounds cog.Bounds, maxZoom int, tiles ...[3]int) *pmtiles.Reader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.pmtiles")
	w, err := pmtiles.NewWriter(path, pmtiles.WriterOptions{
		MaxZoom: maxZoom, TileSize: 256, TileFormat: pmtiles.TileTypePNG, Bounds: bounds,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tiles {
		if err := w.WriteTile(p[0], p[1], p[2], []byte{byte(p[1])}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	r, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// Data across the antimeridian is recorded as -180..180, but fill tiles
// belong only in the columns around the data, not around the globe.
func TestFillBounds(t *testing.T) {
	columns := func(r *pmtiles.Reader) []int {
		b := fillBounds(r)
		var cols []int
		for _, p := range coord.TilesInBounds(3, float64(b[0]), float64(b[1]), float64(b[2]), float64(b[3])) {
			if !slices.Contains(cols, p[1]) {
				cols = append(cols, p[1])
			}
		}
		slices.Sort(cols)
		return cols
	}

	// 179°E to 179°W at z3: columns 7 and 0.
	am := writeArchive(t, cog.Bounds{MinLon: 179, MinLat: -17, MaxLon: 181, MaxLat: -16}, 3,
		[3]int{3, 7, 4}, [3]int{3, 0, 4})
	if got := columns(am); !slices.Equal(got, []int{0, 7}) {
		t.Errorf("antimeridian archive: fill columns %v, want [0 7]", got)
	}

	// A regional archive keeps its bounds.
	ch := writeArchive(t, cog.Bounds{MinLon: 6, MinLat: 46, MaxLon: 10, MaxLat: 48}, 3, [3]int{3, 4, 2})
	if got, want := fillBounds(ch), [4]float32{6, 46, 10, 48}; got != want {
		t.Errorf("regional archive: fill bounds %v, want %v", got, want)
	}

	// Full-width bounds with data that does not cross the antimeridian (a
	// sparse global archive) keep the full width.
	global := writeArchive(t, cog.Bounds{MinLon: -180, MinLat: -60, MaxLon: 180, MaxLat: 60}, 3,
		[3]int{3, 2, 3}, [3]int{3, 5, 3})
	if got := columns(global); len(got) != 8 {
		t.Errorf("global archive: fill columns %v, want all 8", got)
	}
}

// JPEG drops the alpha of a fill or nodata colour and writes its RGB.
func TestJPEGAlphaWarning(t *testing.T) {
	msg := jpegAlphaWarning("--nodata-color", &color.RGBA{0, 255, 0, 128})
	if !strings.Contains(msg, "--nodata-color alpha 128") || !strings.Contains(msg, "rgb(0,255,0)") || strings.Contains(msg, "black") {
		t.Errorf("warning %q, want the colour written: rgb(0,255,0)", msg)
	}
}
