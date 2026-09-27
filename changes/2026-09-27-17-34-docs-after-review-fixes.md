# Documentation after the review fixes

The docs brought in line with the review fix clusters, the library follow-ups and the CLI
parameter changes. Docs and doc comments only; no behaviour changes.

- `README.md`: every flag table regenerated from `-h` (geotiff2pmtiles, pmtransform, and
  new tables for coginfo, checkpmtiles and pmheader), checked by script: each flag has a
  row with the same default, deprecated aliases marked. `--nodata-color`/`--fill-missing`
  with a section on the split, `--source-epsg`, terrarium `--nodata`, `--cpu-profile`.
  Supported Input gains JPEG and sparse strips, palette, WhiteIsZero, sub-byte gray,
  padded 9-15 bit, internal masks, PixelIsPoint, mixed CRSs and 0..360/antimeridian data.
  Limitations drop "one CRS per run", fallback units and palettes, and add what still
  comes from the first file, mask limits, unsupported longitude layouts and pyramid
  aliasing. Disk space: `.partial`, no clustering file, the stale-dir warning, cgroup-aware
  auto limit, writer index memory. Utilities: `coginfo -raw` replaces `debug`. Examples
  use the sample data where it exists; all were run.
- `ARCHITECTURE.md`: file list (`internal/cli`, `cmd/geotiff2pmtiles/sources.go`,
  `integration/transform_mode_test.go`; `cmd/debug` gone); a Conventions section (straight
  alpha, alpha weighting, generation loss, pixel centres, bounds, ±Inf out of domain);
  per-source projection; level, mask and byte-range rules; `SelectTransformMode`,
  `parentTiles`, metadata passthrough; writer/reader memory, `.partial`; errors and
  cancellation.
- `DESIGN.md`: the clusters' design notes folded in under the existing sections, stale
  statements fixed (strip rows, NaN fill, `samples16`, the caches, the flood's step 1,
  the rescale range, the CRS fallback, `mapOverhead`, temp files, added levels, fill),
  and a contents list.
- `BUILDING.md`: the default build needs libwebp; `CGO=0` does not; coginfo is built with
  `go build`. `DEVELOPMENT.md`: how `make check` differs from CI and the commands to run
  CI's checks locally.
- Package doc comments for `cog`, `coord`, `encode`, `pmtiles`, `tile`, and command doc
  comments for geotiff2pmtiles and pmtransform; `ValueRange`'s comment covers 9..16 bits.

Not changed: `CLAUDE.md` still lists `debug` among the utilities and lacks `internal/cli`.
