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
	result := downsampleTile(nil, nil, nil, nil, 256, ResamplingBilinear)
	if result != nil {
		t.Error("downsampleTile with all nil children should return nil")
	}
}

func TestDownsampleTile_SingleChild(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	tileSize := 256

	// Only top-left child.
	tl := solidImage(tileSize, red)
	result := downsampleTile(tl, nil, nil, nil, tileSize, ResamplingNearest)
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

	child := solidImage(tileSize, blue)
	result := downsampleTile(child, child, child, child, tileSize, ResamplingNearest)
	if result == nil {
		t.Fatal("expected non-nil result")
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

	child := solidImage(tileSize, green)
	result := downsampleTile(child, child, child, child, tileSize, ResamplingBilinear)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Bilinear average of identical pixels should be the same color.
	c := result.RGBAAt(64, 64)
	if abs(int(c.R)-int(green.R)) > 1 || abs(int(c.G)-int(green.G)) > 1 || abs(int(c.B)-int(green.B)) > 1 {
		t.Errorf("bilinear solid: got %v, want ~%v", c, green)
	}
}

func TestDownsampleTile_Bilinear_Average(t *testing.T) {
	tileSize := 256

	// Create children with specific colors.
	white := solidImage(tileSize, color.RGBA{255, 255, 255, 255})
	black := solidImage(tileSize, color.RGBA{0, 0, 0, 255})

	// Top-left and top-right: white; bottom-left and bottom-right: black.
	result := downsampleTile(white, white, black, black, tileSize, ResamplingBilinear)
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

	red := solidImage(tileSize, color.RGBA{200, 0, 0, 255})
	green := solidImage(tileSize, color.RGBA{0, 200, 0, 255})
	blue := solidImage(tileSize, color.RGBA{0, 0, 200, 255})
	yellow := solidImage(tileSize, color.RGBA{200, 200, 0, 255})

	result := downsampleTile(red, green, blue, yellow, tileSize, ResamplingNearest)
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

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
