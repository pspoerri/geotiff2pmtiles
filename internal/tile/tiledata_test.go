package tile

import (
	"image"
	"image/color"
	"testing"
)

// --- applyFillColorTransform ---

func TestApplyFillColorTransform_ReplacesTransparentPixels(t *testing.T) {
	tileSize := 4
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	// Set two pixels to non-transparent values.
	img.SetRGBA(0, 0, color.RGBA{255, 0, 0, 255})
	img.SetRGBA(1, 0, color.RGBA{0, 255, 0, 128}) // partial alpha: should NOT be replaced
	// Remaining pixels are transparent (zero RGBA).

	fill := color.RGBA{100, 100, 100, 255}
	applyFillColorTransform(img, fill)

	// Fully opaque pixel should be unchanged.
	if c := img.RGBAAt(0, 0); c != (color.RGBA{255, 0, 0, 255}) {
		t.Errorf("opaque pixel changed: got %v, want red", c)
	}
	// Partially transparent pixel (alpha != 0) should be unchanged.
	if c := img.RGBAAt(1, 0); c != (color.RGBA{0, 255, 0, 128}) {
		t.Errorf("partial-alpha pixel changed: got %v", c)
	}
	// Fully transparent pixels should be replaced with fill.
	if c := img.RGBAAt(2, 0); c != fill {
		t.Errorf("transparent pixel = %v, want fill %v", c, fill)
	}
	if c := img.RGBAAt(0, 1); c != fill {
		t.Errorf("transparent pixel = %v, want fill %v", c, fill)
	}
}

func TestApplyFillColorTransform_AllTransparent(t *testing.T) {
	tileSize := 8
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize)) // all zero (transparent)
	fill := color.RGBA{50, 60, 70, 255}
	applyFillColorTransform(img, fill)

	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			if c := img.RGBAAt(x, y); c != fill {
				t.Fatalf("pixel (%d,%d) = %v, want fill %v", x, y, c, fill)
			}
		}
	}
}

func TestApplyFillColorTransform_NoneTransparent(t *testing.T) {
	tileSize := 4
	red := color.RGBA{255, 0, 0, 255}
	img := solidImage(tileSize, red) // all opaque
	fill := color.RGBA{50, 60, 70, 255}
	applyFillColorTransform(img, fill)

	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			if c := img.RGBAAt(x, y); c != red {
				t.Fatalf("opaque pixel (%d,%d) changed: got %v, want red", x, y, c)
			}
		}
	}
}

// --- detectGray edge cases ---

func TestDetectGray_AcceptsGrayCheckerboard(t *testing.T) {
	img := grayCheckerImage(8, 100, 200)
	g, ok := detectGray(img)
	if !ok {
		t.Error("detectGray should accept R=G=B, A=255 image")
	}
	if g == nil {
		t.Fatal("detectGray returned nil image for valid gray input")
	}
	if g.Bounds().Dx() != 8 || g.Bounds().Dy() != 8 {
		t.Errorf("gray image bounds = %v, want 8×8", g.Bounds())
	}
}

func TestDetectGray_RejectsAlphaNot255(t *testing.T) {
	tileSize := 4
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			img.SetRGBA(x, y, color.RGBA{128, 128, 128, 200}) // alpha != 255
		}
	}
	_, ok := detectGray(img)
	if ok {
		t.Error("detectGray should reject pixels with alpha != 255")
	}
}

func TestDetectGray_RejectsRGBMismatch(t *testing.T) {
	tileSize := 4
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			img.SetRGBA(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	// One pixel has R != G.
	img.SetRGBA(1, 1, color.RGBA{255, 0, 128, 255})
	_, ok := detectGray(img)
	if ok {
		t.Error("detectGray should reject non-gray pixels (R != G)")
	}
}

func TestDetectGray_RejectsRGBAImage(t *testing.T) {
	img := rgbaCheckerImage(8) // has distinct R and B channels
	_, ok := detectGray(img)
	if ok {
		t.Error("detectGray should reject RGBA image with different channel values")
	}
}

// --- detectUniform edge cases ---

func TestDetectUniform_TwoIdenticalPixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{1, 2, 3, 4})
	img.SetRGBA(1, 0, color.RGBA{1, 2, 3, 4})
	c, ok := detectUniform(img)
	if !ok {
		t.Error("expected uniform for 2 identical pixels")
	}
	if c != (color.RGBA{1, 2, 3, 4}) {
		t.Errorf("color = %v, want {1,2,3,4}", c)
	}
}

