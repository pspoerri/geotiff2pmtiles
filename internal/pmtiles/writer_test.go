package pmtiles

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

func TestWriter_WriteAndFinalize(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "test.pmtiles")

	opts := WriterOptions{
		MinZoom:    0,
		MaxZoom:    2,
		Bounds:     cog.Bounds{MinLon: -10, MinLat: -10, MaxLon: 10, MaxLat: 10},
		TileFormat: TileTypePNG,
		TileSize:   256,
	}

	w, err := NewWriter(outPath, opts)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	// Write some test tiles.
	tileData := []byte("fake-tile-data-for-testing")
	tiles := [][3]int{
		{0, 0, 0},
		{1, 0, 0},
		{1, 1, 0},
		{1, 0, 1},
		{1, 1, 1},
		{2, 0, 0},
		{2, 1, 0},
		{2, 2, 1},
	}

	for _, tile := range tiles {
		if err := w.WriteTile(tile[0], tile[1], tile[2], tileData); err != nil {
			t.Fatalf("WriteTile(%d,%d,%d): %v", tile[0], tile[1], tile[2], err)
		}
	}

	// Finalize.
	if err := w.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Read the output file and verify the header.
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	if len(data) < HeaderSize {
		t.Fatalf("output file too small: %d bytes", len(data))
	}

	// Check magic.
	if string(data[0:7]) != "PMTiles" {
		t.Errorf("magic = %q, want \"PMTiles\"", string(data[0:7]))
	}
	if data[7] != 3 {
		t.Errorf("version = %d, want 3", data[7])
	}

	// Check tile type.
	if data[99] != TileTypePNG {
		t.Errorf("tile type = %d, want %d (PNG)", data[99], TileTypePNG)
	}

	// Check zoom range.
	if data[100] != 0 {
		t.Errorf("min zoom = %d, want 0", data[100])
	}
	if data[101] != 2 {
		t.Errorf("max zoom = %d, want 2", data[101])
	}

	// Check tile counts.
	numAddressed := binary.LittleEndian.Uint64(data[72:80])
	if numAddressed != uint64(len(tiles)) {
		t.Errorf("NumAddressedTiles = %d, want %d", numAddressed, len(tiles))
	}

	// Clustered flag.
	if data[96] != 1 {
		t.Errorf("clustered = %d, want 1", data[96])
	}

	// Internal compression should be gzip.
	if data[97] != CompressionGzip {
		t.Errorf("internal compression = %d, want %d", data[97], CompressionGzip)
	}

	// Verify offsets are consistent.
	rootDirOffset := binary.LittleEndian.Uint64(data[8:16])
	rootDirLength := binary.LittleEndian.Uint64(data[16:24])
	metadataOffset := binary.LittleEndian.Uint64(data[24:32])

	if rootDirOffset != HeaderSize {
		t.Errorf("root dir offset = %d, want %d", rootDirOffset, HeaderSize)
	}
	if metadataOffset != rootDirOffset+rootDirLength {
		t.Errorf("metadata offset = %d, want %d", metadataOffset, rootDirOffset+rootDirLength)
	}

	// File should not be trivially small.
	if len(data) < HeaderSize+10 {
		t.Errorf("output file suspiciously small: %d bytes", len(data))
	}
}

