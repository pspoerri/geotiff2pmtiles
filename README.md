# GeoTIFF to PMTiles

A memory-efficient toolset for working with PMTiles v3 archives: convert GeoTIFF/COG files
and transform existing archives (change format, zoom levels, resampling, fill empty tiles).

For more information on the PMTiles format, see the [PMTiles documentation](https://docs.protomaps.com/pmtiles/).

You can visualize generated PMTiles files at [pmtiles.io](https://pmtiles.io/).

## Features

- **Low memory use**: sources are memory-mapped and read tile by tile; encoded tiles are
  streamed to temporary files instead of being held in RAM (see [Disk space, memory and
  temporary files](#disk-space-memory-and-temporary-files)).
- **Four tile formats**: JPEG, PNG, WebP, and Terrarium for elevation data.
- **Sensible defaults**: zoom range, tile format, band order and rescale range are
  detected from the input.
- **Mixed inputs**: files in different CRSs (e.g. Sentinel-2 tiles from several UTM zones)
  go into one archive, each reprojected from its own CRS.
- **Five resampling methods**: Lanczos-3, bicubic, bilinear, nearest, and mode (for
  categorical rasters such as land cover).
- **Transparent borders**: nodata values with tolerance, GDAL internal masks, and an edge
  flood-fill that cleans up JPEG-smeared scan borders. Where one file is transparent, the
  next file that covers the spot shows through.
- **Fast**: parallel tile generation; lower zooms are downsampled from higher ones.
- **Traceable output**: spec-compliant PMTiles v3. Source metadata
  and processing steps are recorded in the archive description.

## Supported Input

| | |
| --- | --- |
| **Files** | GeoTIFF and Cloud Optimized GeoTIFF, including BigTIFF. TIFF with a `.tfw` world file. Directories are scanned recursively |
| **Layout** | Tiled or stripped; pixel-interleaved, or band-interleaved (tiled: JPEG only; stripped: anything but JPEG). Other band-interleaved files fail at open; rewrite them with `gdal_translate -co INTERLEAVE=PIXEL`. JPEG strips are decoded strip by strip; sparse tiles and strips (GDAL `SPARSE_OK`) read as nodata |
| **Compression** | JPEG, LZW, Deflate, ZSTD, uncompressed — with predictors |
| **Pixel types** | 8-bit gray/RGB/RGBA · 1-7 bit gray (raw values; stretch with `--rescale linear --rescale-range`) · palette images (expanded through their color map) · WhiteIsZero gray · 9-16 bit unsigned (linear or log rescaling), bit-packed (e.g. 15-bit Sentinel-2 L2A) or padded to 16 bits · 32/64-bit float and 16/32-bit signed integer (elevation, written as Terrarium; signed 16-bit also as rescaled grayscale) |
| **Bands** | Any band can be mapped to R, G, B or alpha, e.g. NIR-R-G false color |
| **Transparency** | GDAL_NODATA, `--nodata`, an alpha band, or a GDAL internal mask (how JPEG COGs usually mark nodata), which makes masked pixels transparent |
| **CRS** | Native: UTM (EPSG:326xx, 327xx, 258xx), Swiss LV95 (2056), WGS84 (4326), Web Mercator (3857). Other EPSG codes known to [wroge/crs](https://github.com/wroge/crs) use a slower fallback, which is logged when used. Files may use different CRSs. Pixel-is-point rasters (e.g. Copernicus DEM, SRTM) are placed as GDAL places them |
| **Longitudes** | -180..180 and 0..360 grids, including 0..360 grids whose edge lies half a pixel west of 0 (pixel centres from 0, as in GRIB-derived GFS or ERA5 data); data that crosses the antimeridian (e.g. UTM zones 1 and 60); rasters around a pole (e.g. EPSG:3413, 3031) get bounds over every longitude |

### Limitations

- **CRS from GeoKeys only.** User-defined or WKT-only CRSs are rejected unless
  `--source-epsg` names the EPSG code, and `.prj` files are ignored. A plain TIFF with a `.tfw` has no CRS, so it is guessed from the coordinate
  ranges (lon/lat → 4326, Swiss LV95 ranges → 2056, anything else → 3857), and one
  warning gives the number of such files and an example; it is not printed when
  `--source-epsg` is set, and `coginfo` marks a guessed code. Set the CRS of all inputs
  with `--source-epsg`, or per file with `gdal_edit.py -a_srs EPSG:xxxx`.
- **Settings from the first file.** The band layout and bit depth (auto `--bands` and
  rescaling), the band-description preset, and the default nodata for image output come
  from the first input file. Inputs that differ need `--bands`, `--rescale-range` or
  `--nodata`. The CRS, pixel size and value range are read from every file.
- **Float data and signed integers other than 16-bit** can only be written as `terrarium`.
- **Internal masks** apply to image output only: `terrarium` ignores them, and masked
  pixels still count towards the auto `--rescale-range`. A mask is used only when the
  full-resolution image has one and is tiled; a masked file's overviews without their own
  mask are skipped, which makes low zooms slower but keeps them masked.
- **Large single-strip TIFFs** are decoded whole: a 12000×8000 RGB image stored as one
  strip needs 1.2-1.6 GB of memory. `gdal_translate -co TILED=YES` avoids that.
- **Longitudes**: grids written as e.g. -10..350 are not supported. Files on both sides of
  the antimeridian given as -180..180 (one ending at 180, one starting at -180) merge to
  whole-world bounds: the output is correct, but every tile column is visited.
- **Pyramid aliasing**: each lower zoom is downsampled 2x with the `--resampling` kernel at
  its native width, so `bicubic` and `lanczos` overviews alias more than `bilinear` on fine
  periodic texture (fields, roofs). Use `--resampling bilinear` if that shows.

## Usage

```
geotiff2pmtiles [flags] <input-dir-or-files...> <output.pmtiles>
```

### Flags

| Flag            | Default       | Description                                        |
| --------------- | ------------- | -------------------------------------------------- |
| `--format`      | `auto`        | Tile encoding: `auto`, `jpeg`, `png`, `webp`, `terrarium`. `auto` picks `terrarium` for float/signed-integer elevation data, `webp` when nodata or an internal mask is active, else `jpeg`. An explicit value is always used as given |
| `--quality`     | `85`          | JPEG/WebP quality, 1-100. Ignored for `png`/`terrarium`, and for WebP in builds without libwebp (lossless only) |
| `--min-zoom`    | auto (`-1`)   | Minimum zoom level. Auto: the highest zoom at which the whole extent fits in one tile (whole world → 0), capped at `--max-zoom` |
| `--max-zoom`    | auto (`-1`)   | Maximum zoom level, 0-30. Auto: from the finest file's pixel size and `--tile-size` |
| `--tile-size`   | `256`         | Output tile size in pixels: a power of two from 64 to 4096 |
| `--concurrency` | `NumCPU`      | Number of parallel workers (>= 1)                  |
| `--resampling`  | `bicubic`     | Interpolation method: `lanczos`, `bicubic`, `bilinear`, `nearest`, `mode` |
| `--resampling-gamma` | `1`      | Brighten interpolated output: `out = 255·(v/255)^(1/gamma)`. 1 = off, must be > 0, typical 1.5–2.2 for dB-scaled SAR. Ignored for `nearest`/`mode` and `terrarium` |
| `--source-epsg` | `0` (from the files) | EPSG code of the input CRS for every input file, e.g. `32632`, `25832` or `21781`. Overrides the GeoTIFF keys and the guess made for world-file inputs |
| `--bands`       | `auto`        | 1-indexed band numbers for R,G,B, e.g. `4,1,2` for NIR-R-G false color. Auto: `1,2,3`, or gray from band 1 for 1-2 band input. Bands beyond a file's band count are an error |
| `--alpha-band`  | `auto`        | Alpha band: `auto` (band 4 of 8-bit input with 4+ bands), `none`, or a 1-indexed band number |
| `--rescale`     | `auto`        | Rescale mode: `auto`, `linear`, `log`, `none`. Auto: a GDAL band-description preset if present, else linear over `--rescale-range` for 9-16 bit input, none for 8-bit. `none` maps the full range of the sample type (0..2^bits-1, e.g. 0..65535, or 0..32767 for 15-bit; -32768..32767 for Int16) to 0..255. Ignored for `terrarium` |
| `--rescale-range` | auto        | Input value range `min,max` (min < max). Auto: the min/max over all files of the bands selected by `--bands` (never the alpha band), from GDAL `STATISTICS_*` metadata when every selected band has them, else from up to 64 tiles of the coarsest overview. Files without usable values (e.g. all-nodata ocean tiles) are skipped. The selected range is logged |
| `--nodata`      | from file     | Pixels whose bands all equal this value are transparent. Image output: an integer in [-32768, 65535]; default: the first file's GDAL_NODATA tag. `terrarium`: any float, overriding every file's GDAL_NODATA tag (default: each file's own tag) |
| `--nodata-tolerance` | `0`      | Per-band tolerance for `--nodata` matching. Use 4–8 for borders that come from lossy JPEG sources, where the strict nodata value is smeared by compression. Image output only |
| `--nodata-flood` | `false`     | Flood-fill from the COG outer edges through near-nodata pixels: only the region reachable from the image boundary becomes transparent, so interior dark pixels (text, shadows, canopy) stay opaque even with a wide tolerance. Requires nodata. Pair with a generous `--nodata-tolerance` (e.g. 40) for JPEG-smeared scans. Costs ~W·H/8 bytes RAM per source plus an upfront decode pass. Not for `terrarium` |
| `--nodata-color` | `none`       | RGBA color (`"0,0,0,255"` or `"#000000ff"`) that replaces transparent/nodata pixels. `none` keeps them transparent |
| `--fill-missing` | `0,0,0,0`    | RGBA color of the solid tiles written at tile positions inside the bounds that have no data, so uncovered areas render transparent rather than as the viewer's background. `none` or `""` leaves them absent. Not applied to `jpeg` output unless set explicitly (JPEG has no transparency) |
| `--fill-color`  |               | Deprecated: sets both `--nodata-color` and `--fill-missing`, and cannot be combined with them |
| `--mem-limit`   | auto (`0`)    | MB of encoded tiles allowed to queue for the spill file before workers pause. Tiles are always streamed to a temp file as they are produced (turn that off with `--no-spill`). Auto: 90% of RAM, or of the cgroup memory limit on Linux, minus current usage minus 2 GB, never below 256 MB |
| `--no-spill`    | `false`       | Disable disk spilling (keep all tiles in memory)   |
| `--tmp-dir`     | output dir    | Directory for temporary files (about 2× the output size at peak) |
| `--attribution` |               | Attribution string for data sources (stored in metadata) |
| `--type`        | `baselayer`   | Layer type: `baselayer`, `overlay`                 |
| `--verbose`     | `false`       | Verbose progress output                            |
| `--version`     |               | Print version and exit                             |
| `--cpu-profile` |               | Write a CPU profile to the given file              |
| `--mem-profile` |               | Write a heap profile to the given file at exit     |
| `--cpuprofile`, `--memprofile` |  | Deprecated spellings of `--cpu-profile` and `--mem-profile` |

Invalid values stop the run before any output is written: quality outside 1-100, a zoom
range with min > max, a band the file does not have, an unknown CRS, a file without a
geotransform (`--source-epsg` sets only the CRS, not the pixel grid), bounds beyond the
poles (e.g. a projected file given `--source-epsg 4326`), and so on.

### Nodata color and missing tiles

Two different things can be colored:

- **Transparent pixels** in tiles that have data (nodata, masked or alpha-0 pixels):
  `--nodata-color`. Off by default, so they stay transparent.
- **Missing tiles**, i.e. tile positions inside the bounds where no file has data:
  `--fill-missing`. By default they are written as transparent tiles, so a viewer shows
  nothing there instead of its background color; `jpeg` output leaves them absent unless
  the flag is given.

Recoloring nodata without filling missing tiles (`--nodata-color` alone) leaves missing
tiles, and the matching quarters of the tiles above them, transparent. `--fill-color`, the
old flag that set both, still works but prints a deprecation warning. JPEG has no alpha:
in both tools, a translucent color is written as its RGB, opaque, and a warning names it.

### How defaults are chosen

Detected settings are logged at startup:

1. **CRS**: each file's EPSG code from its GeoKeys, else guessed from its `.tfw`
   coordinates (see Limitations). `--source-epsg` sets it for all files.
2. **Format** (`--format auto`): float or signed-integer samples in the first file →
   `terrarium`; nodata active or a GDAL internal mask in any file → `webp`; otherwise
   `jpeg`.
3. **Bands and rescale** (9-16 bit input): GDAL band descriptions (red/green/blue/nir) in
   the first file set the band order and value range. Otherwise linear over the GDAL
   STATISTICS_MINIMUM/MAXIMUM of the rendered bands, else over a pixel scan, merged over
   all files (`Auto rescale range: [...]`).
4. **Alpha**: band 4 for 8-bit files with 4 or more bands; a GDAL internal mask also makes
   pixels transparent.
5. **Nodata**: `--nodata`, else the first file's GDAL_NODATA tag (`terrarium`: each file's
   own tag).
6. **Zoom**: max from the finest file's pixel size and `--tile-size`; min is the highest
   zoom at which the whole extent fits in one tile.

### Examples

The examples that name `integration/testdata/` run on the sample data that
`make test-integration-download` fetches (see [DEVELOPMENT.md](DEVELOPMENT.md)).

Convert a directory of GeoTIFFs with auto zoom detection (scans subfolders recursively):

```bash
./geotiff2pmtiles --verbose integration/testdata/swissimage/ output.pmtiles
```

Convert specific files with a custom zoom range and PNG format:

```bash
./geotiff2pmtiles --format png --min-zoom 10 --max-zoom 18 \
  file1.tif file2.tif output.pmtiles
```

Files in different CRSs, e.g. Sentinel-2 tiles from UTM zones 32N and 33N, go into one
archive as they are:

```bash
./geotiff2pmtiles --format webp T32TNS.tif T33TUM.tif sentinel2.pmtiles
```

Categorical data (e.g. land cover classification) with mode resampling:

```bash
./geotiff2pmtiles --format png --resampling mode \
  landcover/ classification.pmtiles
```

Replace transparent/nodata areas with black and fill tile positions without data with black
tiles:

```bash
./geotiff2pmtiles --nodata-color "0,0,0,255" --fill-missing "0,0,0,255" --format png \
  input/ output.pmtiles
```

Historic JPEG-compressed scan with a black border (output auto-switches to WebP for transparency):

```bash
./geotiff2pmtiles --nodata 0 --nodata-tolerance 8 \
  scan.tif output.pmtiles
# Nodata or an internal mask is active; using webp so transparency is preserved (override with --format=jpeg).
```

Same source but the boundary still shows JPEG-smear speckles — flood-fill from the image edge with a wide tolerance removes the fringe while preserving interior dark detail:

```bash
./geotiff2pmtiles --nodata 0 --nodata-tolerance 40 --nodata-flood \
  scan.tif output.pmtiles
# Building nodata flood masks for 1 source(s)...
# Flood masks built in 710ms
```

Elevation data (auto-detects float or signed-integer GeoTIFF and selects Terrarium encoding):

```bash
./geotiff2pmtiles --verbose integration/testdata/copernicus/ elevation.pmtiles
```

A DEM whose voids are -9999 without a GDAL_NODATA tag:

```bash
./geotiff2pmtiles --format terrarium --nodata -9999 dem/ elevation.pmtiles
```

Signed-integer elevation (e.g. GEBCO bathymetry) as a grayscale image (range
auto-detected and logged):

```bash
./geotiff2pmtiles --format webp gebco_2026_geotiff/ gebco-gray.pmtiles
```

Multi-band satellite data (auto-detected — no band/rescale flags needed):

```bash
./geotiff2pmtiles --format png integration/testdata/esaworldcover/ rgbnir.pmtiles
# Auto-detected: multispectral-rgbnir (bands 1,2,3, rescale linear [0, 10000], nodata 0)
```

RGBNIR satellite data with log rescaling and NIR as alpha:

```bash
./geotiff2pmtiles --bands 1,2,3 --alpha-band 4 --rescale log \
  --rescale-range 1,10000 --format png integration/testdata/esaworldcover/ rgbnir.pmtiles
```

NIR as alpha (vegetation opaque, water/urban transparent — useful as overlay):

```bash
./geotiff2pmtiles --alpha-band 4 --rescale linear --rescale-range 0,10000 \
  --format png --type overlay integration/testdata/esaworldcover/ nir-alpha.pmtiles
```

Convert a plain TIFF with TFW world file (global Natural Earth data):

```bash
./geotiff2pmtiles --format webp --max-zoom 6 integration/testdata/naturalearth/ output.pmtiles
```

## pmtransform

Transform an existing PMTiles archive: change format, zoom levels, resampling,
or fill empty tiles. Always creates a new file — the original is never modified.

```
pmtransform [flags] <input.pmtiles> <output.pmtiles>
```

Tiles are copied byte for byte unless something requires decoding them: a new
`--format` or `--nodata-color` re-encodes every tile, and `--rebuild` rebuilds the lower
levels by downsampling. `--fill-missing` adds fill tiles without touching the existing
ones. Zoom levels below the source's min zoom are always added by downsampling the
source's lowest level, so the existing levels can still be copied. Metadata keys that
pmtransform does not rewrite (e.g. a composite's scene list) are carried over.

### Flags

| Flag            | Default       | Description                                        |
| --------------- | ------------- | -------------------------------------------------- |
| `--format`      | keep source   | Target tile encoding: `jpeg`, `png`, `webp`        |
| `--quality`     | `85`          | JPEG/WebP quality, 1-100 (ignored for `png`)       |
| `--min-zoom`    | auto (`-1`)   | Minimum zoom level. Auto: the source's min zoom, extended down to the zoom where all data fits in one tile |
| `--max-zoom`    | keep source (`-1`) | Maximum zoom level, at most the source max zoom (let the viewer overzoom instead) |
| `--tile-size`   | keep source (`-1`) | Must equal the source tile size; resizing tiles is not supported |
| `--resampling`  | `bicubic`     | Downsampling method for rebuilt or added levels: `lanczos`, `bicubic`, `bilinear`, `nearest`, `mode` |
| `--rebuild`     | `false`       | Rebuild every level below max zoom by downsampling (needed to apply `--resampling` to existing levels) |
| `--terrarium`   | `false` (auto-detected) | Treat tiles as terrarium-encoded elevations so downsampling happens in elevation space. Auto-detected from archive metadata written by geotiff2pmtiles |
| `--nodata-color` | `none`       | RGBA color (`"0,0,0,255"` or `"#000000ff"`) that replaces transparent pixels. Forces re-encoding |
| `--fill-missing` | `none`       | RGBA color of the solid tiles written at tile positions inside the bounds that the source lacks. Does not force re-encoding. For an archive that crosses the antimeridian, only the longitudes around the data are filled |
| `--fill-color`  |               | Deprecated: sets both `--nodata-color` and `--fill-missing`, and cannot be combined with them |
| `--concurrency` | `NumCPU`      | Number of parallel workers (>= 1)                  |
| `--mem-limit`   | auto (`0`)    | MB of encoded tiles allowed to queue for the spill file while levels are rebuilt or added; see geotiff2pmtiles |
| `--no-spill`    | `false`       | Disable disk spilling (keep all tiles in memory)   |
| `--tmp-dir`     | output dir    | Directory for temporary files (about 2× the output size at peak) |
| `--attribution` | keep source   | Attribution string for data sources                |
| `--type`        | keep source   | Layer type: `baselayer`, `overlay`                 |
| `--verbose`     | `false`       | Verbose progress output                            |
| `--version`     |               | Print version and exit                             |
| `--cpu-profile` |               | Write a CPU profile to the given file              |
| `--mem-profile` |               | Write a heap profile to the given file at exit     |
| `--cpuprofile`, `--memprofile` |  | Deprecated spellings of `--cpu-profile` and `--mem-profile` |

### Examples

Convert WebP tiles to PNG format:

```bash
./pmtransform --format png input.pmtiles output.pmtiles
```

Add lower zoom levels down to z8 (existing levels are copied as-is):

```bash
./pmtransform --min-zoom 8 --verbose input.pmtiles output.pmtiles
```

Rebuild the entire pyramid with Lanczos resampling:

```bash
./pmtransform --rebuild --resampling lanczos input.pmtiles output.pmtiles
```

Fill missing tile positions with black tiles, copying the existing tiles as they are:

```bash
./pmtransform --fill-missing "0,0,0,255" input.pmtiles output.pmtiles
```

Also replace transparent pixels with black (re-encodes every tile):

```bash
./pmtransform --nodata-color "0,0,0,255" --fill-missing "0,0,0,255" input.pmtiles output.pmtiles
```

Keep only z10-z14 (tiles are copied, not re-encoded):

```bash
./pmtransform --min-zoom 10 --max-zoom 14 input.pmtiles output.pmtiles
```

## Disk space, memory and temporary files

- Both tools write their temporary files (`pmtiles-tiles-*.tmp` from the archive writer,
  `pmtiles-tilestore-*.tmp` spill files) into a per-run `.geotiff2pmtiles-tmp-*` /
  `.pmtransform-tmp-*` directory next to the output, or under `--tmp-dir`.
- At the end the archive is assembled as `<output>.partial` next to the output and renamed
  over it only when it is complete, so a failed run never destroys an existing archive.
- Keep about twice the expected archive size free next to the output; that is the peak
  while the archive is finalized. Replacing an existing archive needs room for the old one
  too. With `--tmp-dir` on another disk, that disk needs about 1.9× the archive size and
  the output disk 1×.
- The temp directory and the `.partial` file are removed on success, on errors and on
  Ctrl-C/SIGTERM. Only a crash or a hard kill (SIGKILL, out-of-memory) leaves them behind;
  the next run warns about leftover directories but does not delete them, since another
  run may be using them.
- Memory: `--mem-limit` caps the encoded tiles waiting for the spill file. The automatic
  value uses the container's memory limit on Linux when it is visible under
  `/sys/fs/cgroup` (Docker, Kubernetes); otherwise set it explicitly. The archive index takes about 24 bytes per written tile until
  the end of the run (about 2 GB for a global z0-13 archive without gaps); runs of
  identical fill tiles are merged while writing.

## Utilities

| Command | Purpose |
| ------- | ------- |
| `coginfo [-raw] <file.tif>` | COG metadata: EPSG (marked when guessed), size, bounds, levels, GDAL metadata; test-reads a tile of every level |
| `pmheader --show <file.pmtiles>` | Show the header and metadata |
| `pmheader [flags] <in.pmtiles> [out.pmtiles]` | Patch header fields and metadata without touching tile data (uncompressed or gzip-compressed directories, leaves at any depth); `--rebuild-dirs` fixes an oversized root directory. **Without an output path the input is edited in place** (see below) |
| `checkpmtiles <file-or-URL>` | Validate a PMTiles v3 archive: header, every directory, zoom range (min ≤ max, every tile inside it) and tile count (exit code 1 on error) |

`make build-all` builds geotiff2pmtiles, pmtransform, checkpmtiles and pmheader into
`dist/`; `coginfo` runs with `go run ./cmd/coginfo/` and is also attached to each release.

### coginfo flags

| Flag        | Default | Description |
| ----------- | ------- | ----------- |
| `-raw`      | `false` | Also print the raw tags of the full-resolution IFD (compression, samples, sample format, predictor, nodata), its first tile's offset, size and bytes, and the value range of the first tile of float or signed input |
| `-version`  |         | Print version and exit |

For pixel-is-point files coginfo prints the corner origin, as `gdalinfo` does.

### checkpmtiles flags

| Flag        | Default | Description |
| ----------- | ------- | ----------- |
| `-version`  |         | Print version and exit |

Archives with uncompressed or gzip-compressed directories are checked, with leaf
directories at any depth.

### pmheader flags

| Flag              | Default | Description |
| ----------------- | ------- | ----------- |
| `--show`          | `false` | Print header and metadata, then exit |
| `--min-zoom`, `--max-zoom`, `--center-zoom` | | Override the header's MinZoom, MaxZoom or CenterZoom (0–30) |
| `--min-lon`, `--min-lat`, `--max-lon`, `--max-lat` | | Override a header bound, in decimal degrees |
| `--center-lon`, `--center-lat` | | Override the header's center, in decimal degrees |
| `--tile-type`     |         | Override the tile type: `png`, `jpeg`, `webp`, `mvt` |
| `--sync-metadata` | `true`  | Also update the metadata keys `minzoom`, `maxzoom`, `format`, `bounds` and `center` that the metadata has, to match the header overrides. `--set` wins over it. Metadata compressed with brotli or zstd is left as it is, with a warning |
| `--set`           |         | Set a metadata key: `key=value`, where a valid JSON value is stored as JSON and anything else as a string (repeatable) |
| `--unset`         |         | Remove a metadata key (repeatable) |
| `--metadata-file` |         | Replace the entire metadata with the JSON in this file |
| `--rebuild-dirs`  | `false` | Rebuild the directories to fit the 16 KiB root budget (fixes an oversized root); directories and metadata are then gzip-compressed |
| `--verbose`       | `false` | Print a summary of changes |
| `--version`       |         | Print version and exit |

When editing in place, pmheader overwrites the header where it is, and the metadata too if
its new encoding fits the old metadata section. Otherwise, and for `--rebuild-dirs`, it
writes a new file next to the archive, or next to the file a symlink points to, and
renames it over that file, keeping its permissions.

Examples:

```bash
pmheader --show map.pmtiles
pmheader --min-zoom 5 --max-zoom 14 map.pmtiles
pmheader --center-zoom 8 --center-lon 8.3 --center-lat 46.9 map.pmtiles
pmheader --set attribution='© OpenStreetMap' map.pmtiles patched.pmtiles
pmheader --rebuild-dirs map.pmtiles fixed.pmtiles
```

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full project structure, pipeline description, memory efficiency details, and how to add new projections.

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for tests, integration test data and profiling, and
[BUILDING.md](BUILDING.md) for building from source.

## Installation

Prebuilt binaries of geotiff2pmtiles, pmtransform and coginfo for Linux, macOS and Windows
(amd64 and arm64) are attached to each
[release](https://github.com/pspoerri/geotiff2pmtiles/releases). All of them support all
four tile formats; they differ only in the WebP encoder:

| Binary       | WebP encoder                                                              |
| ------------ | ------------------------------------------------------------------------- |
| Windows      | libwebp, statically linked (lossy + lossless) — a single `.exe`, no DLLs   |
| Linux, macOS | pure Go, lossless only (`--quality` is ignored for WebP)                  |

For lossy WebP on Linux or macOS, build from source with libwebp — see [BUILDING.md](BUILDING.md). `--version` tells you
which encoder a binary has:

```
$ geotiff2pmtiles --version
geotiff2pmtiles v1.2.0 (commit abc1234, built 2026-08-21T12:00:00Z)
formats: jpeg, png, webp, terrarium
```

A `CGO_ENABLED=0` build prints `webp (lossless only)` instead.

## License

MIT
