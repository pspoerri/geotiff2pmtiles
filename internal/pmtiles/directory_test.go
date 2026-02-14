package pmtiles

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"testing"
)

func TestZXYToTileID_Z0(t *testing.T) {
	// At zoom 0, there's only one tile (0,0,0) with ID 0.
	if id := ZXYToTileID(0, 0, 0); id != 0 {
		t.Errorf("ZXYToTileID(0,0,0) = %d, want 0", id)
	}
}

func TestZXYToTileID_Z1(t *testing.T) {
	// At zoom 1, there are 4 tiles. IDs start at 1 (offset by z0's 1 tile).
	ids := make(map[uint64]bool)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			id := ZXYToTileID(1, x, y)
			if id < 1 || id > 4 {
				t.Errorf("ZXYToTileID(1,%d,%d) = %d, want in [1,4]", x, y, id)
			}
			if ids[id] {
				t.Errorf("ZXYToTileID(1,%d,%d) = %d is duplicate", x, y, id)
			}
			ids[id] = true
		}
	}
}

func TestZXYToTileID_UniqueAtZ2(t *testing.T) {
	// At zoom 2, 16 tiles. All IDs should be unique.
	ids := make(map[uint64]bool)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			id := ZXYToTileID(2, x, y)
			if ids[id] {
				t.Errorf("ZXYToTileID(2,%d,%d) = %d is duplicate", x, y, id)
			}
			ids[id] = true
		}
	}
	if len(ids) != 16 {
		t.Errorf("got %d unique IDs at z2, want 16", len(ids))
	}
}

func TestZXYToTileID_Monotonic(t *testing.T) {
	// Tile IDs at higher zoom levels should be greater than all IDs at lower zoom levels.
	maxZ1 := uint64(0)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			id := ZXYToTileID(1, x, y)
			if id > maxZ1 {
				maxZ1 = id
			}
		}
	}

	minZ2 := ^uint64(0)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			id := ZXYToTileID(2, x, y)
			if id < minZ2 {
				minZ2 = id
			}
		}
	}

	if minZ2 <= maxZ1 {
		t.Errorf("min z2 ID (%d) should be > max z1 ID (%d)", minZ2, maxZ1)
	}
}

func TestOptimizeRunLengths_Empty(t *testing.T) {
	result := optimizeRunLengths(nil)
	if len(result) != 0 {
		t.Errorf("optimizeRunLengths(nil) = %v, want empty", result)
	}
}

func TestOptimizeRunLengths_SingleEntry(t *testing.T) {
	entries := []Entry{{TileID: 5, Offset: 0, Length: 100, RunLength: 1}}
	result := optimizeRunLengths(entries)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0].RunLength != 1 {
		t.Errorf("RunLength = %d, want 1", result[0].RunLength)
	}
}

func TestOptimizeRunLengths_Consecutive(t *testing.T) {
	// Three consecutive tiles with same length and contiguous offsets.
	entries := []Entry{
		{TileID: 10, Offset: 0, Length: 100, RunLength: 1},
		{TileID: 11, Offset: 100, Length: 100, RunLength: 1},
		{TileID: 12, Offset: 200, Length: 100, RunLength: 1},
	}
	result := optimizeRunLengths(entries)
	if len(result) != 1 {
		t.Fatalf("expected 1 merged entry, got %d", len(result))
	}
	if result[0].TileID != 10 {
		t.Errorf("TileID = %d, want 10", result[0].TileID)
	}
	if result[0].RunLength != 3 {
		t.Errorf("RunLength = %d, want 3", result[0].RunLength)
	}
}

func TestOptimizeRunLengths_NonContiguous(t *testing.T) {
	// Tiles with a gap in tile IDs.
	entries := []Entry{
		{TileID: 10, Offset: 0, Length: 100, RunLength: 1},
		{TileID: 15, Offset: 100, Length: 100, RunLength: 1}, // gap in tile ID
	}
	result := optimizeRunLengths(entries)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
}

func TestOptimizeRunLengths_DifferentLengths(t *testing.T) {
	// Consecutive tile IDs but different data lengths.
	entries := []Entry{
		{TileID: 10, Offset: 0, Length: 100, RunLength: 1},
		{TileID: 11, Offset: 100, Length: 200, RunLength: 1}, // different length
	}
	result := optimizeRunLengths(entries)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (different lengths), got %d", len(result))
	}
}

