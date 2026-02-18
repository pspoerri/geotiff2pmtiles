package tile

import (
	"image"
	"image/color"
)

// TileData represents a tile stored in the pyramid. It uses the most compact
// representation possible:
//
//   - Uniform: all pixels share one color → stores only the color (~0 bytes).
//   - Gray: single-channel data (R=G=B, A=255) → *image.Gray (1 byte/pixel).
//   - RGBA: multi-channel data → *image.RGBA (4 bytes/pixel).
//
// For a 256×256 tile: uniform ≈ 0 B, gray = 64 KB, RGBA = 256 KB.
// TileData implements image.Image so it can be passed directly to encoders
// without expansion.
type TileData struct {
	img      *image.RGBA // non-nil for normal (multi-color, multi-channel) tiles
	gray     *image.Gray // non-nil for single-channel tiles (R=G=B, A=255)
	color    color.RGBA  // the uniform color; meaningful when img == nil && gray == nil
	tileSize int         // tile dimensions (square); used for Bounds() on uniform tiles
}

// Compile-time check that *TileData implements image.Image.
var _ image.Image = (*TileData)(nil)

// newTileData wraps a rendered image, automatically detecting compact storage.
// Priority: uniform (single color) → gray (R=G=B, A=255) → full RGBA.
func newTileData(img *image.RGBA, tileSize int) *TileData {
	if c, ok := detectUniform(img); ok {
		PutRGBA(img)
		return &TileData{color: c, tileSize: tileSize}
	}
	if g, ok := detectGray(img); ok {
		PutRGBA(img)
		return &TileData{gray: g, tileSize: tileSize}
	}
	return &TileData{img: img, tileSize: tileSize}
}

// newTileDataUniform creates a uniform (single-color) tile.
func newTileDataUniform(c color.RGBA, tileSize int) *TileData {
	return &TileData{color: c, tileSize: tileSize}
}

// IsUniform returns true if all pixels share the same color.
func (t *TileData) IsUniform() bool {
	return t.img == nil && t.gray == nil
}

// IsGray returns true if the tile is stored in single-channel gray format.
func (t *TileData) IsGray() bool {
	return t.gray != nil
}

// Color returns the uniform color. Only meaningful when IsUniform() is true.
func (t *TileData) Color() color.RGBA {
	return t.color
}

// isUniformGray returns true if the tile is uniform with R=G=B and A=255.
// Used internally to detect gray-compatible tiles for the downsample fast path.
func (t *TileData) isUniformGray() bool {
	if !t.IsUniform() {
		return false
	}
	c := t.color
	return c.R == c.G && c.R == c.B && c.A == 255
}

// RGBAAt returns the pixel at (x, y).
func (t *TileData) RGBAAt(x, y int) color.RGBA {
	if t.img != nil {
		return t.img.RGBAAt(x, y)
	}
	if t.gray != nil {
		v := t.gray.GrayAt(x, y).Y
		return color.RGBA{R: v, G: v, B: v, A: 255}
	}
	return t.color
}

