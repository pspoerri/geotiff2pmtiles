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
- **Five resampling methods**: Lanczos-3, bicubic, bilinear, nearest, and mode (for
  categorical rasters such as land cover).
- **Transparent borders**: nodata values with tolerance, plus an edge flood-fill that
  cleans up JPEG-smeared scan borders.
- **Fast**: parallel tile generation; lower zooms are downsampled from higher ones.
- **Traceable output**: spec-compliant PMTiles v3. Source metadata
  and processing steps are recorded in the archive description.

## Supported Input

| | |
| --- | --- |
| **Files** | GeoTIFF and Cloud Optimized GeoTIFF. TIFF with a `.tfw` world file. Directories are scanned recursively |
| **Layout** | Tiled or stripped; pixel-interleaved, or band-interleaved (tiled: JPEG only; stripped: anything but JPEG). JPEG-compressed files must be tiled |
| **Compression** | JPEG, LZW, Deflate, ZSTD, uncompressed — with predictors |
| **Pixel types** | 8-bit gray/RGB/RGBA · 16-bit unsigned (linear or log rescaling), including bit-packed 9-15 bit depths such as 15-bit Sentinel-2 L2A · 32/64-bit float and 16/32-bit signed integer (elevation, written as Terrarium; signed 16-bit also as rescaled grayscale) |
| **Bands** | Any band can be mapped to R, G, B or alpha, e.g. NIR-R-G false color |
| **CRS** | Native: UTM (EPSG:326xx, 327xx, 258xx), Swiss LV95 (2056), WGS84 (4326), Web Mercator (3857). Other EPSG codes known to [wroge/crs](https://github.com/wroge/crs) use a slower fallback, which is logged when used (see Limitations). |

### Limitations

- **One CRS per run.** All files are projected with the first file's CRS, and its pixel
  size sets the auto max zoom. Reproject mixed inputs (e.g. Sentinel-2 tiles from several
  UTM zones) to one CRS first: `gdalwarp -t_srs EPSG:4326 ...`.
- **CRS from GeoKeys only.** User-defined or WKT-only CRSs are rejected, and `.prj` files
  are ignored. A plain TIFF with a `.tfw` has no CRS, so it is guessed from the coordinate
  ranges: lon/lat → 4326, Swiss LV95 ranges → 2056, anything else → 3857. A UTM world file
  lands in the wrong place; set the CRS with `gdal_edit.py -a_srs EPSG:xxxx` instead.
- **Fallback CRSs** are assumed to use meters. Geographic CRSs other than EPSG:4326 and
  foot-based CRSs get a wrong auto max zoom; set `--max-zoom` or reproject.
- **Float data and signed integers other than 16-bit** can only be written as `terrarium`.
- **Palette (color-table) TIFFs** render their index values as gray; expand them first
  (`gdal_translate -expand rgb`).

## Usage

```
geotiff2pmtiles [flags] <input-dir-or-files...> <output.pmtiles>
```

### Flags

| Flag            | Default       | Description                                        |
| --------------- | ------------- | -------------------------------------------------- |
| `--format`      | `auto`        | Tile encoding: `auto`, `jpeg`, `png`, `webp`, `terrarium`. `auto` picks `terrarium` for float/signed-integer elevation data, `webp` when nodata is active, else `jpeg`. An explicit value is always used as given |
| `--quality`     | `85`          | JPEG/WebP quality, 1-100. Ignored for `png`/`terrarium`, and for WebP in builds without libwebp (lossless only) |
| `--min-zoom`    | auto (`-1`)   | Minimum zoom level. Auto: the highest zoom at which the whole extent fits in one tile (whole world → 0), capped at `--max-zoom` |
| `--max-zoom`    | auto (`-1`)   | Maximum zoom level, 0-30. Auto: from the first file's pixel size and `--tile-size` |
| `--tile-size`   | `256`         | Output tile size in pixels: a power of two from 64 to 4096 |
| `--concurrency` | `NumCPU`      | Number of parallel workers (>= 1)                  |
| `--resampling`  | `bicubic`     | Interpolation method: `lanczos`, `bicubic`, `bilinear`, `nearest`, `mode` |
| `--resampling-gamma` | `1.0`    | Brighten interpolated output: `out = 255·(v/255)^(1/gamma)`. 1.0 = off, must be > 0, typical 1.5–2.2 for dB-scaled SAR. Ignored for `nearest`/`mode` and `terrarium` |
| `--mem-limit`   | auto (`0`)    | MB of encoded tiles allowed to queue for the spill file before workers pause. Auto: 90% of RAM minus current usage minus 2 GB; if that is under 512 MB, spilling is **off**. Containers report host RAM, so set it explicitly in Docker/Kubernetes |
| `--no-spill`    | `false`       | Disable disk spilling (keep all tiles in memory)   |
| `--tmp-dir`     | output dir    | Directory for temporary files (about 2× the output size at peak) |
| `--fill-color`  | `0,0,0,0`     | RGBA color (`"0,0,0,255"` or `"#000000ff"`) that replaces transparent/nodata pixels and fills tile positions without data inside the bounds, so uncovered areas render transparent rather than as the viewer's background. `""` leaves missing tiles absent. Not applied to `jpeg` output unless set explicitly (JPEG has no transparency) |
| `--attribution` |               | Attribution string for data sources (stored in metadata) |
| `--type`        | `baselayer`   | Layer type: `baselayer`, `overlay`                 |
| `--bands`       | `auto`        | 1-indexed band numbers for R,G,B, e.g. `4,1,2` for NIR-R-G false color. Auto: `1,2,3`, or gray from band 1 for 1-2 band input. Bands beyond the file's band count are an error |
| `--alpha-band`  | `auto`        | Alpha band: `auto` (band 4 of 8-bit input with 4+ bands), `none`, or a 1-indexed band number |
| `--rescale`     | `auto`        | Rescale mode: `auto`, `linear`, `log`, `none`. Auto: a GDAL band-description preset if present, else linear over `--rescale-range` for 16-bit input, none for 8-bit. Ignored for `terrarium` |
| `--rescale-range` | auto        | Input value range `min,max` (min < max). Auto: from GDAL statistics, else sampled pixels, over all bands; the selected range is logged |
| `--nodata`      | from file     | Integer in [-32768, 65535]; pixels whose bands all equal it are transparent. Default: the first file's GDAL_NODATA tag. Ignored for `terrarium`, which always uses each file's GDAL_NODATA tag |
| `--nodata-tolerance` | `0`      | Per-band tolerance for `--nodata` matching. Use 4–8 for borders that come from lossy JPEG sources, where the strict nodata value is smeared by compression |
| `--nodata-flood` | `false`     | Flood-fill from the COG outer edges through near-nodata pixels: only the region reachable from the image boundary becomes transparent, so interior dark pixels (text, shadows, canopy) stay opaque even with a wide tolerance. Requires nodata. Pair with a generous `--nodata-tolerance` (e.g. 40) for JPEG-smeared scans. Costs ~W·H/8 bytes RAM per source plus an upfront decode pass. Not for `terrarium` |
| `--verbose`     | `false`       | Verbose progress output                            |
| `--version`     |               | Print version and exit                             |
| `--cpuprofile`  |               | Write CPU profile to file                          |
| `--memprofile`  |               | Write memory profile to file                       |

Invalid values (quality outside 1-100, a zoom range with min > max, a band the file does
not have, …) stop the run before any output is written.

### How defaults are chosen

Everything is detected from the first input file and logged at startup:

1. **CRS**: the EPSG code in the GeoKeys, else guessed from `.tfw` coordinates (see Limitations).
2. **Format** (`--format auto`): float or signed-integer samples → `terrarium`; nodata
   active → `webp`; otherwise `jpeg`.
3. **Bands and rescale** (16-bit input): GDAL band descriptions (red/green/blue/nir) set the
   band order and value range. Otherwise linear over the GDAL
   STATISTICS_MINIMUM/MAXIMUM, else over a pixel scan (`Auto rescale range: [...]`).
4. **Alpha**: band 4 for 8-bit files with 4 or more bands.
5. **Nodata**: `--nodata`, else the file's GDAL_NODATA tag.
6. **Zoom**: max from the pixel size and `--tile-size`; min is the highest zoom at which the
   whole extent fits in one tile.

### Examples

Convert a directory of GeoTIFFs with auto zoom detection (scans subfolders recursively):

```bash
./geotiff2pmtiles --verbose integration/testdata/swissimage/ output.pmtiles
```

Convert specific files with a custom zoom range and PNG format:

```bash
./geotiff2pmtiles --format png --min-zoom 10 --max-zoom 18 \
  file1.tif file2.tif output.pmtiles
```

Categorical data (e.g. land cover classification) with mode resampling:

```bash
./geotiff2pmtiles --format png --resampling mode \
  landcover/ classification.pmtiles
```

Fill transparent/nodata areas with a solid color (e.g. black):

```bash
./geotiff2pmtiles --fill-color "0,0,0,255" --format png \
  input/ output.pmtiles
```

Historic JPEG-compressed scan with a black border (output auto-switches to WebP for transparency):

```bash
./geotiff2pmtiles --nodata 0 --nodata-tolerance 8 \
  scan.tif output.pmtiles
# Nodata is active; switching output format jpeg → webp so transparency is preserved.
```

Same source but the boundary still shows JPEG-smear speckles — flood-fill from the image edge with a wide tolerance removes the fringe while preserving interior dark detail:

```bash
./geotiff2pmtiles --nodata 0 --nodata-tolerance 40 --nodata-flood \
  scan.tif output.pmtiles
# Building nodata flood masks for 1 source(s)...
# Flood masks built in 710ms
```

Elevation data (auto-detects float or signed-integer GeoTIFF, e.g. GEBCO bathymetry, and selects Terrarium encoding):

```bash
./geotiff2pmtiles --verbose dem/ elevation.pmtiles
```

The same data as a grayscale image (range auto-detected and logged):

```bash
./geotiff2pmtiles --format webp gebco_2026_geotiff/ gebco-gray.pmtiles
```

Multi-band satellite data (auto-detected — no band/rescale flags needed):

```bash
./geotiff2pmtiles --format png data2/ rgbnir.pmtiles
# Auto-detected: multispectral-rgbnir (bands 1,2,3, rescale linear [0, 10000])
```

RGBNIR satellite data with log rescaling and NIR as alpha:

```bash
./geotiff2pmtiles --bands 1,2,3 --alpha-band 4 --rescale log \
  --rescale-range 1,10000 --format png data2/ rgbnir.pmtiles
```

NIR as alpha (vegetation opaque, water/urban transparent — useful as overlay):

```bash
./geotiff2pmtiles --alpha-band 4 --rescale linear \
  --rescale-range 0,10000 --format png --type overlay data2/ nir-alpha.pmtiles
```

Convert a plain TIFF with TFW world file (global Natural Earth data):

```bash
./geotiff2pmtiles --format webp --max-zoom 6 data_tfw/ output.pmtiles
```

## pmtransform

Transform an existing PMTiles archive: change format, zoom levels, resampling,
or fill empty tiles. Always creates a new file — the original is never modified.

```
pmtransform [flags] <input.pmtiles> <output.pmtiles>
```

Tiles are copied byte for byte unless something requires decoding them: a new
`--format` or a `--fill-color` re-encodes every tile, and `--rebuild` rebuilds the lower
levels by downsampling. Zoom levels below the source's min zoom are always added by
downsampling the source's lowest level, so the existing levels can still be copied.

### Flags

| Flag            | Default       | Description                                        |
| --------------- | ------------- | -------------------------------------------------- |
| `--format`      | keep source   | Target tile encoding: `jpeg`, `png`, `webp`        |
| `--quality`     | `85`          | JPEG/WebP quality, 1-100 (ignored for `png`)       |
| `--min-zoom`    | auto (`-1`)   | Minimum zoom level. Auto: the source's min zoom, extended down to the zoom where all data fits in one tile |
| `--max-zoom`    | keep source   | Maximum zoom level, at most the source max zoom (let the viewer overzoom instead) |
| `--tile-size`   | keep source   | Must equal the source tile size; resizing tiles is not supported |
| `--resampling`  | `bicubic`     | Downsampling method for rebuilt or added levels: `lanczos`, `bicubic`, `bilinear`, `nearest`, `mode` |
| `--rebuild`     | `false`       | Rebuild every level below max zoom by downsampling (needed to apply `--resampling` to existing levels) |
| `--terrarium`   | auto-detected | Treat tiles as terrarium-encoded elevations so downsampling happens in elevation space. Auto-detected from archive metadata written by geotiff2pmtiles |
| `--fill-color`  | none          | RGBA color (`"0,0,0,255"` or `"#000000ff"`) that replaces transparent pixels and fills missing tile positions within the bounds. Forces re-encoding |
| `--concurrency` | `NumCPU`      | Number of parallel workers (>= 1)                  |
| `--mem-limit`   | auto (`0`)    | MB of encoded tiles allowed to queue for the spill file during a rebuild; see geotiff2pmtiles |
| `--no-spill`    | `false`       | Disable disk spilling                              |
| `--tmp-dir`     | output dir    | Directory for temporary files (about 2× the output size at peak) |
| `--attribution` | keep source   | Attribution string for data sources                |
| `--type`        | keep source   | Layer type: `baselayer`, `overlay`                 |
| `--verbose`     | `false`       | Verbose progress output                            |
| `--version`     |               | Print version and exit                             |
| `--cpuprofile`  |               | Write CPU profile to file                          |
| `--memprofile`  |               | Write memory profile to file                       |

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

Replace transparent pixels with black and fill missing tile positions:

```bash
./pmtransform --fill-color "0,0,0,255" input.pmtiles output.pmtiles
```

Keep only z10-z14 (tiles are copied, not re-encoded):

```bash
./pmtransform --min-zoom 10 --max-zoom 14 input.pmtiles output.pmtiles
```

## Disk space, memory and temporary files

- Both tools write temporary files (`pmtiles-tiles-*.tmp`, `pmtiles-tilestore-*.tmp`,
  `pmtiles-clustered-*.tmp`) into a per-run `.geotiff2pmtiles-tmp-*` /
  `.pmtransform-tmp-*` directory next to the output, or under `--tmp-dir`.
- Keep about twice the expected archive size free there; that is the peak while the
  archive is finalized.
- The directory is removed on success, on errors and on Ctrl-C/SIGTERM. Only a crash or a
  hard kill (SIGKILL, out-of-memory) leaves it behind; it is then safe to delete.
- Memory: see `--mem-limit`. In containers, set it explicitly.

## Utilities

| Command | Purpose |
| ------- | ------- |
| `coginfo <file.tif>` | COG metadata: EPSG, size, bounds, overviews |
| `pmheader --show <file.pmtiles>` | Show the header and metadata |
| `pmheader [flags] <in.pmtiles> [out.pmtiles]` | Patch header fields and metadata without touching tile data; `--rebuild-dirs` fixes an oversized root directory. **Without an output path the input is edited in place** |
| `checkpmtiles <file-or-URL>` | Validate a PMTiles v3 archive (exit code 1 on error) |
| `debug <file.tif>` | Low-level TIFF/IFD dump for development |

`make build-all` builds geotiff2pmtiles, pmtransform, checkpmtiles and pmheader into
`dist/`; the others run with `go run ./cmd/<name>/`.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full project structure, pipeline description, memory efficiency details, and how to add new projections.

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for tests, integration test data and profiling, and
[BUILDING.md](BUILDING.md) for building from source.

## Installation

Prebuilt binaries for Linux, macOS and Windows (amd64 and arm64) are attached to each
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
