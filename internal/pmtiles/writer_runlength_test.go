package pmtiles

import (
	"bytes"
	"fmt"
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

// Runs of one deduplicated blob (fill tiles) are merged while writing, so
// the index does not hold 24 bytes for every tile of a large --fill-color
// archive until Finalize. Workers write interleaved Hilbert batches, so the
// runs only show once sorted.
func TestWriter_CompactsRunsWhileWriting(t *testing.T) {
	const (
		z       = 9
		n       = 1 << 18 // every z9 tile
		workers = 8
		batch   = 32
	)
	path := filepath.Join(t.TempDir(), "fill.pmtiles")
	w, err := NewWriter(path, WriterOptions{MinZoom: z, MaxZoom: z, TileSize: 256})
	if err != nil {
		t.Fatal(err)
	}
	first := ZXYToTileID(z, 0, 0) // the lowest ID of the zoom
	fill := bytes.Repeat([]byte{0xF1}, 100)
	tile := func(i int) []byte {
		if i%1000 == 0 { // breaks the runs
			return []byte(fmt.Sprintf("unique tile %d", i))
		}
		return fill
	}
	for base := 0; base < n; base += workers * batch {
		for k := 0; k < batch; k++ {
			for wk := 0; wk < workers; wk++ {
				i := base + wk*batch + k
				_, x, y := TileIDToZXY(first + uint64(i))
				if err := w.WriteTile(z, x, y, tile(i)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if len(w.entries) > 65536 {
		t.Errorf("%d entries held for %d tiles in %d runs; want runs merged while writing", len(w.entries), n, 2*n/1000)
	}
	t.Logf("%d entries held for %d tiles in %d runs", len(w.entries), n, 2*n/1000)
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h := r.Header()
	if h.NumAddressedTiles != n || h.NumTileContents != n/1000+2 {
		t.Errorf("addressed/contents = %d/%d, want %d/%d", h.NumAddressedTiles, h.NumTileContents, n, n/1000+2)
	}
	for _, i := range []int{0, 1, 999, 1000, 1001, 123456, n - 1} {
		_, x, y := TileIDToZXY(first + uint64(i))
		got, err := r.ReadTile(z, x, y)
		if err != nil || !bytes.Equal(got, tile(i)) {
			t.Errorf("tile %d = %q, %v; want %q", i, got, err, tile(i))
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
