# Design Decisions

Why things are the way they are: rationale, trade-offs and the bugs that shaped the code.
For code structure and the conventions code must follow see [ARCHITECTURE.md](ARCHITECTURE.md).

**Contents**

- [Input: TIFF decoding](#input-tiff-decoding)
  - [TFW (TIFF World File) support](#tfw-tiff-world-file-support)
  - [Untrusted counts and malformed files](#untrusted-counts-and-malformed-files)
  - [Levels, overviews and internal masks](#levels-overviews-and-internal-masks)
  - [Checks at open](#checks-at-open)
  - [Strip-to-tile promotion](#strip-to-tile-promotion)
  - [TIFF predictor support](#tiff-predictor-support)
  - [Predictors are undone on a copy, never in the mapping](#predictors-are-undone-on-a-copy-never-in-the-mapping)
  - [ZSTD compression](#zstd-compression)
  - [Bit-packed sample depths](#bit-packed-sample-depths)
  - [Photometric interpretation](#photometric-interpretation)
  - [Raw integer reads and the ByteSource seam](#raw-integer-reads-and-the-bytesource-seam)
  - [Reader lifecycle](#reader-lifecycle)
  - [BandConfig: band reordering, alpha, and rescaling](#bandconfig-band-reordering-alpha-and-rescaling)
  - [Preset auto-detection](#preset-auto-detection)
  - [Automatic rescale range](#automatic-rescale-range)
- [Projections](#projections)
  - [Native fast paths, wroge/crs fallback](#native-fast-paths-wrogecrs-fallback)
  - [CRS units](#crs-units)
  - [EPSG selection](#epsg-selection)
  - [Per-source projection](#per-source-projection)
  - [Pixel centres and PixelIsPoint](#pixel-centres-and-pixelispoint)
  - [Antimeridian and 0..360 longitudes](#antimeridian-and-0360-longitudes)
  - [Merged bounds](#merged-bounds)
  - [Web Mercator latitude clamping](#web-mercator-latitude-clamping)
  - [Tile size in resolution calculation](#tile-size-in-resolution-calculation)
- [Nodata and transparency](#nodata-and-transparency)
  - [Straight alpha](#straight-alpha)
  - [Nodata handling for float (Terrarium) data](#nodata-handling-for-float-terrarium-data)
  - [Nodata handling for image (non-float) data](#nodata-handling-for-image-non-float-data)
  - [Source-level nodata flood fill](#source-level-nodata-flood-fill)
  - [Nodata-aware source fallthrough](#nodata-aware-source-fallthrough)
  - [Empty as color transformation (not resampling)](#empty-as-color-transformation-not-resampling)
  - [Nodata color and fill-missing split](#nodata-color-and-fill-missing-split)
- [Resampling and downsampling](#resampling-and-downsampling)
  - [Alpha-weighted averaging](#alpha-weighted-averaging)
  - [Unscaled pyramid kernels](#unscaled-pyramid-kernels)
  - [Kernel lookup tables](#kernel-lookup-tables)
  - [Float kernels at nodata edges](#float-kernels-at-nodata-edges)
  - [YCbCr fast paths](#ycbcr-fast-paths)
  - [Resampling gamma correction](#resampling-gamma-correction)
  - [Mode (most common value) resampling](#mode-most-common-value-resampling)
  - [Downsample edge handling (no extent extension)](#downsample-edge-handling-no-extent-extension)
  - [Nearest-neighbor edge clamping](#nearest-neighbor-edge-clamping)
- [Memory](#memory)
  - [image.RGBA sync.Pool](#imagergba-syncpool)
  - [Per-tile local view in front of the source tile cache](#per-tile-local-view-in-front-of-the-source-tile-cache)
  - [One generic sharded LRU](#one-generic-sharded-lru)
  - [Temporary files in a per-run directory](#temporary-files-in-a-per-run-directory)
  - [Spilling and --mem-limit](#spilling-and---mem-limit)
  - [Disk tile store errors and cancellation](#disk-tile-store-errors-and-cancellation)
  - [Disk tile store memory accounting](#disk-tile-store-memory-accounting)
- [Encoding and platforms](#encoding-and-platforms)
  - [Native libwebp (replacing WASM)](#native-libwebp-replacing-wasm)
  - [Windows WebP: MSYS2 prebuilts, statically linked](#windows-webp-msys2-prebuilts-statically-linked)
  - [Windows support](#windows-support)
- [PMTiles output](#pmtiles-output)
  - [Minimum zoom: whole extent in one tile](#minimum-zoom-whole-extent-in-one-tile)
  - [CLI defaults and validation](#cli-defaults-and-validation)
  - [Progress output](#progress-output)
  - [Root directory 16 KiB budget](#root-directory-16-kib-budget)
  - [Writer index and run merging](#writer-index-and-run-merging)
  - [Writer dedup](#writer-dedup)
  - [Finalize](#finalize)
  - [Archive bounds](#archive-bounds)
  - [Header bounds precision](#header-bounds-precision)
  - [Caller-supplied metadata keys](#caller-supplied-metadata-keys)
  - [Description metadata provenance](#description-metadata-provenance)
  - [Startup settings display](#startup-settings-display)
- [PMTiles reader](#pmtiles-reader)
  - [Run-length index](#run-length-index)
  - [Internal compression](#internal-compression)
  - [Leaf directories](#leaf-directories)
  - [TileIDToZXY](#tileidtozxy)
- [pmtransform](#pmtransform)
  - [Separate binary](#separate-binary)
  - [Passthrough fast path](#passthrough-fast-path)
  - [Mode selection and added levels](#mode-selection-and-added-levels)
  - [Max-zoom anchored rebuild](#max-zoom-anchored-rebuild)
  - [Terrarium-aware rebuild](#terrarium-aware-rebuild)
  - [Tile size discovery](#tile-size-discovery)
  - [Sparse fill optimization](#sparse-fill-optimization)
  - [Metadata and bounds passthrough](#metadata-and-bounds-passthrough)
- [Command-line tools](#command-line-tools)
  - [Shared CLI helpers](#shared-cli-helpers)
  - [checkpmtiles](#checkpmtiles)
  - [pmheader](#pmheader)
  - [coginfo -raw](#coginfo--raw)
- [Testing](#testing)
  - [Integration test plausibility checks](#integration-test-plausibility-checks)

## Input: TIFF decoding

### TFW (TIFF World File) support

TFW sidecar files provide georeferencing for plain TIFFs that lack embedded GeoTIFF
tags. When a `.tfw` file is found alongside a `.tif`, it is parsed for pixel scale
and origin. GeoTIFF tags always take precedence — TFW is only used as a fallback
when ModelPixelScale and ModelTiepoint are absent, and then it supplies only the pixel
grid: a file with GeoKeys but no grid tags keeps its GeoKeys EPSG. (It used to take the
whole GeoInfo from the world file, and the coordinate-range guess turned a UTM 32N file
into Web Mercator.)

Rotated world files (non-zero rotation terms) are rejected with a clear error since
the pipeline assumes axis-aligned rasters.

### Untrusted counts and malformed files

Every count, offset and dimension read from a TIFF is untrusted. The parser bounds it by
the file size or by a structural limit before multiplying or allocating: 65536 directory
entries (tags are distinct uint16 values) and 65536 IFDs, with a repeated IFD offset
ending the chain. Ranges are checked as `offset > n || size > n-offset`, never
`offset+size > n`, because the sum wraps for BigTIFF offsets. Before these checks an IFD
chain pointing back at itself looped forever, and a garbage BigTIFF count (2^60 entries,
or 2^61 LONG8 values whose byte size wrapped to 0) panicked in `make`.

Tag value getters dispatch on the declared TIFF type and never read past the value bytes;
unexpected types yield nil or 0 rather than a guess. A malformed file produces an error
naming the file (`parsing <name>: ...`), never a panic or a hang.

All tile, strip and mask byte ranges go through `Reader.slice`, so a `ByteSource` may rely
on `0 <= off <= end <= Size()` and slice naively. The test double `bytesSource` does
exactly that, so a contract violation fails the tests with a panic.

### Levels, overviews and internal masks

IFD selection happens once, at open (`imageIFDs`), not at read time. IFD 0 is kept; a
later IFD is dropped when it is a mask or page, striped (only IFD 0 is promoted to virtual
tiles), 0-sized or larger than IFD 0, has a different SamplesPerPixel or BitsPerSample,
an unsupported compression, or tiles of more than 2^28 samples. Every IFD used to become
a level: a GDAL COG with an internal mask ends its chain with a 1-bit mask, on which the
auto rescale range failed, and a striped reduced-resolution IFD divided by zero once
`OverviewForZoom` picked it.

The reduced-resolution bit of NewSubfileType is deliberately *not* required: some writers
omit tag 254, and dropping their overviews would make low zooms read full resolution.
Dropping an unusable overview costs only speed, while keeping it used to cost a crash or
swallowed read errors (transparent tiles).

GDAL internal masks are applied as alpha. A usable mask has NewSubfileType bit 2 and
Photometric 4 (both, as TIFF 6.0 requires), is tiled with one sample of 1..8 bits, uses a
supported non-JPEG compression without a predictor on packed bits, and has a tile entry at
every position. Masks are paired with levels by width, height and tile size, never by IFD
position, because GDAL's order varies (`rgb_jpeg_mask.tif` is image, mask, ov1, ov2,
mask1, mask2). Nonzero means valid. A sparse mask tile (byte count 0) is all invalid: a
GDAL `SPARSE_OK` COG omits all-invalid mask tiles and writes all-valid ones, and
`gdal_translate -b mask` reads the omitted ones as 0. Checked against
`gdal_translate -b mask` on GDAL-written COGs: no mismatching pixel at any level.

Masks apply only when level 0 has one. A striped level-0 mask cannot be paired, so such a
source stays unmasked at every zoom rather than masked at low zooms only. When level 0 is
masked, overviews without a mask are dropped: read unmasked, they would turn the masked
area opaque at exactly the zooms where the sources of a composite overlap. That trades
speed for correctness, and `IFDCount`/`NumOverviews` shrink.

The mask is applied in `ReadTile` before the flood mask; both only clear pixels, so they
compose, and the tile cache, `ReadRegion` and the flood build's JPEG path see it. Masked
JPEG tiles lose the resampler's YCbCr fast path because they are materialised as RGBA;
`toRGBA` uses `draw.Draw` for `*image.YCbCr`, byte-identical to the per-pixel loop and 4×
faster (0.8 → 0.22 ms per 256 px tile, against ~0.48 ms for the JPEG decode). The float
and raw uint16 paths (terrarium, the auto rescale range) do not apply masks yet.

### Checks at open

`OpenSource` runs `checkLayout` on every level and wraps its error with the path, so a
file the decoders cannot read fails at open instead of at the first read, where the error
became read warnings and transparent tiles. Rejected: a predictor on bit-packed samples
and planar-separate strips of bit-packed samples. No conformant writer produces either
(libtiff refuses the predictor), and they used to hang or panic. The read-time check stays
for Readers built without `OpenSource`.

A tile may hold at most 2^28 samples (TileWidth × TileHeight × SamplesPerPixel), checked
after strip promotion, which sets TileWidth to the image width. The decoders allocate from
the tags before reading data, and 2^28 caps a single-band RGBA buffer at 1 GiB; garbage
tags used to ask for terabytes. Real tiles hold 2^16..2^24 samples per band, and virtual
strip tiles (full width × at least 256 rows) stay below the cap up to ~350,000 RGB pixels
across. An oversized level 0 fails the open; an oversized overview is dropped.

### Strip-to-tile promotion

Plain TIFFs typically use strip layout (full-width rows) instead of tiles. Since the
rendering pipeline requires tile-level random access, strips are promoted to virtual
tiles at open time. Small strips (e.g. RowsPerStrip=2) are merged into groups of at
least 256 rows to ensure resampling kernels (Lanczos 6×6) never span more than 2×2
tiles. At read time, individual strips are read and decompressed separately, so
non-contiguous strip storage is handled correctly, and each strip contributes exactly
stripRows × rowBytes to its virtual tile, so row positions never shift (rowBytes =
ceil(width × samples × bits / 8), with one sample for planar-separate strips):

- A sparse strip (byte count 0, GDAL `SPARSE_OK`) is filled with zeros and its rows are
  marked; each reader reports them as nodata (transparent in RGB, NaN in float, the GDAL
  nodata value in `ReadUint16Tile`). A row counts as sparse only when it is sparse in
  every plane. Sparse strips used to be skipped, moving every later strip up.
- A strip that decodes short is an error, because truncated LZW decodes without error and
  libtiff also accepts a missing EOI. A strip that decodes long is truncated, since some
  writers pad the last strip. Consequently strips are always read as bit-packed; the
  padded 9..15-bit heuristic below applies to tiles only (it is non-conformant, and GDAL
  cannot read it either).
- RowsPerStrip of 0 or larger than the height means a single strip, as in libtiff
  (2^32-1, the spec default written explicitly, used to open with zero tile rows). Missing
  or short StripByteCounts, or a zero width or height, is an error at open.

Planar-separate strip files (PlanarConfiguration=2, GDAL INTERLEAVE=BAND — GDAL's
default when copying from a band-interleaved source) store each band's strips
consecutively, plane-major. Virtual tiles are derived from one plane's worth of
strips; at read time each plane's strips are decompressed (with the predictor
undone at samplesPerPixel=1) and interleaved into chunky order so downstream
decoding is layout-agnostic. JPEG-compressed planar strips are rejected, since
encoded planes cannot be byte-interleaved.

JPEG-compressed strips (GTiff's default for `COMPRESS=JPEG` without `TILED=YES`) used to
be concatenated and read as raw samples, which rendered noise without an error. Each strip
is its own JPEG stream, so it is decoded on its own (JPEGTables prepended), clipped to its
rows and drawn into an RGBA virtual tile. Unlike tiled JPEG without nodata, there is no
YCbCr passthrough.

The last virtual tile is short whenever the image height is not a multiple of the
virtual tile height (GEBCO: 21600 rows, RowsPerStrip=1, 256-row tiles → 96 rows).
The float decoder accepts that for strip sources and fills the rows outside the
image with NaN; tiled sources keep the strict length check, as do `decodeRawTile` and
`ReadUint16Tile`. The float decoder used to reject the tile, and
because the sampler maps read errors to nodata without caching them, every pixel in
those rows re-read ~8 MB of strips: the bottom 96 rows of each GEBCO file were
nodata and the conversion took 15m38s instead of 1m17s. The concatenation buffer is
sized from the strips' row counts up front, since growing it strip by strip copied
the tile several times over and contended on the heap lock.

### TIFF predictor support

LZW, Deflate and ZSTD compressed TIFFs may use predictors to improve compression. After
decompression, the predictor encoding is reversed before interpreting pixel values.

**Predictor=2** (horizontal differencing) stores each sample as the delta from the
previous sample in the same row. Accumulating the deltas row-by-row recovers the
original values. Supported for 8-, 16-, 32- and 64-bit data, with multi-byte deltas
accumulated at the sample width using the TIFF byte order (64-bit samples, e.g. a Float64
DEM, used to be accumulated byte by byte).

**Predictor=3** (floating-point predictor) is the standard for float32/float64 data
compressed with Deflate or LZW (GDAL default). It first byte-shuffles each row into
byte planes — most-significant byte plane first, regardless of file byte order
(matching libtiff's fpDiff/fpAcc) — then applies byte-level horizontal differencing
at a stride of samplesPerPixel. Reversing it: (1) undo byte differencing by
accumulating bytes at the same stride, (2) unshuffle the MSB-first planes back into
the file's byte order. Getting the plane order wrong scrambles exponent bytes into
the mantissa, turning float tiles into full-range noise.

### Predictors are undone on a copy, never in the mapping

`applyPredictor` rewrites its argument in place. For compressed tiles the
argument is a freshly decompressed buffer, but for `Compression == 1` the tile
bytes alias the read-only mapping — writing there is a SIGBUS on Unix and an
access violation on Windows. The tiled 8/16-bit path already copied first; the
float path did not, so an uncompressed float32 COG with `Predictor=2` or `3`
crashed the process. Both paths now copy when a predictor is present, and only
then (an uncompressed tile without a predictor is still handed out as a
zero-copy view of the mapping).

### ZSTD compression

TIFF compression 50000 (GDAL/libtiff ZSTD) stores each tile or strip as an
independent zstd frame. Go's stdlib has no zstd decoder, so the project depends on
`github.com/klauspost/compress/zstd` — pure Go, so it also works in `CGO_ENABLED=0` builds. A single shared
decoder is used via `DecodeAll`, which is safe for concurrent use.

The Deflate/zlib path uses the same module's `flate`/`zlib` packages, a drop-in
for the stdlib ones that decodes ~20–25% faster with far fewer allocations.
LZW keeps the in-tree TIFF-variant decoder (`lzw.go`); klauspost/compress has
no LZW.

### Bit-packed sample depths

TIFF packs samples that are not a whole number of bytes (e.g. 12 or 15 bits) MSB-first,
restarting each row on a byte boundary. The reader used to assume whole-byte samples, so
a 15-bit raster decoded as noise without an error. Planetary Computer publishes
Sentinel-2 L2A reflectance exactly like that (a 512×512 tile is 512·512·15/8 bytes).
Unpacking reads a 32-bit big-endian window per sample (a ≤16-bit sample starting ≤7 bits
into a byte spans ≤23 bits): the bit-at-a-time version was the hottest function of a
Sentinel-2 composite (17% of CPU). The CLI treats every 9-16 bit input as "16-bit" for
rescaling, so 15-bit input gets the same auto range as 16-bit.

`samples16` is the single decode for 1..16-bit samples, shared by `ReadUint16Tile`,
`decodeRawTile` and, through `ReadUint16Tile`, the value-range scan:

- Whole bytes are read in the file's byte order.
- The tag does not say whether 9..15-bit samples are packed or padded to 16 bits, so the
  tile length decides: padded only when the tile is long enough and the padded length
  exceeds the packed one; a tie goes to packed. Sub-byte depths are always packed.
- Signed samples narrower than 16 bits are sign-extended before any sign bias. That is
  the `ReadUint16Tile` contract: `int16(v)` is the value at every depth, Int8 included.
  Before, -2 sorted above 1 for signed 15-bit data.
- `ReadUint16Tile` checks the format before reading, so sparsity cannot decide whether a
  read errors.

`decodeRawTile` keeps a byte-aligned per-pixel loop for every depth, and that loop must
not call a function value per sample: every call (up to four per pixel) spills the loop's
registers, and adding live values around a call cost about 1 ms per 512×512 tile. So
`pixelSamples` lays samples out once (bytes, `s16` for 9..16 bits, or sub-byte narrowed to
bytes) and the rescale is a table built once per tile (256 entries, or 65536 for 9..16
bits). Future code in that loop must stay call-free. Result: 2.3-2.9× faster on 16- and
15-bit tiles, 25% on 8-bit RGB and par on 8-bit gray compared with the code before the
bit-packed patches. Non-aligned depths above 16 are rejected.

### Photometric interpretation

- **Palette** (Photometric 3) is expanded through the ColorMap (tag 320), shifting each
  16-bit entry right by 8, which is robust to both `c*257` and `c<<8` encodings. Nodata is
  an index, as in GDAL; band mapping and rescaling do not apply; a missing or short
  ColorMap is an error. Palettes used to render their class indices as gray.
- **WhiteIsZero** (Photometric 0) inverts the rendered gray after nodata and rescaling,
  which both refer to stored values. It is keyed on the tag having been read as 0
  (`IFD.whiteIsZero`), not on Photometric's zero value, so an IFD without the tag, or one
  built in code, stays BlackIsZero, as libtiff assumes.
- **Sub-byte gray** renders its raw values 0..2^bits-1 (GDAL semantics) rather than being
  stretched, since a stretch would change what nodata and `--rescale-range` mean.
  `--rescale linear --rescale-range 0,1` (or `0,15`) stretches it. WhiteIsZero at sub-byte
  depths without a rescale inverts within that raw range.

### Raw integer reads and the ByteSource seam

`ReadUint16Tile`/`ReadUint16Region` return samples without the rescale to 8 bits that
`ReadRegion` (an `*image.RGBA`) applies, for callers that composite 16-bit bands
themselves. `ReadUint16Region` errors on a negative size, reads nothing for a zero size,
and requires the region to lie within the image. `Uint16TileCache` differs from the other
caches in one way: a hit is an explicit `ok`, because an empty tile is a legitimate `nil`
and must not miss forever along a satellite datastrip's empty edge. All byte access goes
through a `ByteSource` (`Size`/`Slice`/`Close`); `Open` wraps the memory map in one, and
`OpenSource` accepts any other implementation, e.g. HTTP range reads. Slice is called once
per tile, not per sample, so the interface costs ~2 ns per tile read. These APIs serve
library callers (the satcomposite project); no CLI flag uses them yet.

`sourceReadSeeker` reads ahead in blocks of at least 64 KiB, because the parser issues
roughly 150 tiny reads per COG, a round trip each over HTTP. It may hold a `Slice` result
only while parsing; parsed IFD values are copied, so nothing aliases the source after
`OpenSource` returns. `Slice` results are valid only until `Close`.

### Reader lifecycle

- The setters `SetBandConfig`, `SetEPSG` and `SetID` are plain field writes. Calling them
  before the reader is shared with worker goroutines is what keeps them race-free.
- Reader IDs come from a process-wide atomic counter (positive, unique), so readers opened
  by any path can share a tile cache. `OpenAll` used to number its readers 0..n-1 and every
  other reader had 0, so readers from `Open` or a second `OpenAll` shared cache keys.
  `SetID` is for callers that want stable IDs across re-opens; negative IDs are the
  caller's namespace and never collide with automatic ones.
- `Close` swaps in a `closedSource` sentinel instead of nil, so a read after `Close` is an
  error (`cog: reader is closed`) rather than a nil dereference. This is not a
  synchronization mechanism: `Close` concurrent with reads is still the caller's bug, and
  an mmap view held across `Close` still faults.

### BandConfig: band reordering, alpha, and rescaling

Multi-band GeoTIFFs (e.g. 4-band RGBNIR uint16) need band selection, alpha handling,
and value rescaling to produce usable 8-bit RGBA output. `BandConfig` is set once
after `OpenAll()` and before tile generation — it's immutable during concurrent reads.

**Band reordering**: `Bands [3]int` maps 1-indexed input bands to R,G,B output channels.
For false-color composites, `--bands 4,1,2` maps NIR→R, R→G, G→B. Zero values default
to `1,2,3`. `renderedBands(cfg, spp)` is the single source of truth for which file bands
end up as R, G and B (defaults, band 1 for single-band data, duplicates removed, the alpha
band and bands beyond spp dropped); the value range and the flood mask use it, and new code
that needs "the rendered bands" must call it rather than re-derive them.

**Alpha band**: `AlphaBand` controls transparency. `0` = auto (band 4 for 8-bit spp≥4,
none for 16-bit), `-1` = force no alpha, `>0` = explicit 1-indexed band. When an alpha
band is configured and the source value is 0, the pixel is fully transparent.

**Rescaling**: Linear mode maps `[min,max] → [0,255]` proportionally. Log mode uses
`ln(1 + v - min) / ln(1 + max - min)` for better dynamic range in data with large value
spans (e.g. satellite reflectance). Auto mode selects linear for 9-16 bit data, none for
8-bit. Without a rescale, 9-16 bit data maps the full range of its sample type, signed or
unsigned, to 0..255: Int16 used to be mapped from 0..65535 in the sign-biased space, so
every value ≤ 0 rendered black.

**Backwards compatibility**: Zero-value `BandConfig` activates the legacy code path for
8-bit data, including nodata handling for spp≤2. No behavioral change for existing
8-bit workflows.

### Preset auto-detection

`DetectPreset()` examines the GeoTIFF structure and GDAL metadata (tag 42112) to
auto-configure processing. It returns a `Preset` with a name, optional format
override, and optional `BandConfig`.

**Float detection**: If the sample format is IEEE floating point (SampleFormat=3),
returns the `float-terrarium` preset with `Format: "terrarium"`. The CLI applies
this format override when `--format` is `auto`.

**Signed integers count as float**: `IsFloat()` is also true for SampleFormat=2
(e.g. GEBCO Int16 bathymetry). Signed samples have no sensible uint/RGB reading —
negative depths wrapped to ~56000–65535 and the ocean vanished under any
`--rescale-range` — so they decode to float32 in `decodeRawFloat32Tile` and reuse the
whole terrarium path (nodata, resampling, pyramid) instead of growing a third pipeline.

**Signed int16 as an image** (`--format webp/png/jpeg`): the RGB path keeps its uint16
rescale/nodata machinery and biases samples instead — `raw ^ 0x8000` equals `+32768`
and preserves order, so `decodeRawTile` shifts the rescale range and nodata by the same
bias (`IFD.sampleBias`). Linear and log rescaling depend only on `v - min`, so the output
is identical to true signed arithmetic. The elevation preset carries only a format, so
`parseBandConfig` ignores it for rescaling, and `--nodata` accepts negative values.
Single-band sources render as gray in this path (previously only the red channel was
filled). `--nodata-flood` matches in the same sign-biased space.

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
9-16 bit data, no explicit `--rescale-range`, and no explicit `--bands` override. Explicit
flags always take precedence.

### Automatic rescale range

When `--rescale-range` is omitted and no preset applies, `Reader.ValueRange(cfg)` picks
the range and the CLI logs it (`Auto rescale range: [min, max] (from …)`) instead of
erroring — the old "run gdalinfo yourself" hint was work the tool can do. Only the bands
that are rendered count (`renderedBands`): taking the min/max over every band let a bright
NIR band, or a 0/65535 alpha band, stretch the range so the RGB came out dark. Per source:

1. GDAL `STATISTICS_MINIMUM/MAXIMUM` band items (free, exact), used only when every
   rendered band has them: partial statistics would clip the band that lacks them.
2. Otherwise a pixel scan of the coarsest IFD, on a grid of at most 64 tiles spread over
   both axes (`scanGrid`, every tile when there are few enough), skipping nodata and
   edge-tile padding. A stride through the row-major tile index aliased with the number of
   tiles across: 64 across gave a stride of 64, so only column 0 was read. Bounded so a
   multi-GB source without overviews still starts instantly; the cost is that an extreme
   value in an unsampled tile clips.

`ErrNoValueRange` marks a source whose sampled pixels are all nodata. With several
sources the CLI skips such sources (open-ocean tiles of a composite) and logs how many;
it fails when every source is skipped or on any other error. `mergeValueRanges` is a pure
function over the per-source results, so the rule is tested without readers.

Ranges are unioned across all sources so every tile shares one mapping (per-source
ranges would show seams). Plain min/max rather than percentiles: deterministic and
matches what `gdalinfo -mm` reports; outlier-heavy data still has `--rescale-range`.

## Projections

### Native fast paths, wroge/crs fallback

`FromWGS84` runs once per output pixel, so the common CRSs are hand-written: LV95,
Web Mercator, WGS84 and UTM (Krüger series to n³, agrees with wroge/crs to < 1 mm,
~90 ns/call). ETRS89 UTM (EPSG:258xx) is treated as WGS84; the < 1 m offset does not
matter for tiles.

Every other EPSG code goes through `CRSFallback`, backed by `github.com/wroge/crs`
(pure Go, embedded EPSG registry; roughly doubles the binary to ~14 MB). The library
searches for the best datum transformation path on every call (~50 µs), because the
best path depends on the location. Per pixel that is ~3 s per tile, so the fallback
splits the work: the WGS84 → source datum shift is evaluated with the exact transform
only at the nodes of a 0.05° grid, cached and interpolated bilinearly, and only the
projection math (same datum, no path search) runs per pixel. Result: ~250 ns/call and
< 1 cm from the exact transform (tested with OSGB36, a ~100 m shift). Nearest-node
lookup without interpolation was off by 23 cm, hence the interpolation.

wroge/crs enforces the EPSG area of use on every call, and its datum transformations are
gated by their own areas. Real rasters reach past both: the standard EEA grid in
EPSG:3035 spans lon -57..73, and all four of its corners used to return +Inf, so the
output was one empty tile with no error. The fallback therefore projects with an
unrestricted area, a lon ±540 box rather than `crs.World`, because projection longitudes
run past ±180 across the antimeridian from the central meridian (an NSIDC 3413 corner is
at -191.65).

`ToWGS84` is unproject plus the cached shift grid, with one fixed-point step so the shift
is read at the WGS84 position. Without that step NTF (Paris), with its 2.3° prime meridian
offset, is 7 m off; with it the round trip is < 1 cm.

Grid nodes that no datum transformation covers take the shift at the centre of the CRS's
area of use, computed circularly for areas that cross 180. The rejected alternatives: zero
(PROJ's ballpark) is ~150 m wrong for ED50, Tokyo or NAD27, and a nearest-valid-node
search costs more. The reference shift is ~0 for ETRS89, NAD83 and GDA94 and within tens
of meters for the rest. Only if it fails too is the shift zero.

+Inf is still returned when unproject itself fails; it lies outside every source and so
becomes nodata. Projections must return ±Inf rather than NaN out of domain: NaN
comparisons are false, so a NaN pixel would pass the per-pixel bounds check and read a
clamped pixel. No grid files are downloaded: `crs.SetGridCDN` is never called, so datums
that need NTv2 grids use the library's best grid-free path. The CLI logs a note once per
CRS that uses the fallback.

### CRS units

`PixelSizeInGroundMeters` and `MetersToPixelSizeCRS` take the unit from the EPSG
definition (wroge `Unit.ToSI`; geographic when the conversion is `crs.Geographic`, with
grads scaled to degrees). Web Mercator keeps its cos(lat) special case; unknown codes
default to meters. Every CRS but 4326 and 3857 used to count as meters: a 1/3″ DEM in
EPSG:4269 got an auto max zoom of 28 (13 is right), and State Plane rasters in US survey
feet got one about two levels too low. Do not reintroduce `epsg == 4326` checks for
"degrees"; use the unit.

### EPSG selection

GTModelTypeGeoKey decides which GeoKey applies. The first key in directory order used to
win, so GeographicTypeGeoKey beat ProjectedCSTypeGeoKey; GDAL writes
`GeographicType=<datum>` next to `ProjectedCSType=32767` for custom Albers or LCC grids,
and those metres were read as degrees. A user-defined CRS (32767 in the model or PCS key,
or a projected model without a PCS key) is reported as 32767 and rejected: it is never
replaced by its base geographic CRS and never inferred. Tradeoff: legacy projected files
without a PCS key, which inference used to place by coordinate range (e.g. LV95), now
fail with a message that points to `--source-epsg`.

A world file supplies only the pixel grid. When a file has no CRS at all, `inferEPSG`
still guesses from the coordinate ranges (-180..360/-90..90 → 4326, the LV95 box → 2056,
other metric grids → 3857), but the guess is logged as a warning naming the file, and
`Reader.SetEPSG` (CLI: `--source-epsg`) overrides it. Failing instead of guessing, and
reading `.prj` or `.aux.xml` sidecars, were considered and left out: both change behaviour
for existing TFW+3857 inputs, and satellite composites rarely use world files.

The CLI checks every source's CRS right after opening, before the bounds: unknown codes,
32767, and EPSG 0 (not georeferenced; the reader guesses a CRS for every file with a pixel
grid, so `--source-epsg` cannot help there and the message says what is missing).
`MergedBoundsWGS84` falls back to `WGS84Identity` for an unknown projection, so without
the early check it would produce garbage bounds and zooms before generation failed.

### Per-source projection

Each source is reprojected from its own CRS. All sources used to be projected with the
first file's CRS, so e.g. Sentinel-2 tiles from UTM 32N and 33N were dropped or
misplaced, and the CLI then rejected mixed EPSG codes instead. Now each source carries its
own `coord.Projection`, one instance per EPSG code, built once in `Generate` and shared
read-only by all workers (`CRSFallback` keeps its datum-shift cache per instance, so
sharing matters).

The output tile's CRS box and pixel size (overlap test, overview choice) are computed once
per distinct projection per tile. Lon/lat per output column and row are still
precomputed, and a pixel is projected only when the next candidate source's projection
differs from the previous one, so single-CRS input costs exactly one `FromWGS84` per pixel
as before. Source order still sets priority; interleaved CRSs cost one extra projection
per switch.

A source is tested in its own CRS against tiles anywhere its box overlaps. Transverse
Mercator maps the far hemisphere to northings above 10,000 km, so a UTM source cannot
false-match a tile on the other side of the world. A tile corner exactly on the equator
at CM±90° gives NaN, which makes that tile's box NaN and the source is treated as
overlapping, but every pixel is then rejected by the per-pixel bounds check (a
performance cost only).

With mixed CRSs, the CLI takes the auto max zoom from the finest source (minimum ground
pixel size at the merged centre latitude), searches coverage holes per EPSG group
(degree and metre boxes cannot be compared), and describes the archive with every EPSG
code; the CRS extent and CRS-unit pixel size are given only for a single CRS, else the
finest ground pixel size in metres.

### Pixel centres and PixelIsPoint

The samplers treat integer coordinates as pixel centres, but the dispatchers passed
corner-based coordinates (`(x - OriginX) / pixelSize`), so every raster was drawn half a
source pixel north-west, measured at the overview level in use (~225 m for GEBCO at
native zoom, more from overviews). Now `cog.GeoInfo.OriginX/Y` is always the upper-left
corner of pixel (0,0), and the two dispatchers do the single corner-to-centre conversion
(`pixX - 0.5`, after the `[0, imgW)` bounds check; the sampler clamps handle the edge
half-pixel). Nearest's `Floor(fx + 0.5)` is then the containing pixel. New samplers must
follow this; never add another 0.5 elsewhere. Output pixels are sampled at `px + 0.5`.

`parseGeoInfo` normalises `RasterPixelIsPoint` (GTRasterTypeGeoKey = 2; Copernicus DEM,
SRTM), whose tiepoint is a pixel centre, by moving the origin half a pixel up-left, as
GDAL does by default (`GTIFF_POINT_GEO_IGNORE` is not supported); TFW files, which also
reference pixel centres, get the same treatment. PixelIsPoint files were accidentally right
before (the two errors cancelled); now both conventions land where GDAL puts them, checked
by a one-pixel feature at two zooms with all five resampling modes, within 0.05 output px.

### Antimeridian and 0..360 longitudes

`Projection.ToWGS84` returns longitudes continuous around the projection's central
meridian and does not wrap them per point, so min/max over sampled points keeps working.
Callers wrap the merged interval with `coord.WrapLonRange`. In `cog.Bounds`, MinLon is in
[-180, 180) and MaxLon > 180 means the data crosses the antimeridian; a span of a full turn
is [-180, 180]. Offsets below 1e-6° are rounding noise, because GDAL rasters routinely
end at ±180.0000000001. `TilesInBounds` wraps columns for such bounds and
`MinZoomForSingleTile` returns 0. Before, longitudes were never wrapped: UTM zones 60 and
1 lost everything past 180°.

`WGS84Identity.Lon360` serves 0..360 grids (and e.g. 100..260) by mapping western
longitudes to lon+360. It is decided per source, not per run: an EPSG:4326 source with
minX ≥ -1e-6 and maxX > 180+1e-6 gets its own projection, so a -180..180 source next to a
0..360 one keeps its convention, and projections are keyed on (EPSG, Lon360). The margins
keep a world raster ending at 180.0000001 in the usual convention. Lon360's jump at
Greenwich sits exactly on tile edges (and inside the z0 tile), so a western tile's east
corners map to 0, not 360; `tileCRSBounds` shifts western tiles whole (+360) and gives
the z0 tile [0, 360]. Without that, a source entirely in 180..360 was skipped at max zoom
0 and 1.

Tradeoffs: grids written as e.g. -10..350 or -190..-170 are still unsupported (that needs
a per-source western edge). Min/max of raw longitudes cannot find the gap between sources
given in the -180..180 convention on both sides of 180, which merge to full width: the
tiles are correct but a whole-world band is enumerated.

The archive records crossing bounds as -180..180 (see Archive bounds).

### Merged bounds

`cog.MergedBoundsWGS84` samples 16 intervals along every source edge (68 `ToWGS84` calls
per source) instead of the four corners. Projected edges curve, so extremes can lie
mid-edge: the EEA grid's north edge reaches 72.61°N against 58.95°N at its corners, and a
Sentinel-2 tile straddling its UTM central meridian at 60°N lost 417 m, a row of tiles from
z16 on; 16 intervals leave under 2 m (the residual falls as 1/n²).

A pole counts only when it is a point inside the source: `FromWGS84(0, ±90)` and
`FromWGS84(90, ±90)` must agree within one source pixel, and the point must lie strictly
inside the extent. That rules out EPSG:4326, where the pole is the top edge, and Web
Mercator, where it is at infinity; there the edge samples already cover it. Polar
stereographic, transverse Mercator and LAEA qualify, and then (-180, lat) and (180, lat)
are added so the wrapped range is full width. Without this, a 3413 raster around the pole
gave lon 142..488 and MaxLat 80.8.

Points that do not transform are skipped. When none does, the function returns an error
rather than panicking; empty input returns empty bounds.

### Web Mercator latitude clamping

Latitudes beyond the Web Mercator valid range (~±85.05°) cause the tile coordinate
math to produce Inf/NaN values. In Go, converting +Inf to int wraps to MinInt, which
then gets clamped to 0 — silently reducing the tile grid to a single row. Fixed by
clamping latitude to ±85.0511° in `LonLatToTile` before the Mercator projection.
This was never triggered before because Swiss LV95 data stays well within the valid
Mercator range, but global datasets (like the Natural Earth raster covering ±90°)
require it.

### Tile size in resolution calculation

`ResolutionAtLat()` accepts the actual tile size (e.g. 256 or 512) instead of
hardcoding `DefaultTileSize=256`. This ensures that `OverviewForZoom` selects the
correct overview level for non-standard tile sizes. With 512-pixel tiles at zoom z,
each pixel covers half the ground distance compared to 256-pixel tiles — using the
wrong tile size caused 2x too coarse overview selection, producing blurry output.

## Nodata and transparency

### Straight alpha

Pipeline `*image.RGBA` buffers hold straight (non-premultiplied) alpha, although Go
defines the type as premultiplied (the rules are in ARCHITECTURE.md, "Conventions"). The
samplers in `resample.go` and the COG reader already produced straight alpha, while the
encoders and decoders treated the same buffers as premultiplied: `image/png` and the
pure-Go WebP encoder un-premultiplied a second time (200,100,50,128 was stored as
143,199,99,128), every PNG/WebP decode drawn into an `*image.RGBA` premultiplied again
(read back as 72,100,49,128), and `TileData.At` reported premultiplied colours for uniform
tiles.

Straight rather than premultiplied, because:

- the samplers and COG reader already produce it;
- zero-copy `*image.NRGBA` views (`asNRGBA`) keep the PNG and nativewebp fast paths (PNG's
  NRGBA path is a plain copy, faster than the old un-premultiply);
- libwebp's `WebPEncodeRGBA` and `WebPDecodeRGBA` are straight.

JPEG has no alpha. It writes straight RGB and drops alpha for every input type, so a
semi-transparent pixel shows its full colour rather than being composited onto black; the
guard is needed because Go's generic JPEG path would premultiply NRGBA inputs.
`encode.DecodeImage` returns images with alpha as straight `*image.RGBA` (zero-copy for
NRGBA); its draw fallbacks only ever see opaque decoder types.

The pure-Go WebP decoder (`golang.org/x/image/webp`) returns lossy images as Y'CbCr, which
Go converts with the JFIF full-range matrix, while libwebp uses BT.601 limited range: 10,20,30
came back as 25,33,42 and 250 as 231, changing pmtransform re-encodes of lossy WebP
archives in `CGO_ENABLED=0` builds. `DecodeWebP` converts `*image.YCbCr` and
`*image.NYCbCrA` itself with libwebp's fixed-point arithmetic (`src/dsp/yuv.h`), returning
straight `*image.RGBA` like the cgo path. Chroma is taken nearest, while libwebp
interpolates ("fancy upsampling"): identical to libwebp except at chroma edges (≤ 2).

### Nodata handling for float (Terrarium) data

Float kernels (bilinear, bicubic, Lanczos) skip taps that are NaN or equal to the
source GDAL_NODATA value; Lanczos/bicubic renormalise over the remaining taps, bilinear
falls back to nearest. An unset nodata is stored as NaN, which compares equal to nothing,
so the check is a no-op in that case. The parsed sentinel is rounded through float32
(`parseFloatNodata`) because pixels are float32: a float64 parse of e.g. `-3.4028235e+38`
would otherwise never equal the widened pixel value.

`tile.Config.FloatNodata` (CLI: `--nodata` with terrarium) overrides every source's
GDAL_NODATA in the float path, for DEMs whose voids are marked without the tag. It is
rounded through float32 like the tag and parsed once per source into `sourceInfo.nodata`.
Nodata output stays RGBA 0,0,0,0. `--nodata-tolerance` and `--nodata-flood` stay RGB-only.

### Nodata handling for image (non-float) data

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
*Tiled* planar-separate files with any other compression are rare in practice and
return an explicit "not supported" error rather than silently decoding wrong data
(the previous behaviour silently returned plane 0 as R=G=B). Planar-separate *strips*
are the opposite case — see Strip-to-tile promotion.

**Output-format auto-switch**: when `--nodata` is active, JPEG output cannot carry
alpha, so transparent pixels would be baked back to black in the encoded tile. If
the user did not explicitly set `--format`, the CLI switches the default `jpeg` →
`webp`. If the user explicitly chose `--format=jpeg`, it warns and proceeds.

### Source-level nodata flood fill

Strict per-pixel tolerance matching has a failure mode at the boundary of a
black-padded scan: pixels just inside the true border (values ~10–60 from
JPEG quantisation smear) aren't caught by a small tolerance, leaving a
speckle fringe along the alpha boundary, while widening the tolerance starts
absorbing legitimate interior dark pixels (text, shadows, forest canopy).

`--nodata-flood` solves this by treating the strict tolerance as a *candidate
set* and only making transparent the pixels in that set that are reachable
through it from the COG's outer image boundary. Implementation:

1. **Build** (`Reader.BuildFloodMask`): decode every source tile once and populate a
   packed candidate bitmap (1 bit per source pixel). Uncompressed, LZW, Deflate and ZSTD
   sources are matched on their stored samples (`ReadUint16Tile`): a pixel is a candidate
   when every rendered band is within tolerance of nodata, in raw units and in the same
   sign-biased space `decodeRawTile` uses (an out-of-range nodata matches nothing), or when
   its alpha sample is 0, so the flood passes through transparent pixels whatever RGB they
   carry. The decoded RGB cannot be used there: with `HasNodata` off the decode falls back
   to the GDAL_NODATA tag and zeroes exact matches (a white 255 border then no longer
   matched, and the border stayed opaque), and 9-16 bit RGB is rescaled while the
   tolerance is raw. JPEG sources keep matching the decoded RGB with `HasNodata`
   temporarily cleared; they have no raw samples and no tag fallback. Tile decoding — the
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

### Nodata-aware source fallthrough

`sampleFromTileSources` skips results with alpha=0 and continues to the next source
instead of returning them as "found". This prevents nodata in one source from blocking
valid data in another (multi-source hole filling) and ensures tiles containing only
nodata are correctly detected as empty rather than cascading falsely non-empty tiles
through the pyramid — especially important for JPEG output which cannot represent
transparency.

### Empty as color transformation (not resampling)

Empty tiles and transparent/nodata pixels should be modeled as a **color transformation**
(source color → target value) rather than as inputs to spatial resampling.

- **Rationale**: Resampling (bilinear, Lanczos, bicubic, mode) combines *valid* data.
  Interpolating or voting over "empty" produces meaningless results: blending forest
  with nodata gives garbage; mode over [data, empty, empty, data] depends on how
  empty is represented. Empty is a semantic value ("no data") that should be
  substituted, not interpolated.

- **Model**: Treat empty/nodata/transparent as a distinct "source color" and map it
  to a configurable target. Transformation = lookup substitution; resampling = spatial
  kernel over valid pixels only.

- **Implementation**: two colours, applied only at the source level in both
  `geotiff2pmtiles` and `pmtransform`. `NodataColor` replaces transparent pixels of
  max-zoom rendered or decoded tiles before packing. `FillMissing` writes a solid tile at
  max-zoom positions with no source data and substitutes nil children with fill tiles
  *before* calling downsample, so the existing downsample code receives 4 tiles and
  operates normally. No transform in the downsample path.

### Nodata color and fill-missing split

One `--fill-color` used to do both jobs above. In `pmtransform` the first needs decoding and
the second does not, so filling missing tiles forced a lossy re-encode of every tile. It
is now two flags and two fields (`tile.Config`/`TransformConfig.NodataColor` and
`.FillMissing`):

- `--nodata-color` (default none) recolours transparent pixels; in pmtransform it forces a
  re-encode.
- `--fill-missing` writes solid tiles; in pmtransform it keeps passthrough, writing
  pre-encoded fill tiles next to the copied ones.
- `--fill-color` remains as a deprecated alias that sets both. Combining it with either
  new flag is an error rather than order-dependent last-wins, so the resolved colours
  never depend on flag order.

The geotiff2pmtiles default output is unchanged: `renderTile` only writes found pixels
into a zeroed pooled buffer and `sampleFromTileSources` skips alpha 0, so transparent
max-zoom pixels were already 0,0,0,0 and the old default recolouring was a no-op (checked
tile by tile on SWISSIMAGE). `--nodata-color` without `--fill-missing` leaves missing
children nil, so parents get transparent quadrants there, which matches "missing stays
missing" at every zoom.

**Defaults**: `geotiff2pmtiles` fills missing tiles with transparent (`0,0,0,0`), so areas
without data render transparent rather than as the viewer's background. JPEG cannot store
transparency and would turn every uncovered tile position black, so the default fill is
dropped for `jpeg` output (an explicit `--fill-missing` is kept, with a warning).
`pmtransform` defaults to neither: a non-empty default made the passthrough mode
unreachable, so every run re-encoded lossily.

## Resampling and downsampling

### Alpha-weighted averaging

Averaging filters (bilinear, bicubic and Lanczos, in the max-zoom samplers and in the
pyramid) weight RGB by alpha: RGB = Σ(c·a·w) / Σ(a·w), alpha = Σ(a·w) / Σ(w), which is
equivalent to working in premultiplied space and dividing back. RGB used to be averaged
over every pixel with non-zero alpha at full weight, so an alpha-1 edge pixel pulled as
hard as an opaque one (opaque red beside alpha-1 blue became purple). A result whose alpha
rounds to 0 is written as 0,0,0,0, so transparent pixels stay canonical (better PNG
compression, and uniform-tile detection works); a zero total weight also gives 0,0,0,0.
The "skip taps with alpha 0" check stays, since it only saves work, and the opaque YCbCr
fast paths are untouched. Mode and nearest copy pixels and need no weighting; Terrarium
uses alpha 0/255 only and averages elevations instead. Opaque data gives the same result
as before, and no slowdown was measured (256 px RGBA tile: bilinear 227 → 218 µs,
bicubic 1.41 → 1.38 ms, Lanczos 2.87 → 2.87 ms in the pyramid).

### Unscaled pyramid kernels

The 2× pyramid applies the bicubic and Lanczos kernels at their native width instead of
stretching them by the scale factor, so their overviews alias more than bilinear (a box)
on fine periodic texture. This is a known tradeoff. Each child quadrant is downsampled on
its own, and the RGBA variants count out-of-tile taps as transparent. Stretched kernels
(12-tap Lanczos, 8-tap bicubic, evaluated at d/2) put more weight outside the tile at every
quadrant edge: the in-tile weight drops to 0.946 (Lanczos) or 0.934 (bicubic), so alpha
would fall to about 241/238 along every tile-seam row and column and 228/222 at corners, a
semi-transparent grid on every lower zoom. The current 6/4-tap kernels have an in-tile edge
weight of 1.111/1.062 thanks to their negative lobes, which is the only reason they show no
seam today. A correct fix needs taps that cross child tiles: a mosaic of the 4 children
plus a 3-5 px apron from the children of neighbouring parents, at about 4× the pyramid's
CPU.

### Kernel lookup tables

Same approach for Lanczos-3 and bicubic: a 1024-entry precomputed table with linear
interpolation replaces the kernel evaluation in the inner resampling loops (for bicubic
the Catmull-Rom polynomial `1.5x³ - 2.5x² + 1` / `-0.5x³ + 2.5x² - 4x + 2` over [0, 2)).
The kernels are symmetric so only the positive half is stored. While the polynomial is
cheaper than Lanczos sin() calls, at ~3.25s cumulative CPU it was still worth eliminating.
Bicubic resampling remains the largest CPU cost (~27% when last profiled); that is
inherent to the 4×4 kernel.

The tables have N+1 entries, ending with the zero at the support edge, and `lanczos3`
returns exactly 0 for |x| ≥ 3; the last entry (~1e-6) used to be returned for the whole
final interval. The `idx >= N` guard in `lanczos3LUT`/`bicubicLUT` is required: x < 3 does
not guarantee `int(x*N/3) < N` under float rounding, and `idx+1` would then index past the
table.

### Float kernels at nodata edges

The float Lanczos and bicubic samplers renormalise over the valid taps. Once grid-aligned
sources sample at integer offsets (see Pixel centres), the valid taps can sit at kernel
zeros next to nodata, the weight sum becomes ~1e-6 and the result a spike (-52 m, 1502 m).
If the valid weight is below 0.25 (`minFloatKernelWeight`), the point lies mostly over
nodata and the nearest pixel is used instead (usually nodata, so the pixel stays
transparent). One-sided renormalisation at a steep edge can still leave the data range: in
the test fixture (200 at col 6, 20 at col 7, nodata from col 8), Lanczos at fx=7.5 has a
valid weight of ~0.5 and returns a value below 20. That is a known limit. The RGB
accumulators are deliberately unguarded: alpha = aSum/wTotal collapses to ~0 in the same
situations, so the pixel is skipped.

### YCbCr fast paths

The Lanczos/bicubic YCbCr and NYCbCrA fast paths index chroma for 4:4:4, 4:2:2 and 4:2:0
only (`fastChroma`). 4:4:0, 4:1:1 and 4:1:0 JPEG tiles got wrong colours and then an
index-out-of-range panic that crashed the run; rarer ratios now take the generic
`COffset` path rather than adding index arithmetic to the hot loop.

### Resampling gamma correction

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

### Mode (most common value) resampling

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

### Downsample edge handling (no extent extension)

When Lanczos-3 and bicubic resampling kernels extend beyond the source tile boundary
during pyramid downsampling, out-of-bounds kernel positions are treated as empty
(alpha 0 for RGBA, skipped with weight renormalization for gray) instead of clamping
to the edge pixel. Edge-pixel clamping was visually extending the source extent by
repeating boundary colors into the resampling kernel. With the empty treatment, RGBA
edges fade to transparent naturally, and gray edges are computed from only valid
positions. Bilinear, nearest, and mode are unaffected since their source coordinates
never exceed tile bounds.

### Nearest-neighbor edge clamping

`nearestSampleCached` and `nearestSampleFloat` compute the integer pixel via
`Floor(fx + 0.5)` (round-to-nearest). The caller's bounds check ensures
`pixX ∈ [0, imgW)`, but when `pixX >= imgW - 0.5` the rounding produces
`px = imgW` — one past the last valid pixel. TIFF tiles extend to a multiple
of the tile width, so the read succeeds but returns zero-padded data, creating
a ~1 px band of wrong values at the right/bottom edge of every source file.
Fixed by adding `imgW`/`imgH` parameters and clamping `px`/`py` to
`[0, imgW-1]`/`[0, imgH-1]`, matching the approach already used by the
bilinear, bicubic, and Lanczos sampling functions.

## Memory

### image.RGBA sync.Pool

`*image.RGBA` allocations (256 KB each for 256×256 tiles) are pooled via `sync.Pool`
to reduce GC pressure during tile generation. A `sync.Map` of pools keyed by `(w, h)`
handles multiple tile sizes. `GetRGBA` zeros the pixel buffer with `clear()` before
returning; `PutRGBA` returns to the pool. Zeroing is critical: `renderTile` only writes
pixels where source data is found, so unfound pixels must be transparent (0,0,0,0) —
without clearing, recycled images retain stale pixel data from previous tiles, causing
visible artifacts at data boundaries.

### Per-tile local view in front of the source tile cache

The resamplers look up the source tile once per output pixel. With the shared
cache that is a shard mutex, a map lookup and an LRU `MoveToFront` per pixel —
262k times per 512 px tile, per worker. Sharding does not help much: Hilbert
batching makes neighbouring workers read the *same* source tiles, hence the
same shards. On an 18-core SWISSIMAGE run the lock traffic (and the scheduler
spinning it caused, visible as `runtime.usleep`) was over a third of all CPU
samples.

`renderTile` now wraps the cache in `TileCache.Local()`: a goroutine-local
view with a 4-slot direct-mapped memo indexed by `(col&1, row&1)`, so the 2×2
tile neighbourhood a kernel can straddle never collides. Only a memo miss
touches the shared cache. Result: 22 s → ~15.5 s wall, user CPU 300 s → 190 s,
tile data byte-identical.

Tradeoffs: memo hits do not promote the entry in the shared LRU (harmless — the
view pins the tile for the duration of the render anyway). `renderTileTerrarium` uses
`FloatTileCache.Local()` the same way; its Local views also memoize a cached nil tile.

### One generic sharded LRU

`TileCache`, `FloatTileCache` and `Uint16TileCache` used to be three hand-copied sharded
LRUs, each with its own Local memo. They are now thin wrappers over one generic
`shardedLRU[V]` and its view type `lruView[V]`, with unchanged exported types and
behaviour; new cache types must wrap them too, so that LRU, eviction and stats changes
happen once. A `Local()` view is 256 bytes instead of 2.3 KB with 64 unused shards.

- The memo holds value copies, not pointers into shared entries: `lruEntry` is in the
  same 48-byte size class as the `list.Element`s that `MoveToFront` rewrites under other
  goroutines' locks, so a pointer could false-share. A memo hit reads only
  goroutine-local memory.
- Lookups return `*V`, to be read at once and never modified, rather than copying struct
  values through generic code.
- The exported `Get` methods call `memoGet` then `sharedGet` themselves. A combined
  generic method cannot be inlined (a call alone costs ~57 of the 80 inline budget), and
  the memo hit, nearly every render lookup, must stay one call.
- Hit and miss counters are plain per-shard fields under the shard mutex that `Get`
  already holds, not two atomics every lookup wrote. A Local view's `Stats` is the
  parent's shared-cache totals plus its own memo hits; memo hits are never pushed to the
  parent, which would put a shared write back on the ~1.4 ns hot path.

Tradeoff: a lookup that reaches the shared cache costs ~1 ns more than before (serial hit
8.8 → 9.9 ns); inlinable wrappers and removing the counters were tried and did not recover
it. It matters only to callers that bypass `Local()`; render paths use it and got faster
(memo hit 1.59 → 1.38 ns, `Local()` 188 → 46 ns).

### Temporary files in a per-run directory

The writer's tile file and the disk store's spill files go into one `os.MkdirTemp`
directory under `--tmp-dir` (default: next to the output, the volume that must hold the
archive anyway). The CLI (`cli.MakeTmpDir`) removes that directory, and
`<output>.partial`, on exit, on errors and on SIGINT/SIGTERM. Before that, interrupted
runs left multi-gigabyte `pmtiles-*.tmp` files beside the output; removing one directory
is simpler and more robust than making every component clean up its own files. A crash or
SIGKILL still leaves it behind. All argument validation runs before the directory
and the writer are created, so a bad flag cannot strand files.

Unlinking temp files right after creation, so that a SIGKILL or OOM kill frees their space,
was evaluated and rejected: it is Unix-only (Go opens files without `FILE_SHARE_DELETE`,
so Windows cannot delete open files), it makes multi-GB disk use invisible to `du` and `ls`
during a run, and the empty per-run directory would still leak. Instead, a run warns at
startup about `.geotiff2pmtiles-tmp-*` / `.pmtransform-tmp-*` directories left in the temp
location and never deletes them, since another run may own them. `<output>.partial` has a
fixed name, so a run killed during Finalize leaves at most one, which the next run
truncates.

### Spilling and --mem-limit

Spilling is continuous: every non-uniform tile goes to the spill goroutine as soon as it
is `Put`. `MemoryLimitBytes` only caps the encoded bytes still queued (not yet written)
before `Put` blocks. The 256-slot channel already bounds that queue to about
(257 + workers) × tile size, so the limit only matters when it is small (below about 70 MB
at 256 px or 280 MB at 512 px) or with 1024 px tiles.

Auto is 0.9 × RAM (or the cgroup v2 `memory.max` / v1 `memory.limit_in_bytes` on Linux,
if lower) minus the runtime's `Sys` minus 2 GB, floored at 256 MB. Auto used to return 0
below 512 MB, which means "keep every tile in RAM": the worst choice on exactly the small
or undetectable hosts where the formula came out low (below about 2.85 GB of RAM, or when
RAM detection failed), and containers saw host RAM. The banner prints the resolved value
("Spill queue: N MB (auto)").

The backpressure wait follows the standard `sync.Cond` rule: the condition variable
(`memBytes`) is decremented under the same mutex the waiter holds between its check and
`Wait`. The I/O goroutine used to release bytes and broadcast without that lock, so a
broadcast between a waiter's check and its `Wait` was lost and, at the end of a level,
the worker stayed parked at 0% CPU. Increments in `Put` need no lock, because they can
only keep a waiter waiting.

### Disk tile store errors and cancellation

`DiskTileStore` records its first error and stops writing after it: a short write would
shift every later offset, and the level fails anyway. It still releases queued bytes after
an error, so no producer can park forever. `Get` keeps its nil-on-failure signature for
compatibility, so callers must check `Err()` after reading children, because nil is also
"missing tile"; `Put` and `Drain` return the sticky error. Spill write errors used to be
only logged, and read-back or decode errors made lower zooms silently lose tiles.

Each zoom level runs under `context.WithCancelCause` (stdlib, no errgroup dependency); the
first error wins. A worker error used to stop only that worker: the others finished the
level, and once all had returned, the batch producer blocked forever. Workers check the
context per tile, not per batch, because a batch is 32 max-zoom tiles; producers select on
`ctx.Done` and close their channel via defer.

### Disk tile store memory accounting

The disk tile store tracks memory usage to enforce the configured limit. Three fixes:

**Map pre-allocation**: Go map hints sized to the total tile count (e.g. 112M) waste
gigabytes on empty hash buckets. Fixed by capping pre-allocation to the working set
(tiles that fit in the memory limit).

**Map overhead tracking**: The memory counter only tracked encoded byte slices, ignoring
Go map entry overhead (~128 bytes/uniform entry, ~64 bytes/disk index entry). A
`mapOverhead` counter now tracks it for the store's statistics. It is not part of the
backpressure condition: it only grows (one entry per tile ever written), so gating on it
would deadlock once enough tiles had been spilled.

**Gray tile RGBA leak**: `AsImage()` for gray tiles allocated an RGBA buffer via
`GetRGBA()` that was never returned to the pool. Fixed by caching the expanded image
in `t.img` so that `Release()` returns it.

A remaining design option: the store's spill file duplicates the writer's temp file byte
for byte. If `WriteTile` returned the (offset, length) it stored, or the writer exposed an
`io.ReaderAt`, `DiskTileStore` could index into the writer's temp file and read with
pread, dropping the spill file, `ioLoop` and `memCond`. Generation would peak at about
1.25·f·W (f = the max zoom's share of the archive, about 0.6-0.75) and write about 2 W in
total. The cost is a `TileWriter` interface change (mock writers; `zoomCap` embeds
`TileWriter`), and the in-RAM path must stay for `--no-spill`.

## Encoding and platforms

### Native libwebp (replacing WASM)

The WebP encoder/decoder uses native libwebp via CGo instead of the previous WASM-based
approach (`gen2brain/webp` via `tetratelabs/wazero`). The WASM encoder was the #1 CPU
bottleneck (~41% of CPU, 81s) and caused 51 GB of WASM memory growth allocations due to
per-encode heap instantiation. Native libwebp eliminates this entirely: no WASM runtime
overhead, no per-call memory growth, and the C encoder runs 3-5x faster. The tradeoff is
that a default build needs `CGO_ENABLED=1` and libwebp on the build machine (see
BUILDING.md).

The `!cgo` file (`webp_stub.go`) is no longer a stub: `CGO_ENABLED=0` builds decode WebP
with `golang.org/x/image/webp` (lossy colours converted as libwebp does, see Straight
alpha) and encode with `HugoSmits86/nativewebp`, a pure-Go VP8L (lossless) encoder. There
is no usable pure-Go lossy VP8 encoder — the only CGo-free route to lossy output is libwebp
compiled to WASM under wazero, which was the slow path this project already left behind —
so `--quality` is ignored in such builds. Lossless WebP is still smaller than PNG, so the
nodata jpeg → webp switch no longer needs a PNG fallback. Cross-compilation without a C
toolchain (`CGO_ENABLED=0 GOOS=… go build`) keeps working, which is how CI still builds the
released Linux and macOS binaries. `encode.Formats()` says "webp (lossless only)" in that
case and both CLIs print it under `--version`.

### Windows WebP: MSYS2 prebuilts, statically linked

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

### Windows support

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

## PMTiles output

### Minimum zoom: whole extent in one tile

The auto minimum zoom is the highest zoom at which the whole data extent fits in a single
tile: the whole world or Europe (which straddles the prime meridian) → 0, Switzerland → 6.
Every archive then has a one-tile overview, and no level is spent on tiles that each show
a sliver of the data. The east and south edges are exclusive, so data ending exactly on a
tile boundary (lon 0–90) counts as one tile, not two. Data crossing the antimeridian gets
0. `pmtransform` applies the same rule to the source bounds and only ever extends downward.

### CLI defaults and validation

`--format` defaults to `auto` rather than `jpeg`: Go's `flag` package cannot tell an
explicit `--format jpeg` from the default, so auto-detection used to override a user
who asked for JPEG. Now `auto` is resolved once (terrarium for elevation, webp with
nodata, else jpeg) and any explicit value is final. The same applies to `--bands auto`,
where the default `1,2,3` used to be invalid for 2-band input. Flag values are validated
up front (quality, power-of-two tile size, zoom range, band numbers against each file's
band count, float input only as terrarium, every file's CRS), so mistakes fail in
milliseconds instead of producing an empty or noisy archive with exit code 0.

### Progress output

The live `\r` redraw is used only when stderr is a character device (`os.File.Stat`
`ModeCharDevice`; works for Windows consoles; `/dev/null` counts as a terminal, which is
harmless). Otherwise the bar prints one final line per zoom level: a `\r` frame every
100 ms filled log files with ~36k frames an hour. A multi-hour level therefore logs
nothing until it ends; a periodic line would be the next step if that matters.

### Root directory 16 KiB budget

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

### Writer index and run merging

The writer keeps one 24-byte `Entry` per written tile until `Finalize`: a full-coverage
global z13 level is 67.1M tiles (1.61 GB), z0-13 89.5M entries (2.15 GB).

- `optimizeRunLengths` used to allocate a second index-sized slice at `Finalize` (another
  2.15 GB there); it now merges runs in place. It also honours input run lengths (adding
  adjacent runs, with a uint32 overflow guard; RunLength 0 counts as 1): `pmheader
  --rebuild-dirs`, which passes entries unexpanded, used to cut every run to its first
  tile. `BuildDirectory` sorts and merges its argument in place, so the caller's contents
  are unspecified afterwards.
- `Writer.addEntry` compacts when the slice is at capacity and 4 × the dedup hits since
  the last compaction are at least the new entries: it sorts that tail (workers interleave
  Hilbert batches, so runs only show once sorted) and merges runs of one deduplicated blob
  in place. Runs across chunk boundaries are merged by `BuildDirectory`. Archives without
  repeats never sort; the sort runs under `w.mu` and stalls writers briefly, amortised to
  about one extra full sort. 262,144 interleaved fill tiles in 524 runs leave 1,381
  entries instead of 262,144.
- `NumAddressedTiles` and the capacity of the Finalize copy list come from counters,
  because entries may be runs.

A composite whose ocean is nodata and not filled writes no ocean tiles and gains nothing
from run merging: 24 B per written tile remain, plus 16 B per unique tile at `Finalize`.
Packing entries or an on-disk external sort would shrink that further.

### Writer dedup

Real repeats are uniform or fill tiles, whose encodings are small: 256 px PNG 1.1 KB, JPEG
1.6 KB, WebP 0.2 KB; 512 px 4.5/4.7/0.6 KB; 1024 px 18.9/17.0/2.0 KB. Only tiles of at
most tileSize²/16 bytes (4 KiB, 16 KiB and 64 KiB at 256, 512 and 1024 px) are hashed and
remembered, which keeps the dedup map (about 45 B per entry) and the clustering `seen` map
to small tiles instead of one entry per tile; the dedup map is freed before clustering.
Tradeoff: identical large tiles are stored twice. A hash hit is confirmed with a pread and
`bytes.Equal` under `w.mu` (about 1-2 µs per hit, from the page cache); it used to compare
only the FNV-64a hash and length.

### Finalize

`Finalize` writes `<output>.partial` next to the output, then calls `Sync` and `Close` and
renames it over the output; any failure removes the partial and the temp tile file. It
used to truncate the output with `os.Create` before copying any data and ignored the close
error, so a failed re-run destroyed the previous archive.

- The partial is created with `os.OpenFile(..., 0o666)`, not `CreateTemp`, whose 0600
  would make a served archive unreadable to the web-server user.
- It must not go in `--tmp-dir`, which may be another filesystem (a rename across
  filesystems fails with EXDEV).
- `f.Sync` is `F_FULLFSYNC` on macOS: a few seconds once per run, kept for rename
  durability.
- Re-running over an existing archive needs room for the old and the new archive until
  the rename.

The clustered temp copy is gone: new offsets are assigned in memory and each unique tile
is streamed from the temp file straight into the archive. Memory at `Finalize` is the
index, 16 B per unique tile for the copy list and the `seen` map for small tiles only
(before: 45 B per unique tile in the dedup map plus 45 B in the `seen` map). Disk
amplification on a synthetic run (58 MB archive; W = archive size):

| | Written | Peak |
|---|---|---|
| Before | 4.00 W: temp W + spill ~1 W + clustered W + output W | 2.00 W |
| Now | 3.00 W (clustered copy removed) | 2.00 W (temp + partial at the end of Finalize; about 1.9 W at the end of z_max-1) |

### Archive bounds

Header and metadata bounds come from one function, `pmtiles.archiveBounds`, so they cannot
drift. Data crossing the antimeridian (MaxLon > 180) is recorded as MinLon -180, MaxLon
180, with the centre longitude `math.Remainder((min+max)/2, 360)` (176.5..182.1 gives
179.3, 179..189 gives -176). TileJSON 3.0.0 says bounds "MUST NOT wrap around the
ante-meridian" and the centre must lie within them; MapLibre's `validateBounds` clamps east
to 180, and `TileBounds.contains` needs minX ≤ x < maxX, so west > east shows nothing. The
PMTiles v3 spec only says min/max longitude. The E7 conversions clamp to ±180: 260° used to
overflow the int32 field (platform-dependent; 2147483647 on arm64).

`Generate` keeps the unwrapped bounds for `TilesInBounds`; only the archive records
-180..180. Consequently `pmtransform`, which reads the header, fills the whole latitude
band with `--fill-missing` over a crossing archive; a rebuild without fill derives lower
zooms from the parents of existing tiles and does not enumerate the band.

### Header bounds precision

The bounds and centre passed through float32 before E7 encoding: up to 7.6e-6° (about
0.85 m) off, possibly leaving the bbox slightly inside the data, and a header read and
written back (pmheader patching the zoom) shifted its bounds. The exported float32 fields
were kept because `cmd/pmtransform` and the integration tests build `[4]float32` from
them. `Header` carries an unexported exact E7 shadow instead. Invariant: `NewHeader` and
`DeserializeHeader` set the shadow first, then the float32 fields from it; `Serialize` and
`Bounds()` use the shadow for every field whose float32 value still equals the shadow's,
and the float32 value for fields the caller changed. New code that builds a Header with
bounds must go through the same path, or precision silently drops back to float32.
`NewHeader` rounds minimums down and maximums up (the bbox always contains the data) and
the centre to nearest, after snapping `v*1e7` to the nearest integer when within 1e-3 E7
units, so decimal inputs like 45.82 do not come out as 45.8199999.

### Caller-supplied metadata keys

`WriterOptions.Extra` is merged into the metadata JSON after the derived keys, so an
archive assembled from many sources can record them (e.g. the scenes behind a composite).
A caller's value wins on a clash, by design. `NewWriter` rejects an `Extra` that cannot be
JSON-encoded: the error would otherwise surface in `Finalize`, after every tile had been
written, and before the fix it was ignored and the archive shipped with empty metadata.
The metadata `format` names every tile type (`mvt`, `avif` and `mlt` were "unknown").

### Description metadata provenance

PMTiles archives record processing provenance in the `description` field of the metadata
JSON. When `geotiff2pmtiles` creates an archive, the description captures both the
processing options (format, quality, tile size, zoom, resampling, nodata color, fill)
and source information (file count, every EPSG code, CRS extent for a single CRS, WGS84
extent, pixel size, data format of the first file, coverage holes). When `pmtransform`
transforms an archive, it reads the source description via `ReadMetadata()` and prepends
the new processing steps above it. This creates a stacked provenance trail where each
transformation layer is visible in the output metadata, enabling users to trace the full
history of how an archive was produced.

### Startup settings display

Always print the effective configuration (format, tile size, zoom range, resampling,
concurrency, nodata color and fill, the resolved spill-queue limit, input count, output
path) at startup. Placed after all auto-detection (zoom, format) has resolved so the
values shown are what will actually be used. Printed unconditionally rather than gated
behind `--verbose` because knowing the active settings is essential for reproducibility
and debugging.

## PMTiles reader

### Run-length index

`OpenReader` used to expand every run-length entry into a map slot and a slice element per
addressed tile; a global z0-13 archive (~89M tiles, mostly in long ocean runs) needed
about 6 GB before pmtransform did any work. The Reader now keeps the entries as sorted,
unexpanded runs (RunLength ≥ 1), so memory is proportional to directory entries. Any new
lookup must binary-search the runs (the last entry with TileID ≤ id, then
id - TileID < RunLength); never expand runs into a per-tile map or slice. `TilesAtZoom`
still returns one zoom's coordinates as a slice, because the `PMTilesReader` interface in
`internal/tile/transform.go` needs `len()` and random access; it counts first and
allocates once. A full-coverage global z13 max zoom is ~67M × 24 B = 1.6 GB there, a known
limit of that interface (an iterator would need the tile package to change).

### Internal compression

Directories and metadata are decompressed per `Header.InternalCompression`. None and
Unknown are read raw, following the pmtiles JS reader's convention for Unknown; gzip is
gunzipped; brotli and zstd fail with a clear error, as in go-pmtiles. Everything used to be
gunzipped, so valid archives with uncompressed directories failed with `gzip: invalid
header`. Adding zstd later is one case in `decompress` (klauspost/compress is already a
dependency) plus a fixture. `DeserializeDirectory` stays gzip-only for compatibility; new
code calls `DeserializeDirectoryCompressed` with the header's value, as checkpmtiles and
pmheader do.

### Leaf directories

Leaf pointers (RunLength 0) are followed to any depth; a leaf nested in a leaf, with all
its tiles, used to be dropped silently. The guard is a visited set keyed on each
directory's absolute file offset, with the root marked too. That allows any legitimate
depth, bounds the work by the size of the leaf section, and reports cycles and double
references in corrupt archives. A fixed depth limit would do neither: go-pmtiles stops at
depth 3 and would still permit fan-out bombs.

### TileIDToZXY

IDs ≥ (4^32-1)/3 cannot be addressed with 64-bit IDs; the zoom search computed 4^z as
`n*n`, which overflows from z=32 on, so such IDs looped forever. The function returns
z = -1 for them instead of panicking or changing its signature. The closed form
`bits.Len64(3*id+1)` would overflow exactly at that boundary.

## pmtransform

### Separate binary

`pmtransform` is a standalone CLI rather than a subcommand of `geotiff2pmtiles`. The
GeoTIFF-to-PMTiles pipeline involves CRS detection, reprojection, and COG tile caching —
none of which apply when transforming an existing PMTiles archive. Keeping them as separate
binaries avoids bloating either tool with the other's concerns and makes the usage
clear: `geotiff2pmtiles` for initial conversion, `pmtransform` for post-processing.

### Passthrough fast path

When no format change or nodata recolouring is needed (e.g. just removing zoom levels, or
filling missing tiles), raw tile bytes are copied from the source archive to the output
without decoding or re-encoding. This avoids lossy re-compression artifacts and is
significantly faster (150 ms instead of 820 ms for the Natural Earth example).

### Mode selection and added levels

`tile.SelectTransformMode` takes Rebuild, FormatChanged, NodataColor, MinZoom and
SourceMinZoom and returns the mode plus whether levels are added below the source's min
zoom. FillMissing is deliberately not an input: fill tiles are written pre-encoded in
passthrough, with an encoder for the source format, while the writer's tile type stays the
source's. So the CLI creates an encoder when the mode is not passthrough, when levels are
added, or when FillMissing is set, and changes the tile type only in the first two cases.

Levels below the source's min zoom are produced inside `tile.Transform` by a second pass
in rebuild mode anchored at the source's min zoom, after the main pass has copied or
re-encoded the existing levels. Routing that case through a full rebuild (as before)
decoded and re-encoded every level, with generation loss, just to add a few small ones.
The rebuild pass also re-renders the anchor level; a writer wrapper (`zoomCap`) drops
those tiles. Moving this from `cmd/pmtransform` into `tile.Transform` means the CLI and
the integration helper run the same code (the helper still rebuilt whenever the min zoom
was below the source's). It extends `Transform`'s semantics: passthrough or re-encode with
MinZoom below the header's used to write nothing at those zooms, and without an encoder it
now returns an error. `--max-zoom` above the source max is rejected: there is no detail to
add, and the old behaviour wrote fill-only levels that rendered black for JPEG.

### Max-zoom anchored rebuild

When rebuilding the pyramid (resampling change or adding lower zoom levels), the tool
reads max-zoom tiles from the source archive and uses them as the base layer. Lower zoom
levels are then rebuilt by downsampling from the level above — exactly matching how
`geotiff2pmtiles` works with COG sources. This ensures consistent quality across the
pyramid regardless of what resampling was used in the original archive. Without
`--fill-missing`, each lower zoom visits only the distinct parents of the tiles one level
down (`parentTiles`), not every position in the header bounds: two z6 tiles at opposite
edges of the world used to visit 610 empty positions, and a crossing archive's bounds are
now the whole latitude band.

### Terrarium-aware rebuild

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

### Tile size discovery

The PMTiles v3 header does not store tile size (only format via `TileType`). When
`--tile-size` is omitted (default: keep source), pmtransform discovers the source tile
size by reading and decoding one tile from the max zoom level and using its image
dimensions. If no tile can be decoded (e.g. all empty), it falls back to 256. This
ensures 512px archives stay 512px when rebuilding with `--resampling lanczos` instead
of inadvertently reducing to 256. An explicit `--tile-size` must match the discovered
size: no code path resizes source tiles, and a mismatch produced archives with mixed
tile sizes (re-encode) or panicked (rebuild).

### Sparse fill optimization

When `pmtransform --rebuild --fill-missing` processes sparse datasets, most tile
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

### Metadata and bounds passthrough

Source metadata keys that pmtransform does not rewrite (a composite's scene list, a
stretch, `vector_layers`, …) reach the output through `WriterOptions.Extra`. Since `Extra`
overrides derived keys, only keys outside `derivedMetadataKeys` (name, description,
format, type, minzoom, maxzoom, bounds, center, attribution, encoding) are passed;
`TestDerivedMetadataKeysCoverTheWriter` writes an archive with every option set and
requires every key the writer emits to be in that list, so it cannot drift silently. A
non-terrarium `encoding` is carried forward as `WriterOptions.Encoding`. The output bounds
come from `Header.Bounds()`, the exact E7 values: a passthrough used to change header
bytes 102-117.

## Command-line tools

### Shared CLI helpers

`internal/cli` holds what geotiff2pmtiles and pmtransform duplicated: colour parsing, the
fill-flag resolution, `IsFlagSet`, human-readable sizes, the profiling flags and the
per-run temp directory. `IsFlagSet` takes a `*flag.FlagSet` so tests use private sets.
`Profiles.Start` returns a stop function that callers defer; `log.Fatal` paths skip it as
before. The profiling flags are `--cpu-profile`/`--mem-profile`, in the tools' kebab-case
style; `-cpuprofile`/`-memprofile` remain as deprecated aliases bound to the same
variables.

### checkpmtiles

It reads the whole leaf section in one range request and walks it in memory, so memory
equals the leaf-section size; validating a big archive over HTTP then costs one request
rather than one per leaf. The walk follows RunLength 0 entries to any depth, guards
against loops and pointers outside the section, and parses with the header's internal
compression. Trailing bytes are checked for none and gzip. It fails on MinZoom > MaxZoom,
on zero addressed tiles (counted from the directories, since a header value of 0 means
unknown in the spec), and on a nonzero header count that disagrees with them. It used to
check only the first leaf and failed uncompressed-directory archives with a gzip error.

### pmheader

- Metadata is read and written in the archive's internal compression. `--rebuild-dirs`
  switches header and metadata to gzip, because `BuildDirectory` emits gzip; otherwise
  the header would say none over gzip directories.
- `--sync-metadata` (default on) updates only the keys the metadata already has, each in
  its JSON form: a number or array stays one, strings are written as the writer writes
  them. It applies before `--set`/`--unset`, so explicit values win. The 127-byte in-place
  fast path is kept whenever nothing actually changes in the metadata; a corrupt metadata
  section fails a sync-only patch, with a hint to `--sync-metadata=false`.
- `readAllEntries` mirrors `pmtiles.readEntries`: any depth, loop guard.

### coginfo -raw

`cmd/debug` duplicated half of coginfo; `coginfo -raw` now prints what only it printed
(IFD 0 tags, IsFloat, nodata, the first tile's offset, size and bytes, the float range,
the last guarded by IsFloat). It goes through the reader, so striped files report their
promoted virtual tiles (e.g. 4×256 for a 4×4 strip).

## Testing

### Integration test plausibility checks

Satellite integration tests use a shared `assertPlausiblePMTiles` helper that validates
8 properties of every PMTiles output: zoom range, tile type, geographic bounds (within
tolerance), center point containment, tile count minimums and non-decreasing growth
across zooms, first-tile image decoding, clustering flag, and metadata key presence.
Each dataset defines a `plausibilityExpectation` with approximate bounds and tolerances.
This catches regressions that simple "tile count > 0" checks would miss — e.g. bounds
shifted by a projection bug, missing metadata keys, or broken tile encoding.
