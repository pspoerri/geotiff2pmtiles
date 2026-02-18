package tile

import (
	"image"
	"image/color"
	"math"

	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

// lanczos3Weights2x contains precomputed, normalized 1D Lanczos-3 kernel
// weights for 2× downsampling. Each output pixel's source center is at
// (2·dx + 0.5, 2·dy + 0.5), so the 6 nearest source pixels along each
// axis are at distances -2.5, -1.5, -0.5, 0.5, 1.5, 2.5 from the center.
// The weights are symmetric: w[i] == w[5-i].
var lanczos3Weights2x [6]float64

func init() {
	offsets := [6]float64{-2.5, -1.5, -0.5, 0.5, 1.5, 2.5}
	var sum float64
	for i, d := range offsets {
		lanczos3Weights2x[i] = lanczos3(d)
		sum += lanczos3Weights2x[i]
	}
	for i := range lanczos3Weights2x {
		lanczos3Weights2x[i] /= sum
	}
}

// downsampleTile creates a parent tile by combining up to 4 child tiles.
// The children correspond to the four quadrants:
//
//	topLeft     = (z+1, 2x,   2y)
//	topRight    = (z+1, 2x+1, 2y)
//	bottomLeft  = (z+1, 2x,   2y+1)
//	bottomRight = (z+1, 2x+1, 2y+1)
//
// Any child may be nil (edge tiles / partial coverage). Nil children
// contribute transparent black pixels.
//
// When all four children are uniform with the same color, the result is
// returned as a compact uniform TileData without allocating a full image.
//
// When all non-nil children are single-channel (gray or uniform with R=G=B,
// A=255), the downsample operates directly in gray space — avoiding 4 RGBA
// expansions (saving ~1 MB of temporary allocations per tile).
func downsampleTile(topLeft, topRight, bottomLeft, bottomRight *TileData, tileSize int, mode Resampling) *TileData {
	children := [4]*TileData{topLeft, topRight, bottomLeft, bottomRight}

	// Count non-nil children and check for fast paths.
	nonNilCount := 0
	allUniform := true
	allGrayCompatible := true // all non-nil children are gray or uniform-gray
	for _, c := range children {
		if c == nil {
			continue
		}
		nonNilCount++
		if !c.IsUniform() {
			allUniform = false
		}
		if !c.IsGray() && !c.isUniformGray() {
			allGrayCompatible = false
		}
	}

	if nonNilCount == 0 {
		return nil
	}

	// Fast path: all 4 children present and uniform with the same color.
	if nonNilCount == 4 && allUniform {
		c0 := children[0].Color()
		if children[1].Color() == c0 && children[2].Color() == c0 && children[3].Color() == c0 {
			return newTileDataUniform(c0, tileSize)
		}
	}

	// Gray fast path: all non-nil children are single-channel (gray or
	// uniform with R=G=B, A=255). Downsample in gray space to avoid
	// allocating 4 × 256 KB RGBA expansion images.
	if nonNilCount == 4 && allGrayCompatible {
		return downsampleTileGray(children, tileSize, mode)
	}

	// General RGBA path.
	imgs := [4]*image.RGBA{
		tileDataToRGBA(topLeft),
		tileDataToRGBA(topRight),
		tileDataToRGBA(bottomLeft),
		tileDataToRGBA(bottomRight),
	}

	dst := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	half := tileSize / 2

	quadrants := [4]struct {
		src  *image.RGBA
		dstX int
		dstY int
	}{
		{imgs[0], 0, 0},
		{imgs[1], half, 0},
		{imgs[2], 0, half},
		{imgs[3], half, half},
	}

	for _, q := range quadrants {
		if q.src == nil {
			continue
		}
		downsampleQuadrant(dst, q.src, q.dstX, q.dstY, half, tileSize, mode)
	}

	return newTileData(dst, tileSize)
}

// downsampleTileGray downsamples 4 gray-compatible children directly in
// single-channel space. This avoids allocating 4 × tileSize² × 4 bytes of
// RGBA expansion images — a 5× reduction in temporary memory for
// single-channel datasets like land cover classifications.
func downsampleTileGray(children [4]*TileData, tileSize int, mode Resampling) *TileData {
	// Extract gray images from children (cheap: gray tiles return their
	// internal *image.Gray; uniform tiles allocate a small gray image).
	var grays [4]*image.Gray
	for i, c := range children {
		if c == nil {
			continue
		}
		grays[i] = tileDataToGray(c, tileSize)
	}

	dst := image.NewGray(image.Rect(0, 0, tileSize, tileSize))
	half := tileSize / 2

	type quadrant struct {
		src  *image.Gray
		dstX int
		dstY int
	}

	quads := [4]quadrant{
		{grays[0], 0, 0},
		{grays[1], half, 0},
		{grays[2], 0, half},
		{grays[3], half, half},
	}

	for _, q := range quads {
		if q.src == nil {
			continue
		}
		switch mode {
		case ResamplingNearest:
			downsampleQuadrantGrayNearest(dst, q.src, q.dstX, q.dstY, half, tileSize)
		case ResamplingLanczos:
			downsampleQuadrantGrayLanczos(dst, q.src, q.dstX, q.dstY, half, tileSize)
		default:
			downsampleQuadrantGrayBilinear(dst, q.src, q.dstX, q.dstY, half, tileSize)
		}
	}

	// Detect uniform gray output.
	if c, ok := detectUniformGray(dst); ok {
		return newTileDataUniform(color.RGBA{R: c, G: c, B: c, A: 255}, tileSize)
	}
	return &TileData{gray: dst, tileSize: tileSize}
}

// downsampleQuadrantGrayNearest picks the top-left pixel from each 2×2 block.
func downsampleQuadrantGrayNearest(dst *image.Gray, src *image.Gray, dstOffX, dstOffY, half, tileSize int) {
	srcStride := src.Stride
	dstStride := dst.Stride
	srcPix := src.Pix
	dstPix := dst.Pix
	for dy := 0; dy < half; dy++ {
		sy := dy * 2
		if sy >= tileSize {
			sy = tileSize - 1
		}
		srcRowOff := sy * srcStride
		dstRowOff := (dstOffY + dy) * dstStride
		for dx := 0; dx < half; dx++ {
			sx := dx * 2
			if sx >= tileSize {
				sx = tileSize - 1
			}
			dstPix[dstRowOff+dstOffX+dx] = srcPix[srcRowOff+sx]
		}
	}
}

// downsampleQuadrantGrayBilinear averages a 2×2 block of gray pixels.
func downsampleQuadrantGrayBilinear(dst *image.Gray, src *image.Gray, dstOffX, dstOffY, half, tileSize int) {
	srcStride := src.Stride
	dstStride := dst.Stride
	srcPix := src.Pix
	dstPix := dst.Pix
	for dy := 0; dy < half; dy++ {
		sy := dy * 2
		sy1 := sy + 1
		if sy1 >= tileSize {
			sy1 = tileSize - 1
		}
		srcRow0 := sy * srcStride
		srcRow1 := sy1 * srcStride
		dstRowOff := (dstOffY + dy) * dstStride
		for dx := 0; dx < half; dx++ {
			sx := dx * 2
			sx1 := sx + 1
			if sx1 >= tileSize {
				sx1 = tileSize - 1
			}
			v := (uint16(srcPix[srcRow0+sx]) + uint16(srcPix[srcRow0+sx1]) +
				uint16(srcPix[srcRow1+sx]) + uint16(srcPix[srcRow1+sx1]) + 2) / 4
			dstPix[dstRowOff+dstOffX+dx] = uint8(v)
		}
	}
}

// downsampleQuadrantGrayLanczos uses a Lanczos-3 kernel for gray images.
func downsampleQuadrantGrayLanczos(dst *image.Gray, src *image.Gray, dstOffX, dstOffY, half, tileSize int) {
	w := lanczos3Weights2x
	srcPix := src.Pix
	srcStride := src.Stride
	dstPix := dst.Pix
	dstStride := dst.Stride
	maxIdx := tileSize - 1

	for dy := 0; dy < half; dy++ {
		for dx := 0; dx < half; dx++ {
			var sum float64

			for ky := 0; ky < 6; ky++ {
				sy := 2*dy - 2 + ky
				if sy < 0 {
					sy = 0
				} else if sy > maxIdx {
					sy = maxIdx
				}
				wyVal := w[ky]
				srcRowOff := sy * srcStride

				for kx := 0; kx < 6; kx++ {
					sx := 2*dx - 2 + kx
					if sx < 0 {
						sx = 0
					} else if sx > maxIdx {
						sx = maxIdx
					}
					sum += float64(srcPix[srcRowOff+sx]) * w[kx] * wyVal
				}
			}

			dstPix[(dstOffY+dy)*dstStride+dstOffX+dx] = clampByte(sum)
		}
	}
}

// tileDataToGray extracts an *image.Gray from a TileData. For gray tiles
// this returns the internal image (no allocation). For uniform tiles it
// allocates a filled gray image.
func tileDataToGray(td *TileData, tileSize int) *image.Gray {
	if td == nil {
		return nil
	}
	if td.gray != nil {
		return td.gray
	}
	// Uniform tile: fill a new gray image.
	g := image.NewGray(image.Rect(0, 0, tileSize, tileSize))
	v := td.color.R // R=G=B for gray-compatible uniforms
	pix := g.Pix
	for i := range pix {
		pix[i] = v
	}
	return g
}

// detectUniformGray checks if all pixels in a gray image are the same value.
func detectUniformGray(img *image.Gray) (uint8, bool) {
	pix := img.Pix
	if len(pix) == 0 {
		return 0, false
	}
	v := pix[0]
	for i := 1; i < len(pix); i++ {
		if pix[i] != v {
			return 0, false
		}
	}
	return v, true
}

// downsampleQuadrant scales a tileSize x tileSize source into a half x half
// region of the destination image starting at (dstOffX, dstOffY).
func downsampleQuadrant(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int, mode Resampling) {
	switch mode {
	case ResamplingNearest:
		downsampleQuadrantNearest(dst, src, dstOffX, dstOffY, half, tileSize)
	case ResamplingLanczos:
		downsampleQuadrantLanczos(dst, src, dstOffX, dstOffY, half, tileSize)
	default:
		downsampleQuadrantBilinear(dst, src, dstOffX, dstOffY, half, tileSize)
	}
}

// downsampleQuadrantTerrarium scales a source quadrant using Terrarium-aware averaging.
// Decodes Terrarium RGB → elevation, averages valid values, re-encodes to Terrarium RGB.
func downsampleQuadrantTerrarium(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int, mode Resampling) {
	switch mode {
	case ResamplingNearest:
		downsampleQuadrantTerrariumNearest(dst, src, dstOffX, dstOffY, half, tileSize)
		return
	case ResamplingLanczos:
		downsampleQuadrantTerrariumLanczos(dst, src, dstOffX, dstOffY, half, tileSize)
		return
	}

	for dy := 0; dy < half; dy++ {
		for dx := 0; dx < half; dx++ {
			sx := dx * 2
			sy := dy * 2

			p00 := srcPixel(src, sx, sy, tileSize)
			p10 := srcPixel(src, sx+1, sy, tileSize)
			p01 := srcPixel(src, sx, sy+1, tileSize)
			p11 := srcPixel(src, sx+1, sy+1, tileSize)

			// Decode Terrarium RGB to elevation, average valid values.
			var sum float64
			var count int
			for _, p := range [4]color.RGBA{p00, p10, p01, p11} {
				if p.A == 0 {
					continue // nodata
				}
				elev := encode.TerrariumToElevation(p)
				if !math.IsNaN(elev) {
					sum += elev
					count++
				}
			}

			if count == 0 {
				// All nodata — leave transparent.
				continue
			}

			avg := sum / float64(count)
			dst.SetRGBA(dstOffX+dx, dstOffY+dy, encode.ElevationToTerrarium(avg))
		}
	}
}

// downsampleQuadrantTerrariumNearest picks the top-left valid pixel.
func downsampleQuadrantTerrariumNearest(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int) {
	for dy := 0; dy < half; dy++ {
		for dx := 0; dx < half; dx++ {
			sx := dx * 2
			sy := dy * 2
			p := srcPixel(src, sx, sy, tileSize)
			if p.A > 0 {
				dst.SetRGBA(dstOffX+dx, dstOffY+dy, p)
			}
		}
	}
}

// downsampleQuadrantTerrariumLanczos uses a Lanczos-3 kernel for terrarium data.
// Decodes Terrarium RGB → elevation, applies Lanczos weights to valid values,
// and re-encodes the averaged elevation back to Terrarium RGB.
func downsampleQuadrantTerrariumLanczos(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int) {
	w := lanczos3Weights2x

	for dy := 0; dy < half; dy++ {
		for dx := 0; dx < half; dx++ {
			var elevSum, wSum float64

			for ky := 0; ky < 6; ky++ {
				sy := clamp(2*dy-2+ky, 0, tileSize-1)
				for kx := 0; kx < 6; kx++ {
					sx := clamp(2*dx-2+kx, 0, tileSize-1)
					p := src.RGBAAt(sx, sy)
					if p.A == 0 {
						continue
					}
					elev := encode.TerrariumToElevation(p)
					if math.IsNaN(elev) {
						continue
					}
					wt := w[ky] * w[kx]
					elevSum += elev * wt
					wSum += wt
				}
			}

			if wSum == 0 {
				continue
			}
			dst.SetRGBA(dstOffX+dx, dstOffY+dy, encode.ElevationToTerrarium(elevSum/wSum))
		}
	}
}

// downsampleQuadrantBilinear uses box-filter (average of 2x2 source pixels) to
// produce each output pixel. This is equivalent to bilinear downsampling.
// Pixels with alpha == 0 are treated as nodata and excluded from RGB averaging
// so they don't bleed dark colors into the result.
func downsampleQuadrantBilinear(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int) {
	for dy := 0; dy < half; dy++ {
		for dx := 0; dx < half; dx++ {
			// Map destination pixel to a 2x2 block in the source.
			sx := dx * 2
			sy := dy * 2

			// Read the 2x2 source block, clamping to source bounds.
			p00 := srcPixel(src, sx, sy, tileSize)
			p10 := srcPixel(src, sx+1, sy, tileSize)
			p01 := srcPixel(src, sx, sy+1, tileSize)
			p11 := srcPixel(src, sx+1, sy+1, tileSize)

			pixels := [4]color.RGBA{p00, p10, p01, p11}

			// Alpha: straight average of all 4 (nodata contributes 0).
			aSum := uint16(p00.A) + uint16(p10.A) + uint16(p01.A) + uint16(p11.A)
			a := (aSum + 2) / 4

			// RGB: average only pixels with non-zero alpha.
			var rSum, gSum, bSum uint16
			var count uint16
			for _, p := range pixels {
				if p.A == 0 {
					continue
				}
				rSum += uint16(p.R)
				gSum += uint16(p.G)
				bSum += uint16(p.B)
				count++
			}

			if count == 0 {
				continue // all nodata — leave transparent
			}

			r := (rSum + count/2) / count
			g := (gSum + count/2) / count
			b := (bSum + count/2) / count

			dst.SetRGBA(dstOffX+dx, dstOffY+dy, color.RGBA{
				R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(a),
			})
		}
	}
}

// downsampleQuadrantLanczos uses a Lanczos-3 kernel to downsample a
// tileSize × tileSize source quadrant into a half × half destination region.
// Uses precomputed 1D weights for the fixed 2× downsample factor.
// Pixels with alpha == 0 are excluded from RGB interpolation.
func downsampleQuadrantLanczos(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int) {
	w := lanczos3Weights2x
	srcPix := src.Pix
	srcStride := src.Stride
	dstPix := dst.Pix
	dstStride := dst.Stride
	maxIdx := tileSize - 1

	for dy := 0; dy < half; dy++ {
		for dx := 0; dx < half; dx++ {
			var rSum, gSum, bSum, aSum, wTotal, wRGB float64

			for ky := 0; ky < 6; ky++ {
				sy := 2*dy - 2 + ky
				if sy < 0 {
					sy = 0
				} else if sy > maxIdx {
					sy = maxIdx
				}
				wyVal := w[ky]
				srcRowOff := sy * srcStride

				for kx := 0; kx < 6; kx++ {
					sx := 2*dx - 2 + kx
					if sx < 0 {
						sx = 0
					} else if sx > maxIdx {
						sx = maxIdx
					}

					wt := w[kx] * wyVal
					off := srcRowOff + sx*4
					a := float64(srcPix[off+3])
					aSum += a * wt
					wTotal += wt
					if srcPix[off+3] > 0 {
						rSum += float64(srcPix[off]) * wt
						gSum += float64(srcPix[off+1]) * wt
						bSum += float64(srcPix[off+2]) * wt
						wRGB += wt
					}
				}
			}

			if wRGB == 0 {
				continue
			}

			dstOff := (dstOffY+dy)*dstStride + (dstOffX+dx)*4
			dstPix[dstOff] = clampByte(rSum / wRGB)
			dstPix[dstOff+1] = clampByte(gSum / wRGB)
			dstPix[dstOff+2] = clampByte(bSum / wRGB)
			dstPix[dstOff+3] = clampByte(aSum / wTotal)
		}
	}
}

// downsampleQuadrantNearest picks the top-left pixel from each 2x2 block.
func downsampleQuadrantNearest(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int) {
	for dy := 0; dy < half; dy++ {
		for dx := 0; dx < half; dx++ {
			sx := dx * 2
			sy := dy * 2
			p := srcPixel(src, sx, sy, tileSize)
			dst.SetRGBA(dstOffX+dx, dstOffY+dy, p)
		}
	}
}

// downsampleTileTerrarium creates a parent tile by combining up to 4 child tiles
// using Terrarium-aware averaging (decode RGB→elevation, average, re-encode).
//
// When all four children are uniform with the same color (same elevation),
// the result is returned as a compact uniform TileData.
func downsampleTileTerrarium(topLeft, topRight, bottomLeft, bottomRight *TileData, tileSize int, mode Resampling) *TileData {
	children := [4]*TileData{topLeft, topRight, bottomLeft, bottomRight}

	nonNilCount := 0
	allUniform := true
	for _, c := range children {
		if c == nil {
			continue
		}
		nonNilCount++
		if !c.IsUniform() {
			allUniform = false
		}
	}

	if nonNilCount == 0 {
		return nil
	}

	// Fast path: all 4 children present and uniform with the same color.
	if nonNilCount == 4 && allUniform {
		c0 := children[0].Color()
		if children[1].Color() == c0 && children[2].Color() == c0 && children[3].Color() == c0 {
			return newTileDataUniform(c0, tileSize)
		}
	}

	// Expand children to *image.RGBA for the quadrant functions.
	imgs := [4]*image.RGBA{
		tileDataToRGBA(topLeft),
		tileDataToRGBA(topRight),
		tileDataToRGBA(bottomLeft),
		tileDataToRGBA(bottomRight),
	}

	dst := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	half := tileSize / 2

	quadrants := [4]struct {
		src  *image.RGBA
		dstX int
		dstY int
	}{
		{imgs[0], 0, 0},
		{imgs[1], half, 0},
		{imgs[2], 0, half},
		{imgs[3], half, half},
	}

	for _, q := range quadrants {
		if q.src == nil {
			continue
		}
		downsampleQuadrantTerrarium(dst, q.src, q.dstX, q.dstY, half, tileSize, mode)
	}

	return newTileData(dst, tileSize)
}

// srcPixel reads a pixel from src, clamping coordinates to bounds.
func srcPixel(src *image.RGBA, x, y, tileSize int) color.RGBA {
	if x >= tileSize {
		x = tileSize - 1
	}
	if y >= tileSize {
		y = tileSize - 1
	}
	return src.RGBAAt(x, y)
}
