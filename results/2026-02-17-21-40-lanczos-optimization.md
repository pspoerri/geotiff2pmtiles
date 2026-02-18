# Lanczos-3 Resampling Optimization

**Date:** 2026-02-17  
**Workload:** 4690 tiles (36 GeoTIFFs, zoom 12–18, 512px tiles, webp q85, concurrency 4)  
**Machine:** macOS, 64 GB RAM

## Result: 4.7x speedup

| Metric | Before | After | Improvement |
|--------|--------|-------|-------------|
| **Wall time** | 7m15s | 1m32s | **4.7x faster** |
| **Total CPU samples** | 1365.43s | 262.03s | 5.2x less CPU |
| `lanczosSampleCached` cum | 1160.36s (85%) | 116.46s (44%) | 10x reduction |
| `fetchTileCached` cum | 502.13s | 12.27s | 41x reduction |
| `TileCache.Get` cum | 468.92s | 8.08s | 58x reduction |
| `pixelFromImage` flat | 99.98s | 3.02s | 33x reduction |
| `lanczos3`/`math.Sin` cum | 103.29s | 6.37s (LUT) | 16x reduction |
| `IFDTileSize` flat | 46.53s | 0s (cached) | eliminated |
| `sync/atomic.Add` (locks) | 124.12s | 1.51s | 82x reduction |

## Baseline Profile (before)

Top CPU consumers in the Lanczos path before optimization:

| Hotspot | Flat | Cumulative | Root Cause |
|---------|------|------------|------------|
| `readPixelCached` (×36/pixel) | 38.81s | 721.01s (52.8%) | 36 separate cache lookups for 6×6 kernel, most pixels in same 1–4 tiles |
| `TileCache.Get` map lookups | 33.50s | 468.92s (34.3%) | RWMutex + map access ×36 per pixel |
| `pixelFromImage` (YCbCr→RGB) | 110.05s | 133.32s (9.8%) | Called 36× per pixel via `readPixelCached`, each with type assertion |
| `lanczos3` / `math.Sin` | 16.62s | 103.29s (7.6%) | 12 `math.Sin` calls per pixel for kernel weights |
| `IFDTileSize` | 46.53s | 46.63s (3.4%) | Called per pixel, but constant for a given level |
| `sync/atomic.Add` (RWMutex) | 124.12s | 124.24s (9.1%) | Lock contention from 36 cache lookups/pixel |

## Optimizations Applied

### 1. Batched tile fetches (biggest win)

The 6×6 Lanczos kernel spans at most 4 source tiles (2×2 grid). The original code called
`readPixelCached` 36 times per output pixel, each performing:
- `IFDTileSize` lookup (constant per level)
- `fetchTileCached` with RWMutex lock + map lookup
- `pixelFromImage` with type assertion

The optimized version determines which unique tiles are needed (typically 1, at most 4),
fetches each once, and extracts all 36 pixels directly from the fetched tile images.
Also cached `tileW`/`tileH` in the `tileSource` struct to eliminate per-pixel `IFDTileSize` calls.

**Impact:** Cache lookups dropped from 468.92s to 8.08s (58x reduction).

### 2. Lanczos kernel LUT

Replaced `math.Sin` calls (2 per kernel weight, 12 per pixel) with a 1024-entry precomputed
lookup table with linear interpolation. The kernel is symmetric so only [0, 3) is stored.

**Impact:** Kernel evaluation dropped from 103.29s to 6.37s (16x reduction).

### 3. YCbCr-specialized fast path

When all 36 kernel pixels fall within a single tile (the common case), the type assertion
is done once and specialized accumulation functions handle pixel extraction with inline
YCbCr→RGB conversion. Separate fast paths for YCbCr, NYCbCrA, and RGBA tile types avoid
per-pixel interface dispatch and method call overhead.

**Impact:** `pixelFromImage` dropped from 99.98s to 3.02s (33x reduction).

## Final Profile

After optimization, the profile is well-balanced with no single dominant bottleneck:

| Function | Flat | Cum | % |
|----------|------|-----|---|
| `lanczosAccumYCbCr` | 75.36s | 76.84s | 29.3% |
| `runtime._ExternalCode` | 65.84s | 65.84s | 25.1% |
| `lanczosSampleCached` | 16.80s | 116.46s | 44.5% |
| `downsampleQuadrantLanczos` | 15.17s | 20.14s | 7.7% |
| `lanczos3LUT` | 6.28s | 6.37s | 2.4% |
| `fetchTileCached` | 0.51s | 12.27s | 4.7% |
| `pixelFromImage` (multi-tile fallback) | 3.02s | 4.02s | 1.5% |

The remaining hotspot `lanczosAccumYCbCr` (75.36s) is genuine YCbCr→RGB arithmetic
for 36 pixels per output pixel — irreducible without SIMD intrinsics.
