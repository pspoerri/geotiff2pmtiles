# Design Decisions

## Resampling gamma correction

When converting from dB-space (e.g. SAR backscatter) to RGB for display, the resulting
pixel values can appear too dark because dB values concentrate in the lower end of the
byte range. The `--resampling-gamma` flag applies a power-law gamma encode to the
interpolated output: `output = (interpolated/255)^(1/gamma) * 255`. This brightens
midtones and improves visual contrast without altering the interpolation kernel itself.

Only the encode step is applied — there is no decode of source pixels. This keeps the
accumulation arithmetic unchanged and avoids an extra LUT lookup per pixel per channel
in the inner loop. The encode table uses 4096 entries for sub-byte precision.

Alpha is never gamma-corrected (it is linear by definition). Nearest/mode resampling
and Terrarium (elevation) paths skip gamma entirely. The default gamma of 1.0 produces
bit-identical output to the previous behavior.

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

Planar-separate strip files (PlanarConfiguration=2, GDAL INTERLEAVE=BAND — GDAL's
default when copying from a band-interleaved source) store each band's strips
consecutively, plane-major. Virtual tiles are derived from one plane's worth of
strips; at read time each plane's strips are decompressed (with the predictor
undone at samplesPerPixel=1) and interleaved into chunky order so downstream
decoding is layout-agnostic. JPEG-compressed planar strips are rejected, since
encoded planes cannot be byte-interleaved.

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
This keeps cross-compilation possible without a C toolchain (`make cross-all CGO=0`),
which is how the released Linux and macOS binaries are still built. Because the automatic
jpeg → webp switch for nodata data would otherwise be fatal in such a build, it falls
back to PNG (which also carries alpha) with a warning. `encode.Formats()` reports which
formats a given binary actually has, and both CLIs print it under `--version`, so a
stubbed build is identifiable without triggering the runtime error.

## Windows WebP: MSYS2 prebuilts, statically linked

The Windows binaries used to ship at `CGO_ENABLED=0` without WebP, because the CI build
job cross-compiled every target from Linux and there is no packaged mingw libwebp there.
They are now built natively instead: `windows-latest` (UCRT64) and `windows-11-arm`
(CLANGARM64) via `msys2/setup-msys2`, installing MSYS2's prebuilt `libwebp` package. No
cross toolchain, no libwebp source build, and — unlike a cross-build — the tests and a
smoke run execute on the same machine and architecture that produced the binary.

`-extldflags=-static` pulls libwebp, libsharpyuv and the compiler runtime into the `.exe`
so users do not have to install MSYS2 to run it. The CI smoke test runs the binary with
the MSYS2 `bin` directory off `PATH`: a dynamically linked build fails to start there,
which is what makes the check meaningful rather than decorative.

Windows skips `pkg-config` (`#cgo !windows pkg-config: libwebp` / `#cgo windows LDFLAGS:
-lwebp -lsharpyuv`). Two reasons: MSYS2 puts the headers and libraries on the compiler's
default search path anyway, and cgo invokes `pkg-config` without `--static`, so it would
drop the `-lsharpyuv` from `Libs.private` and the static link would fail on undefined
symbols. The usual workaround — pointing `PKG_CONFIG` at a wrapper that adds `--static` —
does not work on Windows, because Go is a native Windows binary and cannot exec a shell
script. Naming both libraries directly is shorter and works on every environment.

## Windows support

Windows has no `mmap(2)`; `mmap_windows.go` uses `CreateFileMapping` +
`MapViewOfFile` (`PAGE_READONLY`/`FILE_MAP_READ`) from the standard `syscall`
package, avoiding a `golang.org/x/sys` dependency. The mapping handle is closed right
after the view is created — the view holds its own reference to the section —
so `Open` can keep closing the `os.File` immediately, as on Unix. The `[]byte`
is assembled from a hand-written slice header rather than `unsafe.Slice`,
because converting the `uintptr` that `MapViewOfFile` returns into an
`unsafe.Pointer` trips `go vet`'s `unsafeptr` check; the mapping lives outside
the Go heap, so the GC has nothing to track either way.

Two POSIX habits do not survive the port and are handled explicitly rather than
abstracted away:

- **A mapped or open file is locked.** Go opens files without
  `FILE_SHARE_DELETE`, so renaming or deleting over an open handle fails.
  `pmheader` closes its input before the in-place rename, and
  `DiskTileStore.Close` now warns when a spill file cannot be removed instead
  of discarding the error.
