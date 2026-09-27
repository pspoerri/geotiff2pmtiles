package pmtiles

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
)

// --- ZXYToTileID benchmark ---

// BenchmarkZXYToTileID measures the Hilbert-curve tile ID computation called
// for every tile written by the PMTiles writer.
func BenchmarkZXYToTileID(b *testing.B) {
	// Use realistic z14 tile coordinates (Zurich area).
	coords := [4][3]int{
		{14, 8590, 5747},
		{14, 8591, 5748},
		{14, 8589, 5746},
		{14, 8592, 5749},
	}
	var sink uint64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := coords[i&3]
		sink += ZXYToTileID(c[0], c[1], c[2])
	}
	_ = sink
}

// makeEntries builds a slice of N sequential entries with contiguous offsets.
// The tile IDs are taken from a realistic z10 Hilbert ordering.
func makeEntries(n int) []Entry {
	entries := make([]Entry, n)
	offset := uint64(0)
	tileSize := uint32(5000) // ~5 KB per tile, typical JPEG
	for i := 0; i < n; i++ {
		x := i % 1024
		y := (i / 1024) % 1024
		entries[i] = Entry{
			TileID:    ZXYToTileID(10, x, y),
			Offset:    offset,
			Length:    tileSize,
			RunLength: 1,
		}
		offset += uint64(tileSize)
	}
	return entries
}

// BenchmarkBuildDirectory_Small measures directory building for a small tileset
// (≤16K entries) that fits entirely in the root directory without leaf splits.
func BenchmarkBuildDirectory_Small(b *testing.B) {
	entries := makeEntries(1000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = BuildDirectory(entries)
	}
}

// BenchmarkBuildDirectory_Medium measures directory building for a medium
// tileset (~16K entries) at the threshold where leaf dirs may be created.
func BenchmarkBuildDirectory_Medium(b *testing.B) {
	entries := makeEntries(16384)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = BuildDirectory(entries)
	}
}

// BenchmarkBuildDirectory_Large measures directory building for a large tileset
// (~65K entries) that requires leaf directory partitioning.
func BenchmarkBuildDirectory_Large(b *testing.B) {
	entries := makeEntries(65536)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = BuildDirectory(entries)
	}
}

// BenchmarkOptimizeRunLengths measures the run-length merging step in
// directory building, which collapses consecutive same-length tiles.
func BenchmarkOptimizeRunLengths(b *testing.B) {
	// Mix of mergeable and non-mergeable entries.
	entries := makeEntries(10000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = optimizeRunLengths(entries)
	}
}

// BenchmarkTileHash measures the FNV-64a hash used for tile deduplication.
// Called for every WriteTile invocation.
func BenchmarkTileHash(b *testing.B) {
	// Simulate a typical 5 KB encoded tile.
	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(i)
	}
	var sink uint64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink += tileHash(data)
	}
	_ = sink
}

// writeBenchArchive writes every tile of z0..maxZoom: the west quarter of
// the world ("land") gets distinct tiles, the rest one shared "ocean" blob,
// so the directory holds long run-length entries like a global raster.
func writeBenchArchive(b *testing.B, maxZoom int) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "bench.pmtiles")
	w, err := NewWriter(path, WriterOptions{MinZoom: 0, MaxZoom: maxZoom, TileSize: 256})
	if err != nil {
		b.Fatal(err)
	}
	ocean := bytes.Repeat([]byte{0x0F}, 64)
	for z := 0; z <= maxZoom; z++ {
		n := 1 << z
		for x := 0; x < n; x++ {
			for y := 0; y < n; y++ {
				data := ocean
				if x < n/4 {
					data = []byte(fmt.Sprintf("land %d/%d/%d", z, x, y))
				}
				if err := w.WriteTile(z, x, y, data); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	if err := w.Finalize(); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkOpenReader measures opening a z0-9 archive (349,525 addressed
// tiles, three quarters in ocean runs): directory parsing and indexing.
func BenchmarkOpenReader(b *testing.B) {
	path := writeBenchArchive(b, 9)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r, err := OpenReader(path)
		if err != nil {
			b.Fatal(err)
		}
		r.Close()
	}
}

// BenchmarkReadTile measures the tile lookup plus the file read.
func BenchmarkReadTile(b *testing.B) {
	r, err := OpenReader(writeBenchArchive(b, 9))
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := r.ReadTile(9, (i*37)&511, (i*101)&511); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTilesAtZoom measures listing the 262,144 tiles of z9.
func BenchmarkTilesAtZoom(b *testing.B) {
	r, err := OpenReader(writeBenchArchive(b, 9))
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if got := len(r.TilesAtZoom(9)); got != 1<<18 {
			b.Fatalf("TilesAtZoom(9) = %d tiles, want %d", got, 1<<18)
		}
	}
}
