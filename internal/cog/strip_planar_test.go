package cog

import (
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
)

// writeStripTIFF writes a minimal uncompressed classic TIFF organised in
// strips (RowsPerStrip=rps). planar=true writes PlanarConfiguration=2 with
// plane-major strips (GDAL INTERLEAVE=BAND); false writes chunky RGB.
// Pixel values come from pixel(x, y, band).
func writeStripTIFF(t *testing.T, path string, width, height, rps int, planar bool, pixel func(x, y, band int) uint8) {
	t.Helper()

	const spp = 3
	bo := binary.LittleEndian
	stripsPerPlane := (height + rps - 1) / rps
	planes := 1
	sampsPerStripPixel := spp // chunky: strip row holds all samples
	if planar {
		planes = spp
		sampsPerStripPixel = 1
	}
	numStrips := stripsPerPlane * planes

	type entry struct {
		tag, dtype uint16
		count      uint32
		value      uint32
		extern     []byte
	}
	var entries []entry
	add := func(tag, dtype uint16, count, value uint32) {
		entries = append(entries, entry{tag: tag, dtype: dtype, count: count, value: value})
	}
	addExtern := func(tag, dtype uint16, count uint32, data []byte) {
		entries = append(entries, entry{tag: tag, dtype: dtype, count: count, extern: data})
	}

	add(256, 3, 1, uint32(width))  // ImageWidth
	add(257, 3, 1, uint32(height)) // ImageLength
	bits := make([]byte, 2*spp)
	for i := 0; i < spp; i++ {
		bo.PutUint16(bits[i*2:], 8)
	}
	addExtern(258, 3, spp, bits) // BitsPerSample
	add(259, 3, 1, 1)            // Compression = None
	add(262, 3, 1, 2)            // Photometric = RGB
	add(277, 3, 1, spp)          // SamplesPerPixel
	add(278, 3, 1, uint32(rps))  // RowsPerStrip
	planarCfg := uint32(1)
	if planar {
		planarCfg = 2
	}
	add(284, 3, 1, planarCfg) // PlanarConfiguration

	stripOffsets := make([]byte, 4*numStrips)
	stripByteCounts := make([]byte, 4*numStrips)
	addExtern(273, 4, uint32(numStrips), stripOffsets)
	addExtern(279, 4, uint32(numStrips), stripByteCounts)

	ifdSize := 2 + len(entries)*12 + 4
	externOffset := uint32(8 + ifdSize)
	for i := range entries {
		if entries[i].extern != nil {
			entries[i].value = externOffset
			externOffset += uint32(len(entries[i].extern))
		}
	}
	dataStart := externOffset

	// Strip data: plane-major when planar, row-major chunky otherwise.
	var stripData []byte
	off := dataStart
	strip := 0
	writeStrip := func(plane, startRow int) {
		rows := rps
		if startRow+rows > height {
			rows = height - startRow
		}
		size := rows * width * sampsPerStripPixel
		bo.PutUint32(stripOffsets[strip*4:], off)
		bo.PutUint32(stripByteCounts[strip*4:], uint32(size))
		for y := startRow; y < startRow+rows; y++ {
			for x := 0; x < width; x++ {
				if planar {
					stripData = append(stripData, pixel(x, y, plane))
				} else {
					for b := 0; b < spp; b++ {
						stripData = append(stripData, pixel(x, y, b))
					}
				}
			}
		}
		off += uint32(size)
		strip++
	}
	for p := 0; p < planes; p++ {
		for s := 0; s < stripsPerPlane; s++ {
			writeStrip(p, s*rps)
		}
	}

	buf := make([]byte, 0, int(dataStart)+len(stripData))
	buf = append(buf, 'I', 'I')
	buf = bo.AppendUint16(buf, 42)
	buf = bo.AppendUint32(buf, 8)
	buf = bo.AppendUint16(buf, uint16(len(entries)))
	for _, e := range entries {
		buf = bo.AppendUint16(buf, e.tag)
		buf = bo.AppendUint16(buf, e.dtype)
		buf = bo.AppendUint32(buf, e.count)
		buf = bo.AppendUint32(buf, e.value)
	}
	buf = bo.AppendUint32(buf, 0) // next IFD
	for _, e := range entries {
		if e.extern != nil {
			buf = append(buf, e.extern...)
		}
	}
	buf = append(buf, stripData...)

	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatal(err)
	}
}

// checkAllPixels reads every virtual tile and verifies each in-image pixel
// matches the generator function.
func checkAllPixels(t *testing.T, path string, width, height int, pixel func(x, y, band int) uint8) {
	t.Helper()

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	th := int(r.ifds[0].TileHeight)
	tilesDown := (height + th - 1) / th
	bad := 0
	for row := 0; row < tilesDown; row++ {
		img, err := r.ReadTile(0, 0, row)
		if err != nil {
			t.Fatalf("ReadTile row %d: %v", row, err)
		}
		rgba, ok := img.(*image.RGBA)
		if !ok {
			t.Fatalf("ReadTile row %d: got %T, want *image.RGBA", row, img)
		}
		for y := 0; y < th && row*th+y < height; y++ {
			for x := 0; x < width; x++ {
				imgY := row*th + y
				o := rgba.PixOffset(x, y)
				got := [3]uint8{rgba.Pix[o], rgba.Pix[o+1], rgba.Pix[o+2]}
				want := [3]uint8{pixel(x, imgY, 0), pixel(x, imgY, 1), pixel(x, imgY, 2)}
				if got != want {
					if bad < 5 {
						t.Errorf("pixel (%d,%d): got %v, want %v", x, imgY, got, want)
					}
					bad++
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d mismatched pixels", bad)
	}
}

func TestStripTIFF(t *testing.T) {
	pixel := func(x, y, band int) uint8 {
		return uint8(x*31 + y*17 + band*73)
	}
	for _, tc := range []struct {
		name   string
		planar bool
		rps    int
	}{
		{"chunky rps=2", false, 2},
		{"chunky rps=1", false, 1},
		{"planar rps=2", true, 2},
		{"planar rps=1", true, 1}, // GDAL INTERLEAVE=BAND default layout
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 300 rows > 256 so strips are grouped into multiple virtual tiles.
			const width, height = 8, 300
			path := filepath.Join(t.TempDir(), fmt.Sprintf("strip_%v_%d.tif", tc.planar, tc.rps))
			writeStripTIFF(t, path, width, height, tc.rps, tc.planar, pixel)
			checkAllPixels(t, path, width, height, pixel)
		})
	}
}
