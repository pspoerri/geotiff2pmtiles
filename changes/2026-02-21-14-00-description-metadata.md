# PMTiles metadata: description, attribution, type

Processing steps and source information are now recorded in the `description` field
of the PMTiles metadata JSON. `attribution` and `type` are configurable via CLI flags.

## geotiff2pmtiles

When creating a new PMTiles archive from GeoTIFF sources, the description includes:

- **Processing options**: tool version, output format/quality, tile size, zoom range, resampling
- **Source information**: number of files, EPSG code, CRS extent, WGS84 extent, pixel size,
  data format (compression, band count, bit depth), coverage holes

New flags: `--attribution` (data source credit) and `--type` (baselayer/overlay, default: baselayer).

## pmtransform

When transforming an existing PMTiles archive, the description includes the new processing
steps (mode, format, zoom, resampling, fill color) prepended above the source file's
existing description. This creates a stacked provenance trail — each transformation adds
its parameters on top of the previous history.

`attribution` and `type` are carried forward from the source metadata by default, and
can be overridden with `--attribution` and `--type`.

## Modified files

- `internal/pmtiles/header.go` — Added `Name`, `Description`, `Attribution`, and `Type`
  fields to `WriterOptions`
- `internal/pmtiles/writer.go` — `buildMetadata()` uses configurable name, description,
  attribution, and type instead of hardcoded values
- `internal/pmtiles/reader.go` — Added `ReadMetadata()` to read and decompress the
  gzipped JSON metadata blob from an archive
- `internal/cog/reader.go` — Added `FormatDescription()` returning a human-readable
  summary of the raster format (e.g. "LZW, 3x uint8")
- `cmd/geotiff2pmtiles/main.go` — Builds description from source metadata and processing
  options; new `--attribution` and `--type` flags
- `cmd/pmtransform/main.go` — Reads source metadata via `ReadMetadata()`, prepends
  transform processing steps; carries forward attribution/type from source, with
  `--attribution` and `--type` override flags
