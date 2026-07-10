# Fix floating-point predictor byte order (terrarium noise)

## Problem

Terrarium output from Copernicus DEM GeoTIFFs (Float32, Deflate, TIFF
predictor=3) was pure noise: decoded elevations were ±32768 m garbage.

Two independent bugs:

1. **`undoFloatingPointPredictor` reversed the byte planes.** Per the TIFF
   spec / libtiff (`fpAcc` in `tif_predict.c`), predictor-3 byte planes are
   stored most-significant-byte first regardless of file byte order, and the
   byte differencing runs at a stride of samplesPerPixel. Our implementation
   assumed plane 0 = byte 0 of the stored little-endian layout, so every
   float's bytes came back reversed — exponent bytes landed in the mantissa.
   The unit test could not catch this: it constructed its input by inverting
   the same wrong assumptions.

2. **The integration harness never set `tile.Config.IsTerrarium`** (the CLI
   does), so `TestCopernicusDEM` pushed the float DEM through the ordinary
   RGBA path — float bits misread as colors — and its tile-count/bounds
   assertions passed anyway.

## Fix

- `internal/cog/reader.go`: `undoFloatingPointPredictor` unshuffles MSB-first
  byte planes into the file's byte order (new `bo` parameter) and differences
  at samplesPerPixel stride.
- `integration/helpers_test.go`: `runPipeline` sets `IsTerrarium` from the
  format, matching the CLI.

## Testing

- `TestUndoFloatingPointPredictor` rewritten to encode per the libtiff spec
  (little/big endian, spp=1 and spp=3, realistic elevations).
- `TestCopernicusDEM` now asserts decoded terrarium elevations at two known
  coordinates (truth from gdallocationinfo) within ±75 m, guarding the whole
  float path: deflate + predictor 3 + float resampling + terrarium encoding.
- Manual validation vs GDAL on an 81-point grid: mean abs error 31,706 m
  before → 4.2 m after (z12), max 12.5 m.
