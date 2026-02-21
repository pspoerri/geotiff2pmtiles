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

Color transform applied only at the source level; downstream code unchanged:

1. **Max-zoom decoded tiles**: Transparent pixels (alpha=0) → fill color before
   packing into TileData.

2. **Downsampling**: Nil children are substituted with fill tiles *before* calling
   `downsampleTile`. The downsample receives 4 tiles (real or fill) and operates
   normally — no fillColor param or transform in the downsample path.

3. **fillEmptyTiles**: Unchanged — fills missing tile positions.

## Changes

- `internal/tile/transform.go`: Apply fill to max-zoom decoded tiles; substitute nil
  children with `newTileDataUniform(fill)` before `downsampleTile`
- `internal/tile/downsample.go`: Keep `applyFillColorTransform` for max-zoom path;
  remove fillColor from `downsampleTile` and `downsampleTileGray`
- `internal/tile/generator.go`, tests: Revert to 6-param `downsampleTile`
