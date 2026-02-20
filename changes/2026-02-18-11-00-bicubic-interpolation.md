# Bicubic Interpolation Implementation

## Summary

Added Catmull-Rom bicubic interpolation (a = -0.5) as a new `--resampling bicubic` option. This provides a 4×4 pixel neighborhood — a middle ground between bilinear (2×2, smooth but blurry) and Lanczos-3 (6×6, sharp but can ring).

## Kernel

The Catmull-Rom kernel with a = -0.5:
- W(x) = 1.5|x|³ - 2.5|x|² + 1 for |x| ≤ 1
- W(x) = -0.5|x|³ + 2.5|x|² - 4|x| + 2 for 1 < |x| ≤ 2
- W(x) = 0 for |x| > 2

Unlike bilinear (always positive weights), the bicubic kernel has negative lobes at |x| > 1, which provides subtle sharpening. Unlike Lanczos-3 which uses sin/LUT evaluation, bicubic is a simple polynomial — no LUT needed.

## Changes

### generator.go
- Added `ResamplingBicubic` constant
- Updated `ParseResampling` to accept "bicubic"

### resample.go
- `bicubic()`: Catmull-Rom kernel function (pure polynomial, no trig/LUT)
- `bicubicSampleCached()`: Main RGBA sampling with single-tile fast path and multi-tile fallback (at most 2×2 tiles for 4×4 neighborhood)
- `bicubicAccumYCbCr()`: Fast path for JPEG COG tiles (inline YCbCr→RGB)
- `bicubicAccumNYCbCrA()`: Fast path for JPEG+alpha tiles
- `bicubicAccumRGBA()`: Fast path for PNG tiles
- `bicubicAccumGeneric()`: Fallback via image.Image interface
- `bicubicSampleFloat()`: Float/elevation data variant with NaN exclusion
- Wired into `sampleFromTileSources` and `sampleFromTileSourcesFloat` switches

### downsample.go
- `bicubicWeights2x`: Precomputed normalized 1D weights for 2× downsampling (offsets -1.5, -0.5, 0.5, 1.5 → weights -0.0625, 0.5625, 0.5625, -0.0625)
- `downsampleQuadrantBicubic()`: RGBA quadrant downsampling with alpha-aware RGB
- `downsampleQuadrantGrayBicubic()`: Gray-channel quadrant downsampling
- `downsampleQuadrantTerrariumBicubic()`: Terrarium elevation-aware downsampling
- Wired into all downsample switch statements (RGBA, gray, terrarium)

### CLI / docs
- Updated `--resampling` flag help text
- Updated README.md feature list and flags table
- Updated ARCHITECTURE.md pipeline description

## Design Decisions

- **No LUT needed**: The bicubic kernel is a simple polynomial (4 multiplies + adds), unlike Lanczos which requires sin() calls. Direct computation is faster than a table lookup with its potential cache miss.
- **Same optimization pattern as Lanczos**: Batched tile fetches (4×4 neighborhood spans at most 2×2 source tiles), typed fast paths for YCbCr/RGBA, and alpha-aware RGB interpolation.
- **Negative weights**: The kernel's negative lobes at |x| > 1 provide natural sharpening. Values are clamped to [0, 255] via the existing `clampByte` function.

## Characteristics

| Method | Kernel | Quality | Speed | Ringing |
|--------|--------|---------|-------|---------|
| Nearest | 1×1 | Low | Fastest | None |
| Bilinear | 2×2 | Smooth | Fast | None |
| **Bicubic** | **4×4** | **Sharp** | **Medium** | **Minimal** |
| Lanczos-3 | 6×6 | Sharpest | Slower | Some |
