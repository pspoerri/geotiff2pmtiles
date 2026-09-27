package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

// writeFloatTIFF writes a 4x4 single-strip Float32 GeoTIFF in EPSG:4326
// with GDAL_NODATA -9999 and returns the opened reader.
func writeFloatTIFF(t *testing.T, px []float32) *cog.Reader {
	t.Helper()
	bo := binary.LittleEndian
	type entry struct {
		tag, typ     uint16
		count, value uint32
		data         []byte
	}
	doubles := func(v ...float64) (b []byte) {
		for _, f := range v {
			b = bo.AppendUint64(b, math.Float64bits(f))
		}
		return b
	}
	var keys, pixels []byte
	for _, k := range []uint16{1, 1, 0, 2, 1024, 0, 1, 2, 2048, 0, 1, 4326} {
		keys = bo.AppendUint16(keys, k)
	}
	for _, v := range px {
		pixels = bo.AppendUint32(pixels, math.Float32bits(v))
	}
	entries := []entry{
		{256, 4, 1, 4, nil},
		{257, 4, 1, 4, nil},
		{258, 3, 1, 32, nil},
		{259, 3, 1, 1, nil},
		{262, 3, 1, 1, nil},
		{273, 4, 1, 0, nil}, // StripOffsets, set below
		{277, 3, 1, 1, nil},
		{278, 4, 1, 4, nil},
		{279, 4, 1, uint32(len(pixels)), nil},
		{339, 3, 1, 3, nil}, // SampleFormat: IEEE float
		{33550, 12, 3, 0, doubles(0.1, 0.1, 0)},
		{33922, 12, 6, 0, doubles(0, 0, 0, 8, 47, 0)},
		{34735, 3, 12, 0, keys},
		{42113, 2, 6, 0, []byte("-9999\x00")},
	}
	off := uint32(8 + 2 + len(entries)*12 + 4)
	for i := range entries {
		if entries[i].data != nil {
			entries[i].value = off
			off += uint32(len(entries[i].data))
		}
	}
	entries[5].value = off
	buf := bo.AppendUint32(bo.AppendUint16([]byte{'I', 'I'}, 42), 8)
	buf = bo.AppendUint16(buf, uint16(len(entries)))
	for _, e := range entries {
		buf = bo.AppendUint32(bo.AppendUint32(bo.AppendUint16(bo.AppendUint16(buf, e.tag), e.typ), e.count), e.value)
	}
	buf = bo.AppendUint32(buf, 0)
	for _, e := range entries {
		buf = append(buf, e.data...)
	}
	buf = append(buf, pixels...)

	path := filepath.Join(t.TempDir(), "float.tif")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := cog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// -raw carries what cmd/debug printed: the IFD tags, nodata, the first
// tile's bytes and the float range.
func TestPrintRaw(t *testing.T) {
	px := make([]float32, 16)
	for i := range px {
		px[i] = float32(i) * 10
	}
	px[3] = float32(math.NaN())
	r := writeFloatTIFF(t, px)

	var out bytes.Buffer
	printRaw(&out, r)
	for _, want := range []string{
		"Compression: 1, SamplesPerPixel: 1, BitsPerSample: [32], SampleFormat: [3], Predictor: 0",
		"IsFloat (float or signed): true",
		`NoData: "-9999"`,
		"First 20 bytes: 00 00 00 00 00 00 20 41", // 0.0, 10.0
		"Float tile (0,0): ",
		"range [0.00, 150.00]", // NaN skipped
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
