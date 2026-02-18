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

## Performance profile (2026-02-18, bicubic/WebP/512px)

Profiled with 36 Swiss GeoTIFFs, zoom 12-18, 4 workers. Total: 4690 tiles, 72s wall,
213s CPU (295% utilization). The two dominant bottlenecks are:

1. **WebP encoding via WASM** (~41% of CPU) — The pure-Go `gen2brain/webp` encoder
   runs libwebp inside a wazero WASM runtime. Each encode grows WASM memory, causing
   51 GB of total allocations. This is the single largest cost.

2. **Bicubic resampling** (~27% of CPU) — `bicubicAccumYCbCr` is the hot inner loop
   performing 16 YCbCr→RGB conversions per output pixel. Already optimized with direct
   array access; the cost is inherent to the kernel size × tile count.

A third cost is the decode-reencode tax for downsampling: tiles encoded as WebP must be
decoded back to RGBA for the next zoom level, then re-encoded. This adds ~6s CPU and
17 GB of allocations for the DiskTileStore decode path.

## Horizontal differencing predictor

LZW and Deflate compressed TIFFs may use a horizontal differencing predictor
(predictor=2) which stores each sample as the delta from the previous sample in the
same row. After decompression, the predictor is reversed by accumulating the deltas
row-by-row. This applies to both tile-based and strip-based reads. Without this step,
pixel values are raw deltas, producing garbled imagery.
