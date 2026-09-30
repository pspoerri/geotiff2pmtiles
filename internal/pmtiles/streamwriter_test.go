package pmtiles

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A StreamWriter archive reads back tile for tile, keeps runs of one blob
// as one entry, and refuses a tile ID lower than the last.
func TestStreamWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.pmtiles")
	w, err := NewStreamWriter(path, WriterOptions{MinZoom: 0, MaxZoom: 2, TileSize: 256, TileFormat: TileTypePNG, Name: "s"})
	if err != nil {
		t.Fatal(err)
	}
	sea := []byte("sea")
	big := bytes.Repeat([]byte("L"), 5000) // above dedupMax: never deduplicated
	want := map[[3]int][]byte{}
	for _, tl := range []struct {
		z, x, y int
		data    []byte
	}{
		{0, 0, 0, []byte("world")},
		{1, 0, 0, sea}, {1, 0, 1, sea}, {1, 1, 1, sea}, // IDs 1, 2, 3: one run
		{1, 1, 0, big},
		{2, 0, 0, sea}, // same blob, not adjacent: a second entry
	} {
		if err := w.WriteTile(tl.z, tl.x, tl.y, tl.data); err != nil {
			t.Fatalf("z%d/%d/%d: %v", tl.z, tl.x, tl.y, err)
		}
		want[[3]int{tl.z, tl.x, tl.y}] = tl.data
	}
	if err := w.WriteTile(1, 0, 0, sea); err == nil || !strings.Contains(err.Error(), "increasing") {
		t.Errorf("out-of-order write: err = %v, want an ordering error", err)
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".partial"); !os.IsNotExist(err) {
		t.Error("the .partial file is still there")
	}

	r := openTestReader(t, path)
	h := r.Header()
	if h.TileDataOffset != StreamTileDataOffset || h.NumAddressedTiles != 6 || h.NumTileEntries != 4 || h.NumTileContents != 3 {
		t.Errorf("header: data at %d, %d addressed, %d entries, %d contents; want %d, 6, 4, 3",
			h.TileDataOffset, h.NumAddressedTiles, h.NumTileEntries, h.NumTileContents, StreamTileDataOffset)
	}
	fi, _ := os.Stat(path)
	if end := h.LeafDirOffset + h.LeafDirLength; uint64(fi.Size()) != end {
		t.Errorf("file is %d bytes, the last section ends at %d", fi.Size(), end)
	}
	for p, data := range want {
		if got, err := r.ReadTile(p[0], p[1], p[2]); err != nil || !bytes.Equal(got, data) {
			t.Errorf("tile %v = %.10q, %v; want %.10q", p, got, err, data)
		}
	}
	if meta, err := r.ReadMetadata(); err != nil || meta["name"] != "s" {
		t.Errorf("metadata = %v, %v", meta, err)
	}
}

func TestStreamWriterAbortRemovesPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.pmtiles")
	w, err := NewStreamWriter(path, WriterOptions{TileFormat: TileTypePNG})
	if err != nil {
		t.Fatal(err)
	}
	w.WriteTile(0, 0, 0, []byte("x"))
	w.Abort()
	if _, err := os.Stat(path + ".partial"); !os.IsNotExist(err) {
		t.Error("Abort left the .partial file")
	}
}
