package tile

import (
	"cmp"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/bits"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
)

// MergeReader is a PMTilesReader that also lists a level's tiles as runs
// of Hilbert indices (implemented by pmtiles.Reader).
type MergeReader interface {
	PMTilesReader
	TileRanges(z int) [][2]uint64
}

// MergeInput is one archive of a merge.
type MergeInput struct {
	Reader   MergeReader
	Format   string // tile format, for decoding
	TileType uint8  // pmtiles tile type; tiles of the output's type can be copied as they are
}

// MaxMergeInputs is the most archives Merge takes: the inputs covering a
// position are a bit set in a uint64.
const MaxMergeInputs = 64

// MergeConfig holds configuration for Merge.
type MergeConfig struct {
	Encoder     encode.Encoder
	MinZoom     int
	MaxZoom     int
	TileSize    int
	Concurrency int
	// Upsampling scales an input's tiles up above its max zoom:
	// ResamplingNearest or ResamplingBilinear.
	Upsampling Resampling
	// Resampling downsamples the levels below the inputs' lowest common
	// zoom, when MinZoom is below it.
	Resampling Resampling
	// IsTerrarium marks tiles as terrarium-encoded elevations: overlapping
	// pixels are never blended (the first input with any alpha wins), and
	// bilinear upsampling interpolates elevations rather than channels.
	IsTerrarium bool
}

// mergeSource is an input tile that contributes to an output position:
// input's tile at (z, x, y), cropped and scaled up when z is below the
// output zoom.
type mergeSource struct {
	input   int
	z, x, y int
}

// mergePos is an output position: its Hilbert index within the level and
// the inputs covering it, bit j standing for the j-th input by priority.
type mergePos struct {
	h    uint64
	mask uint64
}

// Merge writes the union of several archives. Where they overlap, the input
// with the higher max zoom wins, ties go to the earlier input, and pixels it
// leaves (partly) transparent show the next one through. Above its own max
// zoom an input contributes its max zoom tiles, cropped and scaled up:
// PMTiles has no per-region max zoom, and a missing tile inside the zoom
// range renders blank rather than overzoomed.
//
// Levels go from low to high zoom, and each level's positions are generated
// in Hilbert order by sweeping the inputs' tile runs (see
// pmtiles.Reader.TileRanges): memory grows with the inputs' directories
// rather than the output's tiles, and the writer gets the tiles in strictly
// increasing tile-ID order (see mergeLevel), as a pmtiles.StreamWriter needs.
// A position with a single native tile in the output's tile type is copied
// without decoding, as is one whose first tile is opaque.
//
// cfg.MinZoom may lie below the lowest zoom every input has (the highest
// input min zoom): those levels are downsampled with cfg.Resampling from
// the merged level at that zoom.
func Merge(cfg MergeConfig, inputs []MergeInput, writer TileWriter) (Stats, error) {
	if len(inputs) > MaxMergeInputs {
		return Stats{}, fmt.Errorf("%d inputs; at most %d can be merged at once", len(inputs), MaxMergeInputs)
	}
	if cfg.Upsampling != ResamplingNearest && cfg.Upsampling != ResamplingBilinear {
		return Stats{}, fmt.Errorf("upsampling must be nearest or bilinear")
	}
	order := make([]int, len(inputs))
	for i := range order {
		order[i] = i
	}
	maxZoom := func(i int) int { return int(inputs[i].Reader.Header().MaxZoom) }
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(maxZoom(b), maxZoom(a)) })

	var tileCount, totalBytes atomic.Int64
	// mergeZoom merges level z into w.
	mergeZoom := func(z int, w TileWriter) error {
		// Each input's runs at z, in priority order: its own tiles, or its
		// max zoom runs scaled to z.
		ranges := make([][][2]uint64, len(order))
		for j, i := range order {
			m := maxZoom(i)
			if z <= m {
				ranges[j] = inputs[i].Reader.TileRanges(z)
				continue
			}
			d := z - m
			if cfg.TileSize>>d == 0 {
				return fmt.Errorf("zoom %d is %d levels above an input's max zoom %d; a %d px tile cannot be scaled up that far",
					z, d, m, cfg.TileSize)
			}
			for _, r := range inputs[i].Reader.TileRanges(m) {
				ranges[j] = append(ranges[j], [2]uint64{r[0] << (2 * d), r[1] << (2 * d)})
			}
		}
		total := unionLength(ranges)
		if total == 0 {
			return nil
		}
		pb := newProgressBar(fmt.Sprintf("Zoom %2d", z), int64(total))
		defer pb.Finish()
		return mergeLevel(cfg, inputs, order, z, ranges, w, func(n int) {
			tileCount.Add(1)
			totalBytes.Add(int64(n))
			pb.Increment()
		})
	}

	// Levels below the lowest one every input has are downsampled from the
	// merged base level, as pmtransform adds levels. They have lower tile
	// IDs, so they go to the writer first, then the base level.
	first := cfg.MinZoom
	base := 0
	for _, in := range inputs {
		base = max(base, int(in.Reader.Header().MinZoom))
	}
	if base = min(base, cfg.MaxZoom); cfg.MinZoom < base {
		// ponytail: the base level is held in memory, encoded. It is the
		// lowest level of the inputs, usually small; spill it through a
		// DiskTileStore if merges with a high base zoom outgrow memory.
		baseTiles := &tileCollector{}
		if err := mergeZoom(base, baseTiles); err != nil {
			return Stats{}, err
		}
		added := &tileCollector{}
		capped := &zoomCap{TileWriter: added, maxZoom: base - 1}
		st, err := transformRebuild(TransformConfig{
			Encoder:          cfg.Encoder,
			SourceFormat:     cfg.Encoder.Format(),
			MinZoom:          cfg.MinZoom,
			MaxZoom:          base,
			TileSize:         cfg.TileSize,
			Concurrency:      max(1, cfg.Concurrency),
			Resampling:       cfg.Resampling,
			Mode:             TransformRebuild,
			MemoryLimitBytes: -1, // levels below the base are a third of it at most
			IsTerrarium:      cfg.IsTerrarium,
		}, baseTiles.reader(base), capped)
		if err != nil {
			return Stats{}, fmt.Errorf("adding zoom %d-%d: %w", cfg.MinZoom, base-1, err)
		}
		tileCount.Add(st.TileCount - capped.dropped.Load())
		for _, t := range append(added.sorted(), baseTiles.tiles...) {
			if err := writer.WriteTile(t.z, t.x, t.y, t.data); err != nil {
				return Stats{}, err
			}
			if t.z < base {
				totalBytes.Add(int64(len(t.data)))
			}
		}
		first = base + 1
	}

	for z := first; z <= cfg.MaxZoom; z++ {
		if err := mergeZoom(z, writer); err != nil {
			return Stats{}, err
		}
	}
	return Stats{TileCount: tileCount.Load(), TotalBytes: totalBytes.Load()}, nil
}

