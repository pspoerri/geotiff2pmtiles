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

// --- TileData serialization round-trips ---

func TestSerializeTileData_RoundTrip_RGBA(t *testing.T) {
	tileSize := 8
	img := checkerImage(tileSize, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255})
	td := newTileData(img, tileSize)
	if td.IsUniform() || td.IsGray() {
		t.Fatal("expected full RGBA tile for checker input")
	}

	buf, typ := td.SerializeAppend(nil)
	if typ != tileDataTypeRGBA {
		t.Errorf("type = %v, want tileDataTypeRGBA", typ)
	}
	if len(buf) != tileSize*tileSize*4 {
		t.Errorf("buf len = %d, want %d", len(buf), tileSize*tileSize*4)
	}

	td2 := DeserializeTileData(buf, typ, tileSize)
	if td2 == nil {
		t.Fatal("DeserializeTileData returned nil for valid RGBA data")
	}
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			c1 := td.RGBAAt(x, y)
			c2 := td2.RGBAAt(x, y)
			if c1 != c2 {
				t.Fatalf("pixel (%d,%d): original=%v deserialized=%v", x, y, c1, c2)
			}
		}
	}
}

func TestSerializeTileData_RoundTrip_Gray(t *testing.T) {
	tileSize := 8
	img := grayCheckerImage(tileSize, 50, 200)
	td := newTileData(img, tileSize)
	if !td.IsGray() {
		t.Fatal("expected gray tile for gray checker input")
	}

	buf, typ := td.SerializeAppend(nil)
	if typ != tileDataTypeGray {
		t.Errorf("type = %v, want tileDataTypeGray", typ)
	}
	if len(buf) != tileSize*tileSize {
		t.Errorf("buf len = %d, want %d", len(buf), tileSize*tileSize)
	}

	td2 := DeserializeTileData(buf, typ, tileSize)
	if td2 == nil {
		t.Fatal("DeserializeTileData returned nil for valid gray data")
	}
	if !td2.IsGray() {
		t.Error("deserialized tile should be gray")
	}
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			c1 := td.RGBAAt(x, y)
			c2 := td2.RGBAAt(x, y)
			if c1 != c2 {
				t.Fatalf("pixel (%d,%d): %v vs %v", x, y, c1, c2)
			}
		}
	}
}

func TestSerializeTileData_RoundTrip_Uniform(t *testing.T) {
	tileSize := 8
	blue := color.RGBA{0, 0, 200, 255}
	td := newTileDataUniform(blue, tileSize)

	buf, typ := td.SerializeAppend(nil)
	if typ != tileDataTypeUniform {
		t.Errorf("type = %v, want tileDataTypeUniform", typ)
	}
	if len(buf) != 4 {
		t.Errorf("uniform buf len = %d, want 4", len(buf))
	}
	if buf[0] != blue.R || buf[1] != blue.G || buf[2] != blue.B || buf[3] != blue.A {
		t.Errorf("serialized bytes = %v, want {0,0,200,255}", buf)
	}

	td2 := DeserializeTileData(buf, typ, tileSize)
	if td2 == nil {
		t.Fatal("DeserializeTileData returned nil for valid uniform data")
	}
	if !td2.IsUniform() {
		t.Error("deserialized uniform tile should be uniform")
	}
	if td2.Color() != blue {
		t.Errorf("color = %v, want %v", td2.Color(), blue)
	}
}

func TestSerializeTileData_AppendsToBuf(t *testing.T) {
	td := newTileDataUniform(color.RGBA{1, 2, 3, 4}, 4)
	prefix := []byte{0xDE, 0xAD}
	buf, _ := td.SerializeAppend(prefix)
	// Prefix bytes should be preserved.
	if buf[0] != 0xDE || buf[1] != 0xAD {
		t.Errorf("prefix bytes overwritten: %v", buf[:2])
	}
	// Uniform color appended after prefix.
	if buf[2] != 1 || buf[3] != 2 || buf[4] != 3 || buf[5] != 4 {
		t.Errorf("color bytes = %v, want [1,2,3,4]", buf[2:])
	}
}

func TestDeserializeTileData_TruncatedData(t *testing.T) {
	tileSize := 8
	// Uniform needs 4 bytes.
	if got := DeserializeTileData([]byte{1, 2, 3}, tileDataTypeUniform, tileSize); got != nil {
		t.Errorf("truncated uniform: expected nil, got non-nil")
	}
	// Gray needs tileSize*tileSize bytes.
	if got := DeserializeTileData([]byte{1, 2, 3}, tileDataTypeGray, tileSize); got != nil {
		t.Errorf("truncated gray: expected nil, got non-nil")
	}
	// RGBA needs tileSize*tileSize*4 bytes.
	if got := DeserializeTileData([]byte{1, 2, 3}, tileDataTypeRGBA, tileSize); got != nil {
		t.Errorf("truncated RGBA: expected nil, got non-nil")
	}
	// Unknown type should return nil.
	if got := DeserializeTileData([]byte{1, 2, 3, 4}, tileDataType(99), tileSize); got != nil {
		t.Errorf("unknown type: expected nil, got non-nil")
	}
}

// --- TileData.MemoryBytes ---

func TestTileData_MemoryBytes_Uniform(t *testing.T) {
	td := newTileDataUniform(color.RGBA{1, 2, 3, 4}, 256)
	if got := td.MemoryBytes(); got != 4 {
		t.Errorf("uniform MemoryBytes = %d, want 4", got)
	}
}

func TestTileData_MemoryBytes_Gray(t *testing.T) {
	tileSize := 16
	img := grayCheckerImage(tileSize, 10, 20)
	td := newTileData(img, tileSize)
	if !td.IsGray() {
		t.Fatal("expected gray tile")
	}
	want := int64(tileSize * tileSize)
	if got := td.MemoryBytes(); got != want {
		t.Errorf("gray MemoryBytes = %d, want %d", got, want)
	}
}

func TestTileData_MemoryBytes_RGBA(t *testing.T) {
	tileSize := 16
	img := checkerImage(tileSize, color.RGBA{1, 0, 0, 255}, color.RGBA{0, 0, 1, 255})
	td := newTileData(img, tileSize)
	if td.IsUniform() || td.IsGray() {
		t.Fatal("expected full RGBA tile")
	}
	want := int64(tileSize * tileSize * 4)
	if got := td.MemoryBytes(); got != want {
		t.Errorf("RGBA MemoryBytes = %d, want %d", got, want)
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
