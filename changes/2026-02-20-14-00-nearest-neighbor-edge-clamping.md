# Fix nearest-neighbor out-of-bounds at source tile edges

Fixed a floating-point rounding bug in `nearestSampleCached` and
`nearestSampleFloat` that caused ~1 pixel wide banding artifacts at
the right and bottom edges of every source GeoTIFF file.

## Root cause

The nearest-neighbor sampling computed pixel coordinates via
`px = int(math.Floor(fx + 0.5))` (round-to-nearest). The caller's
bounds check allowed `pixX` in `[0, imgW)`, but when
`pixX >= imgW - 0.5`, the rounding pushed `px` to `imgW` — one past
the last valid pixel. This read from the zero-padded overhang area
of the last internal COG tile, producing wrong (typically 0/nodata)
values.

The affected CRS strip is half a source pixel wide at each right and
bottom edge. For ESA WorldCover tiles (pixel size ≈ 1/12000°), this
is ~4.6 m — enough to corrupt 1 output pixel at higher zoom levels.

All other resampling methods (bilinear, bicubic, Lanczos) already
received `imgW`/`imgH` parameters and clamped internally. The nearest
functions were the only ones missing this.

## Changes

- **resample.go**: Added `imgW, imgH int` parameters to
  `nearestSampleCached` and `nearestSampleFloat`; pixel coordinates
  are now clamped to `[0, imgW-1]` / `[0, imgH-1]` after rounding
- **resample.go**: Updated both call sites in `sampleFromTileSources`
  and `sampleFromTileSourcesFloat` to pass `src.imgW` / `src.imgH`
