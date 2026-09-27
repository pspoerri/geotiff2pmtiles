package cog

import (
	"encoding/binary"
	"testing"
)

// uint16TiledReader builds an in-memory, uncompressed, chunky reader whose
// samples are a known function of position, with one tile left empty.
func uint16TiledReader(t *testing.T, w, h, tw, th, spp, emptyTile int) (*Reader, []uint16) {
	t.Helper()
	across := (w + tw - 1) / tw
	down := (h + th - 1) / th

	// The logical image, row-major and chunky.
	want := make([]uint16, w*h*spp)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for s := 0; s < spp; s++ {
				want[(y*w+x)*spp+s] = uint16(1000*(s+1) + 37*x + 101*y)
			}
		}
	}

	var data []byte
	offsets := make([]uint64, across*down)
	counts := make([]uint64, across*down)
	tileSamples := tw * th * spp
	for row := 0; row < down; row++ {
		for col := 0; col < across; col++ {
			idx := row*across + col
			if idx == emptyTile {
				offsets[idx], counts[idx] = 0, 0 // an empty tile, which is legal
				continue
			}
			buf := make([]byte, tileSamples*2)
			for y := 0; y < th; y++ {
				for x := 0; x < tw; x++ {
					sx, sy := col*tw+x, row*th+y
					for s := 0; s < spp; s++ {
						var v uint16
						if sx < w && sy < h {
							v = want[(sy*w+sx)*spp+s]
						}
						binary.LittleEndian.PutUint16(buf[((y*tw+x)*spp+s)*2:], v)
					}
				}
			}
			offsets[idx] = uint64(len(data))
			counts[idx] = uint64(len(buf))
			data = append(data, buf...)
		}
	}

	// An empty tile's samples read as zero, so the expectation has to match.
	if emptyTile >= 0 {
		col, row := emptyTile%across, emptyTile/across
		for y := row * th; y < min(h, (row+1)*th); y++ {
			for x := col * tw; x < min(w, (col+1)*tw); x++ {
				for s := 0; s < spp; s++ {
					want[(y*w+x)*spp+s] = 0
				}
			}
		}
	}

	ifd := IFD{
		Width: uint32(w), Height: uint32(h),
		TileWidth: uint32(tw), TileHeight: uint32(th),
		SamplesPerPixel: uint16(spp),
		BitsPerSample:   []uint16{16, 16, 16, 16, 16}[:spp],
		SampleFormat:    []uint16{1, 1, 1, 1, 1}[:spp],
		Compression:     1,
		PlanarConfig:    1,
		TileOffsets:     offsets,
		TileByteCounts:  counts,
	}
	return &Reader{bo: binary.LittleEndian, ifds: []IFD{ifd}, src: mmapSource(data)}, want
}

// ReadUint16Region must return the stored samples, unscaled, for regions that
// straddle tile boundaries and for regions that cover a legitimately empty
// tile. ReadRegion cannot serve this: it goes through color.RGBA, so every
// sample loses its low byte and band four becomes alpha.
func TestReadUint16Region(t *testing.T) {
	const w, h, tw, th, spp = 13, 11, 4, 4, 3
	r, want := uint16TiledReader(t, w, h, tw, th, spp, 4)

	for _, reg := range [][4]int{
		{0, 0, w, h},   // everything
		{3, 3, 3, 3},   // straddles both tile boundaries
		{4, 0, 1, h},   // one column, on a tile edge
		{0, 8, w, 3},   // the short bottom row of tiles
		{4, 4, 4, 4},   // exactly the empty tile
		{12, 10, 1, 1}, // the last pixel
	} {
		x, y, rw, rh := reg[0], reg[1], reg[2], reg[3]
		got, gotSpp, err := r.ReadUint16Region(0, x, y, rw, rh)
		if err != nil {
			t.Fatalf("region %v: %v", reg, err)
		}
		if gotSpp != spp {
			t.Fatalf("region %v: spp %d, want %d", reg, gotSpp, spp)
		}
		for dy := 0; dy < rh; dy++ {
			for dx := 0; dx < rw; dx++ {
				for s := 0; s < spp; s++ {
					g := got[((dy*rw+dx)*spp)+s]
					wv := want[(((y+dy)*w+x+dx)*spp)+s]
					if g != wv {
						t.Fatalf("region %v pixel (%d,%d) sample %d = %d, want %d",
							reg, x+dx, y+dy, s, g, wv)
					}
				}
			}
		}
	}
}

// The cache must report a hit with an explicit ok, because nil samples are a
// legitimate empty tile. Signalling misses by a nil slice, as FloatTileCache
// does, would make every empty tile miss forever and be re-read per pixel.
func TestUint16TileCacheCachesEmptyTiles(t *testing.T) {
	uc := NewUint16TileCache(64)

	if _, _, _, _, ok := uc.Get(1, 0, 0, 0); ok {
		t.Fatal("empty cache reported a hit")
	}
	uc.Put(1, 0, 0, 0, nil, 256, 256, 1)
	data, width, height, spp, ok := uc.Get(1, 0, 0, 0)
	if !ok {
		t.Fatal("a cached empty tile was reported as a miss")
	}
	if data != nil {
		t.Errorf("data = %v, want nil", data)
	}
	if width != 256 || height != 256 || spp != 1 {
		t.Errorf("dimensions %dx%d spp %d, want 256x256 spp 1", width, height, spp)
	}

	// The same must hold through a Local view, whose memo is the layer most
	// likely to mistake "cached nil" for "not cached".
	local := uc.Local()
	if _, _, _, _, ok := local.Get(1, 0, 0, 0); !ok {
		t.Error("a cached empty tile missed through a Local view")
	}
	if _, _, _, _, ok := local.Get(1, 0, 0, 0); !ok {
		t.Error("a cached empty tile missed on the memo's second lookup")
	}
	if _, _, _, _, ok := local.Get(2, 0, 0, 0); ok {
		t.Error("a Local view reported a hit for a tile never cached")
	}
}
