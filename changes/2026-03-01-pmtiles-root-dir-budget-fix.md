# Fix PMTiles root directory exceeding 16 KiB budget

## Problem

PMTiles archives with many tiles (60M+) produced root directories that exceeded the 16 KiB
initial fetch budget. The pmtiles.io viewer fetches the first 16,384 bytes to bootstrap;
when the root directory didn't fit, the gzip stream was truncated, causing
"TypeError: Extra bytes past the end" errors.

The `buildDirectory` function used a fixed leaf size of 4,096 entries. For very large
datasets, this created ~15,000 leaf pointers whose compressed root directory was ~18 KiB —
exceeding the 16,257-byte budget (16,384 - 127 header bytes).

## Fix

- **Iterative leaf-size growth**: After splitting into leaf directories, the root directory
  size is checked against the 16 KiB budget. If exceeded, the leaf size increases by 20%
  and the split is retried until the root fits. This matches the reference go-pmtiles
  implementation.

- **NumTileEntries header fix**: The header's `NumTileEntries` field now reflects the
  post-optimization entry count (after run-length merging) instead of the raw count.

- **Integration test validation**: `validatePMTiles` now checks the 16 KiB budget and
  section contiguity for all integration test outputs.

- **checkpmtiles tool**: New `cmd/checkpmtiles` validates PMTiles archives (local files
  or HTTP URLs) for structural correctness: header consistency, 16 KiB budget, directory
  integrity, and trailing bytes.

## Files changed

- `internal/pmtiles/directory.go` — iterative leaf-size growth in `buildDirectory`, extracted `buildLeaves`
- `internal/pmtiles/writer.go` — use post-optimization count for `NumTileEntries`
- `internal/pmtiles/directory_test.go` — added `TestBuildDirectory_LargeSet_RootFitsIn16KiB`
- `internal/pmtiles/bench_test.go` — updated for new `buildDirectory` signature
- `integration/helpers_test.go` — added 16 KiB budget + contiguity checks to `validatePMTiles`
- `cmd/checkpmtiles/main.go` — new PMTiles validation tool
- `Makefile` — added `build-check` target
- `DESIGN.md`, `ARCHITECTURE.md`, `README.md` — updated documentation
