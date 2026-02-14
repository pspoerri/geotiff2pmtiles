package encode

import (
	"bytes"
	"image"

	"github.com/gen2brain/webp"
)

// WebPEncoder encodes tiles as WebP using a pure-Go (WASM-based) encoder.
// No CGo or system libraries required; automatically uses a system libwebp
// via purego if available for better performance, otherwise falls back to WASM.
type WebPEncoder struct {
	Quality int
}

func newWebPEncoder(quality int) (Encoder, error) {
	if quality <= 0 {
		quality = 85
	}
	return &WebPEncoder{Quality: quality}, nil
}

func (e *WebPEncoder) Encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	opts := webp.Options{
		Lossless: false,
		Quality:  e.Quality,
	}
	if err := webp.Encode(&buf, img, opts); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (e *WebPEncoder) Format() string       { return "webp" }
func (e *WebPEncoder) PMTileType() uint8     { return TileTypeWebP }
func (e *WebPEncoder) FileExtension() string { return ".webp" }
