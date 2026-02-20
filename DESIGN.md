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

A `!cgo` stub (`webp_stub.go`) provides graceful error messages when building with
`CGO_ENABLED=0` — the binary compiles but WebP encode/decode returns an error at runtime.
This allows CI cross-compilation without a C toolchain while keeping WebP available for
native builds.

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

## Nodata handling for image (non-float) data

For single-band GeoTIFF data (e.g. ESA WorldCover land cover classifications), pixels
matching the GDAL_NODATA value are decoded as transparent (alpha=0) instead of opaque
black (alpha=255). This is handled in `decodeRawTile` by parsing the nodata string
from the first IFD and comparing each pixel against it. Only applied for spp≤2
(single-band and gray+alpha) since multi-band nodata semantics are more complex.
The nodata value must be an integer in [0, 255] to qualify. This ensures all downstream
code (resampling, downsampling, encoding) automatically treats nodata areas as
transparent — bilinear/Lanczos/bicubic already exclude alpha=0 pixels from RGB
interpolation, and `sampleFromTileSources` skips fully transparent results to try the
next source (see below).

## Nodata-aware source fallthrough

`sampleFromTileSources` skips results with alpha=0 and continues to the next source
instead of returning them as "found". This is critical for two reasons:

1. **Multi-source hole filling**: When source A has nodata at a location but source B
   has valid data, source A's transparent pixel must not block source B from contributing.
   Without this check, the first source whose bounding box covers the coordinate "wins"
   even if it has no actual data there (e.g. empty COG tiles within the raster extent,
   or pixels matching the GDAL_NODATA value).

2. **Empty tile elimination**: Tiles that fall entirely within a source's bounding box
   but contain only nodata/transparent pixels are correctly detected as empty
   (`renderTile` returns nil, `hasData` stays false). This prevents a cascade of
   falsely non-empty tiles through the entire zoom pyramid during downsampling, and
   avoids encoding/writing tiles that carry no visible information — especially
   important for JPEG output which cannot represent transparency (nodata areas would
   appear as solid black).

## Tile size in resolution calculation

`ResolutionAtLat()` accepts the actual tile size (e.g. 256 or 512) instead of
hardcoding `DefaultTileSize=256`. This ensures that `OverviewForZoom` selects the
correct overview level for non-standard tile sizes. With 512-pixel tiles at zoom z,
each pixel covers half the ground distance compared to 256-pixel tiles — using the
wrong tile size caused 2x too coarse overview selection, producing blurry output.

## image.RGBA sync.Pool

`*image.RGBA` allocations (256 KB each for 256×256 tiles) are pooled via `sync.Pool`
to reduce GC pressure during tile generation. A `sync.Map` of pools keyed by `(w, h)`
handles the rare case of multiple tile sizes. `GetRGBA` zeros the pixel buffer with
`clear()` before returning; `PutRGBA` returns to the pool. Zeroing is critical:
`renderTile` only writes pixels where source data is found, so unfound pixels must
be transparent (0,0,0,0) — without clearing, recycled images retain stale pixel
data from previous tiles, causing visible artifacts at data boundaries. Key recycling
points:
- `newTileData`: returns the source RGBA when uniform/gray compaction succeeds
- `TileData.Release()`: returns internal img after encoding + store in the generator
- `renderTile`/`renderTileTerrarium`: pool allocation moved after overlap check so
  empty tiles never allocate; returned on `!hasData` early exit
- Downsample: destination from pool; child images expanded from gray/uniform tracked
  with `poolable` flags and returned after the loop (borrowed `TileData.img` pointers
  are not recycled)

## Disk tile store memory accounting

The disk tile store tracks memory usage to enforce the configured limit. Two key insights
that required fixes:

**Map pre-allocation**: When creating the store for zoom 20 with 112M tiles, Go maps were
pre-allocated with `make(map[K]V, 112M)`. Each map bucket is ~400 bytes (8 entries × key
+ value + metadata), so a 112M-hint `encoded` map alone pre-allocated ~13.4 GB of empty
hash buckets. Combined with the `uniforms` map (~2.3 GB), this was ~16 GB of waste at
creation time, before any tiles were stored. Fixed by capping pre-allocation to the
number of tiles that actually fit in the memory limit (~600K for 12 GB at 20 KB/tile).

**Map overhead tracking**: The memory counter (`memBytes`) only tracked encoded byte slice
lengths (and 4 bytes per uniform tile), completely ignoring Go map entry overhead: ~128
bytes per uniform entry (key + pointer + TileData struct + bucket metadata) and ~64 bytes
per disk index entry. Over 112M tiles, the `index` map alone uses ~7 GB untracked. Added
a separate `mapOverhead` counter that includes these estimates and is included in the
memory limit check.

**Gray tile RGBA leak**: `AsImage()` for gray tiles called `ToRGBA()` which allocated a
256 KB RGBA via `GetRGBA()` that was never returned to the pool. Fixed by caching the
expanded image in `t.img` so that `Release()` returns it.

## Mode (most common value) resampling

For categorical/classified rasters (e.g. ESA WorldCover land cover), interpolation
methods like bilinear or Lanczos produce values that don't correspond to any valid
class. Mode resampling (`--resampling mode`) picks the most frequently occurring
value in each 2×2 block during pyramid downsampling, preserving the dominant category
at each zoom level. At the max zoom level (COG rendering), mode behaves like
nearest-neighbor since each output pixel maps to approximately one source pixel.

For the gray fast path, a branchless counting approach determines the mode of 4 uint8
values without heap allocation. For RGBA, a small stack-allocated array of up to 4
entries avoids maps. Ties prefer the earlier pixel (top-left bias) for deterministic
output. Transparent pixels (alpha == 0) are excluded from the vote so nodata areas
don't dominate the result.

## Nearest-neighbor edge clamping

`nearestSampleCached` and `nearestSampleFloat` compute the integer pixel via
`Floor(fx + 0.5)` (round-to-nearest). The caller's bounds check ensures
`pixX ∈ [0, imgW)`, but when `pixX >= imgW - 0.5` the rounding produces
`px = imgW` — one past the last valid pixel. TIFF tiles extend to a multiple
of the tile width, so the read succeeds but returns zero-padded data, creating
a ~1 px band of wrong values at the right/bottom edge of every source file.
Fixed by adding `imgW`/`imgH` parameters and clamping `px`/`py` to
`[0, imgW-1]`/`[0, imgH-1]`, matching the approach already used by the
bilinear, bicubic, and Lanczos sampling functions.

## Horizontal differencing predictor

LZW and Deflate compressed TIFFs may use a horizontal differencing predictor
(predictor=2) which stores each sample as the delta from the previous sample in the
same row. After decompression, the predictor is reversed by accumulating the deltas
row-by-row. This applies to both tile-based and strip-based reads. Without this step,
pixel values are raw deltas, producing garbled imagery.
