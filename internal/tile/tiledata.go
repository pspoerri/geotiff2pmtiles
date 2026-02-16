package tile

import (
	"image"
	"image/color"
)

// TileData represents a tile stored in the pyramid. For tiles where every pixel
// shares the same color (ocean, transparent gaps, uniform terrain), it stores
// only the single color value — saving ~262 KB per 256×256 tile compared to a
// full image.RGBA.
//
// TileData implements image.Image so it can be passed directly to encoders
// without expansion.
type TileData struct {
	img      *image.RGBA // non-nil for normal (multi-color) tiles
	color    color.RGBA  // the uniform color; meaningful when img == nil
	tileSize int         // tile dimensions (square); used for Bounds() on uniform tiles
}

// Compile-time check that *TileData implements image.Image.
var _ image.Image = (*TileData)(nil)

// newTileData wraps a rendered image, automatically detecting uniform tiles.
// If all pixels share the same color, only the color is stored.
func newTileData(img *image.RGBA, tileSize int) *TileData {
	if c, ok := detectUniform(img); ok {
		return &TileData{color: c, tileSize: tileSize}
	}
	return &TileData{img: img, tileSize: tileSize}
}

// newTileDataUniform creates a uniform (single-color) tile.
func newTileDataUniform(c color.RGBA, tileSize int) *TileData {
	return &TileData{color: c, tileSize: tileSize}
}

// IsUniform returns true if all pixels share the same color.
func (t *TileData) IsUniform() bool {
	return t.img == nil
}

// Color returns the uniform color. Only meaningful when IsUniform() is true.
func (t *TileData) Color() color.RGBA {
	return t.color
}

// RGBAAt returns the pixel at (x, y).
func (t *TileData) RGBAAt(x, y int) color.RGBA {
	if t.img != nil {
		return t.img.RGBAAt(x, y)
	}
	return t.color
}

// ToRGBA returns the full RGBA image. For uniform tiles, this allocates and
// fills a new image. Prefer AsImage() when passing to encoders.
func (t *TileData) ToRGBA() *image.RGBA {
	if t.img != nil {
		return t.img
	}
	img := image.NewRGBA(image.Rect(0, 0, t.tileSize, t.tileSize))
	c := t.color
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = c.R
		pix[i+1] = c.G
		pix[i+2] = c.B
		pix[i+3] = c.A
	}
	return img
}

// AsImage returns an image.Image suitable for encoders. For full tiles it
// returns the underlying *image.RGBA (so encoders can type-switch to the fast
// path). For uniform tiles it returns *TileData itself (which implements
// image.Image via generic At() — trivially fast for uniform data).
func (t *TileData) AsImage() image.Image {
	if t.img != nil {
		return t.img
	}
	return t
}

// --- image.Image interface ---

func (t *TileData) ColorModel() color.Model {
	return color.RGBAModel
}

func (t *TileData) Bounds() image.Rectangle {
	if t.img != nil {
		return t.img.Bounds()
	}
	return image.Rect(0, 0, t.tileSize, t.tileSize)
}

func (t *TileData) At(x, y int) color.Color {
	if t.img != nil {
		return t.img.At(x, y)
	}
	return t.color
}

// --- Uniform detection ---

// detectUniform checks whether every pixel in img shares the same RGBA value.
// Returns the color and true if uniform, or zero-value and false otherwise.
// The scan is sequential over the Pix slice (cache-friendly) and short-circuits
// on the first mismatch, so non-uniform tiles bail out almost immediately.
func detectUniform(img *image.RGBA) (color.RGBA, bool) {
	pix := img.Pix
	if len(pix) < 4 {
		return color.RGBA{}, false
	}
	r, g, b, a := pix[0], pix[1], pix[2], pix[3]
	for i := 4; i < len(pix); i += 4 {
		if pix[i] != r || pix[i+1] != g || pix[i+2] != b || pix[i+3] != a {
			return color.RGBA{}, false
		}
	}
	return color.RGBA{R: r, G: g, B: b, A: a}, true
}

// tileDataToRGBA converts a *TileData to *image.RGBA, returning nil for nil input.
func tileDataToRGBA(td *TileData) *image.RGBA {
	if td == nil {
		return nil
	}
	return td.ToRGBA()
}
