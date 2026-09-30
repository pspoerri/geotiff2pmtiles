package tile

import (
	"bytes"
	"image"
	"image/color"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// A z1 input merged with a z2 one: the z1 tile is scaled up into the z2
// positions it covers, the z2 tile wins where it has data and the z1 tile
// shows through its transparent pixels.
func TestMergeMixedResolutions(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	green := color.RGBA{0, 255, 0, 255}
	enc, _ := encode.NewEncoder("png", 0)
	// halves returns a 4 px tile whose left and right halves are l and r.
	halves := func(l, r color.RGBA) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 4, 4))
		for y := range 4 {
			for x := range 4 {
				img.SetRGBA(x, y, map[bool]color.RGBA{true: l, false: r}[x < 2])
			}
		}
		data, err := enc.Encode(img)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	coarseTile := halves(red, blue)
	coarse := &mockPMTilesReader{header: pmtiles.Header{MinZoom: 1, MaxZoom: 1},
		tiles: map[[3]int][]byte{{1, 0, 0}: coarseTile}}
	fine := &mockPMTilesReader{header: pmtiles.Header{MinZoom: 2, MaxZoom: 2},
		tiles: map[[3]int][]byte{{2, 1, 0}: halves(color.RGBA{}, green)}}
	in := func(r MergeReader) MergeInput {
		return MergeInput{Reader: r, Format: "png", TileType: pmtiles.TileTypePNG}
	}

	w := newMockTileWriter()
	_, err := Merge(MergeConfig{Encoder: enc, MinZoom: 1, MaxZoom: 2, TileSize: 4, Concurrency: 2, Upsampling: ResamplingNearest},
		[]MergeInput{in(coarse), in(fine)}, w) // coarse first: the finer input must still win
	if err != nil {
		t.Fatal(err)
	}

	if n := w.tileCountAtZoom(2); n != 4 {
		t.Errorf("zoom 2 has %d tiles, want the 4 the z1 tile covers", n)
	}
	// z1 is below the fine input's min zoom: it is downsampled from the
	// merged z2, so it shows the fine input too instead of the coarse tile.
	if got := w.tiles[[3]int{1, 0, 0}]; got == nil || bytes.Equal(got, coarseTile) {
		t.Error("z1 was not downsampled from the merged z2")
	}
	at := func(z, x, y, px int) color.RGBA {
		img, err := encode.DecodeImage(w.tiles[[3]int{z, x, y}], "png")
		if err != nil {
			t.Fatalf("z%d/%d/%d: %v", z, x, y, err)
		}
		r, g, b, a := img.At(px, 0).RGBA()
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
	}
	for _, c := range []struct {
		x, px int
		want  color.RGBA
	}{
		{0, 3, red},   // scaled-up left quarter of the z1 tile
		{1, 0, blue},  // the z2 tile is transparent here: z1 shows through
		{1, 3, green}, // the z2 tile wins
	} {
		if got := at(2, c.x, 0, c.px); got != c.want {
			t.Errorf("z2/%d/0 pixel %d = %v, want %v", c.x, c.px, got, c.want)
		}
	}
}

func TestCompositeUnderBlendsPartialAlpha(t *testing.T) {
	dst := &image.RGBA{Pix: []uint8{255, 0, 0, 128}, Stride: 4, Rect: image.Rect(0, 0, 1, 1)}
	src := &image.RGBA{Pix: []uint8{0, 0, 255, 255}, Stride: 4, Rect: image.Rect(0, 0, 1, 1)}
	compositeUnder(dst, src, false)
	if got := dst.Pix; got[3] != 255 || got[0] != 128 || got[2] != 127 {
		t.Errorf("half red over blue = %v, want [128 0 127 255]", got)
	}
}

// Two levels up (z12 → z14): each output pixel is the ancestor pixel its
// quarter-tile crop scales up.
func TestUpsampleCropTwoLevels(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			src.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 0, 255})
		}
	}
	for _, xy := range [][2]int{{0, 0}, {5, 2}, {7, 7}} { // z14 tiles under ancestor 1/0 at z12
		dst := upsampleCrop(src, 2, xy[0], xy[1])
		for py := range 8 {
			for px := range 8 {
				want := src.RGBAAt((xy[0]&3)*2+px/4, (xy[1]&3)*2+py/4)
				if got := dst.RGBAAt(px, py); got != want {
					t.Fatalf("tile %v pixel %d,%d = %v, want %v", xy, px, py, got, want)
				}
			}
		}
	}
}

