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

## Performance profile

Pre-native-libwebp profiling identified two dominant bottlenecks: WebP encoding via WASM
(~41% of CPU, now eliminated by native libwebp via CGo) and bicubic resampling (~27%,
inherent to kernel size, optimized with LUTs and direct array access). A secondary cost
is the decode-reencode tax for pyramid downsampling when using lossy formats.

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
instead of returning them as "found". This prevents nodata in one source from blocking
valid data in another (multi-source hole filling) and ensures tiles containing only
nodata are correctly detected as empty rather than cascading falsely non-empty tiles
through the pyramid — especially important for JPEG output which cannot represent
transparency.

## Empty as color transformation (not resampling)

Empty tiles and transparent/nodata pixels should be modeled as a **color transformation**
(source color → target value) rather than as inputs to spatial resampling.

- **Rationale**: Resampling (bilinear, Lanczos, bicubic, mode) combines *valid* data.
  Interpolating or voting over "empty" produces meaningless results: blending forest
  with nodata gives garbage; mode over [data, empty, empty, data] depends on how
  empty is represented. Empty is a semantic value ("no data") that should be
  substituted, not interpolated.

- **Model**: Treat empty/nodata/transparent as a distinct "source color" and map it
  to a configurable target (e.g. `--fill-color`). Transformation = lookup substitution;
  resampling = spatial kernel over valid pixels only.

- **Current behavior**: Nil children in downsampling contribute nothing (quadrant
  stays transparent); `fillEmptyTiles` writes solid fill for missing tile positions.
  Downsampling does not yet apply a fill-color transformation to transparent pixels
  within tiles or nil-child quadrants — transparent remains transparent.

- **Implemented**: Color transform applied only at the source level in both
  `geotiff2pmtiles` and `pmtransform`. When `FillColor` is set: (1) max-zoom
  rendered/decoded tiles — transparent pixels → fill before packing; (2) max-zoom
  positions with no source data → solid fill tile; (3) downsampling — nil children
  are substituted with fill tiles *before* calling downsample, so the existing
  downsample code receives 4 tiles and operates normally. No transform in the
  downsample path.

## Tile size in resolution calculation

`ResolutionAtLat()` accepts the actual tile size (e.g. 256 or 512) instead of
hardcoding `DefaultTileSize=256`. This ensures that `OverviewForZoom` selects the
correct overview level for non-standard tile sizes. With 512-pixel tiles at zoom z,
each pixel covers half the ground distance compared to 256-pixel tiles — using the
wrong tile size caused 2x too coarse overview selection, producing blurry output.

## image.RGBA sync.Pool

`*image.RGBA` allocations (256 KB each for 256×256 tiles) are pooled via `sync.Pool`
to reduce GC pressure during tile generation. A `sync.Map` of pools keyed by `(w, h)`
handles multiple tile sizes. `GetRGBA` zeros the pixel buffer with `clear()` before
returning; `PutRGBA` returns to the pool. Zeroing is critical: `renderTile` only writes
pixels where source data is found, so unfound pixels must be transparent (0,0,0,0) —
without clearing, recycled images retain stale pixel data from previous tiles, causing
visible artifacts at data boundaries.

## Disk tile store memory accounting

The disk tile store tracks memory usage to enforce the configured limit. Three fixes:

**Map pre-allocation**: Go map hints sized to the total tile count (e.g. 112M) waste
gigabytes on empty hash buckets. Fixed by capping pre-allocation to the working set
(tiles that fit in the memory limit).

**Map overhead tracking**: The memory counter only tracked encoded byte slices, ignoring
Go map entry overhead (~128 bytes/uniform entry, ~64 bytes/disk index entry). Added a
`mapOverhead` counter included in the memory limit check.

**Gray tile RGBA leak**: `AsImage()` for gray tiles allocated an RGBA buffer via
`GetRGBA()` that was never returned to the pool. Fixed by caching the expanded image
in `t.img` so that `Release()` returns it.

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

## Downsample edge handling (no extent extension)

When Lanczos-3 and bicubic resampling kernels extend beyond the source tile boundary
during pyramid downsampling, out-of-bounds kernel positions are treated as empty
(alpha 0 for RGBA, skipped with weight renormalization for gray) instead of clamping
to the edge pixel. Edge-pixel clamping was visually extending the source extent by
repeating boundary colors into the resampling kernel. With the empty treatment, RGBA
edges fade to transparent naturally, and gray edges are computed from only valid
positions. Bilinear, nearest, and mode are unaffected since their source coordinates
never exceed tile bounds.

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

## Description metadata provenance

PMTiles archives record processing provenance in the `description` field of the metadata
JSON. When `geotiff2pmtiles` creates an archive, the description captures both the
processing options (format, quality, tile size, zoom, resampling) and source information
(file count, EPSG, CRS/WGS84 extents, pixel size, data format, coverage holes). When
`pmtransform` transforms an archive, it reads the source description via `ReadMetadata()`
and prepends the new processing steps above it. This creates a stacked provenance trail
where each transformation layer is visible in the output metadata, enabling users to
trace the full history of how an archive was produced.

## pmtransform: separate binary

`pmtransform` is a standalone CLI rather than a subcommand of `geotiff2pmtiles`. The
GeoTIFF-to-PMTiles pipeline involves CRS detection, reprojection, and COG tile caching —
none of which apply when transforming an existing PMTiles archive. Keeping them as separate
binaries avoids bloating either tool with the other's concerns and makes the usage
clear: `geotiff2pmtiles` for initial conversion, `pmtransform` for post-processing.

## pmtransform: passthrough fast path

When no format change or resampling is needed (e.g. just removing zoom levels), raw tile
bytes are copied from the source archive to the output without decoding or re-encoding.
This avoids lossy re-compression artifacts and is significantly faster.

## pmtransform: max-zoom anchored rebuild

When rebuilding the pyramid (resampling change or adding lower zoom levels), the tool
reads max-zoom tiles from the source archive and uses them as the base layer. Lower zoom
levels are then rebuilt by downsampling from the level above — exactly matching how
`geotiff2pmtiles` works with COG sources. This ensures consistent quality across the
pyramid regardless of what resampling was used in the original archive.

## pmtransform: tile size discovery

The PMTiles v3 header does not store tile size (only format via `TileType`). When
`--tile-size` is omitted (default: keep source), pmtransform discovers the source tile
size by reading and decoding one tile from the max zoom level and using its image
dimensions. If no tile can be decoded (e.g. all empty), it falls back to 256. This
ensures 512px archives stay 512px when rebuilding with `--resampling lanczos` instead
of inadvertently reducing to 256.
