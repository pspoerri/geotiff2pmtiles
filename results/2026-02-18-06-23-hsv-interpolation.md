# HSV Color Space Interpolation with sRGB Gamma Correction

**Date:** 2026-02-18  
**Status:** Explored, not merged (branch was rebased with Lanczos-3)

## Goal

Replace linear RGB interpolation with HSV color space interpolation to preserve hue across color boundaries and avoid desaturation artifacts (e.g., red + green → brown/gray in RGB, vs hue-correct blending through the color wheel).

## Approach

### 1. HSV interpolation (both bilinear and downsample paths)

- Convert source pixels from sRGB to HSV before blending
- **S and V**: standard weighted linear interpolation
- **H (hue)**: circular weighted mean via sin/cos/atan2, with hue weights scaled by saturation so achromatic pixels (s≈0, undefined hue) don't corrupt the result
- **Alpha**: unchanged, standard bilinear/box-filter interpolation
- Applied to `bilinearSampleCached` (tile rendering) and `downsampleQuadrantBilinear` (pyramid downsampling)

### 2. sRGB gamma correction

Initial HSV implementation operated in gamma-encoded sRGB space, causing coarse tiles to appear too dark. V = max(R,G,B)/255 in sRGB is gamma-encoded; linearly averaging gamma values systematically underestimates brightness.

Fix: linearize sRGB inputs before computing HSV, re-encode on output.

- **`srgbLUT[256]`**: precomputed lookup table mapping sRGB uint8 → linear float64 (official sRGB transfer function with linear segment at 0.04045 and 2.4 exponent)
- **`linearToSRGB(v float64) uint8`**: reverse conversion with math.Pow for the gamma curve
- **`rgbToHSV`**: reads linear values via LUT, so V represents physical luminance
- **`hsvToRGB`**: applies sRGB gamma on output via `linearToSRGB`

Callers (`bilinearSampleCached`, `downsampleQuadrantBilinear`) were unchanged — gamma correction was fully encapsulated in the conversion functions.

## Findings

### Correctness
- All 17 existing tests passed with both changes
- Full project build clean, no linter errors

### Performance concerns
- HSV interpolation adds 8× sin/cos + 1× atan2 per pixel for hue averaging
- sRGB linearization adds 3× math.Pow per output pixel (input side uses LUT)
- These costs compound with the existing interpolation overhead

### Known limitations of HSV averaging
- HSV is not perceptually uniform; V = max(R,G,B) doesn't represent true luminance
- Mixing complementary hues (e.g., red + cyan) can produce unexpected results since the circular mean picks a single hue direction rather than desaturating
- For typical aerial/satellite imagery where adjacent pixels are similar, results are good; for diverse color mixtures, HSV averaging can produce overly saturated results

### Gamma correction applies independently of color space
The sRGB gamma issue affects **any** interpolation in sRGB space (RGB, HSV, or otherwise). The fix (linearize before blending, re-encode after) could be applied to the current Lanczos-3 pipeline independently of whether HSV is used. All current resampling functions (`bilinearSampleCached`, `lanczosSampleCached`, `lanczosAccumYCbCr`, `downsampleQuadrantBilinear`, `downsampleQuadrantLanczos`) operate on gamma-encoded uint8 values, producing slightly-too-dark averages at each pyramid level.

## Implementation (for reference)

### sRGB LUT and reverse conversion

```go
var srgbLUT [256]float64

func init() {
    for i := range srgbLUT {
        c := float64(i) / 255.0
        if c <= 0.04045 {
            srgbLUT[i] = c / 12.92
        } else {
            srgbLUT[i] = math.Pow((c+0.055)/1.055, 2.4)
        }
    }
}

func linearToSRGB(v float64) uint8 {
    if v <= 0 { return 0 }
    if v >= 1 { return 255 }
    var s float64
    if v <= 0.0031308 {
        s = v * 12.92
    } else {
        s = 1.055*math.Pow(v, 1.0/2.4) - 0.055
    }
    return uint8(s*255 + 0.5)
}
```

### HSV conversion (linear-light)

```go
func rgbToHSV(r, g, b uint8) (h, s, v float64) {
    rf := srgbLUT[r]
    gf := srgbLUT[g]
    bf := srgbLUT[b]
    // standard HSV from linear RGB ...
}

func hsvToRGB(h, s, v float64) (uint8, uint8, uint8) {
    // standard HSV→RGB producing linear values ...
    return linearToSRGB(rf), linearToSRGB(gf), linearToSRGB(bf)
}
```

### Circular hue mean

```go
hw00 := nw00 * s00  // weight hue by saturation
sinH := hw00*math.Sin(h00*deg2rad) + ...
cosH := hw00*math.Cos(h00*deg2rad) + ...
hVal = math.Atan2(sinH, cosH) * rad2deg
```
