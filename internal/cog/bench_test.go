package cog

import (
	"encoding/binary"
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

// --- decodeRawTile benchmarks ---

// BenchmarkDecodeRawTile times the per-pixel decode of one 512x512 source
// tile for the common sample layouts: the 8-bit legacy path, 8-bit RGB, and
// 16-bit and bit-packed 15-bit gray with a linear rescale.
func BenchmarkDecodeRawTile(b *testing.B) {
	const w, h = 512, 512
	for _, bc := range []struct {
		name      string
		bits, spp int
		cfg       BandConfig
	}{
		{"8Gray", 8, 1, BandConfig{}},
		{"8RGB", 8, 3, BandConfig{}},
		{"16Gray", 16, 1, BandConfig{Rescale: RescaleLinear, RescaleMax: 10000}},
		{"15Packed", 15, 1, BandConfig{Rescale: RescaleLinear, RescaleMax: 10000}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			data := make([]byte, (w*bc.spp*bc.bits+7)/8*h)
			for i := range data {
				data[i] = byte(i * 7)
			}
			ifd := IFD{TileWidth: w, TileHeight: h, SamplesPerPixel: uint16(bc.spp),
				BitsPerSample: []uint16{uint16(bc.bits)}, SampleFormat: []uint16{1}}
			r := &Reader{ifds: []IFD{ifd}, bo: binary.LittleEndian, bandCfg: bc.cfg}
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := r.decodeRawTile(&r.ifds[0], data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// --- Local views and the float/uint16 caches ---

// BenchmarkTileCache_LocalGetHit measures the render hot path: per-pixel
// lookups through a Local view that stay within one 2×2 tile neighbourhood,
// so the memo answers them all.
func BenchmarkTileCache_LocalGetHit(b *testing.B) {
	cache := NewTileCache(256)
	img := solidRGBA(256, color.RGBA{100, 150, 200, 255})
	for i := 0; i < 4; i++ {
		cache.Put(1, 0, 10+i&1, 20+i>>1, img)
	}
	local := cache.Local()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = local.Get(1, 0, 10+i&1, 20+i>>1&1)
	}
}

var localSink *TileCache

// BenchmarkTileCache_Local measures creating a Local view, once per rendered tile.
func BenchmarkTileCache_Local(b *testing.B) {
	cache := NewTileCache(256)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		localSink = cache.Local()
	}
}

func BenchmarkFloatTileCache_GetHit(b *testing.B) {
	cache := NewFloatTileCache(256)
	cache.Put(1, 0, 10, 20, make([]float32, 256*256), 256, 256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = cache.Get(1, 0, 10, 20)
	}
}

func BenchmarkFloatTileCache_LocalGetHit(b *testing.B) {
	cache := NewFloatTileCache(256)
	for i := 0; i < 4; i++ {
		cache.Put(1, 0, 10+i&1, 20+i>>1, make([]float32, 256*256), 256, 256)
	}
	local := cache.Local()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = local.Get(1, 0, 10+i&1, 20+i>>1&1)
	}
}

func BenchmarkUint16TileCache_GetHit(b *testing.B) {
	cache := NewUint16TileCache(256)
	cache.Put(1, 0, 10, 20, make([]uint16, 256*256), 256, 256, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _, _ = cache.Get(1, 0, 10, 20)
	}
}

func BenchmarkUint16TileCache_LocalGetHit(b *testing.B) {
	cache := NewUint16TileCache(256)
	for i := 0; i < 4; i++ {
		cache.Put(1, 0, 10+i&1, 20+i>>1, make([]uint16, 256*256), 256, 256, 1)
	}
	local := cache.Local()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _, _ = local.Get(1, 0, 10+i&1, 20+i>>1&1)
	}
}

// BenchmarkCaches_GetHitParallel measures Get hits on the shared caches from
// all goroutines at once, over 1024 tiles spread across the shards.
func BenchmarkCaches_GetHitParallel(b *testing.B) {
	tc, fc, uc := NewTileCache(4096), NewFloatTileCache(4096), NewUint16TileCache(4096)
	img, f, u := image.NewRGBA(image.Rect(0, 0, 1, 1)), []float32{1}, []uint16{1}
	for i := 0; i < 1024; i++ {
		tc.Put(1, 0, i%32, i/32, img)
		fc.Put(1, 0, i%32, i/32, f, 1, 1)
		uc.Put(1, 0, i%32, i/32, u, 1, 1, 1)
	}
	for _, c := range []struct {
		name string
		get  func(col, row int)
	}{
		{"Tile", func(col, row int) { _ = tc.Get(1, 0, col, row) }},
		{"Float", func(col, row int) { _, _, _ = fc.Get(1, 0, col, row) }},
		{"Uint16", func(col, row int) { _, _, _, _, _ = uc.Get(1, 0, col, row) }},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				for i := 0; pb.Next(); i++ {
					c.get(i%32, i/32%32)
				}
			})
		})
	}
}
