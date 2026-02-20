# Profiling Analysis

**Date:** 2026-02-18
**Commit:** 22d6b2f (v0.7-2-g22d6b2f)
**Config:** 36 GeoTIFF files, bicubic resampling, WebP q85, 512px tiles, zoom 12-18, 4 workers
**Result:** 4690 tiles, 165.3 MB, 72.19s wall time, 213.28s total CPU samples (295% utilization)

## CPU Profile Summary

### Top Functions by Flat CPU Time

| Function | Flat | Flat% | Cum | Cum% | Notes |
|----------|------|-------|-----|------|-------|
| `runtime._ExternalCode` (WebP WASM) | 81.23s | 38.09% | 81.23s | 38.09% | WebP encoder via wazero |
| `bicubicAccumYCbCr` | 42.17s | 19.77% | 43.36s | 20.33% | Inner resampling loop |
| `bicubicSampleCached` | 14.00s | 6.56% | 74.48s | 34.92% | Bicubic dispatch + weight prep |
| `pthread_cond_signal` | 10.34s | 4.85% | 10.40s | 4.88% | Thread synchronization |
| `syscall.syscall` | 9.89s | 4.64% | 9.89s | 4.64% | File I/O syscalls |
| `downsampleQuadrantBicubic` | 7.51s | 3.52% | 10.27s | 4.82% | Downsample for lower zooms |
| `bicubic()` kernel | 3.25s | 1.52% | 3.27s | 1.53% | Catmull-Rom weight eval |
| `sync/atomic.Int32.Add` | 3.11s | 1.46% | 3.12s | 1.46% | Progress bar counters |
| `clampByte` | 1.74s | 0.82% | 1.80s | 0.84% | Pixel clamping |
| `SwissLV95.FromWGS84` | 1.54s | 0.72% | 1.54s | 0.72% | Projection transform |
| `drawRGBA` (image/draw) | 1.18s | 0.55% | 5.91s | 2.77% | tileDataToRGBA conversion |
| `mapaccess2` | 1.02s | 0.48% | 5.36s | 2.51% | Cache map lookups |
| `TileCache.Get` | 0.55s | 0.26% | 9.30s | 4.36% | Tile cache read path |
| `fetchTileCached` | 0.40s | 0.19% | 12.54s | 5.88% | Tile fetch + cache |

### CPU Time by Phase (estimated)

| Phase | CPU Time | % | Wall Time |
|-------|----------|---|-----------|
| WebP encoding (WASM) | ~88s | ~41% | embedded in all zooms |
| Bicubic resampling (max zoom) | ~58s | ~27% | ~46s (z18) |
| Downsampling (z17-z12) | ~10s | ~5% | ~26s |
| Tile cache + I/O | ~21s | ~10% | embedded |
| Disk store decode (WebP→RGBA) | ~6s | ~3% | embedded in z17-z12 |
| Runtime overhead | ~30s | ~14% | — |

### `bicubicAccumYCbCr` Line-Level Hotspots

The hottest lines are the YCbCr→RGB conversion and clamping inside the 4×4 kernel loop:

| Line | Time | Operation |
|------|------|-----------|
| 900 | 6.39s | `r` channel clamp |
| 876 | 5.66s | inner kx loop overhead |
| 907 | 4.66s | `g` channel clamp |
| 894 | 4.28s | `yy1 = int32(yData[yi]) * 0x10101` |
| 895 | 2.93s | `cb1 = int32(cbData[ci]) - 128` |
| 902 | 2.87s | `g = yy1 - 22554*cb1 - 46802*cr1` |
| 898 | 2.51s | `r = yy1 + 91881*cr1` |
| 882 | 1.58s | chroma offset computation |

## Memory Profile

### In-Use at Exit: 7.8 MB (excellent)
- 2.6 MB goroutine stacks
- 2.1 MB COG IFD offset arrays
- 1.2 MB CPU profiler buffer
- ~2 MB WebP WASM + gzip

### Total Allocations: 63.9 GB

