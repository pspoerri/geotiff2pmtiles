# Performance Optimizations

## Summary

Five targeted optimizations improve tile generation throughput across all
example datasets. All changes are measured by the existing benchmark suite
(`go test ./internal/tile/ -bench=. -benchmem`).

## Changes

### 1. Eliminate per-pixel `IFDTileSize()` call in resampling (`resample.go`)

`bilinearSampleCached`, `nearestSampleCached`, and `readPixelCached` each
called `src.IFDTileSize(level)` on every output pixel to obtain the source
tile dimensions. The values are constant for all pixels within a tile and were
already precomputed in the `tileSource` struct by `prepareTileSources`.

**Fix:** Added `tw, th int` parameters to all three functions and updated
`sampleFromTileSources` to pass `src.tileW` / `src.tileH` directly, removing
the redundant IFD slice lookup from the hot per-pixel path.

### 2. Pool `lons`/`lats` slices in `renderTile` and `renderTileTerrarium` (`resample.go`)

Both render functions allocated two `[]float64` slices (one for longitudes, one
for latitudes) per tile via `make`. For 512-pixel tiles these are 4 KB each;
across thousands of max-zoom tiles the repeated allocation and zero-init added
measurable GC pressure.

**Fix:** Added a `sync.Pool` (`lonLatPool`) that recycles a single backing
`[]float64` of length `2×tileSize` (lons in the first half, lats in the second)
across consecutive tile renders. The pool is keyed by length so it degrades
gracefully if tile size varies.

### 3. Direct Pix slice access in RGBA downsample quadrant functions (`downsample.go`)

`downsampleQuadrantBilinear`, `downsampleQuadrantNearest`, and
`downsampleQuadrantMode` read source pixels via `srcPixel()` (which calls
`image.RGBAAt` — including a bounds check) and wrote results via `SetRGBA`
(another bounds check). For a 512×512 tile the inner loop makes 65,536 such
calls per quadrant.

**Fix:** Replaced all `srcPixel`/`SetRGBA` calls with direct index arithmetic
on `src.Pix` and `dst.Pix`. The bounds clamps inside `srcPixel` are provably
unnecessary: with `half = tileSize/2`, the source coordinates `sx = 2*dx` and
`sy = 2*dy` satisfy `sx+1 ≤ tileSize−1` and `sy+1 ≤ tileSize−1` for all loop
iterations. Eliminated coordinates are documented inline.

**Benchmark results (256×256 tiles, Apple M1 Max):**

| Benchmark | Before | After | Speedup |
|-----------|--------|-------|---------|
| `Downsample_RGBAChildren_Nearest` | 334 µs | 116 µs | **2.9×** |
| `Downsample_RGBAChildren_Bilinear` | 1,270 µs | 366 µs | **3.47×** |

The gray fast-path (`downsampleQuadrantGray*`) was already using direct slice
access and is unchanged.

### 4. `detectUniform` uses uint64 word comparison (`tiledata.go`)

The previous implementation compared four bytes individually per 4-byte pixel.
For a 512×512 solid-color tile (1 MB of pixel data) this required 1,048,572
individual byte comparisons.

**Fix:** Packs the reference pixel into a `uint32` and reads 8 bytes at a time
using `binary.LittleEndian.Uint64`, making one integer comparison per two
pixels. A single-iteration `uint32` cleanup handles the last 4-byte group when
the slice length is not a multiple of 8.

**Benchmark results (256×256 solid tile):**

| Benchmark | Before | After | Speedup |
|-----------|--------|-------|---------|
| `DetectUniform_Solid` | 93 µs | 29 µs | **3.25×** |
| `NewTileData_Uniform` | 94 µs | 29 µs | **3.3×** |

Non-uniform tiles exit on the first mismatch (≤ 8 bytes read) — unchanged.

### 5. Fill-color tile encoding cache and shared fill tile (`generator.go`)

When `--fill-color` is set and many tiles have no source coverage (e.g.,
sparse datasets or large bounding boxes), the uniform fill tile was
re-encoded by `cfg.Encoder.Encode` for every such tile. For PNG/WebP this
is measurably expensive. Separately, `newTileDataUniform` was called once per
nil child in the downsample loop, creating small but unnecessary allocations.

**Fix:**
- Pre-encode the fill-color tile once before the zoom loop and cache the
  resulting `[]byte` (`fillColorCached`).
- In the worker loop, uniform fill-color tiles bypass `Encode` and use the
  cached bytes directly for `WriteTile`. The PMTiles writer's FNV-64a
  deduplication ensures the bytes are stored on disk only once regardless.
- Reuse a single shared `*TileData` instance (`fillTileShared`) as the
  substitute for nil children during downsampling, eliminating one allocation
  per nil child per tile.

## No behavioral changes

All optimizations are purely mechanical — same output, same resampling
semantics. The full test suite (`go test ./... -count=1`) passes without
modification.
