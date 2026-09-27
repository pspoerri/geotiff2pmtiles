// Package encode encodes tiles as JPEG, PNG, WebP or Terrarium and decodes
// them back for pyramid building and pmtransform. Pipeline *image.RGBA
// buffers hold straight (non-premultiplied) alpha, which the encoders keep,
// and DecodeImage returns images with alpha as straight *image.RGBA. Lossy
// WebP needs libwebp through cgo; builds without cgo encode lossless WebP
// only.
package encode

import (
	"fmt"
	"image"
	"image/draw"
)

// TileType constants matching PMTiles v3 spec.
const (
	TileTypeUnknown = 0
	TileTypeMVT     = 1
	TileTypePNG     = 2
	TileTypeJPEG    = 3
	TileTypeWebP    = 4
)

// Encoder encodes an image into tile bytes.
//
// Pipeline tiles are *image.RGBA buffers holding straight (non-premultiplied)
// alpha, not the premultiplied values the image package defines for that
// type. Encoders read an *image.RGBA's Pix as straight and any other image
// through its At colours with the usual Go semantics.
type Encoder interface {
	// Encode encodes an image to bytes in the tile format.
	Encode(img image.Image) ([]byte, error)

	// Format returns the format name (e.g. "jpeg", "png", "webp").
	Format() string

	// PMTileType returns the PMTiles tile type constant.
	PMTileType() uint8

	// FileExtension returns the appropriate file extension.
	FileExtension() string
}

// NewEncoder creates an encoder for the given format and quality.
func NewEncoder(format string, quality int) (Encoder, error) {
	switch format {
	case "jpeg", "jpg":
		return &JPEGEncoder{Quality: quality}, nil
	case "png":
		return &PNGEncoder{}, nil
	case "webp":
		return newWebPEncoder(quality)
	case "terrarium":
		return &TerrariumEncoder{}, nil
	default:
		return nil, fmt.Errorf("unsupported tile format: %q (supported: %s)", format, Formats())
	}
}

// Formats lists the tile formats this build supports. Lossy WebP needs CGo and
// libwebp; CGO_ENABLED=0 builds fall back to a pure-Go lossless WebP encoder.
func Formats() string {
	if webpCGOAvailable {
		return "jpeg, png, webp, terrarium"
	}
	return "jpeg, png, webp (lossless only), terrarium"
}

// WebPLossy reports whether this build encodes lossy WebP (and so honours
// --quality for WebP).
const WebPLossy = webpCGOAvailable

// asNRGBA returns img as a straight-alpha *image.NRGBA. A pipeline
// *image.RGBA is re-typed over the same Pix without copying, so the standard
// library does not un-premultiply it a second time; other images are converted.
func asNRGBA(img image.Image) *image.NRGBA {
	switch m := img.(type) {
	case *image.NRGBA:
		return m
	case *image.RGBA:
		return &image.NRGBA{Pix: m.Pix, Stride: m.Stride, Rect: m.Rect}
	}
	b := img.Bounds()
	n := image.NewNRGBA(b)
	draw.Draw(n, b, img, b.Min, draw.Src)
	return n
}
