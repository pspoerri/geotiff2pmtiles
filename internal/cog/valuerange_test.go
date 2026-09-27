package cog

import (
	"encoding/binary"
	"errors"
	"slices"
	"testing"
)

// memTIFF describes an in-memory, uncompressed, chunky tiled raster.
type memTIFF struct {
	w, h, tw, th, spp int
	bits              int  // 8 or 16
	signed            bool // SampleFormat 2
	nodata            string
	// sample returns the stored (raw) value of sample s at (x, y).
	sample func(x, y, s int) uint16
}

func (m memTIFF) reader() *Reader {
	across, down := (m.w+m.tw-1)/m.tw, (m.h+m.th-1)/m.th
	bps := m.bits / 8
	var data []byte
	var offsets, counts []uint64
	for row := 0; row < down; row++ {
		for col := 0; col < across; col++ {
			buf := make([]byte, m.tw*m.th*m.spp*bps)
			for y := 0; y < m.th; y++ {
				for x := 0; x < m.tw; x++ {
					sx, sy := col*m.tw+x, row*m.th+y
					if sx >= m.w || sy >= m.h {
						continue
					}
					for s := 0; s < m.spp; s++ {
						i := ((y*m.tw+x)*m.spp + s) * bps
						if v := m.sample(sx, sy, s); bps == 2 {
							binary.LittleEndian.PutUint16(buf[i:], v)
						} else {
							buf[i] = uint8(v)
						}
					}
				}
			}
			offsets = append(offsets, uint64(len(data)))
			counts = append(counts, uint64(len(buf)))
			data = append(data, buf...)
		}
	}
	format := uint16(1)
	if m.signed {
		format = 2
	}
	ifd := IFD{
		Width: uint32(m.w), Height: uint32(m.h),
		TileWidth: uint32(m.tw), TileHeight: uint32(m.th),
		SamplesPerPixel: uint16(m.spp),
		BitsPerSample:   slices.Repeat([]uint16{uint16(m.bits)}, m.spp),
		SampleFormat:    slices.Repeat([]uint16{format}, m.spp),
		Compression:     1,
		PlanarConfig:    1,
		TileOffsets:     offsets,
		TileByteCounts:  counts,
		NoData:          m.nodata,
	}
	return &Reader{bo: binary.LittleEndian, ifds: []IFD{ifd}, src: mmapSource(data)}
}

