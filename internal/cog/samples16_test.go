package cog

import (
	"encoding/binary"
	"image"
	"testing"
)

// oneTileReader builds an in-memory, uncompressed, single-band reader of one
// w×h tile holding data as given.
func oneTileReader(w, h, bits, format int, nodata string, data []byte) *Reader {
	ifd := IFD{Width: uint32(w), Height: uint32(h), TileWidth: uint32(w), TileHeight: uint32(h),
		SamplesPerPixel: 1, BitsPerSample: []uint16{uint16(bits)}, SampleFormat: []uint16{uint16(format)},
		Compression: 1, PlanarConfig: 1, NoData: nodata,
		TileOffsets: []uint64{0}, TileByteCounts: []uint64{uint64(len(data))}}
	return &Reader{src: mmapSource(data), bo: binary.LittleEndian, ifds: []IFD{ifd}}
}

// grayAlpha returns the gray value and alpha of pixel x in row 0.
func grayAlpha(t *testing.T, r *Reader, x int) [2]uint8 {
	t.Helper()
	img, err := r.ReadTile(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	rgba := img.(*image.RGBA)
	o := rgba.PixOffset(x, 0)
	return [2]uint8{rgba.Pix[o], rgba.Pix[o+3]}
}

// Signed samples narrower than 16 bits must be sign-extended before the sign
// bias, in the one decode all three readers share: ReadUint16Tile (int16(v)
// is the value), decodeRawTile (rescale and nodata) and the value-range scan.
// XOR-ing the raw 15-bit two's complement sorted -2 above 1.
func TestSignedPackedSamples(t *testing.T) {
	vals := []int16{-2, -1, 0, 1}
	raw := make([]uint16, 16)
	for i := range raw {
		raw[i] = uint16(vals[i%4]) & 0x7FFF
	}
	r := oneTileReader(16, 1, 15, 2, "-1", pack(raw, 16, 1, 15))

	s, _, _, _, err := r.ReadUint16Tile(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if int16(s[i]) != v {
			t.Errorf("ReadUint16Tile sample %d = %d, want %d", i, int16(s[i]), v)
		}
	}

	r.SetBandConfig(BandConfig{Rescale: RescaleLinear, RescaleMin: -2, RescaleMax: 1})
	for x, want := range [][2]uint8{{0, 255}, {0, 0}, {170, 255}, {255, 255}} {
		if got := grayAlpha(t, r, x); got != want {
			t.Errorf("pixel %d (%d): gray, alpha = %v, want %v", x, vals[x], got, want)
		}
	}

	if lo, hi, err := r.scanRange(BandConfig{}); err != nil || lo != -2 || hi != 1 {
		t.Errorf("scanRange = [%v, %v], %v; want [-2, 1]", lo, hi, err)
	}

	// Int8 too: int16(v) is the value at every depth.
	r = oneTileReader(3, 1, 8, 2, "", []byte{0xFF, 0x80, 0x7F})
	if s, _, _, _, err = r.ReadUint16Tile(0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := [3]int16{int16(s[0]), int16(s[1]), int16(s[2])}; got != [3]int16{-1, -128, 127} {
		t.Errorf("Int8 samples = %v, want [-1 -128 127]", got)
	}
}

// ReadUint16Tile and decodeRawTile must take the same packed-or-padded
// decision. A 12-bit tile padded to 16-bit words read correctly through the
// one and as bit-packed garbage through the other.
func TestPaddedSamplesAgree(t *testing.T) {
	const w, h = 16, 2 // 64 bytes padded; 48 would be packed
	data := make([]byte, 2*w*h)
	for i := 0; i < w*h; i++ {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(i*130))
	}
	r := oneTileReader(w, h, 12, 1, "", data)
	s, _, _, _, err := r.ReadUint16Tile(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.SetBandConfig(BandConfig{Rescale: RescaleLinear, RescaleMax: 4095})
	for x := 0; x < w; x++ {
		if s[x] != uint16(x*130) {
			t.Fatalf("ReadUint16Tile sample %d = %d, want %d", x, s[x], x*130)
		}
		want := uint8((x*130*255 + 4095/2) / 4095)
		if got := grayAlpha(t, r, x); got != [2]uint8{want, 255} {
			t.Errorf("decodeRawTile pixel %d = %v, want gray %d", x, got, want)
		}
	}
}

// ReadUint16Tile must reject a format it cannot read even when the tile is
// empty; otherwise a region over sparse tiles returned zeros with no error,
// and one over data an error, depending on sparsity alone.
func TestReadUint16TileRejectsFormatOfEmptyTile(t *testing.T) {
	r := oneTileReader(4, 4, 32, 3, "", nil) // float32, byte count 0
	if _, _, _, _, err := r.ReadUint16Tile(0, 0, 0); err == nil {
		t.Error("ReadUint16Tile of an empty float32 tile: expected an error")
	}
}

// A depth that is neither whole bytes nor at most 16 bits cannot be decoded;
// it must say so rather than render black.
func TestDecodeRawTileUnsupportedDepth(t *testing.T) {
	r := oneTileReader(16, 1, 20, 1, "", make([]byte, 40))
	if _, err := r.ReadTile(0, 0, 0); err == nil {
		t.Error("ReadTile of a 20-bit tile: expected an error")
	}
}
