package pmtiles

import (
	"bytes"
	"path/filepath"
	"testing"
)

// PMTiles v3: a directory entry with RunLength N means tile IDs
// [TileID, TileID+N) all resolve to the SAME Offset/Length. Distinct tiles
// whose blobs are merely adjacent in the data section must keep their own
// entries, otherwise every spec reader serves the first tile's bytes for the
// rest of the run.

func writeZ1(t *testing.T, path string, tiles [][]byte) *Reader {
	t.Helper()
	w, err := NewWriter(path, WriterOptions{MinZoom: 1, MaxZoom: 1, TileSize: 256})
	if err != nil {
		t.Fatal(err)
	}
	// z=1 Hilbert order: (0,0)=1 (0,1)=2 (1,1)=3 (1,0)=4 — consecutive IDs.
	for i, xy := range [][2]int{{0, 0}, {0, 1}, {1, 1}, {1, 0}} {
		if err := w.WriteTile(1, xy[0], xy[1], tiles[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestWriter_RunLengthSpecSemantics(t *testing.T) {
	// Four DISTINCT tiles of equal encoded length at consecutive IDs.
	tiles := [][]byte{
		bytes.Repeat([]byte{0x11}, 64),
		bytes.Repeat([]byte{0x22}, 64),
		bytes.Repeat([]byte{0x33}, 64),
		bytes.Repeat([]byte{0x44}, 64),
	}
	r := writeZ1(t, filepath.Join(t.TempDir(), "distinct.pmtiles"), tiles)
	h := r.Header()
	if h.NumAddressedTiles != 4 || h.NumTileContents != 4 {
		t.Fatalf("addressed/contents = %d/%d, want 4/4", h.NumAddressedTiles, h.NumTileContents)
	}
	// Adjacent-but-distinct blobs must NOT be merged into a run.
	if h.NumTileEntries != 4 {
		t.Fatalf("NumTileEntries = %d, want 4 (distinct tiles merged into a run-length entry)", h.NumTileEntries)
	}
	for i, xy := range [][2]int{{0, 0}, {0, 1}, {1, 1}, {1, 0}} {
		got, err := r.ReadTile(1, xy[0], xy[1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, tiles[i]) {
			t.Errorf("tile z=1 x=%d y=%d: got first byte 0x%02x, want 0x%02x", xy[0], xy[1], got[0], tiles[i][0])
		}
	}
}

func TestWriter_RunLengthDedup(t *testing.T) {
	// Three IDENTICAL tiles at consecutive IDs plus one different: the
	// legitimate shared-blob case a run-length entry exists for.
	same := bytes.Repeat([]byte{0xAA}, 64)
	other := bytes.Repeat([]byte{0xBB}, 64)
	tiles := [][]byte{same, same, same, other}
	r := writeZ1(t, filepath.Join(t.TempDir(), "dedup.pmtiles"), tiles)
	h := r.Header()
	if h.NumAddressedTiles != 4 || h.NumTileContents != 2 {
		t.Fatalf("addressed/contents = %d/%d, want 4/2", h.NumAddressedTiles, h.NumTileContents)
	}
	// One run of 3 sharing a blob, plus one entry.
	if h.NumTileEntries != 2 {
		t.Fatalf("NumTileEntries = %d, want 2 (run of 3 + 1)", h.NumTileEntries)
	}
	for i, xy := range [][2]int{{0, 0}, {0, 1}, {1, 1}, {1, 0}} {
		got, err := r.ReadTile(1, xy[0], xy[1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, tiles[i]) {
			t.Errorf("tile z=1 x=%d y=%d: got first byte 0x%02x, want 0x%02x", xy[0], xy[1], got[0], tiles[i][0])
		}
	}
}
