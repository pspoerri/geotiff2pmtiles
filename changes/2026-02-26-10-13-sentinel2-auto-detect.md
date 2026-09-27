# Satellite Data Auto-Detection from GDAL Metadata

Auto-detect band ordering and rescale range from GDAL_METADATA XML (tag 42112) so that
multi-band satellite GeoTIFFs work without manual `--bands` or `--rescale-range` flags.
Detection is generic — no product-specific presets. Works with any GDAL-created
multi-band GeoTIFF that has band descriptions (per-band DESCRIPTION items or a
dataset-level "bands" string with "Band N: BXX (Role)" format).

## Changes

- **cog/ifd.go**: Parse GDAL_METADATA XML tag (42112) into `GDALMeta` struct with
  two levels: `Items` (dataset-level) and `BandItems` (per-band, keyed by 0-indexed
  sample). Preserves the `sample` attribute needed for per-band DESCRIPTION/SCALE/OFFSET.

- **cog/reader.go**: Add `Preset` struct, `DetectPreset()` method, and `GDALMeta()`
  accessor. Band role detection from two sources:
  1. Per-band DESCRIPTION items — matches keywords (red, green, blue, nir, etc.)
  2. Dataset-level "bands" string — parses "Band N: BXX (Role)" entries
  Scale/offset: per-band SCALE/OFFSET → dataset-level → default 0-10000.
  Offset support for Landsat-style negative offsets.

- **cmd/geotiff2pmtiles/main.go**: In `parseBandConfig`, when `--rescale auto`
  and 16-bit data with no explicit `--rescale-range`, try `DetectPreset()` before
  erroring. Explicit flags always override.

- **cmd/coginfo/main.go**: Display GDAL metadata (dataset-level and per-band items)
  and detected preset.

- **cog/reader_test.go**: Tests for XML parsing (dataset-level, per-band, empty),
  band detection from "bands" string (standard and reordered), GDAL-standard
  DESCRIPTION (RGBNIR, RGB-only), Landsat-style offset, negative cases, and
  validation against the actual ESA WorldCover S2 RGBNIR file.
