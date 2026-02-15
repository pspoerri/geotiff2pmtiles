package tile

import (
	"image"
	"image/color"
	"math"

	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

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
func downsampleTile(topLeft, topRight, bottomLeft, bottomRight *image.RGBA, tileSize int, mode Resampling) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	half := tileSize / 2
	hasData := false

	// Each quadrant of the parent tile is a half-size version of one child.
	quadrants := [4]struct {
		src  *image.RGBA
		dstX int
		dstY int
	}{
		{topLeft, 0, 0},
		{topRight, half, 0},
		{bottomLeft, 0, half},
		{bottomRight, half, half},
	}

	for _, q := range quadrants {
		if q.src == nil {
			continue
		}
		hasData = true
		downsampleQuadrant(dst, q.src, q.dstX, q.dstY, half, tileSize, mode)
	}

	if !hasData {
		return nil
	}
	return dst
}

// downsampleQuadrant scales a tileSize x tileSize source into a half x half
// region of the destination image starting at (dstOffX, dstOffY).
func downsampleQuadrant(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int, mode Resampling) {
	switch mode {
	case ResamplingNearest:
		downsampleQuadrantNearest(dst, src, dstOffX, dstOffY, half, tileSize)
	default:
		downsampleQuadrantBilinear(dst, src, dstOffX, dstOffY, half, tileSize)
	}
}

// downsampleQuadrantTerrarium scales a source quadrant using Terrarium-aware averaging.
// Decodes Terrarium RGB → elevation, averages valid values, re-encodes to Terrarium RGB.
func downsampleQuadrantTerrarium(dst *image.RGBA, src *image.RGBA, dstOffX, dstOffY, half, tileSize int, mode Resampling) {
	if mode == ResamplingNearest {
		downsampleQuadrantTerrariumNearest(dst, src, dstOffX, dstOffY, half, tileSize)
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
func downsampleTileTerrarium(topLeft, topRight, bottomLeft, bottomRight *image.RGBA, tileSize int, mode Resampling) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	half := tileSize / 2
	hasData := false

	quadrants := [4]struct {
		src  *image.RGBA
		dstX int
		dstY int
	}{
		{topLeft, 0, 0},
		{topRight, half, 0},
		{bottomLeft, 0, half},
		{bottomRight, half, half},
	}

	for _, q := range quadrants {
		if q.src == nil {
			continue
		}
		hasData = true
		downsampleQuadrantTerrarium(dst, q.src, q.dstX, q.dstY, half, tileSize, mode)
	}

	if !hasData {
		return nil
	}
	return dst
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
