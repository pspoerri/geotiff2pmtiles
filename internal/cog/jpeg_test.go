package cog

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"testing"
)

// encodeJPEGGray encodes an in-memory gray image to a stand-alone JPEG byte
// stream (no separate JPEGTables tag — full headers inline). Used by tests to
// synthesise per-band JPEG tile blobs.
func encodeJPEGGray(t *testing.T, pix []uint8, w, h int, quality int) []byte {
	t.Helper()
	g := image.NewGray(image.Rect(0, 0, w, h))
	copy(g.Pix, pix) // assumes Stride == w (true for our tiny tiles)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, g, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

// TestDecodeJPEGTileNodataExact: a single-band JPEG with nodata=0 (exact match,
// high quality) — boundary black pixels become alpha=0.
func TestDecodeJPEGTileNodataExact(t *testing.T) {
	w, h := 16, 16
	// Half black, half mid-gray.
	pix := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				pix[y*w+x] = 0
			} else {
				pix[y*w+x] = 200
			}
		}
	}
	stream := encodeJPEGGray(t, pix, w, h, 95)

	ifd := &IFD{
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: 1,
		BitsPerSample:   []uint16{8},
		Compression:     7,
	}
	r := &Reader{
		bo:      binary.LittleEndian,
		ifds:    []IFD{*ifd},
		bandCfg: BandConfig{HasNodata: true, Nodata: 0, NodataTolerance: 4},
	}

	img, err := r.decodeJPEGTile(ifd, stream)
	if err != nil {
		t.Fatalf("decodeJPEGTile: %v", err)
	}
	rgba, ok := img.(*image.RGBA)
	if !ok {
		t.Fatalf("expected *image.RGBA, got %T", img)
	}

	// Left half should be transparent; right half opaque.
	leftA := rgba.Pix[rgba.PixOffset(0, h/2)+3]
	rightA := rgba.Pix[rgba.PixOffset(w-1, h/2)+3]
	if leftA != 0 {
		t.Errorf("left (nodata) alpha = %d, want 0", leftA)
	}
	if rightA != 255 {
		t.Errorf("right (data) alpha = %d, want 255", rightA)
	}
}

// TestDecodeJPEGTileNoNodataPassthrough: without nodata, the JPEG path returns
// the raw decoded image (not RGBA), preserving the YCbCr/Gray fast path used
// by downstream sampling.
func TestDecodeJPEGTileNoNodataPassthrough(t *testing.T) {
	w, h := 16, 16
	pix := make([]uint8, w*h)
	for i := range pix {
		pix[i] = uint8(i % 256)
	}
	stream := encodeJPEGGray(t, pix, w, h, 95)
	ifd := &IFD{
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: 1,
		BitsPerSample:   []uint16{8},
		Compression:     7,
	}
	r := &Reader{bo: binary.LittleEndian, ifds: []IFD{*ifd}}

	img, err := r.decodeJPEGTile(ifd, stream)
	if err != nil {
		t.Fatalf("decodeJPEGTile: %v", err)
	}
	if _, isRGBA := img.(*image.RGBA); isRGBA {
		t.Errorf("expected non-RGBA passthrough when nodata is off; got *image.RGBA")
	}
}

// TestPlanarSeparateJPEG: three separately-encoded grayscale JPEG planes are
// merged into RGBA with the natural plane→channel mapping.
func TestPlanarSeparateJPEG(t *testing.T) {
	w, h := 16, 16
	// Plane 0: solid 100, plane 1: solid 150, plane 2: solid 200.
	mk := func(v uint8) []byte {
		buf := make([]uint8, w*h)
		for i := range buf {
			buf[i] = v
		}
		return encodeJPEGGray(t, buf, w, h, 95)
	}
	p0, p1, p2 := mk(100), mk(150), mk(200)

	// Lay out the planes end-to-end in a single buffer.
	combined := append(append(append([]byte{}, p0...), p1...), p2...)
	offsets := []uint64{
		0,
		uint64(len(p0)),
		uint64(len(p0) + len(p1)),
	}
	counts := []uint64{uint64(len(p0)), uint64(len(p1)), uint64(len(p2))}

	ifd := &IFD{
		Width:           uint32(w),
		Height:          uint32(h),
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: 3,
		BitsPerSample:   []uint16{8, 8, 8},
		Compression:     7,
		PlanarConfig:    2,
		TileOffsets:     offsets,
		TileByteCounts:  counts,
	}
	r := &Reader{bo: binary.LittleEndian, ifds: []IFD{*ifd}, data: combined}

	img, err := r.ReadTile(0, 0, 0)
	if err != nil {
		t.Fatalf("ReadTile: %v", err)
	}
	rgba, ok := img.(*image.RGBA)
	if !ok {
		t.Fatalf("expected *image.RGBA, got %T", img)
	}

	// JPEG is lossy at solid colours but should be within a small delta.
	i := rgba.PixOffset(w/2, h/2)
	rr, gg, bb, aa := rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2], rgba.Pix[i+3]
	near := func(got, want uint8) bool {
		d := int(got) - int(want)
		if d < 0 {
			d = -d
		}
		return d <= 4
	}
	if !near(rr, 100) || !near(gg, 150) || !near(bb, 200) || aa != 255 {
		t.Errorf("center pixel = (%d,%d,%d,%d), want ≈(100,150,200,255)", rr, gg, bb, aa)
	}
}

// TestPlanarSeparateRawErrors: uncompressed PlanarConfig=2 isn't supported
// yet; ReadTile should report it clearly rather than silently decoding wrong
// pixels.
func TestPlanarSeparateRawErrors(t *testing.T) {
	w, h := 4, 4
	ifd := &IFD{
		Width:           uint32(w),
		Height:          uint32(h),
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: 3,
		BitsPerSample:   []uint16{8, 8, 8},
		Compression:     1, // uncompressed
		PlanarConfig:    2,
		TileOffsets:     []uint64{0},
		TileByteCounts:  []uint64{uint64(w * h)},
	}
	r := &Reader{bo: binary.LittleEndian, ifds: []IFD{*ifd}, data: make([]byte, w*h)}

	if _, err := r.ReadTile(0, 0, 0); err == nil {
		t.Fatal("expected error for uncompressed planar-separate, got nil")
	}
}

// TestNodataTolerance: with tolerance=5, a near-zero value like 3 still
// triggers transparency on the raw decode path.
func TestNodataTolerance(t *testing.T) {
	w, h, spp := 2, 1, 3
	// pixel0 = (3,2,4) — near zero. pixel1 = (200,200,200) — data.
	data := []byte{3, 2, 4, 200, 200, 200}
	ifd := &IFD{
		TileWidth:       uint32(w),
		TileHeight:      uint32(h),
		SamplesPerPixel: uint16(spp),
		BitsPerSample:   []uint16{8, 8, 8},
	}
	r := &Reader{
		bo:      binary.LittleEndian,
		ifds:    []IFD{*ifd},
		bandCfg: BandConfig{HasNodata: true, Nodata: 0, NodataTolerance: 5},
	}
	img, err := r.decodeRawTile(ifd, data)
	if err != nil {
		t.Fatal(err)
	}
	rgba := img.(*image.RGBA)
	if a := rgba.Pix[rgba.PixOffset(0, 0)+3]; a != 0 {
		t.Errorf("pixel0 alpha = %d, want 0 (within tolerance)", a)
	}
	if a := rgba.Pix[rgba.PixOffset(1, 0)+3]; a != 255 {
		t.Errorf("pixel1 alpha = %d, want 255", a)
	}
}
