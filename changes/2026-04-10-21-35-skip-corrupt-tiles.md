# Skip corrupt tiles in pmtransform instead of aborting

**Date:** 2026-04-10

## Problem

When running `pmtransform --rebuild` on large PMTiles archives (e.g., planet-scale
files with 13M+ tiles), a single corrupt or undecodable tile would cause a fatal
error and abort the entire process.

## Changes

- **Corrupt tile skipping**: `transformRebuild` and `transformReencode` now log a
  warning and skip tiles that fail to decode, instead of aborting the process.
- **SkippedTiles stat**: Added `SkippedTiles` counter to `Stats` struct, reported
  in both verbose and summary output.
- **Better WebP decode diagnostics**: `DecodeWebP` now validates the WebP header
  via `WebPGetInfo` before attempting decode, and includes data size and dimensions
  in error messages for easier debugging.
- **Tests**: Added `TestTransformRebuild_CorruptTile_Skipped` and
  `TestTransformReencode_CorruptTile_Skipped` to verify graceful handling.

## Files Modified

- `internal/tile/generator.go` — Added `SkippedTiles` to Stats
- `internal/tile/transform.go` — Skip corrupt tiles with warning in rebuild/reencode
- `internal/tile/transform_test.go` — New tests for corrupt tile handling
- `internal/encode/webp.go` — Better decode error diagnostics via WebPGetInfo
- `cmd/pmtransform/main.go` — Print skipped tile count in output
