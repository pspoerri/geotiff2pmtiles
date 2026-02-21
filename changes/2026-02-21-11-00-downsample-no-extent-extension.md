# Downsample: treat out-of-bounds kernel samples as empty (alpha 0)

## Problem

When Lanczos-3 and bicubic resampling kernels extend beyond the tile boundary
during pyramid downsampling, out-of-bounds positions were clamped to the edge
pixel. This caused edge pixels to be repeated into the kernel, visually
extending the source data extent by smearing colors at the boundary.

## Solution

Changed all downsample quadrant functions with extended kernels to treat
out-of-bounds kernel positions as empty instead of clamping:

- **RGBA Lanczos/bicubic**: Out-of-bounds positions contribute alpha=0 to the
  kernel. The weight is still counted in `wTotal` (for alpha normalization) but
  contributes 0 to `aSum` and nothing to RGB. This produces a natural alpha
  fade at data edges rather than a hard opaque boundary from repeated pixels.

- **Gray Lanczos/bicubic**: Out-of-bounds positions are skipped entirely and
  the remaining weights are renormalized. The output pixel is computed from
  only the valid kernel positions.

- **Terrarium Lanczos/bicubic**: Out-of-bounds positions are skipped (same as
  the existing alpha=0 / nodata handling).

Bilinear, nearest, and mode resampling are not affected because their source
coordinates never extend beyond the tile boundary.

## Files changed

- `internal/tile/downsample.go`: Updated `downsampleQuadrantLanczos`,
  `downsampleQuadrantBicubic`, `downsampleQuadrantGrayLanczos`,
  `downsampleQuadrantGrayBicubic`, `downsampleQuadrantTerrariumLanczos`,
  `downsampleQuadrantTerrariumBicubic`.
