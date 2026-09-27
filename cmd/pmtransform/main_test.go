package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
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
