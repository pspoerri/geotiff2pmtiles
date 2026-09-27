package encode

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// fillImage returns a size×size image whose Pix all hold c (straight alpha,
// the pipeline convention for *image.RGBA).
func fillImage(size int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// assertNear fails when got differs from want by more than tol in any
// channel (RGB is ignored for a fully transparent want).
func assertNear(t *testing.T, what string, got, want color.RGBA, tol int) {
	t.Helper()
	diff := func(a, b uint8) int { return int(math.Abs(float64(a) - float64(b))) }
	if diff(got.A, want.A) > tol ||
		want.A != 0 && (diff(got.R, want.R) > tol || diff(got.G, want.G) > tol || diff(got.B, want.B) > tol) {
		t.Errorf("%s pixel = %v, want %v ±%d", what, got, want, tol)
	}
}

// assertStraightPixel checks that DecodeImage returned an *image.RGBA whose
// Pix at (x, y) is want within tol.
func assertStraightPixel(t *testing.T, img image.Image, x, y int, want color.RGBA, tol int) {
	t.Helper()
	rgba, ok := img.(*image.RGBA)
	if !ok {
		t.Fatalf("decoded %T, want *image.RGBA holding straight alpha", img)
	}
	assertNear(t, "decoded", rgba.RGBAAt(x, y), want, tol)
}

// TestEncodeDecode_StraightAlpha round-trips straight-alpha pixels through
// every alpha-capable encoder and DecodeImage. The PNG and pure-Go WebP
// encoders used to un-premultiply them (200,100,50,128 was stored as
// 143,199,99,128) and decoding premultiplied them again.
func TestEncodeDecode_StraightAlpha(t *testing.T) {
	webpTol := 1
	if WebPLossy {
		webpTol = 8 // libwebp encodes the colour lossily; its alpha is lossless
	}
	colors := []color.RGBA{
		{200, 100, 50, 128},
		{30, 60, 20, 64},
		{250, 250, 250, 40},
		{10, 20, 30, 255},
		{0, 0, 0, 0},
	}
	for _, format := range []string{"png", "webp"} {
		for _, c := range colors {
			t.Run(fmt.Sprintf("%s/%v", format, c), func(t *testing.T) {
				enc, err := NewEncoder(format, 90)
				if err != nil {
					t.Fatalf("NewEncoder: %v", err)
				}
				data, err := enc.Encode(fillImage(16, c))
				if err != nil {
					t.Fatalf("Encode: %v", err)
				}
				tol := 0
				decodeStd := png.Decode
				if format == "webp" {
					tol = webpTol
					decodeStd = xwebp.Decode
				}
				// A lossless file must hold the straight colour for any decoder.
				// (x/image/webp decodes lossy colour with a different YCbCr
				// matrix than libwebp, so lossy files are checked below only.)
				if format == "png" || !WebPLossy {
					std, err := decodeStd(bytes.NewReader(data))
					if err != nil {
						t.Fatalf("decode: %v", err)
					}
					assertNear(t, "stored", color.RGBA(color.NRGBAModel.Convert(std.At(8, 8)).(color.NRGBA)), c, tol)
				}

				img, err := DecodeImage(data, format)
				if err != nil {
					t.Fatalf("DecodeImage: %v", err)
				}
				assertStraightPixel(t, img, 8, 8, c, tol)
			})
		}
	}
}

// TestDecodeImage_StraightAlphaFromPNGTypes checks that every PNG layout the
// standard decoder returns with an alpha channel comes back as straight
// *image.RGBA, not as a type a caller would premultiply by drawing it.
func TestDecodeImage_StraightAlphaFromPNGTypes(t *testing.T) {
	want := color.RGBA{200, 100, 50, 128}
	nrgba := color.NRGBA(want)
	pal := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{nrgba})
	n64 := image.NewNRGBA64(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			n64.Set(x, y, nrgba)
		}
	}
	for _, tc := range []struct {
		name string
		img  image.Image
	}{
		{"nrgba", &image.NRGBA{Pix: fillImage(4, want).Pix, Stride: 16, Rect: image.Rect(0, 0, 4, 4)}},
		{"paletted", pal},
		{"nrgba64", n64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := png.Encode(&buf, tc.img); err != nil {
				t.Fatalf("png.Encode: %v", err)
			}
			img, err := DecodeImage(buf.Bytes(), "png")
			if err != nil {
				t.Fatalf("DecodeImage: %v", err)
			}
			assertStraightPixel(t, img, 1, 1, want, 1)
		})
	}
}

// TestJPEGEncoder_StraightRGB checks that JPEG, which has no alpha, keeps the
// straight RGB of a semi-transparent pixel whatever the image type, as the
// *image.RGBA fast path does. A generic image (such as a uniform tile) used to
// be premultiplied onto black instead.
func TestJPEGEncoder_StraightRGB(t *testing.T) {
	want := color.RGBA{200, 100, 50, 128}
	src := fillImage(16, want)
	for _, tc := range []struct {
		name string
		img  image.Image
	}{
		{"rgba", src},
		{"nrgba", &image.NRGBA{Pix: src.Pix, Stride: src.Stride, Rect: src.Rect}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := (&JPEGEncoder{Quality: 95}).Encode(tc.img)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			img, err := DecodeImage(data, "jpeg")
			if err != nil {
				t.Fatalf("DecodeImage: %v", err)
			}
			// JPEG decodes opaque, so drawing it into RGBA is exact.
			rgba := image.NewRGBA(img.Bounds())
			draw.Draw(rgba, rgba.Rect, img, img.Bounds().Min, draw.Src)
			opaque := want
			opaque.A = 255
			assertStraightPixel(t, rgba, 8, 8, opaque, 4)
		})
	}
}

// TestTerrarium_RoundTripUnchanged checks that Terrarium elevations and the
// alpha-0 nodata marker survive encode and decode exactly.
func TestTerrarium_RoundTripUnchanged(t *testing.T) {
	elevations := []float64{-10994, -0.5, 0, 1234.25, 8848.5, math.NaN()}
	img := image.NewRGBA(image.Rect(0, 0, len(elevations), 1))
	for x, e := range elevations {
		img.SetRGBA(x, 0, ElevationToTerrarium(e))
	}
	data, err := (&TerrariumEncoder{}).Encode(img)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := DecodeImage(data, "terrarium")
	if err != nil {
		t.Fatalf("DecodeImage: %v", err)
	}
	for x, want := range elevations {
		got := TerrariumToElevation(color.RGBAModel.Convert(dec.At(x, 0)).(color.RGBA))
		if math.IsNaN(want) != math.IsNaN(got) || !math.IsNaN(want) && math.Abs(got-want) > 1.0/256 {
			t.Errorf("elevation %d = %v, want %v", x, got, want)
		}
	}
}
