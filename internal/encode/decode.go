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
//
// An image with an alpha channel comes back as an *image.RGBA holding straight
// alpha, the pipeline convention (see Encoder), so callers must use its Pix
// directly and not draw it into another image. Opaque images keep the type
// their decoder returns.
func DecodeImage(data []byte, format string) (image.Image, error) {
	var img image.Image
	var err error
	switch format {
	case "png", "terrarium":
		img, err = png.Decode(bytes.NewReader(data))
	case "jpeg", "jpg":
		return jpeg.Decode(bytes.NewReader(data))
	case "webp":
		img, err = DecodeWebP(data)
	default:
		return nil, fmt.Errorf("unsupported decode format: %q", format)
	}
	if err != nil {
		return nil, err
	}
	return straightRGBA(img), nil
}

// straightRGBA re-types a decoded image with an alpha channel as an
// *image.RGBA holding straight alpha. *image.RGBA is kept as is: the PNG
// decoder returns it only for opaque images, and libwebp's is straight.
func straightRGBA(img image.Image) image.Image {
	switch img.(type) {
	case *image.RGBA, *image.RGBA64, *image.Gray, *image.Gray16, *image.YCbCr:
		return img // opaque, or already straight
	}
	n := asNRGBA(img) // no copy for *image.NRGBA
	return &image.RGBA{Pix: n.Pix, Stride: n.Stride, Rect: n.Rect}
}
