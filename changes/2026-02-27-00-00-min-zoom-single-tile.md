# Fix: Min zoom guarantees single-tile coverage

## Problem

The auto-detected minimum zoom used a fixed offset of `maxZoom - 6`, which did not
guarantee that the entire image fit within a single tile at the minimum zoom level.
For datasets where the natural "overview" tile is several levels below `maxZoom - 6`
(e.g. small country-scale datasets), the minimum zoom was unnecessarily high and the
image could still span multiple tiles.

## Changes

### `internal/coord/mercator.go`
- Added `MinZoomForSingleTile(minLon, minLat, maxLon, maxLat float64) int`
  Searches from zoom 1 upward and returns the highest zoom level at which all four
  corners of the bounding box fall within the same tile. This is the highest zoom
  where the full image is visible in a single tile without panning.

### `cmd/geotiff2pmtiles/main.go`
- Auto min-zoom now calls `coord.MinZoomForSingleTile` instead of `maxZoom - 6`.
- Caps the result at `maxZoom` to avoid invalid ranges for point-like regions.

### `internal/tile/zoom.go`
- Updated `AutoZoomRange` signature to accept bounds `(minLon, minLat, maxLon, maxLat float64)`
  and delegate to `MinZoomForSingleTile` for consistency.

### `internal/coord/mercator_test.go`
- Added `TestMinZoomForSingleTile` covering Switzerland, a tiny Zurich bounding box,
  a degenerate point, the whole world, and the western hemisphere.
  Each case verifies both the expected value and the invariant (single tile at returned
  zoom, multiple tiles at zoom+1).

## Example

For a Switzerland dataset (lon 5.9–10.6°, lat 45.8–47.9°):
- **Before**: `minZoom = maxZoom - 6 = 7` (image spans multiple tiles at zoom 7)
- **After**: `minZoom = 6` (entire image fits in tile 33/22 at zoom 6)
