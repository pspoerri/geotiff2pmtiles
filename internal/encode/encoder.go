package encode

import (
	"fmt"
	"image"
)

// TileType constants matching PMTiles v3 spec.
const (
	TileTypeUnknown = 0
	TileTypeMVT     = 1
	TileTypePNG     = 2
	TileTypeJPEG    = 3
	TileTypeWebP    = 4
	TileTypeAVIF    = 5
)

// Encoder encodes an image into tile bytes.
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
