package cog

import (
	"encoding/binary"
	"testing"
)

// Signed-integer DEMs (e.g. GEBCO Int16 bathymetry) must decode through the
// elevation path with negative values intact.
func TestSignedInt16DecodesAsElevation(t *testing.T) {
	ifd := IFD{
		TileWidth: 2, TileHeight: 1, SamplesPerPixel: 1,
		BitsPerSample: []uint16{16}, SampleFormat: []uint16{2},
	}
	r := &Reader{ifds: []IFD{ifd}, bo: binary.LittleEndian}
	if !r.IsFloat() {
		t.Fatal("IsFloat() = false for signed int16, want true (elevation path)")
	}

	data := make([]byte, 4)
	depth := int16(-9536)
	binary.LittleEndian.PutUint16(data[0:], uint16(depth))
	binary.LittleEndian.PutUint16(data[2:], 8627)
	got, _, _, err := r.decodeRawFloat32Tile(&r.ifds[0], data)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != -9536 || got[1] != 8627 {
		t.Fatalf("got %v, want [-9536 8627]", got)
	}
}

// With a non-terrarium format, signed int16 goes through the RGB rescale path:
// the range, the samples and nodata must all be interpreted as signed.
func TestSignedInt16RGBRescale(t *testing.T) {
	ifd := IFD{
		TileWidth: 4, TileHeight: 1, SamplesPerPixel: 1,
		BitsPerSample: []uint16{16}, SampleFormat: []uint16{2}, NoData: "-32767",
	}
	r := &Reader{ifds: []IFD{ifd}, bo: binary.LittleEndian}
	r.SetBandConfig(BandConfig{Rescale: RescaleLinear, RescaleMin: -10000, RescaleMax: 10000})

	vals := []int16{-10000, 2000, 10000, -32767}
	data := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(v))
	}
	img, err := r.decodeRawTile(&r.ifds[0], data)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]uint32{{0, 255}, {153, 255}, {255, 255}, {0, 0}} // gray, alpha
	for x, w := range want {
		cr, cg, cb, ca := img.At(x, 0).RGBA()
		if cg != cr || cb != cr {
			t.Errorf("pixel %d: single-band must render gray, got r=%d g=%d b=%d", x, cr>>8, cg>>8, cb>>8)
		}
		if cr>>8 != w[0] || ca>>8 != w[1] {
			t.Errorf("pixel %d (%d m): gray=%d alpha=%d, want gray=%d alpha=%d", x, vals[x], cr>>8, ca>>8, w[0], w[1])
		}
	}

	// Without a rescale, the fallback is the full range of the sample type,
	// signed or not; it was 0..65535 in biased space, which made every
	// value <= 0 black. 15 bits unsigned now reach white rather than 127.
	for _, tc := range []struct {
		bits, format int
		vals         []int
		want         []uint8
	}{
		{16, 2, []int{-5000, 0, 1000, 8000}, []uint8{108, 128, 131, 159}},
		{15, 1, []int{0, 16384, 32767}, []uint8{0, 128, 255}},
	} {
		raw := make([]uint16, len(tc.vals))
		for i, v := range tc.vals {
			raw[i] = uint16(v)
		}
		data := make([]byte, 2*len(raw))
		for i, v := range raw {
			binary.LittleEndian.PutUint16(data[2*i:], v)
		}
		if tc.bits == 15 {
			data = pack(raw, len(raw), 1, 15)
		}
		r := oneTileReader(len(raw), 1, tc.bits, tc.format, "", data)
		for x, w := range tc.want {
			if got := grayAlpha(t, r, x); got != [2]uint8{w, 255} {
				t.Errorf("%d-bit %d without rescale: gray %d, want %d", tc.bits, tc.vals[x], got[0], w)
			}
		}
	}

	samples := make([]uint16, len(vals))
	for i, v := range vals {
		samples[i] = uint16(v)
	}
	lo, hi, ok := minMaxSamples(samples, 1, 4, 4, 1, signBias16, uint16(int32(-32767)+signBias16), true)
	if !ok || int(lo)-signBias16 != -10000 || int(hi)-signBias16 != 10000 {
		t.Errorf("signed scan: got [%d, %d], want [-10000, 10000]", int(lo)-signBias16, int(hi)-signBias16)
	}
}
