# GeoTIFF to PMTiles

A memory-efficient toolset for working with PMTiles v3 archives: convert GeoTIFF/COG files
and transform existing archives (change format, zoom levels, resampling, fill empty tiles).

For more information on the PMTiles format, see the [PMTiles documentation](https://docs.protomaps.com/pmtiles/).

You can visualize generated PMTiles files at [pmtiles.io](https://pmtiles.io/).

## Features

- **Low memory use**: sources are memory-mapped and read tile by tile; output tiles spill to disk when RAM runs short.
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
| **Layout** | Tiled or stripped; pixel- or band-interleaved (band-interleaved strips) |
| **Compression** | JPEG, LZW, Deflate, ZSTD, uncompressed — with predictors |
| **Pixel types** | 8-bit RGB/RGBA · 16-bit unsigned (linear or log rescaling) · 32/64-bit float (elevation) |
| **Bands** | Any band can be mapped to R, G, B or alpha, e.g. NIR-R-G false color |
| **CRS** | Native: UTM (EPSG:326xx, 327xx, 258xx), Swiss LV95 (2056), WGS84 (4326), Web Mercator (3857). Any other EPSG code known to [wroge/crs](https://github.com/wroge/crs) works through a slower fallback, which is logged when used. |

## Usage

```
geotiff2pmtiles [flags] <input-dir-or-files...> <output.pmtiles>
```

### Flags

| Flag            | Default       | Description                                        |
| --------------- | ------------- | -------------------------------------------------- |
| `--format`      | `jpeg`        | Tile encoding: `jpeg`, `png`, `webp`, `terrarium`  |
| `--quality`     | `85`          | JPEG/WebP quality (1-100)                          |
| `--min-zoom`    | auto          | Minimum zoom level (default: max_zoom - 6)         |
| `--max-zoom`    | auto          | Maximum zoom level (auto-detected from resolution) |
| `--tile-size`   | `256`         | Output tile size in pixels                         |
| `--concurrency` | `NumCPU`      | Number of parallel workers                         |
| `--resampling`  | `bicubic`     | Interpolation method: `lanczos`, `bicubic`, `bilinear`, `nearest`, `mode` |
| `--resampling-gamma` | `1.0`    | Gamma correction for resampling output encoding (1.0 = disabled, typical 1.5–2.2 for dB-space to RGB) |
| `--mem-limit`   | auto          | Tile store memory limit in MB before disk spilling (0 = auto ~90% of RAM) |
| `--no-spill`    | `false`       | Disable disk spilling (keep all tiles in memory)   |
| `--fill-color`  | `0,0,0,0`     | Substitute transparent/nodata with RGBA color (color transform); also fill missing tile positions. E.g. `"0,0,0,255"` or `"#000000ff"` (default: transparent) |
| `--attribution` |               | Attribution string for data sources (stored in metadata) |
| `--type`        | `baselayer`   | Layer type: `baselayer`, `overlay`                 |
| `--bands`       | `1,2,3`       | 1-indexed band numbers for R,G,B output (e.g. `4,1,2` for NIR-R-G false color) |
| `--alpha-band`  | `auto`        | Alpha band: `auto` (band 4 for 8-bit spp>=4), `-1` (none), or 1-indexed band |
| `--rescale`     | `auto`        | Rescale mode: `auto`, `linear`, `log`, `none` (auto requires `--rescale-range` for 16-bit) |
| `--rescale-range` |             | Input value range `min,max` for rescaling (required for 16-bit data) |
| `--nodata`      |               | Nodata value: pixels with all bands equal to this integer are transparent (auto-detected from GeoTIFF if not set). When set without `--format`, output auto-switches from `jpeg` to `webp` so transparency is preserved. |
| `--nodata-tolerance` | `0`      | Per-band tolerance for `--nodata` matching. Use 4–8 for borders that come from lossy JPEG sources, where the strict nodata value is smeared by compression. |
| `--nodata-flood` | `false`     | Source-level flood-fill from the COG outer edges through near-nodata pixels. Only the connected component reachable from the image boundary becomes transparent; interior dark pixels (text, shadows, canopy) stay opaque even when tolerance is widened. Pair with a generous `--nodata-tolerance` (e.g. 40) to clean up JPEG-smeared boundaries. Costs ~W·H/8 bytes RAM per source plus an upfront decode pass (parallelized across `--concurrency` workers and up to 4 sources at once). |
| `--verbose`     | `false`       | Verbose progress output                            |
| `--version`     |               | Print version and exit                             |
| `--cpuprofile`  |               | Write CPU profile to file                          |
| `--memprofile`  |               | Write memory profile to file                       |

### Examples

Convert a directory of GeoTIFFs with auto zoom detection (scans subfolders recursively):

```bash
./geotiff2pmtiles --verbose integration/testdata/swissimage/ output.pmtiles
```

Convert specific files with custom zoom range and PNG format:

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

Elevation data (auto-detects float GeoTIFF and selects Terrarium encoding):

```bash
./geotiff2pmtiles --verbose dem/ elevation.pmtiles
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

### Flags

| Flag            | Default       | Description                                        |
| --------------- | ------------- | -------------------------------------------------- |
| `--format`      | keep source   | Target tile encoding: `jpeg`, `png`, `webp`        |
| `--quality`     | `85`          | JPEG/WebP quality (1-100)                          |
| `--min-zoom`    | keep source   | Minimum zoom level                                 |
| `--max-zoom`    | keep source   | Maximum zoom level                                 |
| `--tile-size`   | keep source   | Output tile size in pixels (inferred from first decoded tile) |
| `--resampling`  | `bicubic`     | Interpolation method: `lanczos`, `bicubic`, `bilinear`, `nearest`, `mode` |
| `--rebuild`     | `false`       | Force full pyramid rebuild (for resampling changes) |
| `--terrarium`   | auto-detected | Treat tiles as terrarium-encoded elevations so rebuild downsamples in elevation space. Auto-detected from archive metadata written by geotiff2pmtiles |
| `--fill-color`  | `0,0,0,0`     | Substitute transparent/nodata with RGBA color (color transform); also fill missing tile positions. E.g. `"0,0,0,255"` or `"#000000ff"` (default: transparent) |
| `--concurrency` | `NumCPU`      | Number of parallel workers                         |
| `--mem-limit`   | auto          | Tile store memory limit in MB (0 = auto ~90% of RAM) |
| `--no-spill`    | `false`       | Disable disk spilling                              |
| `--attribution` | keep source   | Attribution string for data sources                |
| `--type`        | keep source   | Layer type: `baselayer`, `overlay`                 |
| `--verbose`     | `false`       | Verbose progress output                            |
| `--version`     |               | Print version and exit                             |

### Examples

Convert WebP tiles to PNG format:

```bash
./pmtransform --format png input.pmtiles output.pmtiles
```

Extend zoom range by adding lower zoom levels (rebuilds pyramid):

```bash
./pmtransform --min-zoom 8 --verbose input.pmtiles output.pmtiles
```

Rebuild the entire pyramid with Lanczos resampling:

```bash
./pmtransform --rebuild --resampling lanczos input.pmtiles output.pmtiles
```

Substitute transparent/nodata with black and fill missing tile positions:

```bash
./pmtransform --fill-color "0,0,0,255" input.pmtiles output.pmtiles
```

Remove higher zoom levels (keep only z10-z14):

```bash
./pmtransform --min-zoom 10 --max-zoom 14 input.pmtiles output.pmtiles
```

## Utilities

```bash
go run ./cmd/coginfo/ <file.tif>            # COG metadata: EPSG, size, bounds, overviews
go run ./cmd/debug/ <file.tif>              # low-level TIFF/IFD debugging
go run ./cmd/checkpmtiles/ <file-or-URL>    # validate a PMTiles v3 archive
go run ./cmd/pmheader/ --show <file>        # show/patch header and metadata without touching tile data
```

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