// zxyTile is an encoded tile at z/x/y.
type zxyTile struct {
	z, x, y int
	data    []byte
}

// tileCollector is a TileWriter that keeps the tiles in memory.
type tileCollector struct {
	mu    sync.Mutex
	tiles []zxyTile
}

func (c *tileCollector) WriteTile(z, x, y int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tiles = append(c.tiles, zxyTile{z, x, y, data})
	return nil
}

// sorted returns the tiles in tile-ID order.
func (c *tileCollector) sorted() []zxyTile {
	slices.SortFunc(c.tiles, func(a, b zxyTile) int {
		return cmp.Compare(pmtiles.ZXYToTileID(a.z, a.x, a.y), pmtiles.ZXYToTileID(b.z, b.x, b.y))
	})
	return c.tiles
}

// reader returns the collected tiles as an archive with the single level z.
func (c *tileCollector) reader(z int) PMTilesReader {
	r := &levelReader{header: pmtiles.Header{MinZoom: uint8(z), MaxZoom: uint8(z)}, tiles: map[[3]int][]byte{}}
	for _, t := range c.tiles {
		r.tiles[[3]int{t.z, t.x, t.y}] = t.data
	}
	return r
}

// levelReader is an in-memory PMTilesReader.
type levelReader struct {
	header pmtiles.Header
	tiles  map[[3]int][]byte
}

func (r *levelReader) Header() pmtiles.Header { return r.header }

func (r *levelReader) ReadTile(z, x, y int) ([]byte, error) { return r.tiles[[3]int{z, x, y}], nil }

func (r *levelReader) TilesAtZoom(z int) [][3]int {
	var ts [][3]int
	for k := range r.tiles {
		if k[0] == z {
			ts = append(ts, k)
		}
	}
	return ts
}

