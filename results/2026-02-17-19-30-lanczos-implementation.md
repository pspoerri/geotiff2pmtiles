# Lanczos-3 Resampling Implementation

**Date:** 2026-02-17  
**Goal:** Replace bilinear with Lanczos-3 as the default interpolation method for both per-pixel reprojection and pyramid downsampling.

## Summary

Implemented Lanczos-3 (sinc-windowed sinc) resampling across all tile generation paths:
per-pixel reprojection at max zoom, pyramid downsampling for lower zoom levels, and
terrarium (elevation) encoding. Lanczos is now the default (`--resampling lanczos`);
bilinear and nearest-neighbor remain available.

## Lanczos-3 Kernel

The kernel `L₃(x) = sinc(x) · sinc(x/3)` uses a support radius of 3, giving a 6×6 pixel
neighborhood (36 samples per output pixel) vs bilinear's 2×2 (4 samples).

Simplified form used in implementation:

```
L₃(x) = 3 · sin(πx) · sin(πx/3) / (π²x²)    for |x| < 3
       = 0                                       for |x| ≥ 3
       = 1                                       for x = 0
```

## Changes

### Per-pixel resampling (`resample.go`)

| Function | Description |
|----------|-------------|
| `lanczos3(x)` | Kernel evaluation using simplified sinc product |
| `lanczosSampleCached()` | RGBA interpolation with 6×6 neighborhood; precomputes 1D weights and clamped coordinates; alpha-aware (transparent pixels excluded from RGB, included in alpha) |
| `lanczosSampleFloat()` | Float/elevation interpolation; excludes NaN from weighted sum; falls back to nearest when all neighbors are NaN |

Both dispatch switches (`sampleFromTileSources`, `sampleFromTileSourcesFloat`) updated
with explicit `ResamplingLanczos` cases.

### Pyramid downsampling (`downsample.go`)

For the fixed 2× downsample factor, each output pixel maps to source center
`(2·dx + 0.5, 2·dy + 0.5)`, producing constant distances of ±0.5, ±1.5, ±2.5 along
each axis. The 1D weights are precomputed once in `init()` and reused for every pixel:

```
lanczos3Weights2x = normalized([L₃(-2.5), L₃(-1.5), L₃(-0.5), L₃(0.5), L₃(1.5), L₃(2.5)])
```

| Function | Path |
|----------|------|
| `downsampleQuadrantLanczos()` | RGBA with alpha-aware RGB interpolation |
| `downsampleQuadrantGrayLanczos()` | Single-channel gray (no alpha handling needed) |
| `downsampleQuadrantTerrariumLanczos()` | Decode RGB→elevation, apply Lanczos, re-encode |

### CLI (`main.go`, `generator.go`)

- Default changed from `bilinear` to `lanczos`
- `ParseResampling` accepts `"lanczos"`, `"bilinear"`, `"nearest"`
- `ResamplingLanczos` added to the `Resampling` enum

## Design Decisions

- **Lanczos-3 (a=3)** chosen over Lanczos-2 for maximum quality. The 6×6 kernel
  is 9× more samples than bilinear but provides noticeably sharper output with
  better detail preservation.
- **No kernel scaling for downsample**: The unscaled Lanczos-3 kernel is used for
  the 2× pyramid downsample. A properly scaled kernel (support = a×s = 6 source pixels,
  12×12 = 144 samples) would provide better anti-aliasing but at 4× the cost. Since
  the source is already properly sampled raster data, the unscaled kernel produces
  excellent results.
- **Precomputed weights for downsample**: The 2× factor means the fractional offset
  is always 0.5, so the 1D weights are constant across all output pixels. Computed
  once at init, no `math.Sin` in the hot loop.
- **Alpha-aware interpolation**: Same approach as bilinear — transparent pixels
  (alpha=0) are zeroed out of RGB weights and renormalized. Lanczos negative lobes
  can cause slight ringing at sharp data/nodata boundaries; `clampByte` handles
  out-of-range values.
- **Boundary handling**: Clamp-to-edge for pixels near image boundaries, consistent
  with existing bilinear behavior.

## Files Modified

| File | Lines added |
|------|-------------|
| `internal/tile/resample.go` | +140 (kernel, RGBA sampler, float sampler, switch updates) |
| `internal/tile/downsample.go` | +163 (precomputed weights, RGBA/gray/terrarium downsamplers, switch updates) |
| `internal/tile/generator.go` | +9 (enum constant, parser update) |
| `cmd/geotiff2pmtiles/main.go` | +1 (default flag change) |
| **Total** | **+312 lines** |
