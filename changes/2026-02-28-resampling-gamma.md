# Resampling Gamma Correction

Add `--resampling-gamma` flag to geotiff2pmtiles and pmtransform for gamma-corrected
output encoding after resampling interpolation. Useful for brightening midtones when
converting from dB-space (e.g. SAR backscatter) to RGB for display.

## What changed

- New `--resampling-gamma` CLI flag in both geotiff2pmtiles and pmtransform
  (default 1.0 = disabled, typical values 1.5–2.2)
- `gammaLUTs` struct with precomputed 4096-entry encode table in `resample.go`
- All interpolating resampling methods (bilinear, Lanczos-3, bicubic) accept and
  use the gamma LUTs for output encoding; accumulation stays in source value space
- `Config.ResamplingGamma` and `TransformConfig.ResamplingGamma` fields wired
  through `Generate()` and `Transform()` pipelines
- Terrarium (elevation) and nearest/mode resampling paths are unaffected
- Alpha channel is never gamma-corrected (always linear)

## Files modified

- `internal/tile/resample.go` — gammaLUTs struct, buildGammaLUTs(), encode(), all
  interpolating functions and accumulator variants accept `*gammaLUTs`
- `internal/tile/generator.go` — Config.ResamplingGamma field, LUT construction
- `internal/tile/transform.go` — TransformConfig.ResamplingGamma field
- `cmd/geotiff2pmtiles/main.go` — `--resampling-gamma` flag, config output
- `cmd/pmtransform/main.go` — `--resampling-gamma` flag, config output
- `internal/tile/resample_test.go` — gamma LUT unit tests