// sweepRanges yields, in order, every index that any of the sorted,
// disjoint run lists covers, with the set of lists covering it.
func sweepRanges(ranges [][][2]uint64) func(yield func(mergePos) bool) {
	return func(yield func(mergePos) bool) {
		cur := make([]int, len(ranges)) // each list's first run not yet passed
		for h := uint64(0); ; h++ {
			next := uint64(math.MaxUint64) // the lowest run start above h
			var mask uint64
			for j, rs := range ranges {
				for cur[j] < len(rs) && rs[cur[j]][1] <= h {
					cur[j]++
				}
				if cur[j] == len(rs) {
					continue
				}
				if r := rs[cur[j]]; r[0] <= h {
					mask |= 1 << j
				} else {
					next = min(next, r[0])
				}
			}
			if mask == 0 {
				if next == math.MaxUint64 {
					return
				}
				h = next - 1 // jump the gap
				continue
			}
			if !yield(mergePos{h, mask}) {
				return
			}
		}
	}
}

// mergeBatch is a run of consecutive positions, numbered in sweep order.
type mergeBatch struct {
	seq int
	pos []mergePos
}

// mergedTile is an encoded output tile; mergeResult is a batch's tiles.
type mergedTile struct {
	x, y int
	data []byte
}

type mergeResult struct {
	seq   int
	tiles []mergedTile
}

