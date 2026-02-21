package tile

import (
	"image"
	"image/color"
	"testing"
)

// solidImage creates a tileSize x tileSize RGBA image filled with a single color.
func solidImage(tileSize int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// solidTile creates a uniform TileData filled with a single color.
func solidTile(tileSize int, c color.RGBA) *TileData {
	return newTileDataUniform(c, tileSize)
}

// fullTile wraps an image in TileData (auto-detects uniformity).
func fullTile(img *image.RGBA, tileSize int) *TileData {
	return newTileData(img, tileSize)
}

// checkerImage creates a tileSize x tileSize image with alternating 2x2 blocks of two colors.
func checkerImage(tileSize int, c1, c2 color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			if (x/2+y/2)%2 == 0 {
				img.SetRGBA(x, y, c1)
			} else {
				img.SetRGBA(x, y, c2)
			}
		}
	}
	return img
}

func TestDownsampleTile_AllNil(t *testing.T) {
	result := downsampleTile(nil, nil, nil, nil, 256, ResamplingBilinear, nil)
	if result != nil {
		t.Error("downsampleTile with all nil children should return nil")
	}
}

func TestDownsampleTile_SingleChild(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	tileSize := 256

	// Only top-left child.
	tl := solidTile(tileSize, red)
	result := downsampleTile(tl, nil, nil, nil, tileSize, ResamplingNearest, nil)
	if result == nil {
		t.Fatal("expected non-nil result with one child")
	}

	// Top-left quadrant should be red.
	c := result.RGBAAt(0, 0)
	if c != red {
		t.Errorf("top-left pixel = %v, want %v", c, red)
	}

	// Bottom-right quadrant should be transparent (nil child).
	c = result.RGBAAt(200, 200)
	if c.A != 0 {
		t.Errorf("bottom-right pixel (nil child) has alpha=%d, want 0", c.A)
	}
}

func TestDownsampleTile_Nearest_SolidColor(t *testing.T) {
	blue := color.RGBA{0, 0, 255, 255}
	tileSize := 256

	child := solidTile(tileSize, blue)
	result := downsampleTile(child, child, child, child, tileSize, ResamplingNearest, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Fast path should return a uniform tile.
	if !result.IsUniform() {
		t.Error("expected uniform result for 4 identical solid children")
	}
	if result.Color() != blue {
		t.Errorf("uniform color = %v, want %v", result.Color(), blue)
	}

	// Every pixel should be blue.
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			c := result.RGBAAt(x, y)
			if c != blue {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, c, blue)
			}
		}
	}
}

