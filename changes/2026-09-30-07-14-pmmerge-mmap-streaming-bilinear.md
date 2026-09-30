# pmmerge: memory-mapped reads, streaming merge, bilinear upsampling

- `internal/mmap` holds the mmap helpers that were private to `cog`; `cog` and `pmtiles`
  both use them.
- `pmtiles.Reader` maps the archive. `ReadTile` returns a read-only view of the mapping,
  valid until `Close` (falls back to `ReadAt` where mapping fails). pmtransform gets this
  too. The integration helper `readAllTiles` now clones the bytes it keeps past `Close`.
- `pmtiles.Reader.TileRanges(z)`: a level's tiles as runs of Hilbert indices.
- `tile.Merge` no longer builds a map of every position per level and sorts it: it sweeps
  the inputs' runs (scaled by `<<2d` above an input's max zoom), yielding positions in
  Hilbert order with a bit set of covering inputs. Memory follows the directories; at most
  64 inputs (`tile.MaxMergeInputs`).
- `tile.SourceTileSize` walks runs instead of listing every max-zoom tile.
- `--upsampling nearest|bilinear` (default nearest). Bilinear is alpha-weighted, and
  terrarium is interpolated as elevation; the warning names the method.
- Tests: `TestReader_TileRanges`, `TestSweepRanges`, `TestUpsampleBilinear`,
  `TestUpsampleBilinearTerrarium`. Smoke-run: west half of Natural Earth (z5) merged with
  a z3 copy — 512/512 western z5 tiles copied as they are, eastern ones scaled up, both
  modes pass checkpmtiles.