// TileRanges lets the mock stand in for a MergeReader.
func (r *mockPMTilesReader) TileRanges(z int) [][2]uint64 {
	base := pmtiles.ZXYToTileID(z, 0, 0)
	var hs []uint64
	for _, t := range r.TilesAtZoom(z) {
		hs = append(hs, pmtiles.ZXYToTileID(t[0], t[1], t[2])-base)
	}
	slices.Sort(hs)
	var rs [][2]uint64
	for _, h := range hs {
		if n := len(rs); n > 0 && rs[n-1][1] == h {
			rs[n-1][1]++
		} else {
			rs = append(rs, [2]uint64{h, h + 1})
		}
	}
	return rs
}

func TestSweepRanges(t *testing.T) {
	ranges := [][][2]uint64{
		{{2, 4}, {10, 11}},
		{{0, 1}, {3, 6}},
		nil,
	}
	var got []mergePos
	for p := range sweepRanges(ranges) {
		got = append(got, p)
	}
	want := []mergePos{{0, 0b10}, {2, 0b01}, {3, 0b11}, {4, 0b10}, {5, 0b10}, {10, 0b01}}
	if !slices.Equal(got, want) {
		t.Errorf("sweep = %v, want %v", got, want)
	}
	if n := unionLength(ranges); n != uint64(len(want)) {
		t.Errorf("unionLength = %d, want %d", n, len(want))
	}
}

// Bilinear upsampling interpolates between source pixels and does not let
// transparent pixels darken opaque ones.
func TestUpsampleBilinear(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			if x < 3 {
				src.SetRGBA(x, y, color.RGBA{uint8(x * 60), 200, 0, 255})
			}
		}
	}
	dst := upsampleBilinear(src, 1, 0, 0, false) // top-left quarter, 2x
	// Output pixel 1 samples source x 0.25: a quarter of the way from 0 to 60.
	if got := dst.RGBAAt(1, 0); got.R != 15 || got.A != 255 {
		t.Errorf("pixel 1 = %v, want R 15, opaque", got)
	}
	right := upsampleBilinear(src, 1, 1, 0, false) // top-right quarter
	// Pixel 1 samples source x 2.25, a quarter into the transparent column:
	// colour stays column 2's, alpha drops to 3/4.
	if got := right.RGBAAt(1, 0); got.R != 120 || got.G != 200 || got.A != 191 {
		t.Errorf("edge pixel = %v, want {120 200 0 191}", got)
	}
}

func TestUpsampleBilinearTerrarium(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		src.SetRGBA(0, y, encode.ElevationToTerrarium(100))
		src.SetRGBA(1, y, encode.ElevationToTerrarium(300)) // G/B wrap between the two
	}
	dst := upsampleBilinear(src, 1, 0, 0, true)
	// Output pixel 1 samples source x 0.25: 150 m.
	if got := encode.TerrariumToElevation(dst.RGBAAt(1, 0)); got < 149.9 || got > 150.1 {
		t.Errorf("elevation = %v, want 150", got)
	}
}