// ToRGBA returns the full RGBA image. For uniform and gray tiles, this
// allocates and fills a new image. Prefer AsImage() when passing to encoders.
func (t *TileData) ToRGBA() *image.RGBA {
	if t.img != nil {
		return t.img
	}
	img := GetRGBA(t.tileSize, t.tileSize)
	pix := img.Pix
	if t.gray != nil {
		gPix := t.gray.Pix
		gStride := t.gray.Stride
		for y := 0; y < t.tileSize; y++ {
			gRow := gPix[y*gStride : y*gStride+t.tileSize]
			dstOff := y * img.Stride
			for x, v := range gRow {
				off := dstOff + x*4
				pix[off] = v
				pix[off+1] = v
				pix[off+2] = v
				pix[off+3] = 255
			}
		}
		return img
	}
	c := t.color
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
// path). For gray tiles it expands to RGBA (encoders expect color output).
// For uniform tiles it returns *TileData itself (which implements
// image.Image via generic At() — trivially fast for uniform data).
func (t *TileData) AsImage() image.Image {
	if t.img != nil {
		return t.img
	}
	if t.gray != nil {
		// Encoders (PNG, JPEG, WebP) produce color output, so expand to RGBA.
		return t.ToRGBA()
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
	if t.gray != nil {
		return t.gray.Bounds()
	}
	return image.Rect(0, 0, t.tileSize, t.tileSize)
}

func (t *TileData) At(x, y int) color.Color {
	if t.img != nil {
		return t.img.At(x, y)
	}
	if t.gray != nil {
		v := t.gray.GrayAt(x, y).Y
		return color.RGBA{R: v, G: v, B: v, A: 255}
	}
	return t.color
}

// --- Uniform and gray detection ---

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

// detectGray checks whether the RGBA image is single-channel: R=G=B and A=255
// for every pixel. If so, it extracts an *image.Gray copy (1 byte/pixel instead
// of 4), cutting memory by 75%. Returns nil, false on the first mismatch.
func detectGray(img *image.RGBA) (*image.Gray, bool) {
	pix := img.Pix
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	n := w * h * 4
	if len(pix) < n {
		return nil, false
	}

	// Quick scan: verify R=G=B and A=255 for every pixel.
	for i := 0; i < n; i += 4 {
		if pix[i] != pix[i+1] || pix[i] != pix[i+2] || pix[i+3] != 255 {
			return nil, false
		}
	}

	// Extract the gray channel.
	g := image.NewGray(bounds)
	gPix := g.Pix
	for i, j := 0, 0; i < n; i, j = i+4, j+1 {
		gPix[j] = pix[i]
	}
	return g, true
}

// tileDataToRGBA converts a *TileData to *image.RGBA, returning nil for nil input.
func tileDataToRGBA(td *TileData) *image.RGBA {
	if td == nil {
		return nil
	}
	return td.ToRGBA()
}

// --- Serialization for disk spilling ---

// tileDataType identifies the storage format for disk serialization.
type tileDataType uint8

const (
	tileDataTypeUniform tileDataType = 0 // 4 bytes: R, G, B, A
	tileDataTypeGray    tileDataType = 1 // tileSize*tileSize bytes
	tileDataTypeRGBA    tileDataType = 2 // tileSize*tileSize*4 bytes
)

// SerializeAppend appends the tile's raw pixel data to buf and returns the
// extended slice plus the type tag. The caller stores the type tag separately
// in the index so deserialization knows the format.
func (t *TileData) SerializeAppend(buf []byte) ([]byte, tileDataType) {
	if t.img != nil {
		return append(buf, t.img.Pix...), tileDataTypeRGBA
	}
	if t.gray != nil {
		return append(buf, t.gray.Pix...), tileDataTypeGray
	}
	return append(buf, t.color.R, t.color.G, t.color.B, t.color.A), tileDataTypeUniform
}

// DeserializeTileData reconstructs a TileData from raw bytes and a type tag.
func DeserializeTileData(data []byte, typ tileDataType, tileSize int) *TileData {
	switch typ {
	case tileDataTypeUniform:
		if len(data) < 4 {
			return nil
		}
		return newTileDataUniform(color.RGBA{R: data[0], G: data[1], B: data[2], A: data[3]}, tileSize)
	case tileDataTypeGray:
		expected := tileSize * tileSize
		if len(data) < expected {
			return nil
		}
		g := image.NewGray(image.Rect(0, 0, tileSize, tileSize))
		copy(g.Pix, data[:expected])
		return &TileData{gray: g, tileSize: tileSize}
	case tileDataTypeRGBA:
		expected := tileSize * tileSize * 4
		if len(data) < expected {
			return nil
		}
		img := GetRGBA(tileSize, tileSize)
		copy(img.Pix, data[:expected])
		return &TileData{img: img, tileSize: tileSize}
	}
	return nil
}

// Release returns the tile's internal RGBA image (if any) to the pool.
// After Release, the TileData must not be used.
func (t *TileData) Release() {
	if t.img != nil {
		PutRGBA(t.img)
		t.img = nil
	}
}

// MemoryBytes returns the estimated heap bytes used by this tile's pixel data.
func (t *TileData) MemoryBytes() int64 {
	if t.img != nil {
		return int64(len(t.img.Pix))
	}
	if t.gray != nil {
		return int64(len(t.gray.Pix))
	}
	return 4 // uniform: just the color struct
}
