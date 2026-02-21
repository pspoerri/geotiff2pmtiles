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

1. **Max-zoom tiles with fill**: Enumerate all positions from bounds (not just
   source tiles). For positions with source data, decode and apply fill transform
   (transparent → fill). For gaps, create fill tiles directly. No separate
   `fillEmptyTiles` pass needed during rebuild.

2. **Downsampling**: Nil children are substituted with fill tiles *before* calling
   `downsampleTile`. The downsample receives 4 tiles (real or fill) and operates
   normally — no fillColor param or transform in the downsample path.

3. **fillEmptyTiles**: Simplified — only used by passthrough/reencode modes.
   Rebuild handles all positions inline, eliminating the need for written-tile
   tracking or post-hoc gap filling.

## Changes

- `internal/tile/transform.go`: At max zoom with fill, enumerate all positions
  from bounds; create fill tiles for gaps inline; remove `fillEmptyTiles` from
  rebuild; simplify `fillEmptyTiles` signature (remove writtenTiles param)
- `internal/tile/downsample.go`: Keep `applyFillColorTransform` for max-zoom path;
  `downsampleTile` and `downsampleTileGray` unchanged (no fillColor param)
- `internal/tile/generator.go`, tests: Standard 6-param `downsampleTile`
