package cog

import (
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"testing"
)

func TestUndoHorizontalDifferencing8Bit(t *testing.T) {
	// 2 pixels wide, 2 samples per pixel. First pixel stored as-is, second as delta.
	data := []byte{
		10, 20, 5, 3, // row 0: pixel0=(10,20), delta1=(5,3) → pixel1=(15,23)
		100, 200, 10, 6, // row 1: pixel0=(100,200), delta1=(10,6) → pixel1=(110,206)
	}
	undoHorizontalDifferencing(data, 2, 2, 1, binary.LittleEndian)

	want := []byte{10, 20, 15, 23, 100, 200, 110, 206}
	for i := range want {
		if data[i] != want[i] {
			t.Errorf("byte %d: got %d, want %d", i, data[i], want[i])
		}
	}
}

func TestUndoHorizontalDifferencing16Bit(t *testing.T) {
	bo := binary.LittleEndian
	// 3 pixels wide, 1 sample per pixel (16-bit).
	// pixel0=1000, delta1=500, delta2=200
	// Expected: 1000, 1500, 1700
	data := make([]byte, 6)
	bo.PutUint16(data[0:2], 1000)
	bo.PutUint16(data[2:4], 500)
	bo.PutUint16(data[4:6], 200)

	undoHorizontalDifferencing(data, 3, 1, 2, bo)

	got := []uint16{
		bo.Uint16(data[0:2]),
		bo.Uint16(data[2:4]),
		bo.Uint16(data[4:6]),
	}
	want := []uint16{1000, 1500, 1700}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestUndoHorizontalDifferencing16BitMultiSample(t *testing.T) {
	bo := binary.LittleEndian
	// 2 pixels wide, 4 samples per pixel (like RGBNIR).
	// pixel0: R=100, G=200, B=300, N=400
	// delta1: R=10, G=20, B=30, N=40
	// Expected pixel1: 110, 220, 330, 440
	data := make([]byte, 16)
	bo.PutUint16(data[0:2], 100)
	bo.PutUint16(data[2:4], 200)
	bo.PutUint16(data[4:6], 300)
	bo.PutUint16(data[6:8], 400)
	bo.PutUint16(data[8:10], 10)
	bo.PutUint16(data[10:12], 20)
	bo.PutUint16(data[12:14], 30)
	bo.PutUint16(data[14:16], 40)

	undoHorizontalDifferencing(data, 2, 4, 2, bo)

	want := []uint16{100, 200, 300, 400, 110, 220, 330, 440}
	for i := 0; i < 8; i++ {
		got := bo.Uint16(data[i*2 : i*2+2])
		if got != want[i] {
			t.Errorf("sample %d: got %d, want %d", i, got, want[i])
		}
	}
}

func TestUndoHorizontalDifferencing32Bit(t *testing.T) {
	bo := binary.LittleEndian
	// 3 pixels wide, 1 sample per pixel (32-bit).
	// pixel0=1000, delta1=500, delta2=200
	// Expected: 1000, 1500, 1700
	data := make([]byte, 12)
	bo.PutUint32(data[0:4], 1000)
	bo.PutUint32(data[4:8], 500)
	bo.PutUint32(data[8:12], 200)

	undoHorizontalDifferencing(data, 3, 1, 4, bo)

	got := []uint32{
		bo.Uint32(data[0:4]),
		bo.Uint32(data[4:8]),
		bo.Uint32(data[8:12]),
	}
	want := []uint32{1000, 1500, 1700}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestUndoFloatingPointPredictor(t *testing.T) {
	bo := binary.LittleEndian
	// 3 pixels wide, 1 sample per pixel, float32 (4 bytes per sample).
	// Original float values: 1.0, 2.0, 3.0
	// Step 1 (encode): byte-shuffle each row — group all byte-0, byte-1, byte-2, byte-3.
	// Step 2 (encode): byte-level horizontal differencing.
	// To create test data, we reverse the process.

	origFloats := []float32{1.0, 2.0, 3.0}
	origBytes := make([]byte, 12)
	for i, f := range origFloats {
		bo.PutUint32(origBytes[i*4:i*4+4], math.Float32bits(f))
	}

	// Byte-shuffle: group by byte position.
	shuffled := make([]byte, 12)
	for s := 0; s < 3; s++ {
		for b := 0; b < 4; b++ {
			shuffled[b*3+s] = origBytes[s*4+b]
		}
	}

	// Byte-level differencing.
	encoded := make([]byte, 12)
	encoded[0] = shuffled[0]
	for i := 1; i < 12; i++ {
		encoded[i] = shuffled[i] - shuffled[i-1]
	}

	// Now undo.
	undoFloatingPointPredictor(encoded, 3, 1, 4)

	// Verify we get the original float values back.
	for i, want := range origFloats {
		bits := bo.Uint32(encoded[i*4 : i*4+4])
		got := math.Float32frombits(bits)
		if got != want {
			t.Errorf("pixel %d: got %v, want %v", i, got, want)
		}
	}
}

func TestBuildRescalerLinear(t *testing.T) {
	fn := buildRescaler(RescaleLinear, 0, 65535)

	tests := []struct {
		in   uint16
		want uint8
	}{
		{0, 0},
		{65535, 255},
		{32768, 128}, // ~128
	}
	for _, tt := range tests {
		got := fn(tt.in)
		if got != tt.want {
			t.Errorf("linear(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestBuildRescalerLinearCustomRange(t *testing.T) {
	fn := buildRescaler(RescaleLinear, 100, 10000)

	// Below min → 0.
	if got := fn(50); got != 0 {
		t.Errorf("linear(50) = %d, want 0", got)
	}
	// Above max → 255.
	if got := fn(20000); got != 255 {
		t.Errorf("linear(20000) = %d, want 255", got)
	}
	// At min → 0.
	if got := fn(100); got != 0 {
		t.Errorf("linear(100) = %d, want 0", got)
	}
	// At max → 255.
	if got := fn(10000); got != 255 {
		t.Errorf("linear(10000) = %d, want 255", got)
	}
}

func TestBuildRescalerLog(t *testing.T) {
	fn := buildRescaler(RescaleLog, 1, 10000)

	// At min → 0.
	if got := fn(1); got != 0 {
		t.Errorf("log(1) = %d, want 0", got)
	}
	// At max → 255.
	if got := fn(10000); got != 255 {
		t.Errorf("log(10000) = %d, want 255", got)
	}
	// Midpoint should be higher than linear midpoint (log curve).
	mid := fn(5000)
	linearMid := buildRescaler(RescaleLinear, 1, 10000)(5000)
	if mid <= linearMid {
		t.Errorf("log midpoint (%d) should be > linear midpoint (%d)", mid, linearMid)
	}
	// Below min → 0.
	if got := fn(0); got != 0 {
		t.Errorf("log(0) = %d, want 0", got)
	}
}

func TestBuildRescalerNone(t *testing.T) {
	fn := buildRescaler(RescaleNone, 0, 0)

	if got := fn(42); got != 42 {
		t.Errorf("none(42) = %d, want 42", got)
	}
	if got := fn(255); got != 255 {
		t.Errorf("none(255) = %d, want 255", got)
	}
}

func TestBuildRescalerEdgeCases(t *testing.T) {
	// max == min → always 0.
	fn := buildRescaler(RescaleLinear, 100, 100)
	if got := fn(100); got != 0 {
		t.Errorf("linear equal range: got %d, want 0", got)
	}

	fnLog := buildRescaler(RescaleLog, 100, 100)
	if got := fnLog(100); got != 0 {
		t.Errorf("log equal range: got %d, want 0", got)
	}
}

func TestDecodeRawTile8BitBackwardsCompat(t *testing.T) {
	// Verify that zero-value BandConfig produces identical output to the legacy code.
	// 2x2 tile, 3 samples per pixel (RGB), 8-bit.
	w, h, spp := 2, 2, 3
	data := make([]byte, w*h*spp)
	// pixel (0,0): R=255, G=0, B=0
	data[0], data[1], data[2] = 255, 0, 0
	// pixel (1,0): R=0, G=255, B=0
	data[3], data[4], data[5] = 0, 255, 0
	// pixel (0,1): R=0, G=0, B=255
	data[6], data[7], data[8] = 0, 0, 255
	// pixel (1,1): R=128, G=128, B=128
	data[9], data[10], data[11] = 128, 128, 128

	ifd := &IFD{
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: uint16(spp),
		BitsPerSample:   []uint16{8, 8, 8},
	}

	r := &Reader{bo: binary.LittleEndian, ifds: []IFD{*ifd}}
	img, err := r.decodeRawTile(ifd, data)
	if err != nil {
		t.Fatal(err)
	}

	rgba := img.(*image.RGBA)
	// Check pixel (0,0): R=255, G=0, B=0, A=255.
	assertPixel(t, rgba, 0, 0, color.RGBA{255, 0, 0, 255})
	// Check pixel (1,0): R=0, G=255, B=0, A=255.
	assertPixel(t, rgba, 1, 0, color.RGBA{0, 255, 0, 255})
	// Check pixel (0,1): R=0, G=0, B=255, A=255.
	assertPixel(t, rgba, 0, 1, color.RGBA{0, 0, 255, 255})
	// Check pixel (1,1): R=128, G=128, B=128, A=255.
	assertPixel(t, rgba, 1, 1, color.RGBA{128, 128, 128, 255})
}

func TestDecodeRawTile8BitRGBA(t *testing.T) {
	// 2x1 tile, 4 spp (RGBA), 8-bit, default BandConfig.
	w, h, spp := 2, 1, 4
	data := []byte{
		10, 20, 30, 200, // pixel0: R=10, G=20, B=30, A=200
		50, 60, 70, 0, // pixel1: transparent
	}

	ifd := &IFD{
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: uint16(spp),
		BitsPerSample:   []uint16{8, 8, 8, 8},
	}

	r := &Reader{bo: binary.LittleEndian, ifds: []IFD{*ifd}}
	img, err := r.decodeRawTile(ifd, data)
	if err != nil {
		t.Fatal(err)
	}

	rgba := img.(*image.RGBA)
	// pixel0: auto alpha from band 4 → A=200.
	assertPixel(t, rgba, 0, 0, color.RGBA{10, 20, 30, 200})
	// pixel1: alpha band=0 → transparent.
	assertPixel(t, rgba, 1, 0, color.RGBA{0, 0, 0, 0})
}

func TestDecodeRawTile8BitSingleBandNodata(t *testing.T) {
	// Single-band with nodata=0, zero-value BandConfig.
	w, h, spp := 2, 1, 1
	data := []byte{0, 42}

	ifd := &IFD{
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: uint16(spp),
		BitsPerSample:   []uint16{8},
		NoData:          "0",
	}

	r := &Reader{bo: binary.LittleEndian, ifds: []IFD{*ifd}}
	img, err := r.decodeRawTile(ifd, data)
	if err != nil {
		t.Fatal(err)
	}

	rgba := img.(*image.RGBA)
	// pixel0: nodata → transparent.
	assertPixel(t, rgba, 0, 0, color.RGBA{0, 0, 0, 0})
	// pixel1: value 42 → gray, opaque.
	assertPixel(t, rgba, 1, 0, color.RGBA{42, 42, 42, 255})
}

func TestDecodeRawTile16BitBandReorder(t *testing.T) {
	bo := binary.LittleEndian
	// 2x1 tile, 4 spp (RGBNIR), 16-bit.
	w, h, spp := 2, 1, 4
	data := make([]byte, w*h*spp*2)

	// pixel0: R=1000, G=2000, B=3000, NIR=5000
	bo.PutUint16(data[0:2], 1000)
	bo.PutUint16(data[2:4], 2000)
	bo.PutUint16(data[4:6], 3000)
	bo.PutUint16(data[6:8], 5000)
	// pixel1: R=500, G=1500, B=2500, NIR=0 (transparent)
	bo.PutUint16(data[8:10], 500)
	bo.PutUint16(data[10:12], 1500)
	bo.PutUint16(data[12:14], 2500)
	bo.PutUint16(data[14:16], 0)

	ifd := &IFD{
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: uint16(spp),
		BitsPerSample:   []uint16{16, 16, 16, 16},
	}

	r := &Reader{
		bo:   bo,
		ifds: []IFD{*ifd},
		bandCfg: BandConfig{
			Bands:      [3]int{4, 1, 2}, // NIR, R, G → output R, G, B
			AlphaBand:  -1,              // no alpha
			Rescale:    RescaleLinear,
			RescaleMin: 0,
			RescaleMax: 10000,
		},
	}

	img, err := r.decodeRawTile(ifd, data)
	if err != nil {
		t.Fatal(err)
	}

	rgba := img.(*image.RGBA)

	// pixel0: NIR=5000 → R=128, R=1000 → G=26, G=2000 → B=51.
	rescale := buildRescaler(RescaleLinear, 0, 10000)
	wantR := rescale(5000)
	wantG := rescale(1000)
	wantB := rescale(2000)
	got := rgba.RGBAAt(0, 0)
	if got.R != wantR || got.G != wantG || got.B != wantB || got.A != 255 {
		t.Errorf("pixel(0,0) = %v, want R=%d G=%d B=%d A=255", got, wantR, wantG, wantB)
	}

	// pixel1: no alpha band → opaque.
	got1 := rgba.RGBAAt(1, 0)
	if got1.A != 255 {
		t.Errorf("pixel(1,0) alpha = %d, want 255 (no alpha band)", got1.A)
	}
}

func TestDecodeRawTile16BitWithAlphaBand(t *testing.T) {
	bo := binary.LittleEndian
	// 2x1 tile, 4 spp, 16-bit. Alpha band = 4.
	w, h, spp := 2, 1, 4
	data := make([]byte, w*h*spp*2)

	// pixel0: R=1000, G=2000, B=3000, Alpha=8000
	bo.PutUint16(data[0:2], 1000)
	bo.PutUint16(data[2:4], 2000)
	bo.PutUint16(data[4:6], 3000)
	bo.PutUint16(data[6:8], 8000)
	// pixel1: R=500, G=1500, B=2500, Alpha=0 (transparent)
	bo.PutUint16(data[8:10], 500)
	bo.PutUint16(data[10:12], 1500)
	bo.PutUint16(data[12:14], 2500)
	bo.PutUint16(data[14:16], 0)

	ifd := &IFD{
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: uint16(spp),
		BitsPerSample:   []uint16{16, 16, 16, 16},
	}

	r := &Reader{
		bo:   bo,
		ifds: []IFD{*ifd},
		bandCfg: BandConfig{
			Bands:      [3]int{1, 2, 3},
			AlphaBand:  4,
			Rescale:    RescaleLog,
			RescaleMin: 1,
			RescaleMax: 10000,
		},
	}

	img, err := r.decodeRawTile(ifd, data)
	if err != nil {
		t.Fatal(err)
	}

	rgba := img.(*image.RGBA)

	// pixel0: alpha band = 8000 → rescaled to non-zero.
	got0 := rgba.RGBAAt(0, 0)
	if got0.A == 0 {
		t.Error("pixel(0,0) alpha should be non-zero for source alpha=8000")
	}
	if got0.R == 0 && got0.G == 0 && got0.B == 0 {
		t.Error("pixel(0,0) RGB should be non-zero")
	}

	// pixel1: alpha band = 0 → fully transparent.
	got1 := rgba.RGBAAt(1, 0)
	assertPixel(t, rgba, 1, 0, color.RGBA{0, 0, 0, 0})
	_ = got1
}

func TestBuildRescalerLogCurveShape(t *testing.T) {
	fn := buildRescaler(RescaleLog, 0, 10000)

	// Log should give higher values at the low end compared to linear.
	// Check that log(1000) > linear(1000).
	logVal := fn(1000)
	linVal := buildRescaler(RescaleLinear, 0, 10000)(1000)
	if logVal <= linVal {
		t.Errorf("log(1000)=%d should be > linear(1000)=%d for range 0-10000", logVal, linVal)
	}

	// Check monotonicity.
	prev := fn(0)
	for v := uint16(100); v <= 10000; v += 100 {
		cur := fn(v)
		if cur < prev {
			t.Errorf("log not monotonic: log(%d)=%d < log(%d)=%d", v, cur, v-100, prev)
		}
		prev = cur
	}
}

func TestIFDBytesPerSample(t *testing.T) {
	ifd8 := IFD{BitsPerSample: []uint16{8}}
	if got := ifd8.bytesPerSample(); got != 1 {
		t.Errorf("8-bit: got %d, want 1", got)
	}

	ifd16 := IFD{BitsPerSample: []uint16{16, 16, 16, 16}}
	if got := ifd16.bytesPerSample(); got != 2 {
		t.Errorf("16-bit: got %d, want 2", got)
	}

	ifdEmpty := IFD{}
	if got := ifdEmpty.bytesPerSample(); got != 1 {
		t.Errorf("empty BitsPerSample: got %d, want 1", got)
	}
}

func TestParseGDALMetadata(t *testing.T) {
	xmlStr := `<GDALMetadata>
  <Item name="product_type">Sentinel-2 median L2A (RGBNIR) composite</Item>
  <Item name="bands">Band 1: B04 (Red), Band 2: B03 (Green), Band 3: B02 (Blue), Band 4: B08 (Infrared)</Item>
  <Item name="SCALE" sample="0" role="scale">0.000100000000000000005</Item>
  <Item name="description">ESA WorldCover S2 RGBNIR 10m v200</Item>
</GDALMetadata>`

	result := parseGDALMetadataXML(xmlStr)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Dataset-level items (no sample attribute).
	wantItems := map[string]string{
		"product_type": "Sentinel-2 median L2A (RGBNIR) composite",
		"bands":        "Band 1: B04 (Red), Band 2: B03 (Green), Band 3: B02 (Blue), Band 4: B08 (Infrared)",
		"description":  "ESA WorldCover S2 RGBNIR 10m v200",
	}
	for key, want := range wantItems {
		got, ok := result.Items[key]
		if !ok {
			t.Errorf("missing dataset-level key %q", key)
			continue
		}
		if got != want {
			t.Errorf("key %q: got %q, want %q", key, got, want)
		}
	}

	// Per-band items (sample="0").
	band0, ok := result.BandItems[0]
	if !ok {
		t.Fatal("missing band 0 items")
	}
	if got := band0["SCALE"]; got != "0.000100000000000000005" {
		t.Errorf("band 0 SCALE = %q, want %q", got, "0.000100000000000000005")
	}
}

func TestParseGDALMetadataPerBand(t *testing.T) {
	// GDAL-standard per-band DESCRIPTION items (e.g. from GEE exports or gdal_translate).
	xmlStr := `<GDALMetadata>
  <Item name="DESCRIPTION" sample="0" role="description">Red</Item>
  <Item name="DESCRIPTION" sample="1" role="description">Green</Item>
  <Item name="DESCRIPTION" sample="2" role="description">Blue</Item>
  <Item name="DESCRIPTION" sample="3" role="description">NIR</Item>
  <Item name="SCALE" sample="0" role="scale">0.0001</Item>
  <Item name="OFFSET" sample="0" role="offset">0</Item>
</GDALMetadata>`

	result := parseGDALMetadataXML(xmlStr)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Should have no dataset-level items.
	if len(result.Items) != 0 {
		t.Errorf("expected 0 dataset-level items, got %d", len(result.Items))
	}

	// Should have 4 bands.
	if len(result.BandItems) != 4 {
		t.Errorf("expected 4 bands, got %d", len(result.BandItems))
	}

	wantDescs := []string{"Red", "Green", "Blue", "NIR"}
	for i, want := range wantDescs {
		got := result.BandItems[i]["DESCRIPTION"]
		if got != want {
			t.Errorf("band %d DESCRIPTION = %q, want %q", i, got, want)
		}
	}

	if got := result.BandItems[0]["SCALE"]; got != "0.0001" {
		t.Errorf("band 0 SCALE = %q, want %q", got, "0.0001")
	}
}

func TestParseGDALMetadataEmpty(t *testing.T) {
	result := parseGDALMetadataXML("")
	if result != nil {
		t.Errorf("expected nil for empty string")
	}

	result2 := parseGDALMetadataXML("<GDALMetadata></GDALMetadata>")
	if result2 != nil {
		t.Errorf("expected nil for empty root")
	}
}

func TestDetectPresetFromBandsString(t *testing.T) {
	// Dataset-level "bands" item (e.g. ESA WorldCover S2 RGBNIR).
	// No per-band DESCRIPTION — roles extracted from "Band N: BXX (Role)" format.
	r := &Reader{
		bo: binary.LittleEndian,
		ifds: []IFD{{
			BitsPerSample:   []uint16{16, 16, 16, 16},
			SamplesPerPixel: 4,
			GDALMetadata: &GDALMeta{
				Items: map[string]string{
					"bands": "Band 1: B04 (Red), Band 2: B03 (Green), Band 3: B02 (Blue), Band 4: B08 (Infrared)",
				},
				BandItems: map[int]map[string]string{
					0: {"SCALE": "0.000100000000000000005"},
				},
			},
		}},
	}

	preset, ok := r.DetectPreset()
	if !ok {
		t.Fatal("expected preset to be detected from bands string")
	}
	if preset.Name != "multispectral-rgbnir" {
		t.Errorf("name = %q, want %q", preset.Name, "multispectral-rgbnir")
	}
	// Red=1, Green=2, Blue=3 (matching band numbers in the string).
	if preset.BandCfg.Bands != [3]int{1, 2, 3} {
		t.Errorf("bands = %v, want [1,2,3]", preset.BandCfg.Bands)
	}
	if preset.BandCfg.AlphaBand != -1 {
		t.Errorf("alpha band = %d, want -1", preset.BandCfg.AlphaBand)
	}
	if preset.BandCfg.Rescale != RescaleLinear {
		t.Errorf("rescale = %d, want RescaleLinear", preset.BandCfg.Rescale)
	}
	if preset.BandCfg.RescaleMax != 10000 {
		t.Errorf("rescale max = %.0f, want 10000", preset.BandCfg.RescaleMax)
	}
}

func TestDetectPresetFromBandsStringReordered(t *testing.T) {
	// Reordered bands: Blue first.
	r := &Reader{
		bo: binary.LittleEndian,
		ifds: []IFD{{
			BitsPerSample:   []uint16{16, 16, 16, 16},
			SamplesPerPixel: 4,
			GDALMetadata: &GDALMeta{
				Items: map[string]string{
					"bands": "Band 1: B02 (Blue), Band 2: B03 (Green), Band 3: B04 (Red), Band 4: B08 (Infrared)",
				},
				BandItems: map[int]map[string]string{
					0: {"SCALE": "0.0001"},
				},
			},
		}},
	}

	preset, ok := r.DetectPreset()
	if !ok {
		t.Fatal("expected preset to be detected")
	}
	// Red=band3, Green=band2, Blue=band1.
	if preset.BandCfg.Bands != [3]int{3, 2, 1} {
		t.Errorf("bands = %v, want [3,2,1]", preset.BandCfg.Bands)
	}
}

func TestDetectPresetGDALStandardDescriptions(t *testing.T) {
	// GDAL-standard per-band DESCRIPTION items (e.g. GEE export, PlanetScope).
	r := &Reader{
		bo: binary.LittleEndian,
		ifds: []IFD{{
			BitsPerSample:   []uint16{16, 16, 16, 16},
			SamplesPerPixel: 4,
			GDALMetadata: &GDALMeta{
				Items: map[string]string{},
				BandItems: map[int]map[string]string{
					0: {"DESCRIPTION": "Blue", "SCALE": "0.0001"},
					1: {"DESCRIPTION": "Green"},
					2: {"DESCRIPTION": "Red"},
					3: {"DESCRIPTION": "NIR"},
				},
			},
		}},
	}

	preset, ok := r.DetectPreset()
	if !ok {
		t.Fatal("expected preset to be detected from GDAL band descriptions")
	}
	if preset.Name != "multispectral-rgbnir" {
		t.Errorf("name = %q, want %q", preset.Name, "multispectral-rgbnir")
	}
	// Blue=band1(sample0), Green=band2(sample1), Red=band3(sample2), NIR=band4(sample3)
	// So: R=3, G=2, B=1
	if preset.BandCfg.Bands != [3]int{3, 2, 1} {
		t.Errorf("bands = %v, want [3,2,1]", preset.BandCfg.Bands)
	}
	if preset.BandCfg.RescaleMax != 10000 {
		t.Errorf("rescale max = %.0f, want 10000", preset.BandCfg.RescaleMax)
	}
}

func TestDetectPresetGDALStandardRGBOnly(t *testing.T) {
	// 3-band RGB with GDAL descriptions, no NIR.
	r := &Reader{
		bo: binary.LittleEndian,
		ifds: []IFD{{
			BitsPerSample:   []uint16{16, 16, 16},
			SamplesPerPixel: 3,
			GDALMetadata: &GDALMeta{
				Items: map[string]string{},
				BandItems: map[int]map[string]string{
					0: {"DESCRIPTION": "Red", "SCALE": "0.0001"},
					1: {"DESCRIPTION": "Green"},
					2: {"DESCRIPTION": "Blue"},
				},
			},
		}},
	}

	preset, ok := r.DetectPreset()
	if !ok {
		t.Fatal("expected preset")
	}
	if preset.Name != "multispectral-rgb" {
		t.Errorf("name = %q, want %q", preset.Name, "multispectral-rgb")
	}
	if preset.BandCfg.Bands != [3]int{1, 2, 3} {
		t.Errorf("bands = %v, want [1,2,3]", preset.BandCfg.Bands)
	}
}

func TestDetectPresetWithOffset(t *testing.T) {
	// Landsat-style: scale=0.0000275, offset=-0.2 (per-band metadata).
	r := &Reader{
		bo: binary.LittleEndian,
		ifds: []IFD{{
			BitsPerSample:   []uint16{16, 16, 16},
			SamplesPerPixel: 3,
			GDALMetadata: &GDALMeta{
				Items: map[string]string{},
				BandItems: map[int]map[string]string{
					0: {"DESCRIPTION": "Red", "SCALE": "0.0000275", "OFFSET": "-0.2"},
					1: {"DESCRIPTION": "Green"},
					2: {"DESCRIPTION": "Blue"},
				},
			},
		}},
	}

	preset, ok := r.DetectPreset()
	if !ok {
		t.Fatal("expected preset")
	}
	// 1/0.0000275 ≈ 36364, -(-0.2)/0.0000275 ≈ 7273.
	if preset.BandCfg.RescaleMin != 7273 {
		t.Errorf("rescale min = %.0f, want 7273", preset.BandCfg.RescaleMin)
	}
	if preset.BandCfg.RescaleMax != 36364 {
		t.Errorf("rescale max = %.0f, want 36364", preset.BandCfg.RescaleMax)
	}
}

func TestDetectPresetFloatTerrarium(t *testing.T) {
	// Float32 data → terrarium preset.
	r := &Reader{
		bo: binary.LittleEndian,
		ifds: []IFD{{
			BitsPerSample:   []uint16{32},
			SamplesPerPixel: 1,
			SampleFormat:    []uint16{3}, // IEEE floating point
		}},
	}

	preset, ok := r.DetectPreset()
	if !ok {
		t.Fatal("expected preset for float data")
	}
	if preset.Name != "float-terrarium" {
		t.Errorf("name = %q, want %q", preset.Name, "float-terrarium")
	}
	if preset.Format != "terrarium" {
		t.Errorf("format = %q, want %q", preset.Format, "terrarium")
	}
}

func TestDetectPresetNone(t *testing.T) {
	// 8-bit RGB, no GDAL metadata → no preset.
	r := &Reader{
		bo: binary.LittleEndian,
		ifds: []IFD{{
			BitsPerSample:   []uint16{8, 8, 8},
			SamplesPerPixel: 3,
		}},
	}
	_, ok := r.DetectPreset()
	if ok {
		t.Error("expected no preset for 8-bit RGB without GDAL metadata")
	}
}

func TestDetectPresetInsufficientBands(t *testing.T) {
	// GDAL metadata present but only 2 bands described (no Blue).
	r := &Reader{
		bo: binary.LittleEndian,
		ifds: []IFD{{
			BitsPerSample:   []uint16{16, 16},
			SamplesPerPixel: 2,
			GDALMetadata: &GDALMeta{
				Items: map[string]string{},
				BandItems: map[int]map[string]string{
					0: {"DESCRIPTION": "Red"},
					1: {"DESCRIPTION": "Green"},
				},
			},
		}},
	}
	_, ok := r.DetectPreset()
	if ok {
		t.Error("expected no preset when Blue band is missing")
	}
}

func assertPixel(t *testing.T, img *image.RGBA, x, y int, want color.RGBA) {
	t.Helper()
	got := img.RGBAAt(x, y)
	if got != want {
		t.Errorf("pixel(%d,%d) = %v, want %v", x, y, got, want)
	}
}
