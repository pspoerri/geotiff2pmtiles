# Add plausibility tests for all integration demos

## Changes

- Added `plausibilityExpectation` struct and `assertPlausiblePMTiles()` helper to
  `integration/helpers_test.go` for comprehensive PMTiles output validation.
- Replaced ad-hoc checks in all 8 satellite pipeline tests with the shared helper.
- Fixed `demo-esaworldcover-gamma0` Makefile target: added missing `--rescale-range 0,65535`
  flag required for 16-bit data.

## Plausibility checks

The helper validates 8 properties of every PMTiles output:
1. Zoom levels match requested min/max
2. Tile type matches expected format (PNG/JPEG)
3. Geographic bounds are valid and within tolerance of expected area
4. Center point falls within bounds, center zoom within range
5. Tile count meets minimum, per-zoom non-zero, counts non-decreasing across zoom
6. First tile at max zoom decodes as a valid image
7. Clustered flag is true
8. Metadata contains required keys (name, format, bounds, minzoom, maxzoom)
