# Missing Performance Benchmarks

Added comprehensive performance benchmarks covering all previously unguarded hot paths.

## Files changed

- `internal/tile/bench_test.go` — extended with 20 new benchmarks
- `internal/cog/bench_test.go` — new file (5 benchmarks)
- `internal/coord/bench_test.go` — new file (7 benchmarks)
- `internal/pmtiles/bench_test.go` — new file (6 benchmarks)
- `internal/encode/bench_test.go` — new file (8 benchmarks)

## New benchmarks by category

### Downsample modes (tile package)

Previously only `Nearest` and `Bilinear` were benchmarked. Added:

- `BenchmarkDownsample_GrayChildren_{Lanczos,Bicubic,Mode}`
- `BenchmarkDownsample_RGBAChildren_{Lanczos,Bicubic,Mode}`
- `BenchmarkDownsample_TerrariumChildren_{Nearest,Bilinear,Lanczos}`

`Lanczos` is the high-quality default and the most expensive path (~3-12 ms/tile).
`Terrarium` downsample was entirely unrepresented.

### LUT vs direct computation (tile package)

The Lanczos-3 and bicubic LUT tables are a key design pattern (eliminates
`math.Sin` calls, was 7.56% of CPU). Now guarded against regression:

- `BenchmarkLanczos3LUT` / `BenchmarkLanczos3Direct`
- `BenchmarkBicubicLUT` / `BenchmarkBicubicDirect`

At 1 iteration: LUT ~83 ns vs direct ~333 ns (4× speedup for Lanczos).

### RGBA pool (tile package)

`GetRGBA`/`PutRGBA` is on the critical path for every tile rendered and every
downsample step. `BenchmarkGetPutRGBA` measures the round-trip cost.

### Missing completion variants (tile package)

- `BenchmarkMemoryBytes_{RGBA,Uniform}` — only Gray was benchmarked
- `BenchmarkDeserialize_Uniform` — missing from the deserialize group

### TileCache (cog package)

The sharded LRU cache is inside the per-pixel sampling loop. A previous
optimization eliminated string keys that consumed 18% of CPU. Now covered:

- `BenchmarkTileKeyHash` — FNV-1a integer hash for shard selection
- `BenchmarkTileCache_GetHit` — read lock cache hit (dominant path)
- `BenchmarkTileCache_GetMiss` — cache miss (cold start / eviction)
- `BenchmarkTileCache_Put` — unique tile insertion
- `BenchmarkTileCache_PutDuplicate` — dedup early-return path

### Coordinate functions (coord package)

- `BenchmarkPixelToLonLat` — called 2×tileSize per tile in `renderTile`
- `BenchmarkLonLatToTile` — tile coordinate lookup
- `BenchmarkTileBounds` — tile-to-WGS84 bounds computation
- `BenchmarkSortTilesByHilbert_{Z6,Z8,Z10}` — Hilbert batch sort at 3 scales
- `BenchmarkResolutionAtLat` — per-tile resolution for overview selection

### PMTiles directory (pmtiles package)

- `BenchmarkZXYToTileID` — Hilbert tile ID, called for every tile written
- `BenchmarkBuildDirectory_{Small,Medium,Large}` — root-only / leaf-split paths
- `BenchmarkOptimizeRunLengths` — run-length merging step
- `BenchmarkTileHash` — FNV-64a hash for tile deduplication

### Encoders (encode package)

Previously zero benchmarks. Added:

- `BenchmarkPNGEncode` / `BenchmarkPNGEncode_Gray`
- `BenchmarkJPEGEncode_{Q85,Q75}`
- `BenchmarkTerrariumEncode`
- `BenchmarkElevationToTerrarium` / `BenchmarkTerrariumToElevation`
