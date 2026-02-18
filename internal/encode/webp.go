package encode

/*
#cgo pkg-config: libwebp
#include <stdlib.h>
#include <webp/encode.h>
#include <webp/decode.h>
*/
import "C"
import (
	"fmt"
	"image"
	"image/draw"
	"unsafe"
)

// WebPEncoder encodes tiles as WebP using native libwebp via CGo.
// Requires libwebp to be installed (brew install webp / apt-get install libwebp-dev).
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
	rgba := imageToRGBA(img)
	bounds := rgba.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width == 0 || height == 0 {
		return nil, fmt.Errorf("webp: empty image")
	}

	var output *C.uint8_t
	size := C.WebPEncodeRGBA(
		(*C.uint8_t)(unsafe.Pointer(&rgba.Pix[0])),
		C.int(width),
		C.int(height),
		C.int(rgba.Stride),
		C.float(e.Quality),
		&output,
	)
	if size == 0 || output == nil {
		return nil, fmt.Errorf("webp: encode failed")
	}
	defer C.WebPFree(unsafe.Pointer(output))

	return C.GoBytes(unsafe.Pointer(output), C.int(size)), nil
}

func (e *WebPEncoder) Format() string        { return "webp" }
func (e *WebPEncoder) PMTileType() uint8     { return TileTypeWebP }
func (e *WebPEncoder) FileExtension() string { return ".webp" }

// DecodeWebP decodes WebP image bytes using native libwebp.
func DecodeWebP(data []byte) (image.Image, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("webp: empty data")
	}

	var width, height C.int
	ptr := C.WebPDecodeRGBA(
		(*C.uint8_t)(unsafe.Pointer(&data[0])),
		C.size_t(len(data)),
		&width,
		&height,
	)
	if ptr == nil {
		return nil, fmt.Errorf("webp: decode failed")
	}
	defer C.WebPFree(unsafe.Pointer(ptr))

	w := int(width)
	h := int(height)
	totalBytes := w * 4 * h

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	src := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), totalBytes)
	copy(img.Pix, src)
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
