# Empty as Color Transformation (Not Resampling)

**Date**: 2026-02-21

## Summary

Treat empty tiles and transparent/nodata pixels as a **color transformation** (source
color → target value) rather than as inputs to spatial resampling. Implemented the
transformation in the pmtransform rebuild pipeline.

## Rationale

- **Resampling** (bilinear, Lanczos, bicubic, mode) combines *valid* data. Interpolating
  over "empty" produces meaningless results; for categorical rasters, blending land
  cover with nodata yields invalid classes.

- **Color transformation** = lookup substitution: wherever the source color (e.g.
  transparent black for empty) appears, replace with the target (e.g. `--fill-color`).

- No resampling of empty; only substitution.

## Implementation

When `--fill-color` is set during pmtransform rebuild:

1. **Max-zoom decoded tiles**: Transparent pixels (alpha=0) in source tiles are
   substituted with the fill color before packing into TileData.

2. **Downsampled tiles**: After combining child tiles, any remaining transparent
   pixels (from nil children or nodata within tiles) are substituted with the fill
   color before returning. Applied in `downsampleTile` for the RGBA path and in the
   gray uniform path (uniform 0 → fill).

3. **fillEmptyTiles**: Unchanged — still fills missing tile positions with the
   solid color.

## Changes

- `internal/tile/downsample.go`: Added `applyFillColorTransform`, `downsampleTile(..., fillColor)`,
  `downsampleTileGray(..., fillColor)`; substitute transparent → fill when set
- `internal/tile/transform.go`: Apply fill to max-zoom decoded tiles; pass `cfg.FillColor`
  to `downsampleTile`
- `internal/tile/generator.go`: Pass `nil` for fillColor (no FillColor in generator)
- `internal/tile/downsample_test.go`: Updated call sites; added `TestDownsampleTile_FillColorTransform`
- `internal/tile/bench_test.go`: Updated call sites
- `DESIGN.md`: Added "Empty as color transformation" section
