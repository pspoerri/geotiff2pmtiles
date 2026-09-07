# Round float nodata sentinel through float32

## Problem

Follow-up to PR #44 (nodata sentinel excluded from float resampling kernels). The
GDAL_NODATA string is parsed as float64, but float raster pixels are float32 widened
to float64 at sample time. Integer sentinels like `-32767` compare equal either way,
but a sentinel float32 cannot represent exactly (`-3.4028235e+38`, `-99999.99`) parses
to a float64 that never equals the widened pixel, so such rasters still leaked the
sentinel into bilinear/bicubic/Lanczos sums and into the caller's own nodata filter.

## Change

- `parseFloatNodata` in `internal/tile/resample.go` replaces the inline parse in
  `renderTileTerrarium` and rounds the parsed value through float32.
- Regression tests: the three float kernels exclude a `-32767` sentinel at a nodata
  edge and stay unchanged when nodata is unset (NaN never compares equal); the parser
  matches the float32-rounded value for integer and non-integer sentinels.
