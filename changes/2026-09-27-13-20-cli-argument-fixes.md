# Command-line argument fixes

Found while building a global satellite composite; all reproduced before fixing.

## geotiff2pmtiles

- `--format` defaults to `auto` (terrarium for elevation, webp with nodata, else jpeg).
  An explicit `--format jpeg` was silently replaced by terrarium, because the old check
  compared against the default value. `jpg` is accepted as `jpeg`.
- Validation before any file is written: `--quality` 1-100, `--tile-size` a power of two
  from 64 to 4096 (0/negative panicked, odd sizes left seams), `--concurrency` >= 1
  (0 wrote an empty archive with exit 0), `--max-zoom` <= 30 and min <= max (a swapped
  range wrote a 0-tile archive), `--resampling-gamma` > 0, `--rescale-range` min < max.
- Float, Float16 and non-16-bit signed input is rejected unless the format is terrarium
  (it was decoded from the low byte of each sample into noise). Checked for every source.
- Inputs with different EPSG codes are rejected: all sources were projected with the first
  file's CRS, e.g. Sentinel-2 tiles from several UTM zones were silently misplaced.
- `--bands auto` (default): `1,2,3`, or gray from band 1 for 2-band input (`1,2,3` was
  invalid there). Bands and `--alpha-band` beyond a file's band count are an error.
  `--alpha-band none` is accepted. An explicit `--alpha-band` now survives the
  multispectral preset.
- 9-16 bit input (including bit-packed 15-bit Sentinel-2 L2A) is rescaled like 16-bit;
  the check was `BitsPerSample() == 16`, so 15-bit output was near-black.
- The transparent default `--fill-color` is not applied to jpeg output (it turned every
  uncovered tile position black). An explicit fill with alpha < 255 warns.
- `--nodata`, `--nodata-tolerance`, `--nodata-flood` warn and are ignored for terrarium
  (they were silent no-ops; the flood mask was built for nothing).
- `--resampling-gamma` warns for nearest/mode and terrarium, where it has no effect.
- Builds without libwebp print `webp (lossless; --quality ignored in this build)` and
  warn when nodata switches the output to lossless WebP.
- Help texts rewritten to match behaviour (min-zoom rule, mem-limit semantics, sentinels).

## pmtransform

- `--fill-color` defaults to none. The old `0,0,0,0` default made passthrough unreachable:
  every run decoded and re-encoded every tile (820 ms vs 150 ms on the Natural Earth
  example, with generation loss) and filled missing positions.
- `--fill-color` in re-encode mode now also replaces transparent pixels (it only did so
  with `--rebuild`).
- `--max-zoom` above the source max is rejected (it wrote fill-only levels, black for
  JPEG). `--tile-size` must equal the source tile size (resizing was never implemented;
  it panicked or mixed tile sizes). `--quality`, `--concurrency` and the zoom range are
  validated.
- `--resampling-gamma` removed: nothing in the transform pipeline applied it, but it was
  printed and written into the metadata.
- `--resampling` without `--rebuild` or added levels warns that it has no effect.

## Both tools

- `--tmp-dir`: all temporary files go into one per-run directory (default: next to the
  output), removed on exit, on errors and on SIGINT/SIGTERM. Interrupted runs used to
  leave multi-GB `pmtiles-*.tmp` files behind.

## Utilities

- `pmheader`: zoom overrides must be 0-30 and min <= max (values wrapped around uint8).
- `checkpmtiles`, `coginfo`, `debug`: `-h` prints usage instead of being opened as a
  file; `debug` without arguments no longer panics. Usage errors exit 2.
