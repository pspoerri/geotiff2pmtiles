# Fix: Empty Tile Artifacts & Scaling with Non-256 Tile Sizes

**Date**: 2026-02-19

## Problem

Two issues when running `geotiff2pmtiles --resampling nearest --tile-size 512 --format webp` on ESA data:
1. Weird artifacts around empty/sea tiles
2. Scaling not working properly

## Root Causes

### Bug 1: RGBA pool returning stale pixel data
`GetRGBA()` in `rgbapool.go` returned images from `sync.Pool` without clearing pixel data. In `renderTile`, pixels where no source data is found retained garbage from previous tiles. Boundary tiles (partial data coverage) showed fragments of unrelated tiles in their empty areas.

### Bug 2: `ResolutionAtLat` hardcoded to 256-pixel tiles
`ResolutionAtLat()` in `mercator.go` used `DefaultTileSize=256` regardless of actual tile size. With `--tile-size 512`, the output pixel resolution was computed as 2x too coarse, causing `OverviewForZoom` to select a coarser overview level than needed (blurry output).

### Bug 3: No nodata handling for single-band image data
For single-band GeoTIFF (e.g. ESA WorldCover), the GDAL nodata value (typically 0) was decoded as opaque black `(0,0,0,255)` instead of transparent `(0,0,0,0)`. Sea/empty areas appeared as solid black. The float/terrarium path handled nodata but the image path did not.

## Changes

- `internal/tile/rgbapool.go`: Added `clear(img.Pix)` when returning images from the pool
- `internal/coord/mercator.go`: Added `tileSize` parameter to `ResolutionAtLat()`
- `internal/tile/resample.go`: Pass actual tile size to `ResolutionAtLat()` in both `renderTile` and `renderTileTerrarium`
- `internal/cog/reader.go`: Parse GDAL nodata value in `decodeRawTile()` for single-band (spp≤2) data; set alpha=0 for pixels matching the nodata value
- `internal/coord/mercator_test.go`: Updated tests + added 512-tile-size test case
