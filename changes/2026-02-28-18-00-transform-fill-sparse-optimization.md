# Optimize transform rebuild with fill-color for sparse tiles

## Problem

`pmtransform --rebuild --fill-color` was extremely slow for sparse datasets.
With fill-color, ALL tile positions in bounds were enumerated at every zoom
level and processed through the full pipeline (DiskTileStore lookup, fill
substitution, downsample, encode, write, store) even when 99%+ produced
identical fill tiles.

For a dataset with 1K source tiles covering 1M positions at zoom 14, this
meant ~999K redundant encode calls, ~999K DiskTileStore entries (128 bytes
overhead each), and the cascade multiplied through every lower zoom level.

## Solution

Track which positions contain real (non-fill) data and propagate upward:

1. **Pre-encode fill tile once** before the zoom loop. Reuse the same encoded
   bytes for all fill positions, skipping repeated encoder calls.

2. **Partition tiles at each zoom level**: At max zoom, source tiles are "real"
   and everything else gets pre-encoded fill bytes written directly. At lower
   zooms, a parent is "real" iff at least one of its 4 children was real.
   All-fill parents get pre-encoded bytes without entering the downsample
   pipeline.

3. **Shared fillTileShared**: A single immutable uniform TileData replaces
   per-position allocations. Used for nil-child substitution in the downsample
   path and for the rare case where a source tile returns no data.

Fill tiles never enter DiskTileStore, eliminating map overhead for empty
positions. Only real tiles go through the worker pool.

## Files changed

- `internal/tile/transform.go` — `transformRebuild` function
