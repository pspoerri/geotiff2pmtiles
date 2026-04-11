# Add --replace-corrupt flag to pmtransform

**Date:** 2026-04-10

## Problem

Following the previous fix that made pmtransform skip corrupt tiles instead of
aborting, some users want corrupt tiles to be **replaced** with empty tiles
rather than dropped entirely. This is important when downstream consumers expect
every tile position in the source archive to be present in the output.

## Changes

- **New `--replace-corrupt` flag**: When set, undecodable tiles are replaced with
  a transparent (empty) tile of the configured tile size. The fill-color transform
  is still applied to the substituted tile if `--fill-color` is set.
- **Renamed `Stats.SkippedTiles` → `Stats.CorruptTiles`**: This name is more
  accurate now that corrupt tiles can be either skipped or replaced. The counter
  always reflects how many tiles failed to decode, regardless of action taken.
- **Updated CLI summary**: Distinguishes between "skipped" and "replaced" cases
  in the warning message and shows the active mode in the startup banner.
- **New tests**: `TestTransformRebuild_CorruptTile_Replaced` and
  `TestTransformReencode_CorruptTile_Replaced` verify the substitution behavior.

## Files Modified

- `internal/tile/generator.go` — Renamed `SkippedTiles` → `CorruptTiles`
- `internal/tile/transform.go` — Added `ReplaceCorrupt` config field; substitute
  transparent RGBA on decode failure when enabled
- `internal/tile/transform_test.go` — Updated existing tests for rename, added
  two new tests for the replace behavior
- `cmd/pmtransform/main.go` — New `--replace-corrupt` flag, updated startup
  banner and summary output
- `README.md`, `DESIGN.md`, `ARCHITECTURE.md` — Documented new flag and behavior
