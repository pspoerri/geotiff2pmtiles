# Add --fill-color to geotiff2pmtiles

Added `--fill-color` flag to `geotiff2pmtiles`, matching the existing support in
`pmtransform`. When set, transparent/nodata pixels in rendered tiles are
substituted with the specified color, empty tile positions within bounds are
filled with solid-color tiles, and nil-child quadrants during pyramid
downsampling are replaced with fill tiles.

## Changes

- `internal/tile/generator.go`: Added `FillColor *color.RGBA` to `Config`;
  applied `applyFillColorTransform` to rendered max-zoom tiles, created uniform
  fill tiles for empty positions, and substituted nil children during downsampling
- `cmd/geotiff2pmtiles/main.go`: Added `--fill-color` flag with R,G,B,A and
  hex color parsing; wired into `tile.Config`, settings summary, and description
  metadata
- Updated README.md, ARCHITECTURE.md, DESIGN.md
