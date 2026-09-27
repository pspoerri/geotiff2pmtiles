package pmtiles

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// testDir serializes a directory as stored with internal compression c:
// gzip, or raw bytes for anything else.
func testDir(t *testing.T, c uint8, entries ...Entry) []byte {
	t.Helper()
	gz, err := serializeDirectory(entries)
	if err != nil {
		t.Fatal(err)
	}
	if c == CompressionGzip {
		return gz
	}
	gr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// writeTestArchive assembles an archive from prebuilt sections in the
// Writer's layout: [header][root dir][metadata][leaf dirs][tile data]. It
// lets tests build what the Writer never emits (nested leaves, other
// internal compressions). meta is stored as given.
func writeTestArchive(t *testing.T, c uint8, root, meta, leaves, tiles []byte) string {
	t.Helper()
	h := Header{
		InternalCompression: c,
		TileType:            TileTypePNG,
		RootDirOffset:       HeaderSize,
		RootDirLength:       uint64(len(root)),
	}
	h.MetadataOffset = h.RootDirOffset + h.RootDirLength
	h.MetadataLength = uint64(len(meta))
	h.LeafDirOffset = h.MetadataOffset + h.MetadataLength
	h.LeafDirLength = uint64(len(leaves))
	h.TileDataOffset = h.LeafDirOffset + h.LeafDirLength
	h.TileDataLength = uint64(len(tiles))

	var buf bytes.Buffer
	for _, b := range [][]byte{h.Serialize(), root, meta, leaves, tiles} {
		buf.Write(b)
	}
	path := filepath.Join(t.TempDir(), "test.pmtiles")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func openTestReader(t *testing.T, path string) *Reader {
	t.Helper()
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// A run of N tile IDs shares one blob and must not cost N index slots:
// a global archive addresses tens of millions of tiles, mostly in ocean runs.
func TestReader_RunNotExpanded(t *testing.T) {
	const run = 1 << 20 // all of z10
	root := testDir(t, CompressionGzip, Entry{TileID: ZXYToTileID(10, 0, 0), Length: 3, RunLength: run})
	path := writeTestArchive(t, CompressionGzip, root, nil, nil, []byte("sea"))

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r := openTestReader(t, path)
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
		t.Errorf("OpenReader allocated %d bytes for a single run-length entry, want < 1 MiB", alloc)
	}

	if got := r.NumTiles(); got != run {
		t.Errorf("NumTiles = %d, want %d", got, run)
	}
	for _, xy := range [][2]int{{0, 0}, {1023, 0}, {512, 700}} {
		got, err := r.ReadTile(10, xy[0], xy[1])
		if err != nil || string(got) != "sea" {
			t.Errorf("ReadTile(10, %d, %d) = %q, %v; want \"sea\"", xy[0], xy[1], got, err)
		}
	}
}

func TestReader_RunLookup(t *testing.T) {
	// Tile IDs: z0 = 0, z1 = 1..4, z2 = 5..20. The run at ID 4 continues
	// from z1 into z2; IDs 0, 3, 7..9 and 11.. are absent.
	tiles := []byte("AAbbbC")
	entries := []Entry{
		{TileID: 1, Offset: 0, Length: 2, RunLength: 2},  // IDs 1, 2   -> "AA"
		{TileID: 4, Offset: 2, Length: 3, RunLength: 3},  // IDs 4, 5, 6 -> "bbb"
		{TileID: 10, Offset: 5, Length: 1, RunLength: 1}, // ID 10     -> "C"
	}
	root := testDir(t, CompressionGzip, entries...)
	r := openTestReader(t, writeTestArchive(t, CompressionGzip, root, nil, nil, tiles))

	want := map[uint64]string{1: "AA", 2: "AA", 4: "bbb", 5: "bbb", 6: "bbb", 10: "C"}
	for id := uint64(0); id <= 21; id++ {
		z, x, y := TileIDToZXY(id)
		got, err := r.ReadTile(z, x, y)
		if err != nil {
			t.Fatalf("ReadTile(%d, %d, %d): %v", z, x, y, err)
		}
		if string(got) != want[id] {
			t.Errorf("tile ID %d (z%d/%d/%d) = %q, want %q", id, z, x, y, got, want[id])
		}
	}

	if got := r.NumTiles(); got != 6 {
		t.Errorf("NumTiles = %d, want 6", got)
	}

	for _, tc := range []struct {
		z   int
		ids []uint64
	}{
		{0, nil},
		{1, []uint64{1, 2, 4}},
		{2, []uint64{5, 6, 10}},
		{3, nil},
		{32, nil},
		{-1, nil},
	} {
		got := r.TilesAtZoom(tc.z)
		if len(got) != len(tc.ids) {
			t.Errorf("TilesAtZoom(%d) = %v, want tile IDs %v", tc.z, got, tc.ids)
			continue
		}
		for i, id := range tc.ids {
			z, x, y := TileIDToZXY(id)
			if got[i] != [3]int{z, x, y} {
				t.Errorf("TilesAtZoom(%d)[%d] = %v, want %v (ID %d)", tc.z, i, got[i], [3]int{z, x, y}, id)
			}
		}
	}
}
