package cog

import (
	"encoding/binary"
	"image"
	"math"
	"testing"
)

// stripReader builds an in-memory single-band strip reader with one row per
// strip, holding each strip's bytes as given; a nil strip is sparse (offset
// and byte count 0, as GDAL writes with SPARSE_OK).
func stripReader(t *testing.T, w, bits, format, compression int, nodata string, strips [][]byte) *Reader {
	t.Helper()
	var buf []byte
	offs := make([]uint64, len(strips))
	counts := make([]uint64, len(strips))
	for y, s := range strips {
		if s != nil {
			offs[y], counts[y] = uint64(len(buf)), uint64(len(s))
			buf = append(buf, s...)
		}
	}
	ifd := IFD{Width: uint32(w), Height: uint32(len(strips)), SamplesPerPixel: 1,
		BitsPerSample: []uint16{uint16(bits)}, SampleFormat: []uint16{uint16(format)},
		Compression: uint16(compression), PlanarConfig: 1, RowsPerStrip: 1,
		StripOffsets: offs, StripByteCounts: counts, NoData: nodata}
	sl, err := promoteStripsToTiles(&ifd)
	if err != nil {
		t.Fatal(err)
	}
	return &Reader{src: mmapSource(buf), bo: binary.LittleEndian, ifds: []IFD{ifd}, strip: sl}
}

// row repeats one sample value w times, little-endian at the given depth.
func row(w, bits int, v float64) []byte {
	out := make([]byte, w*bits/8)
	for x := 0; x < w; x++ {
		switch bits {
		case 8:
			out[x] = uint8(v)
		case 16:
			binary.LittleEndian.PutUint16(out[x*2:], uint16(v))
		case 32:
			binary.LittleEndian.PutUint32(out[x*4:], math.Float32bits(float32(v)))
		}
	}
	return out
}

// A sparse strip must keep its rows in place and read as nodata. Skipping it
// moved every later strip up a row and left the bottom row zero (0 m in the
// float path).
func TestSparseStripKeepsRows(t *testing.T) {
	const w = 4
	t.Run("rgb", func(t *testing.T) {
		r := stripReader(t, w, 8, 1, 1, "", [][]byte{row(w, 8, 10), nil, row(w, 8, 30), row(w, 8, 40)})
		img, err := r.ReadTile(0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		rgba := img.(*image.RGBA)
		for y, want := range [][2]uint8{{10, 255}, {0, 0}, {30, 255}, {40, 255}} {
			if got := [2]uint8{rgba.Pix[rgba.PixOffset(1, y)], rgba.Pix[rgba.PixOffset(1, y)+3]}; got != want {
				t.Errorf("row %d: gray, alpha = %v, want %v", y, got, want)
			}
		}
	})
	t.Run("float", func(t *testing.T) {
		r := stripReader(t, w, 32, 3, 1, "", [][]byte{row(w, 32, 100), nil, row(w, 32, 300), row(w, 32, 400)})
		px, _, _, err := r.ReadFloatTile(0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if px[0] != 100 || !math.IsNaN(float64(px[w])) || px[2*w] != 300 || px[3*w] != 400 {
			t.Errorf("rows = %v %v %v %v, want 100 NaN 300 400", px[0], px[w], px[2*w], px[3*w])
		}
		if !math.IsNaN(float64(px[4*w])) {
			t.Errorf("row past the image = %v, want NaN", px[4*w])
		}
	})
	t.Run("uint16 nodata", func(t *testing.T) {
		r := stripReader(t, w, 16, 1, 1, "7", [][]byte{row(w, 16, 1000), nil, row(w, 16, 3000), row(w, 16, 4000)})
		s, _, _, _, err := r.ReadUint16Tile(0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := []uint16{s[0], s[w], s[2*w], s[3*w]}; got[0] != 1000 || got[1] != 7 || got[2] != 3000 || got[3] != 4000 {
			t.Errorf("rows = %v, want [1000 7 3000 4000]", got)
		}
	})
	t.Run("all sparse", func(t *testing.T) {
		r := stripReader(t, w, 32, 3, 1, "", [][]byte{nil, nil})
		if px, _, _, err := r.ReadFloatTile(0, 0, 0); err != nil || px != nil {
			t.Errorf("got %v, %v; want an empty tile", px, err)
		}
	})
}

// lzwLiterals encodes vals as a TIFF LZW stream of 9-bit literal codes after
// a Clear code, ending with EOI unless truncated.
func lzwLiterals(vals []byte, truncated bool) []byte {
	codes := []int{lzwClearCode}
	for _, v := range vals {
		codes = append(codes, int(v))
	}
	if !truncated {
		codes = append(codes, lzwEOICode)
	}
	out := make([]byte, (len(codes)*9+7)/8)
	for i, c := range codes {
		for b := 0; b < 9; b++ {
			if c&(1<<(8-b)) != 0 {
				bit := i*9 + b
				out[bit/8] |= 0x80 >> (bit % 8)
			}
		}
	}
	return out
}

// A strip that decodes short -- truncated LZW decodes without error up to
// the damage -- must fail the read, not shift every later strip up.
func TestShortStripIsAnError(t *testing.T) {
	const w = 4
	full := row(w, 8, 30)
	for _, tc := range []struct {
		name        string
		compression int
		strips      [][]byte
	}{
		{"uncompressed", 1, [][]byte{full, full[:2], full, full}},
		{"lzw truncated", 5, [][]byte{lzwLiterals(full, false), lzwLiterals(full[:2], true), lzwLiterals(full, false)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := stripReader(t, w, 8, 1, tc.compression, "", tc.strips)
			if _, err := r.ReadTile(0, 0, 0); err == nil {
				t.Error("ReadTile: expected an error")
			}
		})
	}

	// The same streams, complete, decode.
	r := stripReader(t, w, 8, 1, 5, "", [][]byte{lzwLiterals(full, false), lzwLiterals(full, false)})
	img, err := r.ReadTile(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if g := img.(*image.RGBA).Pix[img.(*image.RGBA).PixOffset(3, 1)]; g != 30 {
		t.Errorf("pixel (3,1) = %d, want 30", g)
	}
}

// A tile is never legitimately short: truncated LZW rendered a transparent
// tail with no error in the RGB path (the float path already failed).
func TestShortTileIsAnError(t *testing.T) {
	r := oneTileReader(4, 2, 8, 1, "", lzwLiterals([]byte{1, 2, 3, 4, 5}, true))
	r.ifds[0].Compression = 5
	if _, err := r.ReadTile(0, 0, 0); err == nil {
		t.Error("ReadTile of a truncated LZW tile: expected an error")
	}
}
