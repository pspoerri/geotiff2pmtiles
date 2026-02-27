# 2026-02-27: Nodata in BandConfig — multi-band transparency and --nodata flag

## Problem

Nodata values were only applied in the legacy single-band 8-bit path (spp≤2, default
BandConfig). Multi-band and 16-bit files (e.g. ESA Worldcover RGBNIR uint16 nodata=0)
had their nodata pixels decoded as opaque black/RGB instead of transparent (alpha=0).
The GDAL_NODATA tag (42113) was already parsed into `IFD.NoData` but not used in the
general decode path.

## Changes

### `internal/cog/reader.go`

- **`BandConfig`**: added `HasNodata bool` and `Nodata float64` fields. When set,
  pixels where all file bands equal the raw nodata value are decoded as transparent
  (alpha=0) before rescaling. Valid when HasNodata is true.
- **`BandConfig.String()`**: new method returning a human-readable summary including
  bands, rescale range, and nodata value. Used in log output.
- **`DetectPreset()`**: populates `cfg.HasNodata`/`cfg.Nodata` from the GeoTIFF
  GDAL_NODATA tag so the returned preset is self-contained.
- **`decodeRawTile()` general path**: parses nodata from `cfg.HasNodata`/`cfg.Nodata`
  (preset/override) or falls back to `r.ifds[0].NoData`. In the pixel loop, checks
  all `spp` file bands against the nodata value. If all match, the pixel is emitted
  as (0,0,0,0). Only applied when `effectiveAlpha < 0` (no dedicated alpha band).

### `cmd/geotiff2pmtiles/main.go`

- **`--nodata`** flag: overrides the auto-detected nodata value. Must be a non-negative
  integer ≤ 65535.
- **Nodata logic** (after `parseBandConfig`): CLI `--nodata` takes precedence; if not
  set and preset didn't supply a value, auto-detects from `sources[0].NoData()`.
- **`log.Printf("Band config: %s", bandCfg)`**: always logged after the config is
  finalized, showing bands, rescale, and nodata so users can verify the active config.
- Auto-detected preset log updated to use `BandConfig.String()`.

### `integration/satellite_esaworldcover_test.go`

- `TestESAWorldCoverPreset`: added assertions for `HasNodata=true` and `Nodata=0`.

## Behavior

For ESA Worldcover RGBNIR (uint16, 4 bands, nodata=0):
- Auto-detected preset now includes `HasNodata=true, Nodata=0`
- Pixels with all four bands = 0 are decoded as transparent
- Log shows: `Auto-detected: multispectral-rgbnir (bands 1,2,3, rescale linear [0, 10000], nodata 0)`
- Then: `Band config: bands 1,2,3, rescale linear [0, 10000], nodata 0`

For SWIR (uint8, nodata=255): nodata is auto-detected from the GeoTIFF tag; pixels
with all bands = 255 become transparent.