func TestDetectUniform_OnePixelDiffers(t *testing.T) {
	img := solidImage(16, color.RGBA{100, 100, 100, 255})
	img.SetRGBA(15, 15, color.RGBA{100, 100, 101, 255}) // 1 channel differs
	_, ok := detectUniform(img)
	if ok {
		t.Error("expected non-uniform when one pixel differs by 1 channel")
	}
}

func TestDetectUniform_AllTransparent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8)) // all zero
	c, ok := detectUniform(img)
	if !ok {
		t.Error("expected uniform for all-transparent (zero) image")
	}
	if c != (color.RGBA{}) {
		t.Errorf("color = %v, want zero", c)
	}
}

// --- TileData.Bounds ---

func TestTileData_Bounds_Uniform(t *testing.T) {
	tileSize := 32
	td := newTileDataUniform(color.RGBA{0, 0, 0, 255}, tileSize)
	b := td.Bounds()
	if b.Dx() != tileSize || b.Dy() != tileSize {
		t.Errorf("uniform Bounds = %v, want %dx%d", b, tileSize, tileSize)
	}
	if b.Min.X != 0 || b.Min.Y != 0 {
		t.Errorf("uniform Bounds.Min = %v, want (0,0)", b.Min)
	}
}

func TestTileData_Bounds_Gray(t *testing.T) {
	tileSize := 16
	img := grayCheckerImage(tileSize, 50, 100)
	td := newTileData(img, tileSize)
	if !td.IsGray() {
		t.Fatal("expected gray tile")
	}
	b := td.Bounds()
	if b.Dx() != tileSize || b.Dy() != tileSize {
		t.Errorf("gray Bounds = %v, want %dx%d", b, tileSize, tileSize)
	}
}

// --- TileData.IsGray and IsUniform classification ---

func TestTileData_IsGray_TrueForGrayInput(t *testing.T) {
	img := grayCheckerImage(8, 100, 200)
	td := newTileData(img, 8)
	if !td.IsGray() {
		t.Error("expected IsGray() = true for gray checker image")
	}
	if td.IsUniform() {
		t.Error("expected IsUniform() = false for non-uniform gray tile")
	}
}

func TestTileData_IsGray_FalseForRGBA(t *testing.T) {
	img := checkerImage(8, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255})
	td := newTileData(img, 8)
	if td.IsGray() {
		t.Error("expected IsGray() = false for RGBA checker image")
	}
	if td.IsUniform() {
		t.Error("expected IsUniform() = false for RGBA checker")
	}
}

func TestTileData_isUniformGray(t *testing.T) {
	// Uniform gray: R=G=B, A=255 → isUniformGray = true.
	td := newTileDataUniform(color.RGBA{100, 100, 100, 255}, 8)
	if !td.isUniformGray() {
		t.Error("expected isUniformGray() = true for uniform gray tile")
	}

	// Uniform color (not gray): isUniformGray = false.
	tdColor := newTileDataUniform(color.RGBA{100, 0, 0, 255}, 8)
	if tdColor.isUniformGray() {
		t.Error("expected isUniformGray() = false for non-gray uniform tile")
	}

	// Uniform with alpha != 255: isUniformGray = false.
	tdTransparent := newTileDataUniform(color.RGBA{100, 100, 100, 0}, 8)
	if tdTransparent.isUniformGray() {
		t.Error("expected isUniformGray() = false for transparent uniform tile")
	}

	// Non-uniform tile: isUniformGray = false.
	img := grayCheckerImage(8, 50, 150)
	tdGray := newTileData(img, 8)
	if tdGray.isUniformGray() {
		t.Error("expected isUniformGray() = false for non-uniform gray tile")
	}
}
