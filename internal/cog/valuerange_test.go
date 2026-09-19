package cog

import (
	"encoding/binary"
	"testing"
)

func TestValueRangeFromGDALStatistics(t *testing.T) {
	md := &GDALMeta{BandItems: map[int]map[string]string{
		0: {"STATISTICS_MINIMUM": "120", "STATISTICS_MAXIMUM": "4000"},
		1: {"STATISTICS_MINIMUM": "80.5", "STATISTICS_MAXIMUM": "3500"},
	}}
	r := &Reader{ifds: []IFD{{BitsPerSample: []uint16{16}, GDALMetadata: md}}}
	lo, hi, src, err := r.ValueRange()
	if err != nil {
		t.Fatal(err)
	}
	if lo != 80.5 || hi != 4000 || src != "GDAL statistics" {
		t.Fatalf("got [%v, %v] from %q, want [80.5, 4000] from GDAL statistics", lo, hi, src)
	}
}

// The scan must ignore nodata and the padding of edge tiles.
func TestMinMaxUint16SkipsNodataAndPadding(t *testing.T) {
	const tw, spp = 4, 1
	vals := []uint16{
		500, 900, 0, 0, // validW=2: last two columns are padding
		65535, 700, 0, 0, // 65535 is nodata
		0, 0, 0, 0, // validH=2: padding row
	}
	data := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	lo, hi, ok := minMaxUint16(data, binary.LittleEndian, spp, tw, 2, 2, 65535, true)
	if !ok || lo != 500 || hi != 900 {
		t.Fatalf("got [%d, %d] ok=%v, want [500, 900]", lo, hi, ok)
	}
}