// mergeLevel renders zoom z's positions in parallel and writes them in
// sweep order, which is tile-ID order, so that a pmtiles.StreamWriter can
// lay them straight into the archive. Workers take numbered batches and a
// collector writes the results as their turn comes; a token per batch in
// flight caps how far workers can run ahead of a slow batch.
func mergeLevel(cfg MergeConfig, inputs []MergeInput, order []int, z int, ranges [][][2]uint64,
	writer TileWriter, written func(n int)) error {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	workers := max(1, cfg.Concurrency)
	tokens := make(chan struct{}, 4*workers)
	batchCh := make(chan mergeBatch, workers)
	resultCh := make(chan mergeResult, workers)

	go func() {
		defer close(batchCh)
		seq := 0
		batch := make([]mergePos, 0, scheduleBatchSize)
		send := func() bool {
			select {
			case tokens <- struct{}{}:
			case <-ctx.Done():
				return false
			}
			select {
			case batchCh <- mergeBatch{seq, batch}:
				seq++
				batch = make([]mergePos, 0, scheduleBatchSize)
				return true
			case <-ctx.Done():
				return false
			}
		}
		for p := range sweepRanges(ranges) {
			if batch = append(batch, p); len(batch) == scheduleBatchSize && !send() {
				return
			}
		}
		if len(batch) > 0 {
			send()
		}
	}()

	base := pmtiles.ZXYToTileID(z, 0, 0)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := merger{cfg: cfg, inputs: inputs, last: map[int]decodedTile{}}
			srcs := make([]mergeSource, 0, len(order))
			for b := range batchCh {
				res := mergeResult{seq: b.seq, tiles: make([]mergedTile, 0, len(b.pos))}
				for _, p := range b.pos {
					if ctx.Err() != nil {
						return
					}
					_, x, y := pmtiles.TileIDToZXY(base + p.h)
					srcs = srcs[:0]
					for mask := p.mask; mask != 0; mask &= mask - 1 {
						i := order[bits.TrailingZeros64(mask)]
						mz := min(z, int(inputs[i].Reader.Header().MaxZoom))
						srcs = append(srcs, mergeSource{i, mz, x >> (z - mz), y >> (z - mz)})
					}
					data, err := m.tile(z, x, y, srcs)
					if err != nil {
						cancel(fmt.Errorf("tile z%d/%d/%d: %w", z, x, y, err))
						return
					}
					res.tiles = append(res.tiles, mergedTile{x, y, data})
				}
				select {
				case resultCh <- res:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	pending := map[int][]mergedTile{}
	next := 0
	for res := range resultCh {
		if ctx.Err() != nil {
			continue // drain until the workers stop
		}
		pending[res.seq] = res.tiles
		for tiles, ok := pending[next]; ok && ctx.Err() == nil; tiles, ok = pending[next] {
			for _, t := range tiles {
				if err := writer.WriteTile(z, t.x, t.y, t.data); err != nil {
					cancel(fmt.Errorf("writing tile z%d/%d/%d: %w", z, t.x, t.y, err))
					break
				}
				written(len(t.data))
			}
			delete(pending, next)
			next++
			<-tokens
		}
	}
	return context.Cause(ctx)
}

// unionLength returns how many indices the run lists cover together.
func unionLength(ranges [][][2]uint64) uint64 {
	var all [][2]uint64
	for _, rs := range ranges {
		all = append(all, rs...)
	}
	slices.SortFunc(all, func(a, b [2]uint64) int { return cmp.Compare(a[0], b[0]) })
	var n, end uint64
	for _, r := range all {
		if r[1] > end {
			n += r[1] - max(r[0], end)
			end = r[1]
		}
	}
	return n
}

type decodedTile struct {
	src mergeSource
	img *image.RGBA
}

// merger renders output tiles for one worker. last holds each input's most
// recently decoded tile, which the descendants of a scaled-up tile share:
// they are consecutive in Hilbert order.
type merger struct {
	cfg    MergeConfig
	inputs []MergeInput
	last   map[int]decodedTile
}

// tile returns the encoded output tile at z/x/y from srcs (in priority
// order).
func (m *merger) tile(z, x, y int, srcs []mergeSource) ([]byte, error) {
	var dst *image.RGBA
	for i, s := range srcs {
		native := s.z == z
		copyable := native && m.inputs[s.input].TileType == m.cfg.Encoder.PMTileType()
		if i == 0 && copyable && len(srcs) == 1 {
			return m.inputs[s.input].Reader.ReadTile(s.z, s.x, s.y)
		}
		img, err := m.decode(s)
		if err != nil {
			return nil, err
		}
		if !native {
			img = m.upsample(img, z-s.z, x, y)
		}
		if i == 0 {
			if copyable && opaque(img) {
				return m.inputs[s.input].Reader.ReadTile(s.z, s.x, s.y)
			}
			if native { // img is the cached tile, which composite must not change
				img = &image.RGBA{Pix: slices.Clone(img.Pix), Stride: img.Stride, Rect: img.Rect}
			}
			dst = img
		} else {
			compositeUnder(dst, img, m.cfg.IsTerrarium)
		}
		if opaque(dst) {
			break
		}
	}
	return m.cfg.Encoder.Encode(dst)
}

func (m *merger) upsample(src *image.RGBA, d, x, y int) *image.RGBA {
	if m.cfg.Upsampling == ResamplingBilinear {
		return upsampleBilinear(src, d, x, y, m.cfg.IsTerrarium)
	}
	return upsampleCrop(src, d, x, y)
}

// decode returns the decoded tile s, reusing the input's last one.
func (m *merger) decode(s mergeSource) (*image.RGBA, error) {
	if c, ok := m.last[s.input]; ok && c.src == s {
		return c.img, nil
	}
	in := m.inputs[s.input]
	data, err := in.Reader.ReadTile(s.z, s.x, s.y)
	if err != nil {
		return nil, err
	}
	img, err := encode.DecodeImage(data, in.Format)
	if err != nil {
		return nil, fmt.Errorf("decoding z%d/%d/%d: %w", s.z, s.x, s.y, err)
	}
	rgba := imageToRGBA(img)
	if b := rgba.Rect; b.Dx() != m.cfg.TileSize || b.Dy() != m.cfg.TileSize {
		return nil, fmt.Errorf("z%d/%d/%d is %dx%d px, want %d px", s.z, s.x, s.y, b.Dx(), b.Dy(), m.cfg.TileSize)
	}
	m.last[s.input] = decodedTile{s, rgba}
	return rgba, nil
}

// upsampleCrop returns the part of src, an ancestor d levels up, that
// covers the tile at (x, y), scaled up 2^d times by nearest neighbour —
// exact for terrarium and categorical data, blocky for imagery.
func upsampleCrop(src *image.RGBA, d, x, y int) *image.RGBA {
	n := src.Rect.Dx()
	s := n >> d
	ox, oy := (x&(1<<d-1))*s, (y&(1<<d-1))*s
	dst := image.NewRGBA(src.Rect)
	for py := range n {
		row := src.Pix[(oy+py>>d)*src.Stride:]
		out := dst.Pix[py*dst.Stride:]
		for px := range n {
			copy(out[px*4:px*4+4], row[(ox+px>>d)*4:])
		}
	}
	return dst
}

// upsampleBilinear is upsampleCrop with bilinear interpolation, weighted by
// alpha so that transparent pixels do not darken their neighbours.
// Terrarium pixels are interpolated as elevations, not channel by channel.
//
// ponytail: samples past the ancestor's edge clamp to it, so the outer half
// source pixel along ancestor tile borders is flat; reading the neighbouring
// ancestors would fix that.
func upsampleBilinear(src *image.RGBA, d, x, y int, terrarium bool) *image.RGBA {
	n := src.Rect.Dx()
	s := n >> d
	scale := float64(int(1) << d)

	// The source pixel pair and weight of each output column or row.
	type tap struct {
		i0, i1 int
		f      float64
	}
	taps := func(o int) []tap {
		t := make([]tap, n)
		for p := range t {
			v := min(max(float64(o)+(float64(p)+0.5)/scale-0.5, 0), float64(n-1))
			i0 := int(v)
			t[p] = tap{i0, min(i0+1, n-1), v - float64(i0)}
		}
		return t
	}
	xs, ys := taps((x&(1<<d-1))*s), taps((y&(1<<d-1))*s)

	dst := image.NewRGBA(src.Rect)
	for py, ty := range ys {
		for px, tx := range xs {
			corners := [4][]uint8{
				src.Pix[ty.i0*src.Stride+tx.i0*4:], src.Pix[ty.i0*src.Stride+tx.i1*4:],
				src.Pix[ty.i1*src.Stride+tx.i0*4:], src.Pix[ty.i1*src.Stride+tx.i1*4:],
			}
			w := [4]float64{(1 - tx.f) * (1 - ty.f), tx.f * (1 - ty.f), (1 - tx.f) * ty.f, tx.f * ty.f}
			var wa float64
			var acc [3]float64
			for k, p := range corners {
				a := w[k] * float64(p[3]) / 255
				if a == 0 {
					continue
				}
				wa += a
				if terrarium {
					acc[0] += a * encode.TerrariumToElevation(color.RGBA{p[0], p[1], p[2], p[3]})
				} else {
					for c := range 3 {
						acc[c] += a * float64(p[c])
					}
				}
			}
			if wa == 0 {
				continue
			}
			out := dst.Pix[py*dst.Stride+px*4:]
			if terrarium {
				c := encode.ElevationToTerrarium(acc[0] / wa)
				out[0], out[1], out[2] = c.R, c.G, c.B
			} else {
				for c := range 3 {
					out[c] = uint8(acc[c]/wa + 0.5)
				}
			}
			out[3] = uint8(wa*255 + 0.5)
		}
	}
	return dst
}

// compositeUnder puts src under dst, straight alpha ("dst over src"). For
// terrarium, blending would mix elevations, so a pixel with any alpha is
// kept and only fully transparent ones take src.
func compositeUnder(dst, src *image.RGBA, terrarium bool) {
	for i := 0; i < len(dst.Pix); i += 4 {
		da, sa := uint32(dst.Pix[i+3]), uint32(src.Pix[i+3])
		switch {
		case da == 255 || sa == 0 || (terrarium && da > 0):
		case da == 0:
			copy(dst.Pix[i:i+4], src.Pix[i:i+4])
		default:
			w := sa * (255 - da) // src weight, in 255² units
			oa := da*255 + w     // output alpha, in 255² units
			for c := range 3 {
				dst.Pix[i+c] = uint8((uint32(dst.Pix[i+c])*da*255 + uint32(src.Pix[i+c])*w + oa/2) / oa)
			}
			dst.Pix[i+3] = uint8((oa + 127) / 255)
		}
	}
}

func opaque(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 255 {
			return false
		}
	}
	return true
}

// SourceTileSize decodes max zoom tiles until one decodes, and returns its
// width: the PMTiles header does not record the tile size. It returns 256
// if none decodes.
func SourceTileSize(r MergeReader, format string) int {
	z := int(r.Header().MaxZoom)
	base := pmtiles.ZXYToTileID(z, 0, 0)
	for _, rg := range r.TileRanges(z) {
		for h := rg[0]; h < rg[1]; h++ {
			_, x, y := pmtiles.TileIDToZXY(base + h)
			data, err := r.ReadTile(z, x, y)
			if err != nil || data == nil {
				continue
			}
			img, err := encode.DecodeImage(data, format)
			if err != nil {
				continue
			}
			if b := img.Bounds(); b.Dx() > 0 && b.Dy() > 0 {
				return b.Dx()
			}
		}
	}
	return 256
}
