package encode

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
)

// DecodeImage decodes image bytes in the specified format back to an image.Image.
// Supported formats: "png", "terrarium" (PNG-encoded), "jpeg"/"jpg", "webp".
func DecodeImage(data []byte, format string) (image.Image, error) {
	switch format {
	case "png", "terrarium":
		return png.Decode(bytes.NewReader(data))
	case "jpeg", "jpg":
		return jpeg.Decode(bytes.NewReader(data))
	case "webp":
		return DecodeWebP(data)
	default:
		return nil, fmt.Errorf("unsupported decode format: %q", format)
	}
}