// Only the bands that are rendered count: not bands left out by --bands, and
// never the alpha band.
func TestValueRangeFromGDALStatistics(t *testing.T) {
	stats := func(lo, hi string) map[string]string {
		return map[string]string{"STATISTICS_MINIMUM": lo, "STATISTICS_MAXIMUM": hi}
	}
	md := &GDALMeta{BandItems: map[int]map[string]string{
		0: stats("120", "4000"),
		1: stats("80.5", "3500"),
		2: stats("100", "3000"),
		3: stats("0", "65535"), // NIR or alpha
	}}
	r := &Reader{ifds: []IFD{{SamplesPerPixel: 4, BitsPerSample: []uint16{16}, GDALMetadata: md}}}
	for _, tc := range []struct {
		name   string
		cfg    BandConfig
		lo, hi float64
	}{
		{"default bands", BandConfig{}, 80.5, 4000},
		{"selected bands", BandConfig{Bands: [3]int{3, 2, 2}}, 80.5, 3500},
		{"alpha band excluded", BandConfig{Bands: [3]int{1, 2, 4}, AlphaBand: 4}, 80.5, 4000},
		{"unselected band 4", BandConfig{Bands: [3]int{3, 3, 3}}, 100, 3000},
	} {
		lo, hi, src, err := r.ValueRange(tc.cfg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if lo != tc.lo || hi != tc.hi || src != "GDAL statistics" {
			t.Errorf("%s: got [%v, %v] from %q, want [%v, %v] from GDAL statistics", tc.name, lo, hi, src, tc.lo, tc.hi)
		}
	}
}

// A rendered band without statistics must not be clipped to the others'
// range: the pixels are scanned instead.
func TestValueRangePartialStatisticsScans(t *testing.T) {
	r := memTIFF{w: 4, h: 4, tw: 4, th: 4, spp: 3, bits: 16,
		sample: func(x, y, s int) uint16 { return uint16(1000*(s+1) + x) }}.reader()
	r.ifds[0].GDALMetadata = &GDALMeta{BandItems: map[int]map[string]string{
		0: {"STATISTICS_MINIMUM": "1000", "STATISTICS_MAXIMUM": "1003"},
	}}
	lo, hi, src, err := r.ValueRange(BandConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if lo != 1000 || hi != 3003 || src != "sampled pixels" {
		t.Errorf("got [%v, %v] from %q, want [1000, 3003] from sampled pixels", lo, hi, src)
	}
}

// The scan reads the same bands: a bright NIR band or an alpha band of
// 0/65535 must not stretch the range of the rendered RGB.
func TestValueRangeScanRenderedBands(t *testing.T) {
	// Bands B, G, R, then NIR / alpha.
	base := [4]uint16{500, 700, 900, 6000}
	m := memTIFF{w: 16, h: 16, tw: 8, th: 8, spp: 4, bits: 16, nodata: "0",
		sample: func(x, y, s int) uint16 { return base[s] + uint16(x) }}
	alpha := m
	alpha.sample = func(x, y, s int) uint16 {
		if s == 3 {
			return uint16(65535 * (x & 1))
		}
		return base[s] + uint16(x)
	}
	for _, tc := range []struct {
		name   string
		m      memTIFF
		cfg    BandConfig
		lo, hi float64
	}{
		{"bands 3,2,1 skip NIR", m, BandConfig{Bands: [3]int{3, 2, 1}}, 500, 915},
		{"default bands skip band 4", m, BandConfig{}, 500, 915},
		{"alpha band excluded", alpha, BandConfig{Bands: [3]int{1, 2, 4}, AlphaBand: 4}, 500, 715},
		{"single band", m, BandConfig{Bands: [3]int{4, 4, 4}}, 6000, 6015},
	} {
		lo, hi, src, err := tc.m.reader().ValueRange(tc.cfg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if lo != tc.lo || hi != tc.hi || src != "sampled pixels" {
			t.Errorf("%s: got [%v, %v] from %q, want [%v, %v] from sampled pixels", tc.name, lo, hi, src, tc.lo, tc.hi)
		}
	}
}

// 64×64 tiles gave a stride of 64 through the row-major tile index, so only
// tile column 0 was ever read. With that column all nodata, auto-rescale
// failed outright.
func TestValueRangeScanSpreadsOverColumns(t *testing.T) {
	r := memTIFF{w: 256, h: 256, tw: 4, th: 4, spp: 1, bits: 16, nodata: "0",
		sample: func(x, y, s int) uint16 {
			if x < 4 {
				return 0 // column 0 is nodata
			}
			return uint16(1000 + x)
		}}.reader()
	lo, hi, _, err := r.ValueRange(BandConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if lo > 1000+64 || hi < 1000+192 {
		t.Errorf("got [%v, %v]: the sampled tiles do not span the width", lo, hi)
	}
}

// A raster that is nodata throughout reports ErrNoValueRange, so a caller
// merging several sources can skip it.
func TestValueRangeAllNodata(t *testing.T) {
	r := memTIFF{w: 8, h: 8, tw: 4, th: 4, spp: 1, bits: 16, nodata: "0",
		sample: func(x, y, s int) uint16 { return 0 }}.reader()
	if _, _, _, err := r.ValueRange(BandConfig{}); !errors.Is(err, ErrNoValueRange) {
		t.Fatalf("err = %v, want ErrNoValueRange", err)
	}
}

// The scan grid must cover both axes and use the tile budget, whatever the
// shape of the coarsest level.
func TestScanGrid(t *testing.T) {
	for _, tc := range []struct{ across, down, cols, rows int }{
		{64, 64, 8, 8},
		{128, 1, 64, 1},
		{1, 128, 1, 64},
		{10, 100, 8, 8},
		{3, 100, 3, 21},
		{9, 9, 8, 8},
		{8, 8, 8, 8}, // few enough to scan every tile
		{7, 9, 7, 9},
		{0, 5, 0, 0},
	} {
		cols, rows := scanGrid(tc.across, tc.down)
		if len(cols) != tc.cols || len(rows) != tc.rows {
			t.Errorf("%dx%d: %d cols x %d rows, want %d x %d", tc.across, tc.down, len(cols), len(rows), tc.cols, tc.rows)
		}
		for _, axis := range []struct {
			idx []int
			n   int
		}{{cols, tc.across}, {rows, tc.down}} {
			for i, v := range axis.idx {
				if v < 0 || v >= axis.n || (i > 0 && v <= axis.idx[i-1]) {
					t.Errorf("%dx%d: indices %v not distinct and in [0, %d)", tc.across, tc.down, axis.idx, axis.n)
					break
				}
			}
		}
		if n := len(cols) * len(rows); n > maxRangeScanTiles {
			t.Errorf("%dx%d: %d tiles exceed the budget", tc.across, tc.down, n)
		}
	}
}

// The scan must ignore nodata and the padding of edge tiles.
func TestMinMaxSamplesSkipsNodataAndPadding(t *testing.T) {
	const tw, spp = 4, 2
	vals := []uint16{
		500, 1, 900, 1, 0, 0, 0, 0, // validW=2: last two pixels are padding
		65535, 1, 700, 60000, 0, 0, 0, 0, // 65535 is nodata; band 1 is not scanned
		0, 0, 0, 0, 0, 0, 0, 0, // validH=2: padding row
	}
	lo, hi, ok := minMaxSamples(vals, spp, []int{0}, tw, 2, 2, 0, 65535, true)
	if !ok || lo != 500 || hi != 900 {
		t.Fatalf("got [%d, %d] ok=%v, want [500, 900]", lo, hi, ok)
	}
}
