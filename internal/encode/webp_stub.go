//go:build !cgo

package encode

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"

	"github.com/HugoSmits86/nativewebp"
	"golang.org/x/image/webp"
)

const webpCGOAvailable = false

// WebPEncoder encodes tiles as lossless WebP (VP8L) in pure Go. There is no
// pure-Go lossy VP8 encoder, so Quality is ignored in CGO_ENABLED=0 builds;
// build with libwebp for lossy output.
type WebPEncoder struct {
	Quality int
}

func newWebPEncoder(quality int) (Encoder, error) {
	return &WebPEncoder{Quality: quality}, nil
}

func (e *WebPEncoder) Encode(img image.Image) ([]byte, error) {
	if img.Bounds().Empty() {
		return nil, fmt.Errorf("webp: empty image")
	}
	var buf bytes.Buffer
	if err := nativewebp.Encode(&buf, img, nil); err != nil {
		return nil, fmt.Errorf("webp: %w", err)
	}
	return buf.Bytes(), nil
}

func (e *WebPEncoder) Format() string        { return "webp" }
func (e *WebPEncoder) PMTileType() uint8     { return TileTypeWebP }
func (e *WebPEncoder) FileExtension() string { return ".webp" }

// DecodeWebP decodes lossy or lossless WebP bytes in pure Go.
func DecodeWebP(data []byte) (image.Image, error) {
	img, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("webp: %w", err)
	}
	return img, nil
}

func imageToRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
	return rgba
}