func TestBuildDirectory_SmallSet(t *testing.T) {
	entries := make([]Entry, 10)
	offset := uint64(0)
	for i := 0; i < 10; i++ {
		entries[i] = Entry{
			TileID:    ZXYToTileID(2, i%4, i/4),
			Offset:    offset,
			Length:    100,
			RunLength: 1,
		}
		offset += 100
	}

	rootDir, leafDirs, err := buildDirectory(entries)
	if err != nil {
		t.Fatalf("buildDirectory: %v", err)
	}

	// With only 10 entries, should fit in root dir (no leaf dirs).
	if len(leafDirs) != 0 {
		t.Errorf("expected no leaf dirs for small set, got %d bytes", len(leafDirs))
	}

	// Root dir should be valid gzip.
	if len(rootDir) == 0 {
		t.Fatal("root dir is empty")
	}
	decompressed := decompressGzipT(t, rootDir)
	if len(decompressed) == 0 {
		t.Fatal("decompressed root dir is empty")
	}

	// Read number of entries from the decompressed directory.
	numEntries, n := binary.Uvarint(decompressed)
	if n <= 0 {
		t.Fatal("failed to read entry count from directory")
	}
	// The optimized entries count should be <= 10 (due to run-length merging).
	if numEntries == 0 || numEntries > 10 {
		t.Errorf("directory entry count = %d, want 1-10", numEntries)
	}
}

func TestSerializeDirectory_RoundTrip(t *testing.T) {
	entries := []Entry{
		{TileID: 0, Offset: 0, Length: 100, RunLength: 1},
		{TileID: 1, Offset: 100, Length: 200, RunLength: 1},
		{TileID: 5, Offset: 300, Length: 150, RunLength: 3},
	}

	data, err := serializeDirectory(entries)
	if err != nil {
		t.Fatalf("serializeDirectory: %v", err)
	}

	// Decompress.
	decompressed := decompressGzipT(t, data)

	// Read and verify the directory structure.
	r := bytes.NewReader(decompressed)

	numEntries := readUvarint(t, r)
	if numEntries != 3 {
		t.Fatalf("numEntries = %d, want 3", numEntries)
	}

	// Read tile ID deltas.
	var tileIDs []uint64
	var lastID uint64
	for i := uint64(0); i < numEntries; i++ {
		delta := readUvarint(t, r)
		id := lastID + delta
		tileIDs = append(tileIDs, id)
		lastID = id
	}
	if tileIDs[0] != 0 || tileIDs[1] != 1 || tileIDs[2] != 5 {
		t.Errorf("tileIDs = %v, want [0, 1, 5]", tileIDs)
	}

	// Read run lengths.
	var runLengths []uint64
	for i := uint64(0); i < numEntries; i++ {
		rl := readUvarint(t, r)
		runLengths = append(runLengths, rl)
	}
	if runLengths[0] != 1 || runLengths[1] != 1 || runLengths[2] != 3 {
		t.Errorf("runLengths = %v, want [1, 1, 3]", runLengths)
	}

	// Read lengths.
	var lengths []uint64
	for i := uint64(0); i < numEntries; i++ {
		l := readUvarint(t, r)
		lengths = append(lengths, l)
	}
	if lengths[0] != 100 || lengths[1] != 200 || lengths[2] != 150 {
		t.Errorf("lengths = %v, want [100, 200, 150]", lengths)
	}
}

func TestXYToHilbert_Exhaustive_Z2(t *testing.T) {
	// At zoom 2, n=4. All 16 positions should produce unique Hilbert indices.
	n := uint64(4)
	seen := make(map[uint64]bool)
	for y := uint64(0); y < n; y++ {
		for x := uint64(0); x < n; x++ {
			d := xyToHilbert(x, y, n)
			if d >= n*n {
				t.Errorf("xyToHilbert(%d, %d, %d) = %d, out of range [0, %d)", x, y, n, d, n*n)
			}
			if seen[d] {
				t.Errorf("xyToHilbert(%d, %d, %d) = %d is duplicate", x, y, n, d)
			}
			seen[d] = true
		}
	}
	if len(seen) != 16 {
		t.Errorf("got %d unique Hilbert values, want 16", len(seen))
	}
}

func TestXYToHilbert_Exhaustive_Z3(t *testing.T) {
	n := uint64(8)
	seen := make(map[uint64]bool)
	for y := uint64(0); y < n; y++ {
		for x := uint64(0); x < n; x++ {
			d := xyToHilbert(x, y, n)
			if seen[d] {
				t.Errorf("duplicate at (%d, %d): %d", x, y, d)
			}
			seen[d] = true
		}
	}
	if uint64(len(seen)) != n*n {
		t.Errorf("got %d unique values, want %d", len(seen), n*n)
	}
}

// Helper functions for test.

func decompressGzipT(t *testing.T, data []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer r.Close()
	result, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading gzip: %v", err)
	}
	return result
}

func readUvarint(t *testing.T, r io.ByteReader) uint64 {
	t.Helper()
	v, err := binary.ReadUvarint(r)
	if err != nil {
		t.Fatalf("ReadUvarint: %v", err)
	}
	return v
}