func TestDownsampleTile_Bilinear_SolidColor(t *testing.T) {
	green := color.RGBA{0, 200, 0, 255}
	tileSize := 256

	child := solidTile(tileSize, green)
	result := downsampleTile(child, child, child, child, tileSize, ResamplingBilinear, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Fast path should return a uniform tile.
	if !result.IsUniform() {
		t.Error("expected uniform result for 4 identical solid children")
	}

	c := result.RGBAAt(64, 64)
	if abs(int(c.R)-int(green.R)) > 1 || abs(int(c.G)-int(green.G)) > 1 || abs(int(c.B)-int(green.B)) > 1 {
		t.Errorf("bilinear solid: got %v, want ~%v", c, green)
	}
}

func TestDownsampleTile_Bilinear_Average(t *testing.T) {
	tileSize := 256

	// Create children with specific colors.
	white := solidTile(tileSize, color.RGBA{255, 255, 255, 255})
	black := solidTile(tileSize, color.RGBA{0, 0, 0, 255})

	// Top-left and top-right: white; bottom-left and bottom-right: black.
	result := downsampleTile(white, white, black, black, tileSize, ResamplingBilinear, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Check a pixel in the top-left quadrant (should be white).
	cTop := result.RGBAAt(10, 10)
	if cTop.R < 250 {
		t.Errorf("top-left pixel R=%d, want ~255", cTop.R)
	}

	// Check a pixel in the bottom-left quadrant (should be black).
	cBot := result.RGBAAt(10, tileSize-10)
	if cBot.R > 5 {
		t.Errorf("bottom-left pixel R=%d, want ~0", cBot.R)
	}
}

func TestDownsampleTile_FourDistinctColors(t *testing.T) {
	tileSize := 4 // Use small tile for easy verification.

	red := solidTile(tileSize, color.RGBA{200, 0, 0, 255})
	green := solidTile(tileSize, color.RGBA{0, 200, 0, 255})
	blue := solidTile(tileSize, color.RGBA{0, 0, 200, 255})
	yellow := solidTile(tileSize, color.RGBA{200, 200, 0, 255})

	result := downsampleTile(red, green, blue, yellow, tileSize, ResamplingNearest, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	half := tileSize / 2

	// Top-left quadrant should be red.
	c := result.RGBAAt(0, 0)
	if c.R < 190 || c.G > 10 || c.B > 10 {
		t.Errorf("top-left quadrant = %v, want red", c)
	}

	// Top-right quadrant should be green.
	c = result.RGBAAt(half, 0)
	if c.R > 10 || c.G < 190 || c.B > 10 {
		t.Errorf("top-right quadrant = %v, want green", c)
	}

	// Bottom-left quadrant should be blue.
	c = result.RGBAAt(0, half)
	if c.R > 10 || c.G > 10 || c.B < 190 {
		t.Errorf("bottom-left quadrant = %v, want blue", c)
	}

	// Bottom-right quadrant should be yellow.
	c = result.RGBAAt(half, half)
	if c.R < 190 || c.G < 190 || c.B > 10 {
		t.Errorf("bottom-right quadrant = %v, want yellow", c)
	}
}

func TestSrcPixel_Clamping(t *testing.T) {
	tileSize := 4
	img := solidImage(tileSize, color.RGBA{100, 100, 100, 255})
	// Set a distinct corner pixel.
	img.SetRGBA(3, 3, color.RGBA{255, 0, 0, 255})

	// Out-of-bounds coordinates should clamp.
	c := srcPixel(img, 10, 10, tileSize)
	if c.R != 255 {
		t.Errorf("srcPixel(10,10) = %v, want clamped to (3,3) = red", c)
	}

	c = srcPixel(img, 0, 0, tileSize)
	if c.R != 100 {
		t.Errorf("srcPixel(0,0) = %v, want grey (100)", c)
	}
}

// --- TileData-specific tests ---

func TestDetectUniform_Solid(t *testing.T) {
	tileSize := 64
	blue := color.RGBA{0, 100, 200, 255}
	img := solidImage(tileSize, blue)

	c, ok := detectUniform(img)
	if !ok {
		t.Fatal("expected solid image to be detected as uniform")
	}
	if c != blue {
		t.Errorf("detected color = %v, want %v", c, blue)
	}
}

func TestDetectUniform_NonUniform(t *testing.T) {
	tileSize := 64
	img := checkerImage(tileSize, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255})

	_, ok := detectUniform(img)
	if ok {
		t.Error("expected checker image to NOT be detected as uniform")
	}
}

func TestDetectUniform_Transparent(t *testing.T) {
	tileSize := 64
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))

	c, ok := detectUniform(img)
	if !ok {
		t.Fatal("expected blank (transparent) image to be detected as uniform")
	}
	if c != (color.RGBA{}) {
		t.Errorf("detected color = %v, want transparent zero", c)
	}
}

func TestTileData_UniformRGBAAt(t *testing.T) {
	tileSize := 256
	red := color.RGBA{255, 0, 0, 255}
	td := newTileDataUniform(red, tileSize)

	for _, pt := range [][2]int{{0, 0}, {128, 128}, {255, 255}} {
		c := td.RGBAAt(pt[0], pt[1])
		if c != red {
			t.Errorf("RGBAAt(%d,%d) = %v, want %v", pt[0], pt[1], c, red)
		}
	}
}

func TestTileData_ToRGBA(t *testing.T) {
	tileSize := 16
	green := color.RGBA{0, 200, 0, 255}
	td := newTileDataUniform(green, tileSize)

	img := td.ToRGBA()
	if img.Bounds().Dx() != tileSize || img.Bounds().Dy() != tileSize {
		t.Fatalf("ToRGBA size = %v, want %dx%d", img.Bounds(), tileSize, tileSize)
	}

	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			c := img.RGBAAt(x, y)
			if c != green {
				t.Fatalf("ToRGBA pixel (%d,%d) = %v, want %v", x, y, c, green)
			}
		}
	}
}

func TestTileData_AsImage_FullTile(t *testing.T) {
	tileSize := 16
	img := checkerImage(tileSize, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255})
	td := newTileData(img, tileSize)

	if td.IsUniform() {
		t.Error("checker tile should not be uniform")
	}

	// AsImage should return the underlying *image.RGBA.
	asImg := td.AsImage()
	if _, ok := asImg.(*image.RGBA); !ok {
		t.Errorf("AsImage() for full tile should be *image.RGBA, got %T", asImg)
	}
}

