package cog

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
)

// maskTile returns a 16x16 1-bit mask tile, rows packed MSB first, with
// valid(x, y) set for the pixels to keep.
func maskTile(valid func(x, y int) bool) []byte {
	b := make([]byte, 16*2)
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if valid(x, y) {
				b[y*2+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	return b
}

// maskEntries returns the entries of a 1-bit GDAL internal mask with 16x16
// tiles, as GDAL writes it: NewSubfileType 4 (5 for an overview's mask)
// and Photometric 4.
func maskEntries(w int, subfile uint64, offs, counts []uint64) []tagEntry {
	return imageEntries(w, w, 16, 16, 1, offs, counts,
		entry(tagNewSubfileType, dtLong, subfile), entry(tagPhotometric, dtShort, 4))
}

// alphaGrid returns '#' for each opaque and '.' for each transparent pixel
// of every 4th row and column of img.
func alphaGrid(img image.Image) string {
	rgba := toRGBA(img)
	var s []byte
	for y := 0; y < rgba.Rect.Dy(); y += 4 {
		for x := 0; x < rgba.Rect.Dx(); x += 4 {
			if rgba.Pix[rgba.PixOffset(x, y)+3] != 0 {
				s = append(s, '#')
			} else {
				s = append(s, '.')
			}
		}
		s = append(s, '/')
	}
	return string(s)
}

// A GDAL internal mask makes the pixels it marks invalid transparent, so
// that later sources show through them. Masks are matched to levels by size
// (GDAL writes the image, its mask, the overviews, then their masks, or
// interleaves them), and a missing (sparse) mask tile is all invalid, as
// GDAL reads it.
func TestInternalMaskAppliedAsAlpha(t *testing.T) {
	gray := bytes.Repeat([]byte{200}, 16*16)
	blobs := [][]byte{
		gray, // image tiles (all four share it) and the overview
		maskTile(func(x, y int) bool { return x < 8 }), // level 0 tile (0,0): left half
		maskTile(func(x, y int) bool { return true }),  // level 0 tile (1,0): all valid
		make([]byte, 32), // level 0 tile (1,1): none valid
		maskTile(func(x, y int) bool { return y < 8 }), // overview: top half
	}
	build := func(overviewMask bool) []byte {
		return buildTIFF(false, blobs, func(o []uint64) [][]tagEntry {
			img, m00, m10, m11, ov := o[0], o[1], o[2], o[3], o[4]
			ifds := [][]tagEntry{
				imageEntries(32, 32, 16, 16, 8, []uint64{img, img, img, img}, []uint64{256, 256, 256, 256}),
				imageEntries(16, 16, 16, 16, 8, []uint64{img}, []uint64{256}, entry(tagNewSubfileType, dtLong, 1)),
				// Tile (0,1) of the mask is sparse.
				maskEntries(32, 4, []uint64{m00, m10, 0, m11}, []uint64{32, 32, 0, 32}),
			}
			if overviewMask {
				ifds = append(ifds, maskEntries(16, 5, []uint64{ov}, []uint64{32}))
			}
			return ifds
		})
	}

	r, err := openCrafted(t, "masked.tif", build(true))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.IFDCount(); got != 2 {
		t.Fatalf("IFDCount() = %d, want 2", got)
	}
	for _, tt := range []struct {
		level, col, row int
		want            string
	}{
		{0, 0, 0, "##../##../##../##../"},
		{0, 1, 0, "####/####/####/####/"},
		{0, 0, 1, "..../..../..../..../"},
		{0, 1, 1, "..../..../..../..../"},
		{1, 0, 0, "####/####/..../..../"},
	} {
		img, err := r.ReadTile(tt.level, tt.col, tt.row)
		if err != nil {
			t.Fatal(err)
		}
		if got := alphaGrid(img); got != tt.want {
			t.Errorf("level %d tile (%d,%d): alpha %s, want %s", tt.level, tt.col, tt.row, got, tt.want)
		}
	}
	if img, _ := r.ReadTile(0, 0, 0); toRGBA(img).Pix[0] != 200 {
		t.Errorf("valid pixel changed: %v", toRGBA(img).Pix[:4])
	}

	// An overview without a mask would read the masked area as opaque at
	// the zooms it serves; it is dropped, so level 0 serves them instead.
	r2, err := openCrafted(t, "masked-no-overview-mask.tif", build(false))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if got := r2.IFDCount(); got != 1 {
		t.Errorf("without an overview mask: IFDCount() = %d, want 1", got)
	}
}

// toRGBA takes draw's fast path for YCbCr tiles, which masked JPEG COGs send
// through it on every read; it must give the bytes At does.
func TestToRGBAYCbCrMatchesAt(t *testing.T) {
	for _, ratio := range []image.YCbCrSubsampleRatio{
		image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422,
		image.YCbCrSubsampleRatio420, image.YCbCrSubsampleRatio440,
	} {
		img := image.NewYCbCr(image.Rect(3, 5, 3+37, 5+21), ratio)
		for i := range img.Y {
			img.Y[i] = uint8(i * 31)
		}
		for i := range img.Cb {
			img.Cb[i], img.Cr[i] = uint8(i*17), uint8(255-i*13)
		}
		got := toRGBA(img)
		for y := 0; y < 21; y++ {
			for x := 0; x < 37; x++ {
				r, g, b, a := img.At(3+x, 5+y).RGBA()
				want := [4]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
				if p := got.Pix[got.PixOffset(x, y):]; [4]uint8(p[:4]) != want {
					t.Fatalf("%v: pixel (%d,%d) = %v, want %v", ratio, x, y, p[:4], want)
				}
			}
		}
	}
}

// JPEG COGs carry nodata as an internal mask (GDAL converts an alpha band
// to one), and their tiles decode as YCbCr.
func TestInternalMaskOnJPEGTiles(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 100, 150, 200, 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	blobs := [][]byte{buf.Bytes(), maskTile(func(x, y int) bool { return y < 8 })}
	data := buildTIFF(false, blobs, func(o []uint64) [][]tagEntry {
		return [][]tagEntry{
			withEntries(imageEntries(16, 16, 16, 16, 8, o[:1], []uint64{uint64(len(blobs[0]))}),
				entry(tagCompression, dtShort, 7), entry(tagPhotometric, dtShort, 6),
				entry(tagSamplesPerPixel, dtShort, 3)),
			maskEntries(16, 4, o[1:], []uint64{32}),
		}
	})
	r, err := openCrafted(t, "jpeg-mask.tif", data)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	img, err := r.ReadTile(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := alphaGrid(img), "####/####/..../..../"; got != want {
		t.Errorf("alpha %s, want %s", got, want)
	}
	if p := toRGBA(img).Pix[:4]; absDiff(int(p[0]), 100) > 3 || absDiff(int(p[2]), 200) > 3 || p[3] != 255 {
		t.Errorf("valid pixel = %v, want ~100,150,200,255", p)
	}
}