// With many workers the tiles still reach the writer in tile-ID order: a
// StreamWriter refuses anything else.
func TestMergeWritesInTileIDOrder(t *testing.T) {
	enc, _ := encode.NewEncoder("png", 0)
	coarse := &mockPMTilesReader{header: pmtiles.Header{MinZoom: 2, MaxZoom: 2}, tiles: map[[3]int][]byte{}}
	fine := &mockPMTilesReader{header: pmtiles.Header{MinZoom: 2, MaxZoom: 4}, tiles: map[[3]int][]byte{}}
	for x := range 4 {
		for y := range 4 {
			coarse.tiles[[3]int{2, x, y}] = encodePNGTile(t, 4, color.RGBA{uint8(x * 60), uint8(y * 60), 0, 255})
		}
	}
	for z := 2; z <= 4; z++ {
		for x := range 1 << z / 2 { // western half
			for y := range 1 << z {
				fine.tiles[[3]int{z, x, y}] = encodePNGTile(t, 4, color.RGBA{0, 0, uint8(x), 255})
			}
		}
	}
	path := filepath.Join(t.TempDir(), "m.pmtiles")
	w, err := pmtiles.NewStreamWriter(path, pmtiles.WriterOptions{MinZoom: 2, MaxZoom: 4, TileSize: 4, TileFormat: pmtiles.TileTypePNG})
	if err != nil {
		t.Fatal(err)
	}
	in := func(r MergeReader) MergeInput {
		return MergeInput{Reader: r, Format: "png", TileType: pmtiles.TileTypePNG}
	}
	stats, err := Merge(MergeConfig{Encoder: enc, MinZoom: 2, MaxZoom: 4, TileSize: 4, Concurrency: 8, Upsampling: ResamplingBilinear},
		[]MergeInput{in(coarse), in(fine)}, w)
	if err != nil {
		w.Abort()
		t.Fatal(err)
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	if want := int64(16 + 64 + 256); stats.TileCount != want {
		t.Errorf("%d tiles, want %d", stats.TileCount, want)
	}
}

// Levels below the inputs' min zoom are downsampled from the merged base
// level and written first, still in tile-ID order.
func TestMergeAddsLevelsBelow(t *testing.T) {
	enc, _ := encode.NewEncoder("png", 0)
	a := &mockPMTilesReader{header: pmtiles.Header{MinZoom: 2, MaxZoom: 3}, tiles: map[[3]int][]byte{}}
	b := &mockPMTilesReader{header: pmtiles.Header{MinZoom: 3, MaxZoom: 3}, tiles: map[[3]int][]byte{}}
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	a.tiles[[3]int{2, 0, 0}] = encodePNGTile(t, 4, red)
	for x := range 2 {
		for y := range 2 {
			a.tiles[[3]int{3, x, y}] = encodePNGTile(t, 4, red)
			b.tiles[[3]int{3, x + 2, y}] = encodePNGTile(t, 4, blue) // east of a, same z1 tile
		}
	}
	path := filepath.Join(t.TempDir(), "m.pmtiles")
	w, err := pmtiles.NewStreamWriter(path, pmtiles.WriterOptions{MinZoom: 0, MaxZoom: 3, TileSize: 4, TileFormat: pmtiles.TileTypePNG})
	if err != nil {
		t.Fatal(err)
	}
	in := func(r MergeReader) MergeInput {
		return MergeInput{Reader: r, Format: "png", TileType: pmtiles.TileTypePNG}
	}
	stats, err := Merge(MergeConfig{Encoder: enc, MinZoom: 0, MaxZoom: 3, TileSize: 4, Concurrency: 4,
		Upsampling: ResamplingNearest, Resampling: ResamplingBilinear}, []MergeInput{in(a), in(b)}, w)
	if err != nil {
		w.Abort()
		t.Fatal(err)
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	// z3: 8 tiles; z2: 2 (a's native one is replaced by the merge base); z1, z0: 1 each.
	if stats.TileCount != 12 {
		t.Errorf("%d tiles, want 12", stats.TileCount)
	}
	r, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// A position only one input covers at its own zoom is copied as it is.
	if got, _ := r.ReadTile(3, 2, 1); !bytes.Equal(got, b.tiles[[3]int{3, 2, 1}]) {
		t.Error("z3/2/1 was not copied from b as it is")
	}
	data, _ := r.ReadTile(1, 0, 0)
	img, err := encode.DecodeImage(data, "png")
	if err != nil {
		t.Fatalf("z1/0/0: %v", err)
	}
	// Left half from a (red), right half from b (blue), bottom transparent.
	if l, rt := img.At(0, 0), img.At(3, 0); l != (color.RGBA{255, 0, 0, 255}) && l != (color.NRGBA{255, 0, 0, 255}) || rt == l {
		t.Errorf("z1/0/0 top row = %v .. %v, want red .. blue", l, rt)
	}
}
