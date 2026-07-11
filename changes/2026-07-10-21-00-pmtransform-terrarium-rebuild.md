# Terrarium-aware pmtransform rebuild

## Problem

`pmtransform --rebuild` on a terrarium archive downsampled tiles with the
ordinary per-channel RGBA path. Terrarium packs elevation across R/G/B
(`elevation = R*256 + G + B/256 - 32768`), so averaging channels
independently corrupts values wherever the 4 source pixels straddle a 256 m
channel boundary. Measured on the Copernicus DEM against GDAL truth at z10:
mean abs error 27.3 m / max 107 m with 7 of 81 points >75 m off, vs
16.6 m / 51 m / 0 from the generator's elevation-space downsampler.

There was also no way to know an archive is terrarium: the PMTiles tile type
is just PNG.

## Fix

- `pmtiles.WriterOptions.Encoding` — written to the metadata JSON as
  `"encoding": "terrarium"` by geotiff2pmtiles when using terrarium format.
- `tile.TransformConfig.IsTerrarium` — rebuild uses
  `downsampleTileTerrarium` (same as the generator) when set.
- pmtransform auto-detects terrarium from the source metadata and offers an
  explicit `--terrarium` flag for archives written before the metadata key
  existed. Encoding is propagated to the output archive. Re-encoding
  terrarium to a lossy format (jpeg/webp) prints a warning.

## Testing

`TestCopernicusDEM` now rebuilds the terrarium archive via `runTransform`
(exercising metadata auto-detection end-to-end) and asserts decoded
elevations. CLI-validated on both paths (auto-detect and `--terrarium` on a
metadata-less archive): rebuilt pyramids match generator output exactly
(mean 16.6 m, max 51 m, 0 points >75 m on the 81-point GDAL truth grid).