- **Paths are case-insensitive and accept both separators.** String equality is
  not a same-file test, so `pmheader` and `pmtransform` compare identity with
  `os.SameFile`, and `.pmtiles` extension checks use `strings.EqualFold` on
  `filepath.Ext`. `collectTIFFs` also expands `*`/`?`/`[` itself, since neither
  cmd.exe nor PowerShell globs arguments.

RAM detection uses `GlobalMemoryStatusEx` through `syscall.NewLazyDLL`
(kernel32 is already loaded in every process, so no DLL search occurs).

The progress bar dropped its `\033[K` erase-to-end-of-line in favour of padding
the redraw with spaces: Windows consoles only process VT sequences when
`ENABLE_VIRTUAL_TERMINAL_PROCESSING` is set, and padding needs no platform code
at all.

## Predictors are undone on a copy, never in the mapping

`applyPredictor` rewrites its argument in place. For compressed tiles the
argument is a freshly decompressed buffer, but for `Compression == 1` the tile
bytes alias the read-only mapping — writing there is a SIGBUS on Unix and an
access violation on Windows. The tiled 8/16-bit path already copied first; the
float path did not, so an uncompressed float32 COG with `Predictor=2` or `3`
crashed the process. Both paths now copy when a predictor is present, and only
then (an uncompressed tile without a predictor is still handed out as a
zero-copy view of the mapping).

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

## Nodata handling for float (Terrarium) data

Float kernels (bilinear, bicubic, Lanczos) skip taps that are NaN or equal to the
source GDAL_NODATA value; Lanczos/bicubic renormalise over the remaining taps, bilinear
falls back to nearest. An unset nodata is stored as NaN, which compares equal to nothing,
so the check is a no-op in that case. The parsed sentinel is rounded through float32
(`parseFloatNodata`) because pixels are float32: a float64 parse of e.g. `-3.4028235e+38`
would otherwise never equal the widened pixel value.

## Nodata handling for image (non-float) data

Pixels matching the GDAL_NODATA value are decoded as transparent (alpha=0) so downstream
resampling, downsampling, and encoding automatically treat them as empty.

**Legacy path** (spp≤2, 8-bit, default BandConfig): nodata parsed from the GDAL_NODATA
tag (IFD field, integer in [0,255]); single-band sets alpha=0 for matching pixels,
gray+alpha also overrides the alpha channel.

**General path** (multi-band or 16-bit, including presets): nodata is stored in
`BandConfig.HasNodata`/`BandConfig.Nodata`. `DetectPreset()` automatically populates
these from the GDAL_NODATA tag (integer in [0,65535]) so the preset is self-contained.
The `--nodata` CLI flag overrides the auto-detected value. In the pixel loop, all `spp`
file bands are compared to the raw nodata value before rescaling; if all are within
`BandConfig.NodataTolerance` of the nodata value, the pixel is emitted as (0,0,0,0).
Only applied when there is no dedicated alpha band (`effectiveAlpha < 0`), since an
alpha band already encodes transparency directly.

**Tolerance for lossy-JPEG borders**: COGs scanned from historic imagery often have
black-padded borders that are *intended* as nodata but, because the file is JPEG-
compressed, decode as 1..8 rather than strict 0. `BandConfig.NodataTolerance` (CLI:
`--nodata-tolerance`) widens the match to `|sample - nodata| ≤ tolerance` so those
borders disappear cleanly. Default 0 keeps exact-match semantics.

**JPEG decode path**: previously returned the JPEG-decoded image untouched, silently
ignoring `BandConfig.HasNodata`. `decodeJPEGTile` now materialises the image as RGBA
and zeroes alpha+RGB for nodata pixels when `HasNodata` is set; without nodata it
still returns the raw `*image.YCbCr`/`*image.Gray` to preserve the fast sampling path.

**Planar-separate JPEG** (`PlanarConfiguration=2`): each band lives in its own
per-plane JPEG tile, with offsets laid out plane-major. `ReadTile` dispatches to
`decodePlanarSeparateJPEG`, which decodes each plane (using `grayBytes` to extract
the single channel from `*image.Gray` or single-band `*image.YCbCr`) and merges into
RGBA: plane 0→R, 1→G, 2→B, 3→A. Missing channels are duplicated from plane 0 so
grayscale planar-separate files render correctly. Nodata is applied after the merge.
Uncompressed and Deflate/LZW planar-separate are rare in practice and currently
return an explicit "not supported" error rather than silently decoding wrong data
(the previous behaviour silently returned plane 0 as R=G=B).

