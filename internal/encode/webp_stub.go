//go:build !cgo

package encode

import (
	"fmt"
	"image"
)

const webpCGOAvailable = false

func newWebPEncoder(quality int) (Encoder, error) {
	return nil, fmt.Errorf("webp: native libwebp encoder requires CGO (install libwebp-dev and build with CGO_ENABLED=1)")
}

// DecodeWebP is unavailable without CGO.
func DecodeWebP(data []byte) (image.Image, error) {
	return nil, fmt.Errorf("webp: native libwebp decoder requires CGO (install libwebp-dev and build with CGO_ENABLED=1)")
}

func imageToRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	return rgba
}
