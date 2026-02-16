package encode

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/gen2brain/webp"
)

// DecodeImage decodes image bytes in the specified format back to an image.Image.
// Supported formats: "png", "terrarium" (PNG-encoded), "jpeg"/"jpg", "webp".
func DecodeImage(data []byte, format string) (image.Image, error) {
	r := bytes.NewReader(data)
	switch format {
	case "png", "terrarium":
		return png.Decode(r)
	case "jpeg", "jpg":
		return jpeg.Decode(r)
	case "webp":
		return decodeWebP(r)
	default:
		return nil, fmt.Errorf("unsupported decode format: %q", format)
	}
}

// decodeWebP decodes a WebP image. Separated for clarity and to allow
// fallback strategies if the WebP codec API changes.
func decodeWebP(r io.Reader) (image.Image, error) {
	return webp.Decode(r)
}