func TestTileData_AsImage_UniformTile(t *testing.T) {
	tileSize := 16
	blue := color.RGBA{0, 0, 255, 255}
	td := newTileDataUniform(blue, tileSize)

	asImg := td.AsImage()
	// Should implement image.Image (it's *TileData itself).
	bounds := asImg.Bounds()
	if bounds.Dx() != tileSize || bounds.Dy() != tileSize {
		t.Errorf("AsImage bounds = %v, want %dx%d", bounds, tileSize, tileSize)
	}

	rr, g, b, a := asImg.At(5, 5).RGBA()
	if uint8(rr>>8) != 0 || uint8(g>>8) != 0 || uint8(b>>8) != 255 || uint8(a>>8) != 255 {
		t.Errorf("AsImage.At(5,5) = (%d,%d,%d,%d), want blue", rr>>8, g>>8, b>>8, a>>8)
	}
}

func TestDownsampleTile_UniformFastPath(t *testing.T) {
	tileSize := 256
	ocean := color.RGBA{0, 50, 150, 255}

	child := solidTile(tileSize, ocean)
	result := downsampleTile(child, child, child, child, tileSize, ResamplingBilinear, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.IsUniform() {
		t.Error("expected uniform result from 4 identical uniform children")
	}
	if result.Color() != ocean {
		t.Errorf("color = %v, want %v", result.Color(), ocean)
	}
}

func TestDownsampleTile_MixedUniformChildren(t *testing.T) {
	tileSize := 4

	red := solidTile(tileSize, color.RGBA{200, 0, 0, 255})
	blue := solidTile(tileSize, color.RGBA{0, 0, 200, 255})

	// Different uniform colors: must not use the fast path.
	result := downsampleTile(red, red, blue, blue, tileSize, ResamplingNearest, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Top-left quadrant should be red, bottom-left should be blue.
	cTop := result.RGBAAt(0, 0)
	if cTop.R < 190 || cTop.B > 10 {
		t.Errorf("top-left = %v, want red", cTop)
	}
	cBot := result.RGBAAt(0, tileSize/2)
	if cBot.R > 10 || cBot.B < 190 {
		t.Errorf("bottom-left = %v, want blue", cBot)
	}
}

func TestDownsampleTile_NilAndUniform(t *testing.T) {
	tileSize := 256
	green := color.RGBA{0, 200, 0, 255}

	child := solidTile(tileSize, green)
	// Only top-left is present; the rest are nil.
	result := downsampleTile(child, nil, nil, nil, tileSize, ResamplingNearest, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Should NOT be uniform (has transparent gaps from nil children).
	// (It could be non-uniform due to the transparent quadrants.)
	cTopLeft := result.RGBAAt(0, 0)
	if cTopLeft != green {
		t.Errorf("top-left = %v, want %v", cTopLeft, green)
	}
	cBotRight := result.RGBAAt(200, 200)
	if cBotRight.A != 0 {
		t.Errorf("bottom-right alpha = %d, want 0", cBotRight.A)
	}
}

func TestDownsampleTile_FillColorTransform(t *testing.T) {
	tileSize := 8
	green := color.RGBA{0, 200, 0, 255}
	fill := color.RGBA{128, 128, 128, 255}

	child := solidTile(tileSize, green)
	// Only top-left present; nil children normally contribute transparent.
	// With fillColor, transparent pixels should be substituted.
	result := downsampleTile(child, nil, nil, nil, tileSize, ResamplingNearest, &fill)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	cTopLeft := result.RGBAAt(0, 0)
	if cTopLeft != green {
		t.Errorf("top-left (data) = %v, want %v", cTopLeft, green)
	}
	cBotRight := result.RGBAAt(tileSize/2+1, tileSize/2+1)
	if cBotRight != fill {
		t.Errorf("bottom-right (was transparent, now fill) = %v, want %v", cBotRight, fill)
	}
}

func TestDownsampleTile_Mode_SolidColor(t *testing.T) {
	tileSize := 256
	red := color.RGBA{200, 0, 0, 255}

	child := solidTile(tileSize, red)
	result := downsampleTile(child, child, child, child, tileSize, ResamplingMode, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.IsUniform() {
		t.Error("expected uniform result for 4 identical solid children")
	}
	if result.Color() != red {
		t.Errorf("color = %v, want %v", result.Color(), red)
	}
}

func TestDownsampleTile_Mode_FourDistinctColors(t *testing.T) {
	tileSize := 4

	red := solidTile(tileSize, color.RGBA{200, 0, 0, 255})
	green := solidTile(tileSize, color.RGBA{0, 200, 0, 255})
	blue := solidTile(tileSize, color.RGBA{0, 0, 200, 255})
	yellow := solidTile(tileSize, color.RGBA{200, 200, 0, 255})

	result := downsampleTile(red, green, blue, yellow, tileSize, ResamplingMode, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	half := tileSize / 2

	c := result.RGBAAt(0, 0)
	if c.R < 190 || c.G > 10 || c.B > 10 {
		t.Errorf("top-left quadrant = %v, want red", c)
	}
	c = result.RGBAAt(half, 0)
	if c.R > 10 || c.G < 190 || c.B > 10 {
		t.Errorf("top-right quadrant = %v, want green", c)
	}
	c = result.RGBAAt(0, half)
	if c.R > 10 || c.G > 10 || c.B < 190 {
		t.Errorf("bottom-left quadrant = %v, want blue", c)
	}
	c = result.RGBAAt(half, half)
	if c.R < 190 || c.G < 190 || c.B > 10 {
		t.Errorf("bottom-right quadrant = %v, want yellow", c)
	}
}

func TestDownsampleTile_Mode_PicksMajority(t *testing.T) {
	tileSize := 4

	// Create a tile where 3 out of 4 pixels in each 2×2 block are red,
	// and 1 is blue. Mode should pick red.
	red := color.RGBA{200, 0, 0, 255}
	blue := color.RGBA{0, 0, 200, 255}

	img := solidImage(tileSize, red)
	// Set one pixel per 2×2 block to blue (bottom-right corner).
	img.SetRGBA(1, 1, blue)
	img.SetRGBA(3, 1, blue)
	img.SetRGBA(1, 3, blue)
	img.SetRGBA(3, 3, blue)

	child := fullTile(img, tileSize)
	result := downsampleTile(child, child, child, child, tileSize, ResamplingMode, nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// All output pixels in the top-left quadrant should be red (3/4 majority).
	for y := 0; y < tileSize/2; y++ {
		for x := 0; x < tileSize/2; x++ {
			c := result.RGBAAt(x, y)
			if c != red {
				t.Fatalf("pixel (%d,%d) = %v, want %v (mode=red majority)", x, y, c, red)
			}
		}
	}
}

func TestModeGray(t *testing.T) {
	tests := []struct {
		name       string
		a, b, c, d uint8
		want       uint8
	}{
		{"all same", 10, 10, 10, 10, 10},
		{"3 same a", 10, 10, 10, 20, 10},
		{"3 same b", 10, 20, 20, 20, 20},
		{"2 pairs prefer first", 10, 10, 20, 20, 10},
		{"2 same c/d", 10, 20, 30, 30, 30},
		{"all distinct", 10, 20, 30, 40, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modeGray(tt.a, tt.b, tt.c, tt.d)
			if got != tt.want {
				t.Errorf("modeGray(%d,%d,%d,%d) = %d, want %d",
					tt.a, tt.b, tt.c, tt.d, got, tt.want)
			}
		})
	}
}

func TestModeRGBA(t *testing.T) {
	red := color.RGBA{200, 0, 0, 255}
	blue := color.RGBA{0, 0, 200, 255}
	transparent := color.RGBA{0, 0, 0, 0}

	// 3 red, 1 blue -> red
	got := modeRGBA(red, red, red, blue)
	if got != red {
		t.Errorf("3 red, 1 blue: got %v, want %v", got, red)
	}

	// 2 red, 2 blue -> red (first wins tie)
	got = modeRGBA(red, blue, red, blue)
	if got != red {
		t.Errorf("2 red, 2 blue: got %v, want %v", got, red)
	}

	// Transparent pixels are excluded
	got = modeRGBA(transparent, blue, transparent, blue)
	if got != blue {
		t.Errorf("2 transparent, 2 blue: got %v, want %v", got, blue)
	}

	// All transparent -> transparent
	got = modeRGBA(transparent, transparent, transparent, transparent)
	if got != transparent {
		t.Errorf("all transparent: got %v, want transparent", got)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