**Output-format auto-switch**: when `--nodata` is active, JPEG output cannot carry
alpha, so transparent pixels would be baked back to black in the encoded tile. If
the user did not explicitly set `--format`, the CLI switches the default `jpeg` →
`webp`. If the user explicitly chose `--format=jpeg`, it warns and proceeds.

## Source-level nodata flood fill

Strict per-pixel tolerance matching has a failure mode at the boundary of a
black-padded scan: pixels just inside the true border (values ~10–60 from
JPEG quantisation smear) aren't caught by a small tolerance, leaving a
speckle fringe along the alpha boundary, while widening the tolerance starts
absorbing legitimate interior dark pixels (text, shadows, forest canopy).

`--nodata-flood` solves this by treating the strict tolerance as a *candidate
set* and only making transparent the pixels in that set that are reachable
through it from the COG's outer image boundary. Implementation:

1. **Build** (`Reader.BuildFloodMask`): decode every source tile once with
   `HasNodata` temporarily cleared, populate a packed candidate bitmap (1 bit
   per source pixel) for "all RGB within tolerance". Tile decoding — the
   dominant cost — is fanned out over a worker pool; each worker assembles
   word-local bit accumulators and merges them with `atomic.OrUint64`, so
   horizontally adjacent tiles that share boundary words never race. Then a
   (serial) 4-connected scanline flood fill seeded from every boundary pixel
   (row 0, row H-1, col 0, col W-1) that's in the candidate set writes a
   second packed bitmap which is retained on the Reader. Memory: 2 × W·H/8
   bytes during build, 1 × W·H/8 bytes resident. The CLI additionally builds
   masks for up to 4 sources concurrently, dividing the `--concurrency`
   budget among them.
