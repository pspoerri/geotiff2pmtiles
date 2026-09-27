//go:build !cgo

package encode

import (
	"bytes"
	"fmt"
	"image"

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
	if err := nativewebp.Encode(&buf, asNRGBA(img), nil); err != nil {
		return nil, fmt.Errorf("webp: %w", err)
	}
	return buf.Bytes(), nil
}

func (e *WebPEncoder) Format() string        { return "webp" }
func (e *WebPEncoder) PMTileType() uint8     { return TileTypeWebP }
func (e *WebPEncoder) FileExtension() string { return ".webp" }

// DecodeWebP decodes lossy or lossless WebP bytes in pure Go.
//
// Lossy WebP is BT.601 limited-range Y'CbCr, but x/image/webp returns it as
// an *image.YCbCr (*image.NYCbCrA with alpha), whose colours Go computes
// with the full-range JFIF matrix: 10,20,30 read as 25,33,42 and 250 as 231.
// Such images are converted to straight RGBA here, as libwebp does.
func DecodeWebP(data []byte) (image.Image, error) {
	img, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("webp: %w", err)
	}
	return limitedRangeRGBA(img), nil
}

// limitedRangeRGBA converts a lossy WebP decode to straight *image.RGBA with
// libwebp's fixed-point BT.601 arithmetic (src/dsp/yuv.h). Each pixel takes
// its chroma sample as is, where libwebp interpolates between samples
// ("fancy upsampling"), so colours can differ slightly at chroma edges.
// Other images are returned unchanged.
func limitedRangeRGBA(img image.Image) image.Image {
	var m *image.YCbCr
	var alpha *image.NYCbCrA
	switch t := img.(type) {
	case *image.YCbCr:
		m = t
	case *image.NYCbCrA:
		m, alpha = &t.YCbCr, t
	default:
		return img
	}
	mulHi := func(v, coeff int) int { return v * coeff >> 8 }
	clip := func(v int) uint8 {
		if v&^(256<<6-1) == 0 {
			return uint8(v >> 6)
		} else if v < 0 {
			return 0
		}
		return 255
	}
	b := m.Rect
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			yy := mulHi(int(m.Y[m.YOffset(x, y)]), 19077)
			ci := m.COffset(x, y)
			u, v := int(m.Cb[ci]), int(m.Cr[ci])
			p := out.Pix[out.PixOffset(x-b.Min.X, y-b.Min.Y):][:4]
			p[0] = clip(yy + mulHi(v, 26149) - 14234)
			p[1] = clip(yy - mulHi(u, 6419) - mulHi(v, 13320) + 8708)
			p[2] = clip(yy + mulHi(u, 33050) - 17685)
			p[3] = 255
			if alpha != nil {
				p[3] = alpha.A[alpha.AOffset(x, y)]
			}
		}
	}
	return out
}
