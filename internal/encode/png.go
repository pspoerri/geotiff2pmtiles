package encode

import (
	"bytes"
	"image"
	"image/png"
)

// PNGEncoder encodes tiles as PNG.
type PNGEncoder struct{}

func (e *PNGEncoder) Encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := &png.Encoder{CompressionLevel: png.BestSpeed}
	err := enc.Encode(&buf, img)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (e *PNGEncoder) Format() string       { return "png" }
func (e *PNGEncoder) PMTileType() uint8     { return TileTypePNG }
func (e *PNGEncoder) FileExtension() string { return ".png" }
