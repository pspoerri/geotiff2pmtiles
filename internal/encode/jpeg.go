package encode

import (
	"bytes"
	"image"
	"image/jpeg"
)

// JPEGEncoder encodes tiles as JPEG.
type JPEGEncoder struct {
	Quality int // 1-100, default 85
}

func (e *JPEGEncoder) Encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	quality := e.Quality
	if quality <= 0 {
		quality = 85
	}
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (e *JPEGEncoder) Format() string       { return "jpeg" }
func (e *JPEGEncoder) PMTileType() uint8     { return TileTypeJPEG }
func (e *JPEGEncoder) FileExtension() string { return ".jpg" }
