package cog

import (
	"image"
	"image/color"
	"testing"
)

// --- tileKeyHash benchmarks ---

// BenchmarkTileKeyHash measures the FNV-1a inspired hash used for shard
// selection on every TileCache.Get and TileCache.Put call. This is on the
// inner per-pixel sampling loop.
func BenchmarkTileKeyHash(b *testing.B) {
	keys := [4]tileKey{
		{id: 1, level: 0, col: 100, row: 200},
		{id: 2, level: 3, col: 512, row: 300},
		{id: 1, level: 1, col: 0, row: 0},
		{id: 3, level: 5, col: 1024, row: 768},
	}
	var sink uint64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink += tileKeyHash(keys[i&3])
	}
	_ = sink
}

// --- TileCache benchmarks ---

// solidRGBA returns a tileSize×tileSize RGBA image filled with a single color.
func solidRGBA(tileSize int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = c.R
		pix[i+1] = c.G
		pix[i+2] = c.B
		pix[i+3] = c.A
	}
	return img
}

// BenchmarkTileCache_GetHit measures a cache hit — the dominant case during
// tile rendering when the LRU warms up. Uses a read lock, so multiple
// goroutines can share the cache without serialization.
func BenchmarkTileCache_GetHit(b *testing.B) {
	cache := NewTileCache(256)
	img := solidRGBA(256, color.RGBA{100, 150, 200, 255})
	cache.Put(1, 0, 10, 20, img)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.Get(1, 0, 10, 20)
	}
}

// BenchmarkTileCache_GetMiss measures a cache miss — the path taken before
// the cache is warm, or when a tile is evicted. Falls through to a nil return.
func BenchmarkTileCache_GetMiss(b *testing.B) {
	cache := NewTileCache(256)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Use varying coordinates so we don't accidentally hit a cached entry.
		_ = cache.Get(1, 0, i%1024, (i/1024)%1024)
	}
}

// BenchmarkTileCache_Put measures inserting unique tiles. Each iteration uses
// distinct coordinates so no early-return deduplication is triggered.
func BenchmarkTileCache_Put(b *testing.B) {
	// Use a large cache so eviction doesn't dominate.
	cache := NewTileCache(b.N + 64)
	img := solidRGBA(256, color.RGBA{100, 150, 200, 255})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Put(1, 0, i%8192, (i/8192)%8192, img)
	}
}

// BenchmarkTileCache_PutDuplicate measures inserting the same key repeatedly.
// The implementation returns immediately on duplicate, so this exercises the
// hot dedup path (read lock → map lookup → early return).
func BenchmarkTileCache_PutDuplicate(b *testing.B) {
	cache := NewTileCache(256)
	img := solidRGBA(256, color.RGBA{100, 150, 200, 255})
	cache.Put(1, 0, 5, 5, img)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Put(1, 0, 5, 5, img)
	}
}
