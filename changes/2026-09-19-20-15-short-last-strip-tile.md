# Short last strip tile in the float (terrarium) path

Found by profiling `--resampling mode --format terrarium` on GEBCO 2026
(8 files, 21600x21600, int16, uncompressed, RowsPerStrip=1).

- `decodeRawFloat32Tile` rejected the last virtual tile of a strip TIFF when the
  image height is not a multiple of the 256-row virtual tile height ("float tile
  data too short"). The sampler turned the error into nodata and never cached it,
  so the bottom 96 rows of every file were transparent, and each pixel there
  re-read ~8 MB of strips (102 TB allocated over the run). Strip sources now
  decode the rows present; tiled sources keep the strict check.
- `readStripsRaw` preallocates its buffer from the strip byte counts instead of
  growing it once per strip.
- GEBCO z0-8: 15m38s -> 1m17s wall, 12760s -> 845s user CPU.
- Test: `TestDecodeFloatShortLastStripTile`.

- Samplers now log the first source tile read error instead of silently treating
  it as nodata, so this class of bug is visible.

Remaining hotspot: `FloatTileCache.Get` takes a shard mutex per sampled pixel
(~40% of CPU in the new profile).