| Source | Alloc | % | Notes |
|--------|-------|---|-------|
| wazero `MemoryInstance.Grow` | 51.0 GB | 79.8% | WebP WASM heap growth |
| `image.NewRGBA` | 9.5 GB | 14.9% | Rendering + downsample |
| wazero `NewMemoryInstance` | 1.1 GB | 1.8% | WebP module init per encode |
| `io.ReadAll` | 760 MB | 1.2% | Disk store reads |
| `image.NewYCbCr` | 358 MB | 0.6% | JPEG COG tile decoding |
| `DiskTileStore.Get` (cum) | 17.0 GB | 26.6% | Decode stored tiles for downsample |
| `DiskTileStore.decodeEncoded` (cum) | 16.9 GB | 26.4% | WebP decode back to RGBA |

## Key Findings

### 1. WebP Encoding is the #1 Bottleneck (~41% of CPU)

The pure-Go WASM-based WebP encoder (`gen2brain/webp` via `tetratelabs/wazero`) dominates:
- 81.23s in `_ExternalCode` (WASM execution)
- 7.01s in Go-side encode overhead
- 51 GB of WASM memory growth allocations
- Each encode call instantiates/grows WASM memory, creating extreme GC pressure

### 2. Bicubic Resampling is the #2 Bottleneck (~27% of CPU)

`bicubicAccumYCbCr` is already well-optimized with direct array access and inline YCbCr→RGB. The remaining cost is inherent to the 4×4 kernel (16 samples per pixel × 512² pixels × 3540 tiles at z18 = ~14.8 billion pixel reads).

### 3. Disk Store Decode Creates a Double-Encode Tax

For zoom levels below max, tiles are re-decoded from WebP for downsampling:
- `DiskTileStore.decodeEncoded`: 6.12s CPU, 16.9 GB alloc
- This means each parent tile requires decoding 4 WebP children, then re-encoding as WebP — a "decode-downsample-reencode" pipeline that pays the WebP cost twice.

### 4. `image.NewRGBA` Allocation Pressure

9.5 GB of RGBA allocations across the run. At 512×512×4 = 1 MB per image, this is ~9500 images. No apparent image pooling.

### 5. `image/draw.drawRGBA` Overhead (5.91s)

Used in `tileDataToRGBA` to convert decoded tiles to RGBA format. The `image/draw` package uses the slow `NYCbCrA.RGBA64At` path (4.71s) which involves type assertions and method dispatch.

### 6. Progress Bar Atomic Counter (3.11s)

`sync/atomic.Int32.Add` at 1.46% seems excessive for a progress counter. This is called per-tile and contended across all workers.

## Optimization Opportunities (ranked by impact)

### High Impact

1. **CGo WebP encoder** — Replace `gen2brain/webp` (WASM) with a CGo binding to libwebp. Would eliminate the 51 GB WASM memory churn and likely 3-5× faster encode. Would require `CGO_ENABLED=1`.

2. **Bicubic LUT** — Add a precomputed LUT for `bicubic()` (like `lanczos3LUT`). Currently 3.25s evaluating the cubic polynomial. A 1024-entry LUT with linear interpolation would eliminate this.

3. **Keep raw pixel data for downsampling** — Instead of encode→store→decode for downsampling, keep the decoded RGBA/Gray in memory (or a separate uncompressed spill) for the immediate parent level. This eliminates the decode-reencode tax (6s CPU + 17 GB alloc).

### Medium Impact

4. **RGBA image pool** — Pool `image.RGBA` allocations with `sync.Pool` to reduce 9.5 GB of RGBA alloc/GC pressure.

5. **Direct pixel copy in `tileDataToRGBA`** — Bypass `image/draw.DrawMask` for YCbCr→RGBA conversion with a direct loop (avoids the slow `RGBA64At` path that costs 4.71s).

6. **Batch progress updates** — Update the progress counter every N tiles instead of per-tile to reduce atomic contention (3.11s).

### Lower Impact

7. **Precompute chroma offsets in `bicubicAccumYCbCr`** — The `switch ratio` inside the inner loop evaluates on every kernel position. Could precompute `ci` offsets for the 4×4 grid once.

8. **Reduce `mapaccess2` cost** — The 5.36s in map access comes from `TileCache.Get`. The sharding helps but the Go map hash for struct keys still has overhead. Consider a flat array cache indexed by (col % N, row % N) for the common single-source case.
