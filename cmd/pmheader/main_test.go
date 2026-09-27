package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// writeArchive writes a small archive with the PMTiles writer (gzip
// directories and metadata).
func writeArchive(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.pmtiles")
	w, err := pmtiles.NewWriter(path, pmtiles.WriterOptions{
		MinZoom: 0, MaxZoom: 1, TileSize: 256, TileFormat: pmtiles.TileTypePNG,
		Bounds: cog.Bounds{MinLon: 8, MinLat: 46, MaxLon: 9, MaxLat: 47},
		Extra:  map[string]any{"scenes": []any{"S2A_1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][3]int{{0, 0, 0}, {1, 1, 0}} {
		if err := w.WriteTile(p[0], p[1], p[2], []byte{byte(p[0])}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	return path
}

// rawDir serializes an uncompressed PMTiles directory.
func rawDir(entries ...pmtiles.Entry) []byte {
	b := binary.AppendUvarint(nil, uint64(len(entries)))
	var last uint64
	for _, e := range entries {
		b = binary.AppendUvarint(b, e.TileID-last)
		last = e.TileID
	}
	for _, e := range entries {
		b = binary.AppendUvarint(b, uint64(e.RunLength))
	}
	for _, e := range entries {
		b = binary.AppendUvarint(b, uint64(e.Length))
	}
	for _, e := range entries {
		b = binary.AppendUvarint(b, e.Offset+1)
	}
	return b
}

// writeNestedUncompressed writes an archive with internal compression none
// whose root points to a leaf that points to another leaf with 3 tiles.
func writeNestedUncompressed(t *testing.T) string {
	t.Helper()
	inner := rawDir(pmtiles.Entry{TileID: 0, RunLength: 1, Length: 4}, pmtiles.Entry{TileID: 1, RunLength: 2, Length: 4})
	outer := rawDir(pmtiles.Entry{TileID: 0, Offset: 0, Length: uint32(len(inner))})
	root := rawDir(pmtiles.Entry{TileID: 0, Offset: uint64(len(inner)), Length: uint32(len(outer))})
	meta := []byte(`{"name":"nested","maxzoom":"1"}`)
	leaves := append(append([]byte{}, inner...), outer...)
	tiles := []byte("tile")

	h := pmtiles.Header{
		RootDirOffset: pmtiles.HeaderSize, RootDirLength: uint64(len(root)),
		NumAddressedTiles: 3, NumTileEntries: 2, NumTileContents: 1,
		Clustered: true, InternalCompression: pmtiles.CompressionNone,
		TileType: pmtiles.TileTypePNG, MinZoom: 0, MaxZoom: 1,
	}
	h.MetadataOffset = h.RootDirOffset + h.RootDirLength
	h.MetadataLength = uint64(len(meta))
	h.LeafDirOffset = h.MetadataOffset + h.MetadataLength
	h.LeafDirLength = uint64(len(leaves))
	h.TileDataOffset = h.LeafDirOffset + h.LeafDirLength
	h.TileDataLength = uint64(len(tiles))

	var buf []byte
	for _, part := range [][]byte{h.Serialize(), root, meta, leaves, tiles} {
		buf = append(buf, part...)
	}
	path := filepath.Join(t.TempDir(), "nested.pmtiles")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// readBack opens path with the library reader and returns its header,
// metadata and tile count.
func readBack(t *testing.T, path string) (pmtiles.Header, map[string]any, int) {
	t.Helper()
	r, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	meta, err := r.ReadMetadata()
	if err != nil {
		t.Fatal(err)
	}
	return r.Header(), meta, r.NumTiles()
}

func optI(v int) optInt         { return optInt{val: v, set: true} }
func optF(v float32) optFloat32 { return optFloat32{val: v, set: true} }

func TestPatchSyncsMetadata(t *testing.T) {
	path := writeArchive(t)
	err := patch(path, "", patchOptions{
		syncMetadata: true,
		maxZoom:      optI(5), tileTypeStr: "webp",
		minLon: optF(8.5), centerZoom: optI(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	h, meta, n := readBack(t, path)
	if h.MaxZoom != 5 || h.TileType != pmtiles.TileTypeWebP || n != 2 {
		t.Errorf("header max zoom %d, type %d, %d tiles", h.MaxZoom, h.TileType, n)
	}
	for k, want := range map[string]any{
		"maxzoom": "5", "minzoom": "0", "format": "webp",
		"bounds": "8.500000,46.000000,9.000000,47.000000",
		"center": "8.500000,46.500000,3",
	} {
		if meta[k] != want {
			t.Errorf("metadata %s = %v, want %v", k, meta[k], want)
		}
	}
	if _, ok := meta["scenes"]; !ok {
		t.Error("an unrelated key was lost")
	}

	// An explicit --set wins over the sync.
	if err := patch(path, "", patchOptions{syncMetadata: true, maxZoom: optI(6), setKV: []string{`maxzoom="7"`}}); err != nil {
		t.Fatal(err)
	}
	if h, meta, _ := readBack(t, path); h.MaxZoom != 6 || meta["maxzoom"] != "7" {
		t.Errorf("header max zoom %d, metadata %v; want 6 and the --set 7", h.MaxZoom, meta["maxzoom"])
	}

	// --sync-metadata=false patches the header alone.
	if err := patch(path, "", patchOptions{maxZoom: optI(4)}); err != nil {
		t.Fatal(err)
	}
	if h, meta, _ := readBack(t, path); h.MaxZoom != 4 || meta["maxzoom"] != "7" {
		t.Errorf("header max zoom %d, metadata %v; want 4 and the old 7", h.MaxZoom, meta["maxzoom"])
	}
}

// Numbers and arrays keep their JSON form.
func TestSyncMetadataKeepsForm(t *testing.T) {
	meta := map[string]any{"maxzoom": 9.0, "bounds": []any{0.0, 0.0, 1.0, 1.0}}
	h := pmtiles.Header{MaxZoom: 12, MinLon: 2, MinLat: 3, MaxLon: 4, MaxLat: 5}
	if !syncMetadata(meta, patchOptions{maxZoom: optI(12), minLon: optF(2)}, h) {
		t.Fatal("nothing synced")
	}
	if meta["maxzoom"] != 12.0 {
		t.Errorf("maxzoom = %#v, want the number 12", meta["maxzoom"])
	}
	if b, ok := meta["bounds"].([]any); !ok || len(b) != 4 || b[0] != 2.0 || b[3] != 5.0 {
		t.Errorf("bounds = %#v, want [2 3 4 5]", meta["bounds"])
	}
	if _, ok := meta["center"]; ok {
		t.Error("a key the metadata lacked was added")
	}

	// A format stored as something other than a string becomes the name.
	meta = map[string]any{"format": 2.0}
	if !syncMetadata(meta, patchOptions{tileTypeStr: "png"}, pmtiles.Header{TileType: pmtiles.TileTypePNG}) || meta["format"] != "png" {
		t.Errorf("format = %#v, want \"png\"", meta["format"])
	}
}

// Uncompressed directories and metadata, with leaves two levels deep: the
// old code assumed gzip everywhere and followed one leaf level.
func TestPatchUncompressedNestedArchive(t *testing.T) {
	src := writeNestedUncompressed(t)
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := readBack(t, src)
	if err := showHeader(f, h); err != nil {
		t.Errorf("--show: %v", err)
	}
	entries, err := readAllEntries(f, h)
	f.Close()
	if err != nil || len(entries) != 2 {
		t.Fatalf("readAllEntries = %d entries, %v; want the 2 of the inner leaf", len(entries), err)
	}

	// --set keeps the archive's compression.
	setOut := filepath.Join(t.TempDir(), "set.pmtiles")
	if err := patch(src, setOut, patchOptions{setKV: []string{"attribution=me"}, maxZoom: optI(1), syncMetadata: true}); err != nil {
		t.Fatal(err)
	}
	if h, meta, n := readBack(t, setOut); h.InternalCompression != pmtiles.CompressionNone || meta["attribution"] != "me" || n != 3 {
		t.Errorf("--set: compression %d, metadata %v, %d tiles", h.InternalCompression, meta, n)
	}

	// --rebuild-dirs writes gzip directories, so the metadata follows.
	rebuilt := filepath.Join(t.TempDir(), "rebuilt.pmtiles")
	if err := patch(src, rebuilt, patchOptions{rebuildDirs: true}); err != nil {
		t.Fatal(err)
	}
	h, meta, n := readBack(t, rebuilt)
	if h.InternalCompression != pmtiles.CompressionGzip || meta["name"] != "nested" || n != 3 {
		t.Errorf("--rebuild-dirs: compression %d, metadata %v, %d tiles", h.InternalCompression, meta, n)
	}
}
