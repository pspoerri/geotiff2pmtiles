package encode

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// TerrariumEncoder encodes tiles as Terrarium-format PNG.
// The input image should already have Terrarium-encoded RGB values.
type TerrariumEncoder struct{}

func (e *TerrariumEncoder) Encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := &png.Encoder{CompressionLevel: png.BestSpeed}
	err := enc.Encode(&buf, img)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (e *TerrariumEncoder) Format() string       { return "terrarium" }
func (e *TerrariumEncoder) PMTileType() uint8     { return TileTypePNG }
func (e *TerrariumEncoder) FileExtension() string { return ".png" }

// ElevationToTerrarium converts a float64 elevation value to Terrarium RGB.
// Terrarium formula: elevation = (R * 256 + G + B / 256) - 32768
// Range: approximately -32768 to +32767.996 meters.
func ElevationToTerrarium(elevation float64) color.RGBA {
	if math.IsNaN(elevation) || math.IsInf(elevation, 0) {
		return color.RGBA{0, 0, 0, 0} // nodata → transparent
	}

	value := elevation + 32768.0

	// Clamp to valid range.
	if value < 0 {
		value = 0
	}
	if value > 65535.996 {
		value = 65535.996
	}

	// R = floor(value / 256)
	rVal := int(value / 256)
	if rVal > 255 {
		rVal = 255
	}
	if rVal < 0 {
		rVal = 0
	}

	// G = floor(value - R * 256)
	remainder := value - float64(rVal)*256.0
	gVal := int(remainder)
	if gVal > 255 {
		gVal = 255
	}
	if gVal < 0 {
		gVal = 0
	}

	// B = floor((value - R * 256 - G) * 256)
	bVal := int((remainder - float64(gVal)) * 256.0)
	if bVal > 255 {
		bVal = 255
	}
	if bVal < 0 {
		bVal = 0
	}

	return color.RGBA{R: uint8(rVal), G: uint8(gVal), B: uint8(bVal), A: 255}
}

// TerrariumToElevation converts Terrarium RGB values back to elevation.
// Returns NaN if the pixel is transparent (nodata).
func TerrariumToElevation(c color.RGBA) float64 {
	if c.A == 0 {
		return math.NaN()
	}
	return float64(c.R)*256.0 + float64(c.G) + float64(c.B)/256.0 - 32768.0
}
