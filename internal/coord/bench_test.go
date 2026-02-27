package coord

import "testing"

// --- PixelToLonLat benchmark ---

// BenchmarkPixelToLonLat measures the per-row latitude computation in
// renderTile. Called tileSize times per tile to precompute the lat array
// (the key O(n) trig optimization over the naive O(n²) approach).
func BenchmarkPixelToLonLat(b *testing.B) {
	z, tx, ty, tileSize := 10, 535, 358, 256
	var sinkLon, sinkLat float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		px := float64(i%tileSize) + 0.5
		py := float64(i%tileSize) + 0.5
		lon, lat := PixelToLonLat(z, tx, ty, tileSize, px, py)
		sinkLon += lon
		sinkLat += lat
	}
	_, _ = sinkLon, sinkLat
}

// --- LonLatToTile benchmark ---

// BenchmarkLonLatToTile measures tile coordinate lookup, used during bounds
// computation and zoom-level inference.
func BenchmarkLonLatToTile(b *testing.B) {
	coords := [4][2]float64{
		{8.5417, 47.3769},  // Zurich
		{-74.0060, 40.713}, // New York
		{139.69, 35.689},   // Tokyo
		{2.3522, 48.856},   // Paris
	}
	var sinkX, sinkY int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := coords[i&3]
		x, y := LonLatToTile(c[0], c[1], 14)
		sinkX += x
		sinkY += y
	}
	_, _ = sinkX, sinkY
}

// --- TileBounds benchmark ---

// BenchmarkTileBounds measures the inverse tile-to-WGS84 bounds computation,
// called once per output tile in renderTile to determine the CRS bounding box.
func BenchmarkTileBounds(b *testing.B) {
	var sinkA, sinkB float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		x := i % 16384
		y := (i / 16384) % 16384
		minLon, minLat, _, _ := TileBounds(14, x, y)
		sinkA += minLon
		sinkB += minLat
	}
	_, _ = sinkA, sinkB
}

// --- SortTilesByHilbert benchmarks ---

// makeTileList builds a slice of [3]int tiles covering all tiles at zoom z.
func makeTileList(z int) [][3]int {
	n := 1 << uint(z)
	tiles := make([][3]int, 0, n*n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			tiles = append(tiles, [3]int{z, x, y})
		}
	}
	return tiles
}

// BenchmarkSortTilesByHilbert_Z8 measures sorting 256×256 = 65536 tiles.
// This is a realistic zoom-level batch size for a moderate-resolution dataset.
func BenchmarkSortTilesByHilbert_Z8(b *testing.B) {
	for i := 0; i < b.N; i++ {
		// Rebuild the slice each iteration so we sort an unsorted list.
		b.StopTimer()
		tiles := makeTileList(8)
		b.StartTimer()
		SortTilesByHilbert(tiles)
	}
}

// BenchmarkSortTilesByHilbert_Z10 measures sorting 1024×1024 = 1M tiles.
// Representative of a high-resolution global dataset at z10.
func BenchmarkSortTilesByHilbert_Z10(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tiles := makeTileList(10)
		b.StartTimer()
		SortTilesByHilbert(tiles)
	}
}

// BenchmarkSortTilesByHilbert_Z6 measures sorting 4096 tiles.
// Representative of a small regional dataset or a single zoom batch.
func BenchmarkSortTilesByHilbert_Z6(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tiles := makeTileList(6)
		b.StartTimer()
		SortTilesByHilbert(tiles)
	}
}

// --- ResolutionAtLat benchmark ---

// BenchmarkResolutionAtLat measures the per-tile resolution calculation
// called in renderTile to select the correct COG overview level.
func BenchmarkResolutionAtLat(b *testing.B) {
	lats := [4]float64{0, 30, 47.37, 60}
	var sink float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink += ResolutionAtLat(lats[i&3], 14, 256)
	}
	_ = sink
}
