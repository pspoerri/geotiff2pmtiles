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

	lo, hi, ok := minMaxUint16(data, binary.LittleEndian, 1, 4, 4, 1, signBias16, uint16(int32(-32767)+signBias16), true)
	if !ok || int(lo)-signBias16 != -10000 || int(hi)-signBias16 != 10000 {
		t.Errorf("signed scan: got [%d, %d], want [-10000, 10000]", int(lo)-signBias16, int(hi)-signBias16)
	}
}
