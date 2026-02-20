# Mode (most common value) resampling

Added `--resampling mode` for categorical/classified rasters (e.g. land cover
classifications) where interpolated values are meaningless.

## Changes

- **generator.go**: Added `ResamplingMode` constant and `"mode"` to `ParseResampling`
- **resample.go**: Mode uses nearest-neighbor for COG-level sampling (max zoom),
  since each output pixel maps to ~1 source pixel
- **downsample.go**: Added `downsampleQuadrantMode` (RGBA) and
  `downsampleQuadrantGrayMode` (gray) that pick the most frequent pixel value
  in each 2×2 block during pyramid downsampling; helper functions `modeRGBA`
  and `modeGray` with stack-only allocation and top-left tie-breaking
- **downsample.go**: Wired mode into all dispatch switches (RGBA, gray, Terrarium)
- **main.go**: Updated `--resampling` flag help to include `mode`
- **downsample_test.go**: Added tests for mode downsampling (solid, distinct
  colors, majority picking) and unit tests for `modeGray` / `modeRGBA`
- **README.md**: Updated features, flags table, and added usage example
- **ARCHITECTURE.md**: Updated resampling description
- **DESIGN.md**: Added design decision explaining mode resampling rationale
