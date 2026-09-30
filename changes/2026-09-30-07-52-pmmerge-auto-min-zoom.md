# pmmerge: auto min zoom

- `--min-zoom -1` (default) is now auto, as in geotiff2pmtiles and pmtransform: down to
  the highest zoom at which the merged extent fits in one tile. Levels below the highest
  input min zoom are downsampled from the merged level there; `--resampling` (default
  bicubic) picks the method. The max zoom stays the highest input max zoom.
- `tile.Merge` takes a `MinZoom` below the inputs': it merges the base level into memory,
  downsamples the added levels with `transformRebuild` and writes them before the base
  level, so the StreamWriter still gets increasing tile IDs. `MergeConfig.Resampling`.
- An input's own tiles below the base are replaced by the downsampled merge.
- Tests: `TestMergeAddsLevelsBelow`; `TestMergeMixedResolutions` now expects z1 to be
  downsampled. Sentinel-2 example re-merged: zoom 0-14, 54 added tiles, checkpmtiles passes.