2. **Use**: the three decode paths (`decodeJPEGTile`, `decodePlanarSeparateJPEG`,
   `decodeRawTile`) skip their own nodata logic when `Reader.floodMask != nil`
   — leaving RGB *intact*. `ReadTile` then zeroes alpha at the marked
   coordinates: at level 0 via `applyFloodMaskRGBA`, which walks the mask a
   64-pixel word at a time (all-zero words — the common case on interior
   tiles — cost one load; set bits are visited by trailing-zeros iteration);
   at overview levels via `applyFloodMaskRGBAScaled`, which samples the
   level-0 mask at the center of each overview pixel's footprint so
   transparency survives reads through `OverviewForZoom` (e.g. when
   `--max-zoom` sits below the source's native resolution). Skipping the
   per-pixel masking in the decode paths is essential: if they zeroed RGB for
   every candidate, interior false positives would be permanently destroyed
   and the mask couldn't recover them.

The scanline flood is preferred over breadth-first / depth-first traversal
because its peak queue size scales with the *perimeter* of the flooded region,
not its area. For a 24081×18046 source with a thick black border, the queue
stays well under 1 MB while filling tens of millions of pixels.

Trade-offs vs per-tile flood fill:
- Per-tile flood at the output zoom level is cheaper but has tile-seam
  artefacts and can mark interior tiles that happen to be fully outside the
  scan as opaque (no edge pixel to seed from).
- Source-level flood is global and seam-free, but costs an upfront decode pass
  (~6.5 s single-threaded for a 158 MB JPEG-compressed BigTIFF; the parallel
  build divides that by roughly the worker count) plus the resident bitmap.
- Building at a downsampled overview would be cheaper still, but historic
  scans like the motivating Katahdin file have no overviews. A future
  optimisation: build the mask on the smallest available overview and
  upsample. For now, full-resolution is simple and correct.

All downstream code (bilinear/Lanczos/bicubic resampling, mode downsampling,
`sampleFromTileSources`) excludes alpha=0 pixels from interpolation and voting, and
tries the next source on fully-transparent results (see below).

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

## TIFF predictor support

LZW, Deflate and ZSTD compressed TIFFs may use predictors to improve compression. After
decompression, the predictor encoding is reversed before interpreting pixel values.

## ZSTD compression

TIFF compression 50000 (GDAL/libtiff ZSTD) stores each tile or strip as an
independent zstd frame. Go's stdlib has no zstd decoder, so this is the one
place the project takes an external Go dependency: `github.com/klauspost/compress/zstd`,
pure Go, so it works in the `CGO_ENABLED=0` Windows builds. A single shared
decoder is used via `DecodeAll`, which is safe for concurrent use.

The Deflate/zlib path uses the same module's `flate`/`zlib` packages, a drop-in
for the stdlib ones that decodes ~20–25% faster with far fewer allocations.
LZW keeps the in-tree TIFF-variant decoder (`lzw.go`); klauspost/compress has
no LZW.

**Predictor=2** (horizontal differencing) stores each sample as the delta from the
previous sample in the same row. Accumulating the deltas row-by-row recovers the
original values. Supported for 8-bit, 16-bit, and 32-bit data, with multi-byte deltas
accumulated at the sample width using the TIFF byte order.

**Predictor=3** (floating-point predictor) is the standard for float32/float64 data
compressed with Deflate or LZW (GDAL default). It first byte-shuffles each row into
byte planes — most-significant byte plane first, regardless of file byte order
(matching libtiff's fpDiff/fpAcc) — then applies byte-level horizontal differencing
at a stride of samplesPerPixel. Reversing it: (1) undo byte differencing by
accumulating bytes at the same stride, (2) unshuffle the MSB-first planes back into
the file's byte order. Getting the plane order wrong scrambles exponent bytes into
the mantissa, turning float tiles into full-range noise.

## BandConfig: band reordering, alpha, and rescaling

Multi-band GeoTIFFs (e.g. 4-band RGBNIR uint16) need band selection, alpha handling,
and value rescaling to produce usable 8-bit RGBA output. `BandConfig` is set once
after `OpenAll()` and before tile generation — it's immutable during concurrent reads.

**Band reordering**: `Bands [3]int` maps 1-indexed input bands to R,G,B output channels.
For false-color composites, `--bands 4,1,2` maps NIR→R, R→G, G→B. Zero values default
to `1,2,3`.

**Alpha band**: `AlphaBand` controls transparency. `0` = auto (band 4 for 8-bit spp≥4,
none for 16-bit), `-1` = force no alpha, `>0` = explicit 1-indexed band. When an alpha
band is configured and the source value is 0, the pixel is fully transparent.

**Rescaling**: `buildRescaler` returns a `func(uint16) uint8` closure. Linear mode maps
`[min,max] → [0,255]` proportionally. Log mode uses `ln(1 + v - min) / ln(1 + max - min)`
for better dynamic range in data with large value spans (e.g. satellite reflectance).
Auto mode selects linear 0-65535 for 16-bit data, none for 8-bit.

**Backwards compatibility**: Zero-value `BandConfig` activates the legacy code path for
8-bit data, including nodata handling for spp≤2. No behavioral change for existing
8-bit workflows.

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

## pmtransform: terrarium-aware rebuild

Terrarium tiles pack elevation across RGB (`elevation = R*256 + G + B/256 - 32768`), so
the ordinary per-channel downsampling corrupts values wherever the four source pixels
straddle a 256 m channel boundary: averaging G=255 and G=0 yields ~127 with no carry
into R, an error of up to ~128 m. Rebuild therefore switches to the generator's
elevation-space downsampler (`downsampleTileTerrarium`) when the archive is terrarium.

The PMTiles header can't express this — terrarium's `TileType` is just PNG — so
`geotiff2pmtiles` records `"encoding": "terrarium"` in the metadata JSON. pmtransform
auto-detects that key, propagates it to the output archive, and offers an explicit
`--terrarium` flag for archives written before the key existed. The flag and key only
widen detection; there is no way to force RGBA downsampling on a marked archive, since
that is never correct. Re-encoding terrarium to a lossy format (jpeg/webp) warns instead
of erroring — the output is visually usable, just no longer valid elevation data.

## pmtransform: tile size discovery

The PMTiles v3 header does not store tile size (only format via `TileType`). When
`--tile-size` is omitted (default: keep source), pmtransform discovers the source tile
size by reading and decoding one tile from the max zoom level and using its image
dimensions. If no tile can be decoded (e.g. all empty), it falls back to 256. This
ensures 512px archives stay 512px when rebuilding with `--resampling lanczos` instead
of inadvertently reducing to 256.

## Preset auto-detection

`DetectPreset()` examines the GeoTIFF structure and GDAL metadata (tag 42112) to
auto-configure processing. It returns a `Preset` with a name, optional format
override, and optional `BandConfig`.

**Float detection**: If the sample format is IEEE floating point (SampleFormat=3),
returns the `float-terrarium` preset with `Format: "terrarium"`. The CLI applies
this format override when using the default format (jpeg), replacing the previous
inline float detection logic.

**GDAL_METADATA parsing**: TIFF tag 42112 contains an XML blob with `<Item>` elements.
Items have a `name` attribute and optional `sample` (0-indexed band) and `role`
attributes. The XML is parsed into `GDALMeta` with two levels: `Items` (dataset-level)
and `BandItems` (per-band, keyed by sample index).

**Multi-band detection** auto-configures band mapping and rescaling from two sources of
band descriptions:

1. **Per-band DESCRIPTION items** — `<Item name="DESCRIPTION" sample="N">` values
   containing color role keywords (red, green, blue, nir, near-infrared, infrared).
   This is the GDAL standard and works with any GDAL-created multi-band GeoTIFF:
   Google Earth Engine exports, PlanetScope, HLS, gdal_translate output, etc.

2. **Dataset-level "bands" string** — fallback for files that use a `bands` item
   containing `"Band N: BXX (Role)"` entries (e.g. ESA WorldCover composites).
   Roles are matched through the same keyword table as per-band DESCRIPTION items.

Strategy 1 is tried first; if it finds Red+Green+Blue, detection succeeds. Otherwise
strategy 2 is attempted. This avoids any product-specific hardcoding — detection is
purely driven by the band descriptions present in the GeoTIFF.

Scale/offset detection checks per-band `SCALE`/`OFFSET` items first (sample 0), then
falls back to dataset-level items. The rescale range is derived as `[min, max]` where
`max = round(1/scale)` and `min = round(-offset/scale)` when offset is negative (e.g.
Landsat Collection 2: scale=0.0000275, offset=-0.2 → range [7273, 36364]). When no
scale is found, defaults to 0-10000 (the most common reflectance quantification).

The CLI tries auto-detection in `parseBandConfig` only when `--rescale auto` (default),
16-bit data, no explicit `--rescale-range`, and no explicit `--bands` override. Explicit
flags always take precedence.

## Transform rebuild: sparse fill optimization

When `pmtransform --rebuild --fill-color` processes sparse datasets, most tile
positions in bounds contain no source data and produce identical fill tiles. Rather
than processing every position through the full downsample → encode → store pipeline,
positions are partitioned into "real" (need decode/downsample/encode) and "fill" (write
pre-encoded bytes directly).

At max zoom, source tiles are "real" and all others get pre-encoded fill bytes written
directly. At lower zooms, a parent is "real" iff at least one of its 4 children was real
— propagated upward via a `realPositions` set. This reduces the expensive path from
O(positions_in_bounds) to O(real_positions) per zoom level, and fill tiles never enter
the DiskTileStore (avoiding 128 bytes of map overhead per entry).

The fill tile is pre-encoded once before the zoom loop (matching the pattern in
`generator.go`) and a shared immutable `fillTileShared` TileData replaces per-position
allocations for nil-child substitution during downsampling.

## Root directory 16 KiB budget

The PMTiles v3 spec requires the header (127 bytes) plus root directory to fit within
a single 16 KiB initial HTTP fetch. Web viewers like pmtiles.io fetch the first 16,384
bytes, parse the header, and decompress the root directory from the remaining bytes. If
the root directory is larger, the gzip stream is truncated and decompression fails with
a "stream end" or "extra bytes past the end" error.

`buildDirectory` first tries a flat root directory. If the compressed size exceeds the
budget (16,384 − 127 = 16,257 bytes), entries are split into leaf directories. The
leaf size starts at 4,096 entries; if the resulting root directory (containing leaf
pointers) still exceeds the budget, the leaf size is increased by 20% and the split is
retried until the root fits. This matches the reference go-pmtiles implementation and
guarantees compatibility with all PMTiles v3 readers regardless of dataset size.

For very large datasets (e.g. 60M+ tiles), the initial 4,096-entry leaves can produce
~15,000 leaf pointers whose compressed root directory exceeds 16 KiB. The iterative
growth resolves this by using larger leaves (fewer root entries) until the root fits.

## Integration test plausibility checks

Satellite integration tests use a shared `assertPlausiblePMTiles` helper that validates
8 properties of every PMTiles output: zoom range, tile type, geographic bounds (within
tolerance), center point containment, tile count minimums and non-decreasing growth
across zooms, first-tile image decoding, clustering flag, and metadata key presence.
Each dataset defines a `plausibilityExpectation` with approximate bounds and tolerances.
This catches regressions that simple "tile count > 0" checks would miss — e.g. bounds
shifted by a projection bug, missing metadata keys, or broken tile encoding.
