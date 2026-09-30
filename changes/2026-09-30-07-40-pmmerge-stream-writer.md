# pmmerge: streaming writer, no temp copy

- `pmtiles.StreamWriter`: tiles in increasing tile-ID order go straight into
  `<output>.partial` (tile data at offset 16384, metadata and leaf directories after it,
  root directory and header written in place at Finalize). Peak disk use is the archive
  itself instead of twice it. Runs are merged as tiles arrive; small tiles are
  deduplicated against the bytes already written. Out-of-order writes are an error.
- `tile.Merge` goes from low to high zoom and writes each level through an ordered
  collector (`mergeLevel`): workers take numbered batches, results are written in
  sequence, a token per in-flight batch bounds memory.
- pmmerge uses the StreamWriter; `--tmp-dir` is gone. Inputs without tiles are skipped.
  Name and description carry over when all inputs agree; each input's non-derived
  metadata goes to `sources` (with file, minzoom, maxzoom).
- `pmtiles.DerivedMetadataKeys` (moved from pmtransform) lists the writer-derived keys.
- checkpmtiles checks the section layout as the spec states it (after the header, no
  overlaps, last section ends the file) instead of the Writer's contiguous order.
- Tests: `TestStreamWriter`, `TestStreamWriterAbortRemovesPartial`,
  `TestMergeWritesInTileIDOrder` (8 workers into a StreamWriter), checkpmtiles cases for
  a StreamWriter archive and overlapping sections. Natural Earth sanity run: tiles
  byte-identical to the two-pass output.
