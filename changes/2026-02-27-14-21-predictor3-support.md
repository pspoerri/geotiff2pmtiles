# Add TIFF Predictor=3 (floating-point) support

**Date:** 2026-02-27

## Problem

The Copernicus DEM GLO-30 COG uses Deflate compression with Predictor=3
(floating-point horizontal differencing). The code only handled Predictor=2,
so float32 tile data was read without reversing the byte-shuffle and
byte-level differencing, producing garbled elevation values that appeared
as banding artifacts in the output.

Additionally, `bytesPerSample()` only returned correct values for 8-bit
and 16-bit data, returning 1 for 32-bit float data instead of 4.

## Changes

- **internal/cog/ifd.go**: Fixed `bytesPerSample()` to compute from
  `BitsPerSample[0] / 8` instead of hardcoded 8/16-bit checks. Now
  correctly returns 4 for float32 and 8 for float64 data.

- **internal/cog/reader.go**: Added `undoFloatingPointPredictor()` that
  reverses Predictor=3 encoding: (1) undo byte-level horizontal
  differencing, (2) byte-unshuffle to restore sample byte order.
  Added `applyPredictor()` helper to dispatch between Predictor=2 and
  Predictor=3. Updated all predictor handling sites (readTileRaw,
  readStripTileRaw, ReadTile) to use the unified helper.

- **internal/cog/reader.go**: Extended `undoHorizontalDifferencing()` with
  a 32-bit path that accumulates uint32 deltas (matching existing 16-bit
  path), for correct Predictor=2 on 32-bit data.

- **internal/cog/reader_test.go**: Added `TestUndoHorizontalDifferencing32Bit`
  and `TestUndoFloatingPointPredictor` tests.

## Impact

Fixes Copernicus DEM and any other float GeoTIFF/COG using Predictor=3
(the GDAL default for float + Deflate/LZW compression).
