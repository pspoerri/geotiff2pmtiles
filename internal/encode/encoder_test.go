package encode

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// testImage creates a 256x256 RGBA image with a gradient pattern.
func testImage(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	return img
}

func TestNewEncoder(t *testing.T) {
	tests := []struct {
		format   string
		wantFmt  string
		wantType uint8
		wantExt  string
		wantErr  bool
	}{
		{"jpeg", "jpeg", TileTypeJPEG, ".jpg", false},
		{"jpg", "jpeg", TileTypeJPEG, ".jpg", false},
		{"png", "png", TileTypePNG, ".png", false},
		{"webp", "webp", TileTypeWebP, ".webp", false},
		{"bmp", "", 0, "", true},
		{"", "", 0, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			enc, err := NewEncoder(tt.format, 85)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if enc.Format() != tt.wantFmt {
				t.Errorf("Format() = %q, want %q", enc.Format(), tt.wantFmt)
			}
			if enc.PMTileType() != tt.wantType {
				t.Errorf("PMTileType() = %d, want %d", enc.PMTileType(), tt.wantType)
			}
			if enc.FileExtension() != tt.wantExt {
				t.Errorf("FileExtension() = %q, want %q", enc.FileExtension(), tt.wantExt)
			}
		})
	}
}

func TestPNGEncoder_RoundTrip(t *testing.T) {
	enc := &PNGEncoder{}
	img := testImage(256)

	data, err := enc.Encode(img)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Encode produced empty data")
	}

	// Verify it's valid PNG.
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}

	// PNG is lossless — pixels should be identical.
	bounds := decoded.Bounds()
	if bounds.Dx() != 256 || bounds.Dy() != 256 {
		t.Errorf("decoded size = %dx%d, want 256x256", bounds.Dx(), bounds.Dy())
	}

	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			or, og, ob, oa := img.At(x, y).RGBA()
			dr, dg, db, da := decoded.At(x, y).RGBA()
			if or != dr || og != dg || ob != db || oa != da {
				t.Fatalf("pixel mismatch at (%d,%d): orig=(%d,%d,%d,%d) decoded=(%d,%d,%d,%d)",
					x, y, or>>8, og>>8, ob>>8, oa>>8, dr>>8, dg>>8, db>>8, da>>8)
			}
		}
	}
}

func TestJPEGEncoder_Encode(t *testing.T) {
	enc := &JPEGEncoder{Quality: 85}
	img := testImage(256)

	data, err := enc.Encode(img)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Encode produced empty data")
	}

	// Verify it's valid JPEG.
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("jpeg.Decode: %v", err)
	}

	bounds := decoded.Bounds()
	if bounds.Dx() != 256 || bounds.Dy() != 256 {
		t.Errorf("decoded size = %dx%d, want 256x256", bounds.Dx(), bounds.Dy())
	}

	// JPEG is lossy — check that pixels are close but not necessarily identical.
	maxDiff := 0
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			or, _, _, _ := img.At(x, y).RGBA()
			dr, _, _, _ := decoded.At(x, y).RGBA()
			diff := int(or>>8) - int(dr>>8)
			if diff < 0 {
				diff = -diff
			}
			if diff > maxDiff {
				maxDiff = diff
			}
		}
	}
	// At quality 85, max diff should be small (JPEG compression artifacts).
	if maxDiff > 30 {
		t.Errorf("JPEG max pixel diff = %d, want <= 30 for quality 85", maxDiff)
	}
}

func TestJPEGEncoder_Format(t *testing.T) {
	enc := &JPEGEncoder{Quality: 90}
	if enc.Format() != "jpeg" {
		t.Errorf("Format() = %q, want \"jpeg\"", enc.Format())
	}
	if enc.PMTileType() != TileTypeJPEG {
		t.Errorf("PMTileType() = %d, want %d", enc.PMTileType(), TileTypeJPEG)
	}
	if enc.FileExtension() != ".jpg" {
		t.Errorf("FileExtension() = %q, want \".jpg\"", enc.FileExtension())
	}
}

func TestPNGEncoder_Format(t *testing.T) {
	enc := &PNGEncoder{}
	if enc.Format() != "png" {
		t.Errorf("Format() = %q, want \"png\"", enc.Format())
	}
	if enc.PMTileType() != TileTypePNG {
		t.Errorf("PMTileType() = %d, want %d", enc.PMTileType(), TileTypePNG)
	}
	if enc.FileExtension() != ".png" {
		t.Errorf("FileExtension() = %q, want \".png\"", enc.FileExtension())
	}
}

func TestPNGEncoder_TransparentImage(t *testing.T) {
	// Ensure PNG preserves transparency.
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if x < 32 {
				img.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{0, 0, 0, 0}) // transparent
			}
		}
	}

	enc := &PNGEncoder{}
	data, err := enc.Encode(img)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}

	// Check opaque pixel.
	r, g, b, a := decoded.At(10, 10).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 || a>>8 != 255 {
		t.Errorf("opaque pixel = (%d,%d,%d,%d), want (255,0,0,255)", r>>8, g>>8, b>>8, a>>8)
	}

	// Check transparent pixel.
	_, _, _, a = decoded.At(50, 10).RGBA()
	if a>>8 != 0 {
		t.Errorf("transparent pixel alpha = %d, want 0", a>>8)
	}
}
