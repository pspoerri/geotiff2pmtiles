package cog

import (
	"encoding/binary"
	"image/color"
	"testing"
)

// A palette image (land-cover classes, scanned maps) must render its
// ColorMap colours, not its class indices as gray; nodata is an index.
func TestPaletteExpandsColorMap(t *testing.T) {
	const bits, n = 4, 16
	cmap := make([]uint16, 3*n)
	for i, c := range []color.RGBA{{255, 0, 0, 0}, {0, 255, 0, 0}, {0, 0, 128, 0}, {10, 20, 30, 0}} {
		cmap[i], cmap[n+i], cmap[2*n+i] = uint16(c.R)*257, uint16(c.G)*257, uint16(c.B)<<8
	}
	for _, tc := range []struct {
		name string
		cfg  BandConfig
	}{
		{"default", BandConfig{}},
		{"cli", BandConfig{Bands: [3]int{1, 2, 3}}}, // what --bands auto sets
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := oneTileReader(4, 1, bits, 1, "3", pack([]uint16{0, 1, 2, 3}, 4, 1, bits))
			r.ifds[0].Photometric, r.ifds[0].ColorMap = 3, cmap
			r.SetBandConfig(tc.cfg)
			img, err := r.ReadTile(0, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for x, want := range []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 128, 255}, {}} {
				if got := color.RGBAModel.Convert(img.At(x, 0)); got != want {
					t.Errorf("index %d: got %v, want %v", x, got, want)
				}
			}
		})
	}

	r := oneTileReader(4, 1, 8, 1, "", []byte{0, 1, 2, 3})
	r.ifds[0].Photometric = 3 // no ColorMap
	if _, err := r.ReadTile(0, 0, 0); err == nil {
		t.Error("palette without a ColorMap: expected an error")
	}
}

// WhiteIsZero (Photometric 0) renders inverted; nodata and the rescale range
// still refer to the stored values. Only a Photometric tag of 0 means it: the
// field's zero value, in an IFD without the tag, must not.
func TestWhiteIsZeroInverts(t *testing.T) {
	tag := func(v byte) []tiffEntry {
		return []tiffEntry{{Tag: tagPhotometric, DataType: dtShort, Count: 1, Value: []byte{v, 0, 0, 0}}}
	}
	if !buildIFD(tag(0), binary.LittleEndian).whiteIsZero ||
		buildIFD(tag(1), binary.LittleEndian).whiteIsZero || buildIFD(nil, binary.LittleEndian).whiteIsZero {
		t.Error("whiteIsZero must be set by a Photometric tag of 0 only")
	}

	r := oneTileReader(3, 1, 8, 1, "100", []byte{0, 100, 255})
	r.ifds[0].whiteIsZero = true
	if got := grayAlpha(t, r, 0); got != [2]uint8{255, 255} {
		t.Errorf("stored 0: gray, alpha = %v, want white", got)
	}
	if got := grayAlpha(t, r, 1); got[1] != 0 {
		t.Errorf("stored 100 (nodata): alpha = %d, want 0", got[1])
	}
	if got := grayAlpha(t, r, 2); got != [2]uint8{0, 255} {
		t.Errorf("stored 255: gray, alpha = %v, want black", got)
	}

	r = oneTileReader(2, 1, 16, 1, "", []byte{0, 0, 0xE8, 0x03}) // 0, 1000
	r.ifds[0].whiteIsZero = true
	r.SetBandConfig(BandConfig{Rescale: RescaleLinear, RescaleMax: 1000})
	if a, b := grayAlpha(t, r, 0), grayAlpha(t, r, 1); a[0] != 255 || b[0] != 0 {
		t.Errorf("16-bit rescaled: gray %d, %d; want 255, 0", a[0], b[0])
	}
}