func TestWriter_EmptyTile(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "empty.pmtiles")

	w, err := NewWriter(outPath, WriterOptions{
		Bounds:     cog.Bounds{MinLon: 0, MaxLon: 1, MinLat: 0, MaxLat: 1},
		TileFormat: TileTypePNG,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	// Write an empty tile — should be silently ignored.
	if err := w.WriteTile(0, 0, 0, nil); err != nil {
		t.Fatalf("WriteTile(nil): %v", err)
	}
	if err := w.WriteTile(0, 0, 0, []byte{}); err != nil {
		t.Fatalf("WriteTile(empty): %v", err)
	}

	// Write one real tile so Finalize has something to work with.
	if err := w.WriteTile(0, 0, 0, []byte("data")); err != nil {
		t.Fatalf("WriteTile: %v", err)
	}

	if err := w.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	// Should have exactly 1 addressed tile (the empty ones were skipped).
	numAddressed := binary.LittleEndian.Uint64(data[72:80])
	if numAddressed != 1 {
		t.Errorf("NumAddressedTiles = %d, want 1", numAddressed)
	}
}

func TestWriter_DoubleFinalize(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "double.pmtiles")

	w, err := NewWriter(outPath, WriterOptions{
		Bounds:     cog.Bounds{},
		TileFormat: TileTypePNG,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	w.WriteTile(0, 0, 0, []byte("data"))
	if err := w.Finalize(); err != nil {
		t.Fatalf("first Finalize: %v", err)
	}

	// Second finalize should return an error.
	if err := w.Finalize(); err == nil {
		t.Error("second Finalize should return error")
	}
}

func TestWriter_Abort(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "aborted.pmtiles")

	w, err := NewWriter(outPath, WriterOptions{
		Bounds:     cog.Bounds{},
		TileFormat: TileTypePNG,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	w.WriteTile(0, 0, 0, []byte("data"))
	w.Abort()

	// Output file should not exist.
	if _, err := os.Stat(outPath); err == nil {
		t.Error("output file should not exist after Abort")
	}
}

func TestWriter_Deduplication(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "dedup.pmtiles")

	w, err := NewWriter(outPath, WriterOptions{
		MinZoom:    0,
		MaxZoom:    2,
		Bounds:     cog.Bounds{MinLon: -10, MaxLon: 10, MinLat: -10, MaxLat: 10},
		TileFormat: TileTypePNG,
		TileSize:   256,
		TempDir:    tmpDir,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	// Write several tiles with the same data (simulates uniform tiles).
	uniformData := []byte("uniform-tile-data-same-everywhere")
	uniqueData := []byte("unique-tile-data-different")
	tiles := [][3]int{
		{0, 0, 0}, // uniform
		{1, 0, 0}, // uniform
		{1, 1, 0}, // unique
		{1, 0, 1}, // uniform
		{1, 1, 1}, // uniform
	}

	for _, tile := range tiles {
		data := uniformData
		if tile[1] == 1 && tile[2] == 0 {
			data = uniqueData
		}
		if err := w.WriteTile(tile[0], tile[1], tile[2], data); err != nil {
			t.Fatalf("WriteTile(%d,%d,%d): %v", tile[0], tile[1], tile[2], err)
		}
	}

	// Verify dedup hits: 4 uniform tiles, first is original, 3 are deduped.
	if w.dedupHits != 3 {
		t.Errorf("dedupHits = %d, want 3", w.dedupHits)
	}

	if err := w.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	// All 5 tiles should be addressed.
	numAddressed := binary.LittleEndian.Uint64(data[72:80])
	if numAddressed != 5 {
		t.Errorf("NumAddressedTiles = %d, want 5", numAddressed)
	}

	// Only 2 unique tile contents (uniform + unique).
	numContents := binary.LittleEndian.Uint64(data[88:96])
	if numContents != 2 {
		t.Errorf("NumTileContents = %d, want 2", numContents)
	}
}

func TestWriter_ConcurrentWrites(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "concurrent.pmtiles")

	w, err := NewWriter(outPath, WriterOptions{
		MinZoom:    0,
		MaxZoom:    3,
		Bounds:     cog.Bounds{MinLon: -180, MaxLon: 180, MinLat: -85, MaxLat: 85},
		TileFormat: TileTypeJPEG,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	// Write tiles concurrently from multiple goroutines.
	done := make(chan error, 64)
	for z := 0; z <= 3; z++ {
		n := 1 << z
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				go func(z, x, y int) {
					done <- w.WriteTile(z, x, y, []byte("tile-data"))
				}(z, x, y)
			}
		}
	}

	// Wait for all writes.
	totalTiles := 1 + 4 + 16 + 64 // z0-z3
	for i := 0; i < totalTiles; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent WriteTile: %v", err)
		}
	}

	if err := w.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	numAddressed := binary.LittleEndian.Uint64(data[72:80])
	if numAddressed != uint64(totalTiles) {
		t.Errorf("NumAddressedTiles = %d, want %d", numAddressed, totalTiles)
	}
}
