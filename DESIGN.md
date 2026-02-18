# Design Decisions

## Startup settings display

Always print the effective configuration (format, tile size, zoom range, resampling,
concurrency, memory limit, input count, output path) at startup. Placed after all
auto-detection (zoom, format) has resolved so the values shown are what will actually
be used. Printed unconditionally rather than gated behind `--verbose` because knowing
the active settings is essential for reproducibility and debugging.

## TFW (TIFF World File) support

TFW sidecar files provide georeferencing for plain TIFFs that lack embedded GeoTIFF
tags. When a `.tfw` file is found alongside a `.tif`, it is parsed for pixel scale
and origin. GeoTIFF tags always take precedence — TFW is only used as a fallback
when ModelPixelScale and ModelTiepoint are absent.

Rotated world files (non-zero rotation terms) are rejected with a clear error since
the pipeline assumes axis-aligned rasters.

## Strip-to-tile promotion

Plain TIFFs typically use strip layout (full-width rows) instead of tiles. Since the
rendering pipeline requires tile-level random access, strips are promoted to virtual
tiles at open time. Small strips (e.g. RowsPerStrip=2) are merged into groups of at
least 256 rows to ensure resampling kernels (Lanczos 6×6) never span more than 2×2
tiles. At read time, individual strips are read and decompressed separately then
concatenated, so non-contiguous strip storage is handled correctly.

## EPSG inference from coordinates

When GeoTIFF tags don't provide an EPSG code, the coordinate ranges from the TFW are
used as a heuristic: values in the -180..360 / -90..90 range map to EPSG:4326 (WGS84),
Swiss LV95 coordinate ranges to EPSG:2056, and Web Mercator ranges to EPSG:3857.

## Web Mercator latitude clamping

Latitudes beyond the Web Mercator valid range (~±85.05°) cause the tile coordinate
math to produce Inf/NaN values. In Go, converting +Inf to int wraps to MinInt, which
then gets clamped to 0 — silently reducing the tile grid to a single row. Fixed by
clamping latitude to ±85.0511° in `LonLatToTile` before the Mercator projection.
This was never triggered before because Swiss LV95 data stays well within the valid
Mercator range, but global datasets (like the Natural Earth raster covering ±90°)
require it.

## Native libwebp (replacing WASM)

The WebP encoder/decoder uses native libwebp via CGo instead of the previous WASM-based
approach (`gen2brain/webp` via `tetratelabs/wazero`). The WASM encoder was the #1 CPU
bottleneck (~41% of CPU, 81s) and caused 51 GB of WASM memory growth allocations due to
per-encode heap instantiation. Native libwebp eliminates this entirely: no WASM runtime
overhead, no per-call memory growth, and the C encoder runs 3-5x faster. The tradeoff is
that builds now require `CGO_ENABLED=1` and libwebp installed on the system
(`brew install webp` on macOS, `apt-get install libwebp-dev` on Linux).

## Performance profile (2026-02-18, bicubic/WebP/512px, pre-native-libwebp)

Profiled with 36 Swiss GeoTIFFs, zoom 12-18, 4 workers. Total: 4690 tiles, 72s wall,
213s CPU (295% utilization). The two dominant bottlenecks were:

1. **WebP encoding via WASM** (~41% of CPU) — Now replaced by native libwebp via CGo.

2. **Bicubic resampling** (~27% of CPU) — `bicubicAccumYCbCr` is the hot inner loop
   performing 16 YCbCr→RGB conversions per output pixel. Already optimized with direct
   array access; the cost is inherent to the kernel size × tile count.

A third cost is the decode-reencode tax for downsampling: tiles encoded as WebP must be
decoded back to RGBA for the next zoom level, then re-encoded. This adds ~6s CPU and
17 GB of allocations for the DiskTileStore decode path.

## Bicubic kernel LUT

Same approach as the Lanczos-3 LUT: a 1024-entry precomputed table over [0, 2) with
linear interpolation replaces the Catmull-Rom polynomial evaluation (`1.5x³ - 2.5x² + 1`
/ `-0.5x³ + 2.5x² - 4x + 2`) in the inner resampling loops. The kernel is symmetric
so only the positive half is stored. While the polynomial is cheaper than Lanczos sin()
calls, at ~3.25s cumulative CPU it was still worth eliminating.

## image.RGBA sync.Pool

`*image.RGBA` allocations (256 KB each for 256×256 tiles) are pooled via `sync.Pool`
to reduce GC pressure during tile generation. A `sync.Map` of pools keyed by `(w, h)`
handles the rare case of multiple tile sizes. `GetRGBA` zeros the pixel buffer with
`clear()` before returning; `PutRGBA` returns to the pool. Key recycling points:
- `newTileData`: returns the source RGBA when uniform/gray compaction succeeds
- `TileData.Release()`: returns internal img after encoding + store in the generator
- `renderTile`/`renderTileTerrarium`: pool allocation moved after overlap check so
  empty tiles never allocate; returned on `!hasData` early exit
- Downsample: destination from pool; child images expanded from gray/uniform tracked
  with `poolable` flags and returned after the loop (borrowed `TileData.img` pointers
  are not recycled)

## Horizontal differencing predictor

LZW and Deflate compressed TIFFs may use a horizontal differencing predictor
(predictor=2) which stores each sample as the delta from the previous sample in the
same row. After decompression, the predictor is reversed by accumulating the deltas
row-by-row. This applies to both tile-based and strip-based reads. Without this step,
pixel values are raw deltas, producing garbled imagery.
