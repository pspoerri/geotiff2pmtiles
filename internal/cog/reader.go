package cog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/zlib"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"log"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
)

// RescaleMode specifies how to rescale sample values to uint8.
type RescaleMode int

const (
	RescaleNone   RescaleMode = iota // No rescaling (identity for 8-bit)
	RescaleLinear                    // Linear mapping from [min,max] → [0,255]
	RescaleLog                       // Logarithmic mapping from [min,max] → [0,255]
)

// BandConfig controls band selection, alpha handling, rescaling, and nodata for multi-band GeoTIFFs.
// Zero value produces identical behavior to the legacy code path (bands 1,2,3; auto alpha for spp≥4; no rescaling).
type BandConfig struct {
	Bands      [3]int      // 1-indexed input band → R,G,B output. Zero value = default (1,2,3)
	AlphaBand  int         // 1-indexed alpha band. 0=auto, -1=none
	Rescale    RescaleMode // Rescaling mode
	RescaleMin float64     // Input value range minimum
	RescaleMax float64     // Input value range maximum
	HasNodata  bool        // if true, pixels with all bands == Nodata are decoded as transparent (alpha=0)
	Nodata     float64     // raw (pre-rescale) nodata value; valid when HasNodata is true
	// NodataTolerance widens the match: a sample is considered nodata when
	// |sample - Nodata| <= NodataTolerance. Useful for lossy-JPEG borders where
	// compression smears strict nodata values (e.g. boundary blacks of 1..5
	// instead of exactly 0). 0 = exact match.
	NodataTolerance float64
}

// String returns a human-readable summary of the band configuration.
func (cfg BandConfig) String() string {
	bands := cfg.Bands
	if bands == ([3]int{}) {
		bands = [3]int{1, 2, 3}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "bands %d,%d,%d", bands[0], bands[1], bands[2])
	switch cfg.Rescale {
	case RescaleLinear:
		fmt.Fprintf(&b, ", rescale linear [%.0f, %.0f]", cfg.RescaleMin, cfg.RescaleMax)
	case RescaleLog:
		fmt.Fprintf(&b, ", rescale log [%.0f, %.0f]", cfg.RescaleMin, cfg.RescaleMax)
	}
	if cfg.AlphaBand > 0 {
		fmt.Fprintf(&b, ", alpha band %d", cfg.AlphaBand)
	}
	if cfg.HasNodata {
		fmt.Fprintf(&b, ", nodata %.0f", cfg.Nodata)
		if cfg.NodataTolerance > 0 {
			fmt.Fprintf(&b, " (tol %.0f)", cfg.NodataTolerance)
		}
	}
	return b.String()
}

// Bounds represents geographic bounds in WGS84.
type Bounds struct {
	MinLon, MaxLon float64
	MinLat, MaxLat float64
}

// CenterLat returns the center latitude.
func (b Bounds) CenterLat() float64 {
	return (b.MinLat + b.MaxLat) / 2
}

// Reader provides tile-level access to a COG/GeoTIFF file. Its bytes come
// from a ByteSource (the memory-mapped file for Open), which the tile readers
// use lock-free and concurrently.
type Reader struct {
	bo    binary.ByteOrder
	strip *stripLayout // non-nil for strip-based TIFFs promoted to virtual tiles

	// floodMask, when non-nil, replaces per-pixel nodata-tolerance matching.
	// Bit (y*floodMaskW + x) set ⇒ that source pixel is transparent. Built by
	// BuildFloodMask: it captures the connected component of near-nodata pixels
	// reachable from the COG's outer boundary, so interior dark pixels stay
	// opaque even when --nodata-tolerance is widened.
	floodMask *bitmap

	path       string
	src        ByteSource // the file's bytes: memory-mapped by Open, anything by OpenSource, closedSource after Close
	ifds       []IFD
	geo        GeoInfo
	bandCfg    BandConfig // band selection and rescaling config (set via SetBandConfig)
	id         int        // unique numeric ID for fast cache keying (from nextReaderID, or SetID)
	floodMaskW int
	floodMaskH int
}

// stripLayout stores the original strip layout for strip-based TIFFs.
// Virtual tiles are composed from multiple strips at read time.
// For planar-separate files (PlanarConfiguration=2) the strip sequence is
// plane-major: all of plane 0's strips, then plane 1's, and so on.
type stripLayout struct {
	offsets        []uint64
	byteCounts     []uint64
	rowsPerStrip   uint32
	stripsPerTile  int // number of original strips per virtual tile
	stripsPerPlane int // strips covering one plane (== total strips when chunky)
	planes         int // 1 for chunky; SamplesPerPixel for planar-separate
}

// Open opens a COG/GeoTIFF file by memory-mapping it and parsing its structure.
// If a TFW (TIFF World File) sidecar is found, it is used for georeferencing
// when the TIFF lacks embedded GeoTIFF tags. Strip-based TIFFs are supported
// by converting the strip layout into a virtual tile layout.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	size := fi.Size()
	if size == 0 {
		return nil, fmt.Errorf("%s: empty file", path)
	}

	data, err := mmapFile(f.Fd(), int(size))
	if err != nil {
		return nil, fmt.Errorf("mmap %s: %w", path, err)
	}

	return OpenSource(path, mmapSource(data))
}

// OpenSource opens a COG/GeoTIFF over an arbitrary ByteSource.
//
// Open is this with the file memory-mapped; any other source -- HTTP range
// requests against object storage, say -- goes through the same parser.
// `name` is used for error messages and TFW sidecar lookup; for a remote
// source the sidecar probe simply finds nothing.
//
// OpenSource takes ownership of src: it is closed if OpenSource fails, and by
// Reader.Close otherwise, so the caller must not close it too.
func OpenSource(name string, src ByteSource) (*Reader, error) {
	path := name

	ifds, bo, err := parseTIFF(&sourceReadSeeker{src: src})
	if err != nil {
		src.Close()
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if len(ifds) == 0 {
		src.Close()
		return nil, fmt.Errorf("%s: no IFDs found", path)
	}

	ifds = imageIFDs(ifds)
	first := &ifds[0]
	if first.Width == 0 || first.Height == 0 {
		src.Close()
		return nil, fmt.Errorf("%s: image is %dx%d pixels", path, first.Width, first.Height)
	}

	// Strip-based TIFFs: convert the strip layout into virtual tiles.
	var sl *stripLayout
	if first.TileWidth == 0 || first.TileHeight == 0 {
		if len(first.StripOffsets) > 0 {
			if sl, err = promoteStripsToTiles(first); err != nil {
				src.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		} else {
			src.Close()
			return nil, fmt.Errorf("%s: no tile or strip layout found", path)
		}
	}

	if !supportedCompression(first.Compression) {
		src.Close()
		return nil, fmt.Errorf("%s: unsupported compression type %d", path, first.Compression)
	}

	geo := parseGeoInfo(first)

	// If GeoTIFF tags are absent, try a TFW sidecar. It only gives the pixel
	// grid, so a CRS from the GeoKeys still applies.
	if geo.PixelSizeX == 0 && geo.PixelSizeY == 0 {
		if tfwPath := findTFW(path); tfwPath != "" {
			tfw, err := parseTFW(tfwPath)
			if err != nil {
				src.Close()
				return nil, err
			}
			epsg := geo.EPSG
			geo = tfw.toGeoInfo()
			geo.EPSG = epsg
		}
	}

	// Infer EPSG when GeoKeys didn't provide one. The guess can be badly
	// wrong (any metric grid outside Switzerland reads as Web Mercator), so
	// say so.
	if geo.EPSG == 0 && geo.PixelSizeX > 0 {
		geo.EPSG = inferEPSG(geo, first.Width, first.Height)
		log.Printf("WARNING: %s has no CRS in its GeoTIFF keys; guessed EPSG:%d from the coordinate ranges. If that is wrong, set the source CRS (--source-epsg)", path, geo.EPSG)
	}

	return &Reader{
		src:   src,
		bo:    bo,
		ifds:  ifds,
		geo:   geo,
		path:  path,
		strip: sl,
		id:    int(nextReaderID.Add(1)),
	}, nil
}

// nextReaderID numbers readers across the process, so readers opened
// separately still have distinct tile cache keys.
var nextReaderID atomic.Int64

// promoteStripsToTiles converts a strip-based IFD into a virtual tile layout.
// Small strips are grouped into larger virtual tiles (>= 256 rows) so that
// resampling kernels (e.g. Lanczos 6x6) never span more than 2 tiles.
// Returns the stripLayout needed to reconstruct virtual tiles at read time.
func promoteStripsToTiles(ifd *IFD) (*stripLayout, error) {
	if ifd.Width == 0 || ifd.Height == 0 {
		return nil, fmt.Errorf("image has zero size %dx%d", ifd.Width, ifd.Height)
	}
	if len(ifd.StripByteCounts) < len(ifd.StripOffsets) {
		return nil, fmt.Errorf("StripByteCounts has %d entries, StripOffsets %d",
			len(ifd.StripByteCounts), len(ifd.StripOffsets))
	}
	// 0, and anything past the height (such as the spec's default of
	// 2^32-1 written out explicitly), means a single strip.
	rps := ifd.RowsPerStrip
	if rps == 0 || rps > ifd.Height {
		rps = ifd.Height
	}

	const minTileHeight = 256
	stripsPerTile := 1
	if rps < minTileHeight {
		stripsPerTile = int((minTileHeight + rps - 1) / rps)
	}
	virtualTileH := rps * uint32(stripsPerTile)

	// Planar-separate files store each band's strips consecutively, so the
	// strip count is planes * stripsPerPlane and virtual tiles must be
	// derived from one plane's worth of strips.
	planes := 1
	if ifd.PlanarConfig == 2 && ifd.SamplesPerPixel > 1 {
		planes = int(ifd.SamplesPerPixel)
	}
	stripsPerPlane := (int(ifd.Height) + int(rps) - 1) / int(rps)
	if maxPerPlane := len(ifd.StripOffsets) / planes; stripsPerPlane > maxPerPlane {
		stripsPerPlane = maxPerPlane // malformed file: fewer strips than rows imply
	}
	numVirtualTiles := (stripsPerPlane + stripsPerTile - 1) / stripsPerTile

	virtualOffsets := make([]uint64, numVirtualTiles)
	virtualByteCounts := make([]uint64, numVirtualTiles)
	for i := 0; i < numVirtualTiles; i++ {
		startStrip := i * stripsPerTile
		virtualOffsets[i] = ifd.StripOffsets[startStrip]
		var totalBytes uint64
		endStrip := startStrip + stripsPerTile
		if endStrip > stripsPerPlane {
			endStrip = stripsPerPlane
		}
		for p := 0; p < planes; p++ {
			for s := startStrip; s < endStrip; s++ {
				totalBytes += ifd.StripByteCounts[p*stripsPerPlane+s]
			}
		}
		virtualByteCounts[i] = totalBytes
	}

	sl := &stripLayout{
		offsets:        ifd.StripOffsets,
		byteCounts:     ifd.StripByteCounts,
		rowsPerStrip:   rps,
		stripsPerTile:  stripsPerTile,
		stripsPerPlane: stripsPerPlane,
		planes:         planes,
	}

	ifd.TileWidth = ifd.Width
	ifd.TileHeight = virtualTileH
	ifd.TileOffsets = virtualOffsets
	ifd.TileByteCounts = virtualByteCounts

	return sl, nil
}

// Close closes the underlying ByteSource (unmapping the file for Open).
// Reads after Close return an error, and closing again does nothing.
func (r *Reader) Close() error {
	if r.src == nil {
		return nil
	}
	err := r.src.Close()
	r.src = closedSource{}
	return err
}

// Path returns the file path.
func (r *Reader) Path() string {
	return r.path
}

// ID returns the unique numeric identifier for this reader.
// Used as a fast cache key instead of the file path string.
func (r *Reader) ID() int {
	return r.id
}

// SetID replaces the reader's cache key, e.g. with one that stays stable
// across re-opens of the same remote file. Every reader already has a unique
// positive ID, so SetID is optional; an ID set here must not collide with
// another reader sharing the cache (negative IDs never do). Like
// SetBandConfig, call it before the reader is shared with other goroutines.
func (r *Reader) SetID(id int) { r.id = id }

// GeoInfo returns the parsed geographic metadata.
func (r *Reader) GeoInfo() GeoInfo {
	return r.geo
}

// Width returns the full-resolution image width.
func (r *Reader) Width() int {
	return int(r.ifds[0].Width)
}

// Height returns the full-resolution image height.
func (r *Reader) Height() int {
	return int(r.ifds[0].Height)
}

// PixelSize returns the pixel size in CRS units (from the first IFD).
func (r *Reader) PixelSize() float64 {
	return r.geo.PixelSizeX
}

// NumOverviews returns the number of overview levels: the IFDs beyond the
// first, not counting masks and other IFDs that are not overviews.
func (r *Reader) NumOverviews() int {
	return len(r.ifds) - 1
}

// IFDCount returns the number of levels: the full-resolution IFD plus its
// overviews.
func (r *Reader) IFDCount() int {
	return len(r.ifds)
}

// BoundsInCRS returns the bounding box in the source CRS.
func (r *Reader) BoundsInCRS() (minX, minY, maxX, maxY float64) {
	ifd := &r.ifds[0]
	minX = r.geo.OriginX
	maxY = r.geo.OriginY
	maxX = minX + float64(ifd.Width)*r.geo.PixelSizeX
	minY = maxY - float64(ifd.Height)*r.geo.PixelSizeY
	return
}

// EPSG returns the source CRS: from the GeoKeys, guessed from a world file's
// coordinate ranges, or as set by SetEPSG.
func (r *Reader) EPSG() int {
	return r.geo.EPSG
}

// SetEPSG overrides the source CRS, e.g. when the file carries none and the
// guess from its coordinate ranges is wrong. Like SetBandConfig, call it
// before the reader is shared with other goroutines.
func (r *Reader) SetEPSG(epsg int) {
	r.geo.EPSG = epsg
}

// readTileRaw reads and decompresses raw tile bytes at the given column and row.
// Returns the raw (decompressed) bytes and the IFD for that level.
func (r *Reader) readTileRaw(level, col, row int) ([]byte, *IFD, error) {
	if level < 0 || level >= len(r.ifds) {
		return nil, nil, fmt.Errorf("invalid IFD level %d (have %d)", level, len(r.ifds))
	}

	ifd := &r.ifds[level]
	tilesAcross := ifd.TilesAcross()
	tilesDown := ifd.TilesDown()

	if col < 0 || col >= tilesAcross || row < 0 || row >= tilesDown {
		return nil, nil, fmt.Errorf("tile (%d,%d) out of range (%dx%d)", col, row, tilesAcross, tilesDown)
	}
	if err := r.checkLayout(ifd, level); err != nil {
		return nil, nil, err
	}

	// Strip-based: read individual strips and concatenate.
	if r.strip != nil && level == 0 {
		return r.readStripTileRaw(ifd, row)
	}

	tileIdx := row*tilesAcross + col
	if tileIdx >= len(ifd.TileOffsets) || tileIdx >= len(ifd.TileByteCounts) {
		return nil, nil, fmt.Errorf("tile index %d out of range", tileIdx)
	}

	offset := ifd.TileOffsets[tileIdx]
	size := ifd.TileByteCounts[tileIdx]

	if size == 0 {
		return nil, ifd, nil // empty tile
	}

	data, err := r.slice("tile data", offset, size)
	if err != nil {
		return nil, nil, err
	}

	var decompressed []byte
	switch ifd.Compression {
	case 7: // JPEG — not applicable for float tiles
		return data, ifd, nil
	case 1: // No compression
		if ifd.Predictor == 2 || ifd.Predictor == 3 {
			// data aliases the read-only mapping and applyPredictor rewrites
			// its argument in place, so undo the predictor on a copy.
			decompressed = append([]byte(nil), data...)
		} else {
			decompressed = data
		}
	case 8, 32946: // Deflate / zlib
		dec, err := decompressDeflate(data)
		if err != nil {
			return nil, nil, fmt.Errorf("decompressing deflate tile: %w", err)
		}
		decompressed = dec
	case 5: // LZW
		dec, err := decompressLZW(data)
		if err != nil {
			return nil, nil, fmt.Errorf("decompressing LZW tile: %w", err)
		}
		decompressed = dec
	case 50000: // ZSTD (GDAL/libtiff)
		dec, err := decompressZSTD(data)
		if err != nil {
			return nil, nil, fmt.Errorf("decompressing zstd tile: %w", err)
		}
		decompressed = dec
	default:
		return nil, nil, fmt.Errorf("unsupported compression: %d", ifd.Compression)
	}

	applyPredictor(ifd, decompressed, int(ifd.TileWidth), r.bo)
	return decompressed, ifd, nil
}

// checkLayout rejects the sample layouts the decoders cannot handle -- they
// would loop forever, panic or misread -- so that the first read fails with a
// clear error. No conformant writer produces them: libtiff refuses a
// predictor at depths other than 8, 16, 32 and 64, and planar-separate strips
// of bit-packed samples would have to be unpacked before interleaving.
func (r *Reader) checkLayout(ifd *IFD, level int) error {
	bits := ifd.bitsPerSample()
	switch {
	case bits%8 == 0:
		return nil
	case ifd.Predictor > 1:
		return fmt.Errorf("predictor %d with %d-bit samples is not supported", ifd.Predictor, bits)
	case r.strip != nil && level == 0 && r.strip.planes > 1:
		return fmt.Errorf("planar-separate (PlanarConfiguration=2) strips of %d-bit samples are not supported", bits)
	}
	return nil
}

// readStripTileRaw reads the strips that compose a virtual tile row and
// returns the concatenated, decompressed bytes. Planar-separate strips are
// interleaved into chunky order so downstream decoding sees a normal tile.
func (r *Reader) readStripTileRaw(ifd *IFD, tileRow int) ([]byte, *IFD, error) {
	sl := r.strip
	startStrip := tileRow * sl.stripsPerTile
	endStrip := startStrip + sl.stripsPerTile
	if endStrip > sl.stripsPerPlane {
		endStrip = sl.stripsPerPlane
	}

	if sl.planes == 1 {
		combined, err := r.readStripsRaw(ifd, startStrip, endStrip)
		if err != nil {
			return nil, nil, err
		}
		if len(combined) == 0 {
			return nil, ifd, nil
		}
		applyPredictor(ifd, combined, int(ifd.Width), r.bo)
		return combined, ifd, nil
	}

	// Planar-separate: read each plane's strips for this tile row, then
	// interleave samples. JPEG strips cannot be byte-interleaved.
	if ifd.Compression == 7 {
		return nil, nil, fmt.Errorf("planar-separate (PlanarConfiguration=2) strip TIFFs with JPEG compression are not supported")
	}
	bps := ifd.bytesPerSample()
	planeBufs := make([][]byte, sl.planes)
	planeLen := 0
	for p := 0; p < sl.planes; p++ {
		buf, err := r.readStripsRaw(ifd, p*sl.stripsPerPlane+startStrip, p*sl.stripsPerPlane+endStrip)
		if err != nil {
			return nil, nil, err
		}
		// Each plane row holds one sample per pixel, so the predictor is
		// undone with samplesPerPixel=1 before interleaving.
		switch ifd.Predictor {
		case 2:
			undoHorizontalDifferencing(buf, int(ifd.Width), 1, bps, r.bo)
		case 3:
			undoFloatingPointPredictor(buf, int(ifd.Width), 1, bps, r.bo)
		}
		planeBufs[p] = buf
		planeLen = max(planeLen, len(buf))
	}
	if planeLen == 0 {
		return nil, ifd, nil
	}
	combined := make([]byte, planeLen*sl.planes)
	for p, buf := range planeBufs {
		// A plane whose strips are all sparse is nil and leaves its band zero.
		for i := 0; i+bps <= len(buf); i += bps {
			copy(combined[(i/bps*sl.planes+p)*bps:], buf[i:i+bps])
		}
	}
	return combined, ifd, nil
}

// readStripsRaw decompresses and concatenates strips [start, end).
//
// Every strip contributes exactly its own rows, so rows keep their position
// in the virtual tile. A sparse strip (byte count 0, as GDAL writes with
// SPARSE_OK) becomes zero rows -- the readers mark them nodata through
// sparseRows -- and a strip that decodes short (corrupt LZW, say, which
// decodes without error up to the damage) is an error rather than a shift of
// every later strip. A range of sparse strips only returns nil, like an
// empty tile.
func (r *Reader) readStripsRaw(ifd *IFD, start, end int) ([]byte, error) {
	sl := r.strip
	rowBytes := sl.rowBytes(ifd)

	// Size the buffer up front. Growing an ~10 MB slice strip by strip
	// (RowsPerStrip=1) copies it several times over and contends on the
	// heap lock.
	total := 0
	for s := start; s < end; s++ {
		total += sl.stripRows(s, ifd.Height) * rowBytes
	}
	combined := make([]byte, 0, total)

	sparse := true
	for s := start; s < end; s++ {
		if s >= len(sl.offsets) || s >= len(sl.byteCounts) {
			return nil, fmt.Errorf("strip %d out of range (%d strips)", s, len(sl.offsets))
		}
		want := sl.stripRows(s, ifd.Height) * rowBytes
		offset := sl.offsets[s]
		size := sl.byteCounts[s]
		if size == 0 {
			combined = append(combined, make([]byte, want)...)
			continue
		}
		sparse = false
		chunk, err := r.slice("data", offset, size)
		if err != nil {
			return nil, fmt.Errorf("strip %d: %w", s, err)
		}

		var dec []byte
		switch ifd.Compression {
		case 1: // No compression
			dec = chunk
		case 7: // JPEG: each strip is a separate JPEG stream
			return nil, fmt.Errorf("JPEG-compressed strips cannot be concatenated")
		case 8, 32946: // Deflate / zlib
			if dec, err = decompressDeflate(chunk); err != nil {
				return nil, fmt.Errorf("decompressing deflate strip %d: %w", s, err)
			}
		case 5: // LZW
			if dec, err = decompressLZW(chunk); err != nil {
				return nil, fmt.Errorf("decompressing LZW strip %d: %w", s, err)
			}
		case 50000: // ZSTD
			if dec, err = decompressZSTD(chunk); err != nil {
				return nil, fmt.Errorf("decompressing zstd strip %d: %w", s, err)
			}
		default:
			return nil, fmt.Errorf("unsupported compression: %d", ifd.Compression)
		}
		if len(dec) < want {
			return nil, fmt.Errorf("strip %d decodes to %d bytes, want %d", s, len(dec), want)
		}
		// Longer is fine: some writers pad the last strip to RowsPerStrip.
		combined = append(combined, dec[:want]...)
	}

	if sparse {
		return nil, nil
	}
	return combined, nil
}

// rowBytes returns the byte length of one strip row: one plane's samples for
// planar-separate files, every sample otherwise; bit-packed rows start on a
// byte boundary.
func (sl *stripLayout) rowBytes(ifd *IFD) int {
	spp := max(1, int(ifd.SamplesPerPixel)/sl.planes)
	return (int(ifd.Width)*spp*ifd.bitsPerSample() + 7) / 8
}

// stripRows returns the number of image rows in strip s, counted from the
// start of its plane: RowsPerStrip, or fewer for the last strip.
func (sl *stripLayout) stripRows(s int, height uint32) int {
	y := (s % sl.stripsPerPlane) * int(sl.rowsPerStrip)
	return max(0, min(int(sl.rowsPerStrip), int(height)-y))
}

// sparseRows returns the row ranges [y0, y1) of virtual tile tileRow that lie
// in sparse strips (byte count 0 in every plane). They hold no data, so the
// readers report them as nodata rather than as the zeros that fill them.
func (sl *stripLayout) sparseRows(tileRow int, height uint32) [][2]int {
	var rows [][2]int
	start := tileRow * sl.stripsPerTile
	for s := start; s < min(start+sl.stripsPerTile, sl.stripsPerPlane); s++ {
		sparse := true
		for p := 0; p < sl.planes && sparse; p++ {
			sparse = sl.byteCounts[p*sl.stripsPerPlane+s] == 0
		}
		if sparse {
			y0 := (s - start) * int(sl.rowsPerStrip)
			rows = append(rows, [2]int{y0, y0 + sl.stripRows(s, height)})
		}
	}
	return rows
}

// applyPredictor reverses TIFF predictor encoding on decompressed data.
func applyPredictor(ifd *IFD, data []byte, width int, bo binary.ByteOrder) {
	switch ifd.Predictor {
	case 2:
		undoHorizontalDifferencing(data, width, int(ifd.SamplesPerPixel), ifd.bytesPerSample(), bo)
	case 3:
		undoFloatingPointPredictor(data, width, int(ifd.SamplesPerPixel), ifd.bytesPerSample(), bo)
	}
}

// undoHorizontalDifferencing reverses TIFF predictor=2 (horizontal differencing).
// Each sample is stored as the difference from the previous sample in the same row.
// This accumulates the deltas to recover the original values.
// For multi-byte data, deltas are accumulated at the sample width.
func undoHorizontalDifferencing(data []byte, width, samplesPerPixel, bytesPerSample int, bo binary.ByteOrder) {
	rowBytes := width * samplesPerPixel * bytesPerSample
	if rowBytes <= 0 {
		return // no whole-byte samples (checkLayout rejects a predictor there)
	}
	switch bytesPerSample {
	case 8:
		for off := 0; off+rowBytes <= len(data); off += rowBytes {
			row := data[off : off+rowBytes]
			for x := samplesPerPixel; x < width*samplesPerPixel; x++ {
				byteOff := x * 8
				prevOff := (x - samplesPerPixel) * 8
				cur := bo.Uint64(row[byteOff : byteOff+8])
				prev := bo.Uint64(row[prevOff : prevOff+8])
				bo.PutUint64(row[byteOff:byteOff+8], cur+prev)
			}
		}
	case 4:
		for off := 0; off+rowBytes <= len(data); off += rowBytes {
			row := data[off : off+rowBytes]
			for x := samplesPerPixel; x < width*samplesPerPixel; x++ {
				byteOff := x * 4
				prevOff := (x - samplesPerPixel) * 4
				cur := bo.Uint32(row[byteOff : byteOff+4])
				prev := bo.Uint32(row[prevOff : prevOff+4])
				bo.PutUint32(row[byteOff:byteOff+4], cur+prev)
			}
		}
	case 2:
		for off := 0; off+rowBytes <= len(data); off += rowBytes {
			row := data[off : off+rowBytes]
			for x := samplesPerPixel; x < width*samplesPerPixel; x++ {
				byteOff := x * 2
				prevOff := (x - samplesPerPixel) * 2
				cur := bo.Uint16(row[byteOff : byteOff+2])
				prev := bo.Uint16(row[prevOff : prevOff+2])
				bo.PutUint16(row[byteOff:byteOff+2], cur+prev)
			}
		}
	default:
		// 8-bit path.
		for off := 0; off+rowBytes <= len(data); off += rowBytes {
			row := data[off : off+rowBytes]
			for x := samplesPerPixel; x < rowBytes; x++ {
				row[x] += row[x-samplesPerPixel]
			}
		}
	}
}

// undoFloatingPointPredictor reverses TIFF predictor=3 (floating-point predictor).
// Predictor=3 first byte-shuffles sample bytes (grouping by byte position across
// all samples, most-significant byte plane first regardless of file byte order),
// then applies byte-level horizontal differencing at a stride of samplesPerPixel
// (matching libtiff's fpDiff/fpAcc).
// To reverse: (1) undo byte differencing, (2) unshuffle bytes back into the
// file's byte order so downstream decoding with the file's ByteOrder works.
func undoFloatingPointPredictor(data []byte, width, samplesPerPixel, bytesPerSample int, bo binary.ByteOrder) {
	rowBytes := width * samplesPerPixel * bytesPerSample
	if rowBytes <= 0 {
		return // no whole-byte samples (checkLayout rejects a predictor there)
	}
	tmp := make([]byte, rowBytes)

	for off := 0; off+rowBytes <= len(data); off += rowBytes {
		row := data[off : off+rowBytes]

		// Step 1: Undo byte-level horizontal differencing (stride = spp).
		for i := samplesPerPixel; i < rowBytes; i++ {
			row[i] += row[i-samplesPerPixel]
		}

		// Step 2: Byte-unshuffle.
		// Encoded layout: MSB plane of all samples first, down to the LSB plane.
		// Target layout: consecutive samples in the file's byte order.
		// The interface compare is hoisted: per-byte it was ~30% of decode time.
		sampleCount := width * samplesPerPixel
		little := bo == binary.LittleEndian
		for s := 0; s < sampleCount; s++ {
			for b := 0; b < bytesPerSample; b++ {
				plane := b // big-endian file: byte b is plane b
				if little {
					plane = bytesPerSample - 1 - b
				}
				tmp[s*bytesPerSample+b] = row[plane*sampleCount+s]
			}
		}
		copy(row, tmp)
	}
}

// ReadFloatTile reads and decodes a single float32 tile.
// Returns the float32 data and tile dimensions (width, height).
// For empty tiles, returns nil data.
func (r *Reader) ReadFloatTile(level, col, row int) ([]float32, int, int, error) {
	data, ifd, err := r.readTileRaw(level, col, row)
	if err != nil {
		return nil, 0, 0, err
	}

	w := int(ifd.TileWidth)
	h := int(ifd.TileHeight)

	if data == nil {
		return nil, w, h, nil // empty tile
	}

	px, w, h, err := r.decodeRawFloat32Tile(ifd, data)
	if err == nil && r.strip != nil && level == 0 {
		// Sparse strips hold no data: NaN (nodata), never 0 m.
		nan := float32(math.NaN())
		for _, ys := range r.strip.sparseRows(row, ifd.Height) {
			for i := ys[0] * w; i < ys[1]*w; i++ {
				px[i] = nan
			}
		}
	}
	return px, w, h, err
}

// decodeRawFloat32Tile decodes raw bytes as float32 pixel data.
func (r *Reader) decodeRawFloat32Tile(ifd *IFD, data []byte) ([]float32, int, int, error) {
	w := int(ifd.TileWidth)
	h := int(ifd.TileHeight)
	spp := int(ifd.SamplesPerPixel)
	pixelCount := w * h

	bps := 32
	if len(ifd.BitsPerSample) > 0 {
		bps = int(ifd.BitsPerSample[0])
	}

	bytesPerSample := bps / 8
	expectedSize := pixelCount * spp * bytesPerSample

	// The last virtual tile of a strip TIFF is legitimately short when the
	// image height is not a multiple of the virtual tile height; decode the
	// rows present and leave the rest NaN (outside the image). No other strip
	// tile is short: readStripsRaw rejects a strip that decodes short.
	decodeCount := pixelCount
	if len(data) < expectedSize {
		if r.strip == nil {
			return nil, 0, 0, fmt.Errorf("float tile data too short: got %d, need %d", len(data), expectedSize)
		}
		decodeCount = len(data) / (spp * bytesPerSample)
	}

	// We extract just the first band (elevation).
	result := make([]float32, pixelCount)
	signedInt := len(ifd.SampleFormat) > 0 && ifd.SampleFormat[0] == 2
	for i := 0; i < decodeCount; i++ {
		off := i * spp * bytesPerSample
		switch {
		case signedInt && bps == 16:
			result[i] = float32(int16(r.bo.Uint16(data[off : off+2])))
		case signedInt && bps == 32:
			result[i] = float32(int32(r.bo.Uint32(data[off : off+4])))
		case signedInt:
			return nil, 0, 0, fmt.Errorf("unsupported signed int bits per sample: %d", bps)
		case bps == 32:
			bits := r.bo.Uint32(data[off : off+4])
			result[i] = math.Float32frombits(bits)
		case bps == 64:
			bits := r.bo.Uint64(data[off : off+8])
			result[i] = float32(math.Float64frombits(bits))
		default:
			return nil, 0, 0, fmt.Errorf("unsupported float bits per sample: %d", bps)
		}
	}
	for i := decodeCount; i < pixelCount; i++ {
		result[i] = float32(math.NaN())
	}

	return result, w, h, nil
}

// ReadUint16Tile reads one tile's samples without the rescaling to 8 bits that
// every other integer read path applies. `decodeRawTile` understands 16-bit
// sources perfectly well, but it exists to produce an *image*: it runs every
// sample through `buildRescaler` and hands back an `*image.RGBA`, so the
// original values are gone by the time a caller sees them. `ReadFloatTile` is
// the equivalent escape hatch for float32 data; this is the integer one.
//
// Samples come back chunky, exactly as they are stored: sample s of pixel i is
// at samples[i*spp+s]. 8-bit and bit-packed sources are widened rather than
// rejected, so a caller can read every depth up to 16 bits through one path.
//
// Two things are deliberately left to the caller. Signed data (SampleFormat 2)
// is returned as two's complement sign-extended to 16 bits, so int16(v) is the
// value at every depth, 8 and 15 bits included -- unlike decodeRawTile, which
// folds it into unsigned space via sampleBias so its rescaler can order it; a
// raw accessor that silently shifted values by 32768 would be a trap. And
// nodata is not applied, because for reflectance the sentinel has to be
// tested before any offset is subtracted. (The rows of a sparse strip, which
// hold no samples at all, read as the nodata value, as GDAL reads them.)
//
// An empty tile returns nil samples with the dimensions still filled in, as
// ReadFloatTile does.
func (r *Reader) ReadUint16Tile(level, col, row int) (samples []uint16, w, h, spp int, err error) {
	if level < 0 || level >= len(r.ifds) {
		return nil, 0, 0, 0, fmt.Errorf("invalid IFD level %d (have %d)", level, len(r.ifds))
	}
	// Checked before the read, so that an empty tile cannot hide the format.
	if err := r.checkUint16(&r.ifds[level]); err != nil {
		return nil, 0, 0, 0, err
	}
	data, ifd, err := r.readTileRaw(level, col, row)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	w = int(ifd.TileWidth)
	h = int(ifd.TileHeight)
	spp = int(ifd.SamplesPerPixel)
	if spp <= 0 {
		spp = 1
	}
	if data == nil {
		return nil, w, h, spp, nil // empty tile
	}

	// A strip TIFF's last virtual tile is legitimately short when the height
	// is not a multiple of the tile height; the missing rows are outside the
	// image and are never sampled. A tile is never short.
	samples, rows := r.samples16(ifd, data, w, h, spp)
	if rows < h && r.strip == nil {
		return nil, 0, 0, 0, fmt.Errorf("tile data too short: %d bytes hold %d of %d rows", len(data), rows, h)
	}
	if r.strip != nil && level == 0 {
		// Sparse strips read as the file's nodata value, as GDAL reads them.
		if nd, ok := r.nodata16(ifd); ok {
			for _, ys := range r.strip.sparseRows(row, ifd.Height) {
				for i := ys[0] * w * spp; i < ys[1]*w*spp; i++ {
					samples[i] = nd
				}
			}
		}
	}
	return samples, w, h, spp, nil
}

// checkUint16 reports why ReadUint16Tile cannot read the samples of ifd, if
// it cannot. readTileRaw returns JPEG payloads undecoded, and interleaves
// planar-separate *strips* into chunky order but not planar-separate tiles;
// either would be misread as chunky samples.
func (r *Reader) checkUint16(ifd *IFD) error {
	switch bits := ifd.bitsPerSample(); {
	case len(ifd.SampleFormat) > 0 && ifd.SampleFormat[0] == 3:
		return fmt.Errorf("tile is IEEE float; use ReadFloatTile")
	case ifd.Compression == 7:
		return fmt.Errorf("JPEG-compressed tiles have no raw integer samples")
	case ifd.PlanarConfig == 2 && r.strip == nil && ifd.SamplesPerPixel > 1:
		return fmt.Errorf("planar-separate (PlanarConfiguration=2) tiles are not supported")
	case bits < 1 || bits > 16:
		return fmt.Errorf("unsupported bits per sample: %d", bits)
	}
	return nil
}

// samples16 decodes the w×h×spp samples of a tile 1..16 bits deep to one
// uint16 each. It is the layout decision ReadUint16Tile and decodeRawTile
// share, so that the two read every file alike.
//
// Whole-byte depths are read in the file's byte order. Other depths are
// bit-packed with each row starting on a byte boundary, as the TIFF spec says
// and libtiff and GDAL write them: Planetary Computer's Sentinel-2 L2A
// reflectance is BitsPerSample=15 and its 512x512 tiles are exactly
// 512*512*15/8 bytes. Some writers pad 9..15-bit samples to 16-bit words
// instead; the tag does not say which, but the tile length does. A tie (rows
// too narrow for the two lengths to differ) goes to packed, as the spec
// requires, and sub-byte depths are always packed.
//
// Signed samples narrower than 16 bits are sign-extended, so int16(v) is the
// value at every depth. A short buffer (the last virtual tile of a strip
// TIFF) decodes the whole rows present; rows says how many.
func (r *Reader) samples16(ifd *IFD, data []byte, w, h, spp int) (samples []uint16, rows int) {
	bits := ifd.bitsPerSample()
	perRow := w * spp
	packedRow := (perRow*bits + 7) / 8
	samples = make([]uint16, perRow*h)
	switch {
	case bits == 8:
		rows = min(h, len(data)/max(1, perRow))
		for i := range samples[:rows*perRow] {
			samples[i] = uint16(data[i])
		}
	case bits == 16 || bits > 8 && len(data) >= 2*perRow*h && 2*perRow > packedRow:
		// Whole 16-bit words: 16-bit samples, or 9..15 bits padded to 16.
		rows = min(h, len(data)/max(1, 2*perRow))
		for i := range samples[:rows*perRow] {
			samples[i] = r.bo.Uint16(data[2*i:])
		}
	default:
		rows = min(h, len(data)/max(1, packedRow))
		unpackBits(samples, data, perRow, rows, bits, packedRow)
	}
	if bits < 16 && len(ifd.SampleFormat) > 0 && ifd.SampleFormat[0] == 2 {
		shift := 16 - bits
		for i, v := range samples[:rows*perRow] {
			samples[i] = uint16(int16(v<<shift) >> shift)
		}
	}
	return samples, rows
}

// nodata16 returns the GDAL nodata value as ReadUint16Tile returns samples
// (signed values as two's complement), if it is set and fits the sample type.
func (r *Reader) nodata16(ifd *IFD) (uint16, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(r.ifds[0].NoData), 64)
	lo, hi := 0.0, float64(math.MaxUint16)
	if len(ifd.SampleFormat) > 0 && ifd.SampleFormat[0] == 2 {
		lo, hi = math.MinInt16, math.MaxInt16
	}
	if err != nil || v != math.Trunc(v) || v < lo || v > hi {
		return 0, false
	}
	return uint16(int32(v)), true
}

// unpackBits reads row-aligned, MSB-first packed samples of the given depth.
//
// TIFF packs samples most-significant bit first and restarts each row on a
// byte boundary, independent of the file's byte order -- that governs whole
// words, not the bit stream. FillOrder 2 (LSB first) is vanishingly rare; the
// tag is not read, so such a file would decode wrong.
//
// A sample of at most 16 bits starting at most 7 bits into a byte spans at
// most 23 bits, so it always lies inside a 32-bit window read from the byte
// the sample starts in. That turns the unpack into one big-endian load, one
// shift and one mask per sample, with no loop over bit groups.
//
// This is worth the trouble because of where it sits: Planetary Computer
// publishes Sentinel-2 reflectance bit-packed at 15 bits, so every sample of
// every band tile passes through here. Profiling a band-path chunk, the
// previous bit-at-a-time version was the single hottest function in the
// program at 17% of total CPU -- more than Deflate.
func unpackBits(dst []uint16, data []byte, perRow, rows, bits, rowBytes int) {
	if bits <= 0 || bits > 16 || perRow <= 0 {
		return
	}
	mask := uint32(1)<<uint(bits) - 1

	for y := 0; y < rows; y++ {
		base := y * rowBytes
		row := dst[y*perRow : y*perRow+perRow : y*perRow+perRow]

		// The window must stay inside data. Everything before this sample
		// index is safe, which is every sample but the last few of the last
		// row in practice.
		safe := perRow
		if n := ((len(data) - base - 4) * 8) / bits; n < safe {
			safe = n
		}
		if safe < 0 {
			safe = 0
		}

		off := 0
		for i := 0; i < safe; i++ {
			bo := base + off>>3
			w := binary.BigEndian.Uint32(data[bo : bo+4])
			row[i] = uint16((w >> uint(32-bits-off&7)) & mask)
			off += bits
		}

		// Tail: the last few samples of the last row (or of a truncated
		// strip), where a 32-bit read would run past the buffer. Assemble the
		// window byte by byte, treating bytes past the end as zero.
		for i := safe; i < perRow; i++ {
			bo := base + off>>3
			var w uint32
			for k := 0; k < 4; k++ {
				w <<= 8
				if j := bo + k; j >= 0 && j < len(data) {
					w |= uint32(data[j])
				}
			}
			row[i] = uint16((w >> uint(32-bits-off&7)) & mask)
			off += bits
		}
	}
}

// ReadTile reads and decodes a single tile at the given column and row from the specified IFD level.
// Level 0 is the full resolution; higher levels are overviews.
// This is safe for concurrent use: the source is read-only and ByteSource
// requires concurrent reads to be safe.
func (r *Reader) ReadTile(level, col, row int) (image.Image, error) {
	img, err := r.readTileDecoded(level, col, row)
	if err != nil {
		return nil, err
	}
	// A built flood mask supersedes per-pixel nodata matching. Level 0 maps
	// 1:1 onto the mask; overview levels sample the mask at the center of
	// each overview pixel's level-0 footprint so transparency survives reads
	// through OverviewForZoom (e.g. when --max-zoom is below the source's
	// native resolution).
	if r.floodMask != nil {
		ifd := &r.ifds[level]
		tw := int(ifd.TileWidth)
		th := int(ifd.TileHeight)
		rgba := toRGBA(img)
		if level == 0 {
			r.applyFloodMaskRGBA(rgba, col*tw, row*th, tw, th)
		} else {
			r.applyFloodMaskRGBAScaled(rgba, int(ifd.Width), int(ifd.Height), col*tw, row*th, tw, th)
		}
		return rgba, nil
	}
	return img, nil
}

// readTileDecoded is the raw decode dispatch, without flood-mask post-processing.
func (r *Reader) readTileDecoded(level, col, row int) (image.Image, error) {
	if level < 0 || level >= len(r.ifds) {
		return nil, fmt.Errorf("invalid IFD level %d (have %d)", level, len(r.ifds))
	}

	ifd := &r.ifds[level]
	tilesAcross := ifd.TilesAcross()
	tilesDown := ifd.TilesDown()

	if col < 0 || col >= tilesAcross || row < 0 || row >= tilesDown {
		return nil, fmt.Errorf("tile (%d,%d) out of range (%dx%d)", col, row, tilesAcross, tilesDown)
	}
	if err := r.checkLayout(ifd, level); err != nil {
		return nil, err
	}

	// Strip-based: compose virtual tile from individual strips.
	if r.strip != nil && level == 0 {
		if ifd.Compression == 7 {
			return r.decodeJPEGStripTile(ifd, row)
		}
		data, _, err := r.readStripTileRaw(ifd, row)
		if err != nil {
			return nil, err
		}
		if data == nil {
			return image.NewRGBA(image.Rect(0, 0, int(ifd.TileWidth), int(ifd.TileHeight))), nil
		}
		img, err := r.decodeRawTile(ifd, data)
		if err != nil {
			return nil, err
		}
		// Sparse strips hold no data: transparent, like an empty tile.
		if rgba, ok := img.(*image.RGBA); ok {
			for _, ys := range r.strip.sparseRows(row, ifd.Height) {
				clear(rgba.Pix[ys[0]*rgba.Stride : ys[1]*rgba.Stride])
			}
		}
		return img, nil
	}

	// Planar-separate layout: each band is stored in its own per-tile blob,
	// with offsets/byte-counts laid out plane-major. The chunked layout
	// requires special handling, so dispatch before the standard tile lookup.
	if ifd.PlanarConfig == 2 && ifd.SamplesPerPixel > 1 {
		if ifd.Compression != 7 {
			return nil, fmt.Errorf("planar-separate (PlanarConfiguration=2) is only supported for JPEG-compressed COGs; got compression %d", ifd.Compression)
		}
		return r.decodePlanarSeparateJPEG(ifd, col, row, tilesAcross, tilesDown)
	}

	tileIdx := row*tilesAcross + col
	if tileIdx >= len(ifd.TileOffsets) || tileIdx >= len(ifd.TileByteCounts) {
		return nil, fmt.Errorf("tile index %d out of range", tileIdx)
	}

	offset := ifd.TileOffsets[tileIdx]
	size := ifd.TileByteCounts[tileIdx]

	if size == 0 {
		return image.NewRGBA(image.Rect(0, 0, int(ifd.TileWidth), int(ifd.TileHeight))), nil
	}

	data, err := r.slice("tile data", offset, size)
	if err != nil {
		return nil, err
	}

	switch ifd.Compression {
	case 7: // JPEG
		return r.decodeJPEGTile(ifd, data)
	case 1: // No compression
		if ifd.Predictor == 2 || ifd.Predictor == 3 {
			buf := make([]byte, len(data))
			copy(buf, data)
			applyPredictor(ifd, buf, int(ifd.TileWidth), r.bo)
			return r.decodeRawTile(ifd, buf)
		}
		return r.decodeRawTile(ifd, data)
	case 8, 32946: // Deflate / zlib
		decompressed, err := decompressDeflate(data)
		if err != nil {
			return nil, fmt.Errorf("decompressing deflate tile: %w", err)
		}
		applyPredictor(ifd, decompressed, int(ifd.TileWidth), r.bo)
		return r.decodeRawTile(ifd, decompressed)
	case 5: // LZW
		decompressed, err := decompressLZW(data)
		if err != nil {
			return nil, fmt.Errorf("decompressing LZW tile: %w", err)
		}
		applyPredictor(ifd, decompressed, int(ifd.TileWidth), r.bo)
		return r.decodeRawTile(ifd, decompressed)
	case 50000: // ZSTD
		decompressed, err := decompressZSTD(data)
		if err != nil {
			return nil, fmt.Errorf("decompressing zstd tile: %w", err)
		}
		applyPredictor(ifd, decompressed, int(ifd.TileWidth), r.bo)
		return r.decodeRawTile(ifd, decompressed)
	default:
		return nil, fmt.Errorf("unsupported compression: %d", ifd.Compression)
	}
}

// decompressDeflate decompresses deflate/zlib compressed data.
// TIFF compression 8 uses zlib format (deflate with zlib header).
// Falls back to raw deflate if zlib fails.
func decompressDeflate(data []byte) ([]byte, error) {
	// Try zlib (deflate with 2-byte header) first — this is the TIFF standard.
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err == nil {
		defer r.Close()
		result, err := io.ReadAll(r)
		if err == nil {
			return result, nil
		}
	}

	// Fall back to raw deflate (some writers omit the zlib header).
	fr := flate.NewReader(bytes.NewReader(data))
	defer fr.Close()
	return io.ReadAll(fr)
}

// decompressLZW decompresses TIFF-style LZW compressed data.
// Uses a TIFF-specific LZW decoder that handles the "deferred increment"
// code width behavior required by the TIFF 6.0 spec.
func decompressLZW(data []byte) ([]byte, error) {
	return decompressTIFFLZW(data)
}

// zstdDecoder is shared across goroutines; DecodeAll is safe for concurrent use.
// WithDecoderConcurrency(0) sizes its worker pool to GOMAXPROCS.
var zstdDecoder = func() *zstd.Decoder {
	d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
	if err != nil {
		panic(err)
	}
	return d
}()

func decompressZSTD(data []byte) ([]byte, error) {
	return zstdDecoder.DecodeAll(data, nil)
}

// decodeJPEGTile decodes a JPEG-compressed tile, optionally prepending JPEG tables.
// When BandConfig.HasNodata is set, the result is materialised as RGBA and pixels
// whose channels all match the nodata value (within NodataTolerance) are made
// transparent. Without nodata, the underlying JPEG image is returned as-is to
// preserve the YCbCr fast path in downstream sampling.
func (r *Reader) decodeJPEGTile(ifd *IFD, data []byte) (image.Image, error) {
	img, err := decodeJPEGBytes(ifd, data)
	if err != nil {
		return nil, err
	}
	cfg := r.bandCfg
	// Flood mask, when present, is the authority for transparency — skip the
	// per-pixel tolerance check (which would zero RGB and prevent the flood
	// mask from "reverting" interior-speckle false positives).
	if !cfg.HasNodata || r.floodMask != nil {
		return img, nil
	}
	rgba := toRGBA(img)
	applyNodataMaskRGBA(rgba, uint8(cfg.Nodata), uint8(cfg.NodataTolerance))
	return rgba, nil
}

// decodeJPEGStripTile decodes virtual tile tileRow of a JPEG strip TIFF.
// Every strip is a JPEG stream of its own (abbreviated when JPEGTables is
// set), so the strips are decoded one by one and stacked rather than
// concatenated. Sparse strips stay transparent, rows past the image too, and
// nodata is applied as decodeJPEGTile applies it.
func (r *Reader) decodeJPEGStripTile(ifd *IFD, tileRow int) (image.Image, error) {
	sl := r.strip
	if sl.planes > 1 {
		return nil, fmt.Errorf("planar-separate (PlanarConfiguration=2) strip TIFFs with JPEG compression are not supported")
	}
	w := int(ifd.TileWidth)
	out := image.NewRGBA(image.Rect(0, 0, w, int(ifd.TileHeight)))
	start := tileRow * sl.stripsPerTile
	for s := start; s < min(start+sl.stripsPerTile, sl.stripsPerPlane); s++ {
		offset, size := sl.offsets[s], sl.byteCounts[s]
		if size == 0 {
			continue
		}
		data, err := r.slice("data", offset, size)
		if err != nil {
			return nil, fmt.Errorf("strip %d: %w", s, err)
		}
		img, err := decodeJPEGBytes(ifd, data)
		if err != nil {
			return nil, fmt.Errorf("strip %d: %w", s, err)
		}
		// Clipped to the strip's rows: the last one may be short.
		y0 := (s - start) * int(sl.rowsPerStrip)
		dst := image.Rect(0, y0, w, y0+sl.stripRows(s, ifd.Height))
		draw.Draw(out, dst, img, img.Bounds().Min, draw.Src)
	}
	if cfg := r.bandCfg; cfg.HasNodata && r.floodMask == nil {
		applyNodataMaskRGBA(out, uint8(cfg.Nodata), uint8(cfg.NodataTolerance))
	}
	return out, nil
}

// decodeJPEGBytes is the raw JPEG decode (with JPEGTables prepended if present).
// It does not apply nodata.
func decodeJPEGBytes(ifd *IFD, data []byte) (image.Image, error) {
	var jpegData []byte
	if len(ifd.JPEGTables) > 0 {
		// JPEG tables contain the header with quantization/Huffman tables.
		// Strip the trailing EOI (0xFFD9) from tables and the leading SOI (0xFFD8) from data.
		tables := ifd.JPEGTables
		if len(tables) >= 2 && tables[len(tables)-2] == 0xFF && tables[len(tables)-1] == 0xD9 {
			tables = tables[:len(tables)-2]
		}
		tileData := data
		if len(tileData) >= 2 && tileData[0] == 0xFF && tileData[1] == 0xD8 {
			tileData = tileData[2:]
		}
		jpegData = make([]byte, len(tables)+len(tileData))
		copy(jpegData, tables)
		copy(jpegData[len(tables):], tileData)
	} else {
		jpegData = data
	}
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, fmt.Errorf("decoding JPEG tile: %w", err)
	}
	return img, nil
}

// toRGBA materialises any image.Image into an *image.RGBA. If the input is
// already RGBA the original is returned (no copy).
func toRGBA(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok {
		return rgba
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	// Use draw via image/color to handle YCbCr/Gray/etc generically.
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			rr, gg, bb, aa := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(rr >> 8)
			dst.Pix[i+1] = uint8(gg >> 8)
			dst.Pix[i+2] = uint8(bb >> 8)
			dst.Pix[i+3] = uint8(aa >> 8)
		}
	}
	return dst
}

// applyNodataMaskRGBA zeroes the alpha (and RGB) of any pixel where every RGB
// channel is within tol of nodataVal. RGB is also zeroed so consumers that
// ignore alpha (e.g. JPEG output) at least render a neutral colour.
func applyNodataMaskRGBA(img *image.RGBA, nodataVal, tol uint8) {
	pix := img.Pix
	nd := int(nodataVal)
	t := int(tol)
	for i := 0; i+3 < len(pix); i += 4 {
		if absDiff(int(pix[i+0]), nd) <= t &&
			absDiff(int(pix[i+1]), nd) <= t &&
			absDiff(int(pix[i+2]), nd) <= t {
			pix[i+0] = 0
			pix[i+1] = 0
			pix[i+2] = 0
			pix[i+3] = 0
		}
	}
}

func absDiff(a, b int) int {
	if a >= b {
		return a - b
	}
	return b - a
}

// decodePlanarSeparateJPEG handles PlanarConfig=2 (band-interleaved) JPEG COGs.
// Each band is stored as a separate single-channel JPEG tile; tile offsets are
// laid out plane-major: [plane0 tiles..., plane1 tiles..., ...].
//
// The resulting image is a 4-channel RGBA where R,G,B come from the first three
// planes (or are duplicated from plane 0 when the file is single-band), and
// alpha comes from plane 4 if SamplesPerPixel >= 4. Pixels matching the
// configured nodata value (with tolerance) are made transparent.
func (r *Reader) decodePlanarSeparateJPEG(ifd *IFD, col, row, tilesAcross, tilesDown int) (image.Image, error) {
	tilesPerPlane := tilesAcross * tilesDown
	planes := int(ifd.SamplesPerPixel)
	if planes < 1 {
		return nil, fmt.Errorf("planar separate: invalid SamplesPerPixel %d", planes)
	}
	tw := int(ifd.TileWidth)
	th := int(ifd.TileHeight)

	// Decode each plane's tile into a slice of per-pixel uint8 samples.
	planeSamples := make([][]uint8, planes)
	for p := 0; p < planes; p++ {
		idx := p*tilesPerPlane + row*tilesAcross + col
		if idx >= len(ifd.TileOffsets) || idx >= len(ifd.TileByteCounts) {
			return nil, fmt.Errorf("planar separate: tile index %d out of range (have %d entries)", idx, len(ifd.TileOffsets))
		}
		offset := ifd.TileOffsets[idx]
		size := ifd.TileByteCounts[idx]
		if size == 0 {
			planeSamples[p] = make([]uint8, tw*th) // zero plane
			continue
		}
		raw, err := r.slice("planar separate: tile", offset, size)
		if err != nil {
			return nil, err
		}
		img, err := decodeJPEGBytes(ifd, raw)
		if err != nil {
			return nil, fmt.Errorf("planar separate plane %d: %w", p, err)
		}
		planeSamples[p] = grayBytes(img, tw, th)
	}

	// Merge planes into RGBA. Default mapping: plane 0→R, 1→G, 2→B, 3→A.
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	pix := out.Pix
	hasAlphaPlane := planes >= 4
	for i := 0; i < tw*th; i++ {
		r0 := planeSamples[0][i]
		var g0, b0 uint8 = r0, r0
		if planes >= 2 {
			g0 = planeSamples[1][i]
		}
		if planes >= 3 {
			b0 = planeSamples[2][i]
		}
		var a0 uint8 = 255
		if hasAlphaPlane {
			a0 = planeSamples[3][i]
		}
		j := i * 4
		pix[j+0] = r0
		pix[j+1] = g0
		pix[j+2] = b0
		pix[j+3] = a0
	}

	cfg := r.bandCfg
	if cfg.HasNodata && r.floodMask == nil {
		applyNodataMaskRGBA(out, uint8(cfg.Nodata), uint8(cfg.NodataTolerance))
	}
	return out, nil
}

// grayBytes extracts a tw×th grayscale byte buffer from a decoded image,
// handling *image.Gray, *image.YCbCr (Y plane), and the generic fallback.
func grayBytes(img image.Image, tw, th int) []uint8 {
	out := make([]uint8, tw*th)
	switch im := img.(type) {
	case *image.Gray:
		// Stride may differ from width; copy row by row.
		for y := 0; y < th; y++ {
			srcOff := y * im.Stride
			dstOff := y * tw
			copy(out[dstOff:dstOff+tw], im.Pix[srcOff:srcOff+tw])
		}
	case *image.YCbCr:
		// Single-channel JPEG sometimes decodes as YCbCr 4:4:4 with chroma = 128.
		for y := 0; y < th; y++ {
			for x := 0; x < tw; x++ {
				yi := im.YOffset(x, y)
				out[y*tw+x] = im.Y[yi]
			}
		}
	default:
		b := img.Bounds()
		for y := 0; y < th; y++ {
			for x := 0; x < tw; x++ {
				c := color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
				out[y*tw+x] = c.Y
			}
		}
	}
	return out
}

// decodeRawTile decodes an uncompressed tile.
// Supports 1..16-bit samples (bit-packed below 8 bits and at 9..15, see samples16),
// palette and WhiteIsZero images, band reordering, alpha band selection, and rescaling
// via the reader's BandConfig. For single-band data, pixels matching the GDAL nodata value
// are set to alpha=0 (transparent) so downstream code treats them as empty.
// Zero-value BandConfig produces identical behavior to the legacy code path.
func (r *Reader) decodeRawTile(ifd *IFD, data []byte) (image.Image, error) {
	w := int(ifd.TileWidth)
	h := int(ifd.TileHeight)
	spp := int(ifd.SamplesPerPixel)
	bits := ifd.bitsPerSample()
	// Only 9..16 bits are read as wide samples. Deeper aligned types (24, 32)
	// keep their previous single-byte behaviour rather than being silently
	// reinterpreted here.
	is16 := bits > 8 && bits <= 16
	buf, s16, bps, avail, err := r.pixelSamples(ifd, data, w, h, spp)
	if err != nil {
		return nil, err
	}
	// Only the last virtual tile of a strip TIFF is legitimately short (see
	// decodeRawFloat32Tile); a short tile is corrupt, such as truncated LZW,
	// which decodes without error up to the damage.
	if r.strip == nil && avail < w*h*spp*bps {
		return nil, fmt.Errorf("tile data too short: %d of %d samples", avail/bps, w*h*spp)
	}
	if ifd.Photometric == 3 {
		return r.expandPalette(ifd, buf, s16, avail, w, h, spp)
	}
	pixelBytes := spp * bps

	// Signed 16-bit: samples, nodata and the rescale range all move into the
	// biased unsigned space (see signBias16).
	bias := ifd.sampleBias()
	fbias := float64(bias)

	// Resolve band mapping from config (with defaults).
	cfg := r.bandCfg
	bandR, bandG, bandB := cfg.Bands[0], cfg.Bands[1], cfg.Bands[2]
	if bandR == 0 {
		bandR = 1
	}
	if bandG == 0 {
		bandG = 2
	}
	if bandB == 0 {
		bandB = 3
	}
	// Convert to 0-indexed.
	bandR--
	bandG--
	bandB--
	if spp == 1 {
		// Single-band data renders as gray rather than filling only red.
		bandR, bandG, bandB = 0, 0, 0
	}

	// Resolve alpha band: 0=auto, -1=none, >0=explicit (1-indexed).
	alphaBand := cfg.AlphaBand
	effectiveAlpha := -1 // -1 means no alpha band
	if alphaBand == 0 {
		// Auto: band 4 (0-indexed: 3) for 8-bit spp≥4 with zero-value BandConfig.
		if !is16 && spp >= 4 {
			effectiveAlpha = 3
		}
	} else if alphaBand > 0 {
		effectiveAlpha = alphaBand - 1 // convert to 0-indexed
	}
	// alphaBand == -1 → effectiveAlpha stays -1 (no alpha)

	// Determine if we're in the legacy single-band nodata path.
	// Only for spp≤2, no explicit band config, and no alpha band.
	useLegacyNodata := false
	var hasNodata bool
	var nodataVal uint8
	isDefaultBandCfg := cfg.Bands == [3]int{} && cfg.AlphaBand == 0 && cfg.Rescale == RescaleNone
	if spp <= 2 && isDefaultBandCfg && !is16 && r.floodMask == nil {
		useLegacyNodata = true
		nd := r.ifds[0].NoData
		if nd != "" {
			v, err := strconv.ParseFloat(strings.TrimSpace(nd), 64)
			if err == nil && v >= 0 && v <= 255 && v == math.Floor(v) {
				nodataVal = uint8(v)
				hasNodata = true
			}
		}
	}

	// General-path nodata: prefer BandConfig, fall back to IFD tag.
	// Used for multi-band or 16-bit data not handled by the legacy path.
	// Skipped entirely when a flood mask is present — the mask is authoritative
	// and the loop must leave RGB untouched so the mask can revert false positives.
	var genHasNodata bool
	var genNodataU16 uint16
	var genNodataTol uint16
	if !useLegacyNodata && r.floodMask == nil {
		if nd := cfg.Nodata + fbias; cfg.HasNodata && (nd < 0 || nd > 65535) {
			// Not representable in this raster's sample type: matches nothing.
		} else if cfg.HasNodata {
			genHasNodata = true
			genNodataU16 = uint16(nd)
			if cfg.NodataTolerance > 0 {
				genNodataTol = uint16(cfg.NodataTolerance)
			}
		} else if nd := r.ifds[0].NoData; nd != "" {
			if v, err := strconv.ParseFloat(strings.TrimSpace(nd), 64); err == nil && v+fbias >= 0 && v+fbias <= 65535 && v == math.Floor(v) {
				genHasNodata = true
				genNodataU16 = uint16(v + fbias)
			}
		}
	}

	// Build rescaler. For 9..16-bit data with no explicit rescaling (e.g.
	// coginfo or debug tools using zero-value BandConfig), fall back to the
	// full range of the sample type -- 0..65535, or -32768..32767 when signed
	// -- so values are at least visible rather than uint8-truncated.
	rescaleMode := cfg.Rescale
	rescaleMin := cfg.RescaleMin
	rescaleMax := cfg.RescaleMax
	if is16 && rescaleMode == RescaleNone {
		rescaleMode = RescaleLinear
		rescaleMin, rescaleMax = 0, float64(int(1)<<bits-1)
		if bias != 0 {
			rescaleMin, rescaleMax = -float64(int(1)<<(bits-1)), float64(int(1)<<(bits-1)-1)
		}
	}
	// Tabulated once per tile, so that the per-pixel loop makes no calls: a
	// call per sample (up to four per pixel) spills the loop's registers, and
	// 65536 entries cost less than the calls for a 256x256 tile.
	rescaler := buildRescaler(rescaleMode, rescaleMin+fbias, rescaleMax+fbias)
	lut := make([]uint8, 256)
	if is16 {
		lut = make([]uint8, 1<<16)
	}
	for i := range lut {
		lut[i] = rescaler(uint16(i))
	}
	rescale := func(v uint16) uint8 { return lut[v] }

	// readSample reads one sample from the pixel data at the given 0-indexed band.
	readSample := func(pixelOff, band int) uint16 {
		off := pixelOff + band*bps
		if off+bps > avail {
			return 0
		}
		if is16 {
			return s16[off] ^ uint16(bias)
		}
		return uint16(buf[off])
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	pix := img.Pix

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pixelOff := (y*w + x) * pixelBytes
			if pixelOff+pixelBytes > avail {
				break
			}
			pixIdx := (y*w + x) * 4

			if useLegacyNodata {
				// Legacy path for 8-bit spp≤2 with default config.
				switch spp {
				case 1:
					v := buf[pixelOff]
					pix[pixIdx+0] = v
					pix[pixIdx+1] = v
					pix[pixIdx+2] = v
					if hasNodata && v == nodataVal {
						pix[pixIdx+3] = 0
					} else {
						pix[pixIdx+3] = 255
					}
				case 2:
					v := buf[pixelOff]
					pix[pixIdx+0] = v
					pix[pixIdx+1] = v
					pix[pixIdx+2] = v
					a := buf[pixelOff+1]
					if hasNodata && v == nodataVal {
						a = 0
					}
					pix[pixIdx+3] = a
				}
				continue
			}

			// General path: band reordering + rescaling.
			var rV, gV, bV uint16
			if bandR < spp {
				rV = readSample(pixelOff, bandR)
			}
			if bandG < spp {
				gV = readSample(pixelOff, bandG)
			}
			if bandB < spp {
				bV = readSample(pixelOff, bandB)
			}

			// Nodata check: if all file bands equal the nodata value, emit transparent.
			// Only applies when there is no explicit alpha band (alpha=0 already handles
			// transparency for files with an alpha channel).
			if genHasNodata && effectiveAlpha < 0 {
				isNodata := true
				for b := 0; b < spp; b++ {
					s := readSample(pixelOff, b)
					var diff uint16
					if s >= genNodataU16 {
						diff = s - genNodataU16
					} else {
						diff = genNodataU16 - s
					}
					if diff > genNodataTol {
						isNodata = false
						break
					}
				}
				if isNodata {
					pix[pixIdx+0] = 0
					pix[pixIdx+1] = 0
					pix[pixIdx+2] = 0
					pix[pixIdx+3] = 0
					continue
				}
			}

			// Alpha.
			var a uint8 = 255
			if effectiveAlpha >= 0 && effectiveAlpha < spp {
				rawAlpha := readSample(pixelOff, effectiveAlpha)
				if rawAlpha == 0 {
					// Transparent source pixel: leave as alpha=0.
					pix[pixIdx+0] = 0
					pix[pixIdx+1] = 0
					pix[pixIdx+2] = 0
					pix[pixIdx+3] = 0
					continue
				}
				a = rescale(rawAlpha)
				if a == 0 {
					a = 1 // Avoid fully transparent for non-zero source alpha
				}
			}

			pix[pixIdx+0] = rescale(rV)
			pix[pixIdx+1] = rescale(gV)
			pix[pixIdx+2] = rescale(bV)
			pix[pixIdx+3] = a
		}
	}

	if ifd.whiteIsZero {
		// WhiteIsZero: the smallest value is white. Nodata and the rescale
		// work on the stored values, so the rendered gray is inverted after
		// them, within its range: sub-byte samples without a rescale render
		// as their raw values.
		top := uint8(255)
		if bits < 8 && rescaleMode == RescaleNone {
			top = uint8(1<<bits - 1)
		}
		for i := 0; i < len(pix); i += 4 {
			if pix[i+3] != 0 {
				pix[i], pix[i+1], pix[i+2] = top-pix[i], top-pix[i+1], top-pix[i+2]
			}
		}
	}
	return img, nil
}

// expandPalette renders a palette tile (Photometric 3) through its ColorMap:
// the first sample of each pixel indexes 16-bit red, green and blue tables.
// The band mapping and rescale do not apply to an index; nodata, as in
// GDAL, is an index.
func (r *Reader) expandPalette(ifd *IFD, buf []byte, s16 []uint16, avail, w, h, spp int) (image.Image, error) {
	bits := ifd.bitsPerSample()
	if bits > 16 {
		return nil, fmt.Errorf("palette with %d-bit samples is not supported", bits)
	}
	n := 1 << bits
	cmap := ifd.ColorMap
	if len(cmap) < 3*n {
		return nil, fmt.Errorf("palette image has %d ColorMap entries, want %d", len(cmap), 3*n)
	}
	spp = max(1, spp)

	nodata, hasNodata := -1, false
	if cfg := r.bandCfg; r.floodMask == nil {
		v, err := strconv.ParseFloat(strings.TrimSpace(r.ifds[0].NoData), 64)
		if cfg.HasNodata {
			v, err = cfg.Nodata, nil
		}
		if err == nil && v == math.Trunc(v) && v >= 0 && v < float64(n) {
			nodata, hasNodata = int(v), true
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h && (i+1)*spp <= avail; i++ {
		var idx int
		if s16 != nil {
			idx = int(s16[i*spp]) & (n - 1) // signed samples are sign-extended
		} else {
			idx = int(buf[i*spp]) & (n - 1)
		}
		if hasNodata && idx == nodata {
			continue // transparent
		}
		p := img.Pix[i*4 : i*4+4 : i*4+4]
		p[0], p[1], p[2], p[3] = uint8(cmap[idx]>>8), uint8(cmap[n+idx]>>8), uint8(cmap[2*n+idx]>>8), 255
	}
	return img, nil
}

// pixelSamples lays a tile out for decodeRawTile's per-pixel loop, which
// indexes whole samples at offsets of bps units, avail of them readable:
// bytes of buf, or for 9..16-bit depths words of s16.
//
// Whole-byte depths are used as they are. 9..16-bit samples are decoded once
// by samples16, the layout ReadUint16Tile reads (bit-packed or padded,
// sign-extended), and sub-byte samples the same way, narrowed to a byte each
// for the 8-bit path. The loop thus stays the byte-aligned one, with no
// per-sample layout branch.
func (r *Reader) pixelSamples(ifd *IFD, data []byte, w, h, spp int) (buf []byte, s16 []uint16, bps, avail int, err error) {
	switch bits := ifd.bitsPerSample(); {
	case bits < 1 || bits > 16 && bits%8 != 0:
		return nil, nil, 0, 0, fmt.Errorf("unsupported bits per sample: %d", bits)
	case bits > 8 && bits <= 16:
		s16, rows := r.samples16(ifd, data, w, h, spp)
		return nil, s16, 1, rows * w * spp, nil
	case bits < 8:
		s, rows := r.samples16(ifd, data, w, h, spp)
		buf = make([]byte, rows*w*spp)
		for i := range buf {
			buf[i] = byte(s[i])
		}
		return buf, nil, 1, len(buf), nil
	}
	return data, nil, ifd.bytesPerSample(), len(data), nil
}

// ReadPixelRGBA reads a single pixel at the given coordinates from level 0.
// Returns R, G, B, A values. Coordinates are in pixel space of the full-resolution image.
func (r *Reader) ReadPixelRGBA(px, py int) (uint8, uint8, uint8, uint8, error) {
	ifd := &r.ifds[0]
	tw := int(ifd.TileWidth)
	th := int(ifd.TileHeight)

	col := px / tw
	row := py / th
	localX := px % tw
	localY := py % th

	img, err := r.ReadTile(0, col, row)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	rr, g, b, a := img.At(localX, localY).RGBA()
	return uint8(rr >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8), nil
}

// ReadRegion reads a rectangular region from the specified IFD level and returns it as an RGBA image.
// The coordinates are in pixel space of that IFD level.
func (r *Reader) ReadRegion(level, startX, startY, width, height int) (*image.RGBA, error) {
	if level < 0 || level >= len(r.ifds) {
		return nil, fmt.Errorf("invalid level %d", level)
	}
	ifd := &r.ifds[level]
	tw := int(ifd.TileWidth)
	th := int(ifd.TileHeight)

	dst := image.NewRGBA(image.Rect(0, 0, width, height))

	// Determine which tiles we need to read.
	colStart := startX / tw
	colEnd := (startX + width - 1) / tw
	rowStart := startY / th
	rowEnd := (startY + height - 1) / th

	for row := rowStart; row <= rowEnd; row++ {
		for col := colStart; col <= colEnd; col++ {
			tile, err := r.ReadTile(level, col, row)
			if err != nil {
				return nil, err
			}

			// Compute the overlap region.
			tileMinX := col * tw
			tileMinY := row * th

			srcMinX := max(startX, tileMinX) - tileMinX
			srcMinY := max(startY, tileMinY) - tileMinY
			srcMaxX := min(startX+width, tileMinX+tw) - tileMinX
			srcMaxY := min(startY+height, tileMinY+th) - tileMinY

			dstMinX := max(startX, tileMinX) - startX
			dstMinY := max(startY, tileMinY) - startY

			for y := srcMinY; y < srcMaxY; y++ {
				for x := srcMinX; x < srcMaxX; x++ {
					rr, g, b, a := tile.At(x, y).RGBA()
					dst.SetRGBA(dstMinX+(x-srcMinX), dstMinY+(y-srcMinY), color.RGBA{
						R: uint8(rr >> 8),
						G: uint8(g >> 8),
						B: uint8(b >> 8),
						A: uint8(a >> 8),
					})
				}
			}
		}
	}

	return dst, nil
}

// ReadUint16Region reads a rectangular region as raw samples. ReadRegion
// cannot be reused for this: it goes through color.RGBA and so truncates
// every sample to eight bits by construction, and it assigns band four as
// alpha, neither of which survives a 16-bit multi-band chunk. This mirrors
// its tile-overlap arithmetic exactly and copies uint16 samples instead.
//
// Samples come back chunky, as ReadUint16Tile returns them: sample s of pixel
// i is at samples[i*spp+s], with i counted row-major over the region. Empty
// tiles leave their part of the region zero. The region must lie within the
// image: past its right or bottom edge the samples are whatever the writer
// put in the tile padding. A region of zero width or height reads nothing.
func (r *Reader) ReadUint16Region(level, startX, startY, width, height int) ([]uint16, int, error) {
	if level < 0 || level >= len(r.ifds) {
		return nil, 0, fmt.Errorf("invalid level %d", level)
	}
	ifd := &r.ifds[level]
	tw := int(ifd.TileWidth)
	th := int(ifd.TileHeight)
	spp := int(ifd.SamplesPerPixel)
	if spp <= 0 {
		spp = 1
	}
	if width < 0 || height < 0 {
		return nil, 0, fmt.Errorf("invalid region size %dx%d", width, height)
	}

	dst := make([]uint16, width*height*spp)
	if len(dst) == 0 {
		return dst, spp, nil
	}

	colStart := startX / tw
	colEnd := (startX + width - 1) / tw
	rowStart := startY / th
	rowEnd := (startY + height - 1) / th

	for row := rowStart; row <= rowEnd; row++ {
		for col := colStart; col <= colEnd; col++ {
			// Same IFD, so the same samples per pixel as spp.
			tile, tileW, _, _, err := r.ReadUint16Tile(level, col, row)
			if err != nil {
				return nil, 0, err
			}
			if tile == nil {
				continue // empty tile: leave the region zero
			}

			tileMinX := col * tw
			tileMinY := row * th

			srcMinX := max(startX, tileMinX) - tileMinX
			srcMinY := max(startY, tileMinY) - tileMinY
			srcMaxX := min(startX+width, tileMinX+tw) - tileMinX
			srcMaxY := min(startY+height, tileMinY+th) - tileMinY

			dstMinX := max(startX, tileMinX) - startX
			dstMinY := max(startY, tileMinY) - startY

			run := (srcMaxX - srcMinX) * spp
			for y := srcMinY; y < srcMaxY; y++ {
				so := (y*tileW + srcMinX) * spp
				do := ((dstMinY+(y-srcMinY))*width + dstMinX) * spp
				copy(dst[do:do+run], tile[so:so+run])
			}
		}
	}

	return dst, spp, nil
}

// SampleBilinear samples a pixel at fractional coordinates using bilinear interpolation.
// fx, fy are in pixel coordinates of the given IFD level.
func (r *Reader) SampleBilinear(level int, fx, fy float64) (uint8, uint8, uint8, uint8, error) {
	if level < 0 || level >= len(r.ifds) {
		return 0, 0, 0, 0, fmt.Errorf("invalid level %d", level)
	}

	ifd := &r.ifds[level]
	imgW := int(ifd.Width)
	imgH := int(ifd.Height)

	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := x0 + 1
	y1 := y0 + 1

	// Clamp to image bounds.
	x0 = clampInt(x0, 0, imgW-1)
	y0 = clampInt(y0, 0, imgH-1)
	x1 = clampInt(x1, 0, imgW-1)
	y1 = clampInt(y1, 0, imgH-1)

	dx := fx - math.Floor(fx)
	dy := fy - math.Floor(fy)

	// Read the four surrounding pixels. We need up to 4 tile reads,
	// but often they'll be in the same tile.
	r00, g00, b00, a00, err := r.readPixelFromLevel(level, x0, y0)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	r10, g10, b10, a10, err := r.readPixelFromLevel(level, x1, y0)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	r01, g01, b01, a01, err := r.readPixelFromLevel(level, x0, y1)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	r11, g11, b11, a11, err := r.readPixelFromLevel(level, x1, y1)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	lerp := func(a, b float64, t float64) float64 {
		return a*(1-t) + b*t
	}
	bilerp := func(v00, v10, v01, v11 uint8) uint8 {
		top := lerp(float64(v00), float64(v10), dx)
		bot := lerp(float64(v01), float64(v11), dx)
		return uint8(clampFloat(lerp(top, bot, dy), 0, 255))
	}

	return bilerp(r00, r10, r01, r11),
		bilerp(g00, g10, g01, g11),
		bilerp(b00, b10, b01, b11),
		bilerp(a00, a10, a01, a11), nil
}

func (r *Reader) readPixelFromLevel(level, px, py int) (uint8, uint8, uint8, uint8, error) {
	ifd := &r.ifds[level]
	tw := int(ifd.TileWidth)
	th := int(ifd.TileHeight)

	col := px / tw
	row := py / th
	localX := px % tw
	localY := py % th

	img, err := r.ReadTile(level, col, row)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	rr, g, b, a := img.At(localX, localY).RGBA()
	return uint8(rr >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8), nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// DebugIFD returns the raw IFD for debugging purposes.
func (r *Reader) DebugIFD(level int) IFD {
	return r.ifds[level]
}

// RawBytes returns a copy of up to n bytes of the source starting at offset,
// or nil when offset is at or past the end or the read fails.
func (r *Reader) RawBytes(offset uint64, n int) []byte {
	size := uint64(r.src.Size())
	if offset >= size || n <= 0 {
		return nil
	}
	end := offset + uint64(n) // offset < size <= MaxInt64, so no overflow
	if end > size {
		end = size
	}
	b, err := r.src.Slice(offset, end)
	if err != nil {
		return nil
	}
	return append([]byte(nil), b...)
}

// OpenAll opens multiple COG files and returns their readers.
// It first validates that all files exist and are readable before opening any,
// so the user is informed about all missing or inaccessible files upfront.
func OpenAll(paths []string) ([]*Reader, error) {
	// Pre-validate: check that every file exists and is accessible before
	// doing any expensive parsing. This ensures the user learns about all
	// missing files at once instead of discovering them one at a time.
	var missing []string
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf("%d of %d input file(s) cannot be accessed:\n", len(missing), len(paths))
		for _, p := range missing {
			msg += fmt.Sprintf("  - %s\n", p)
		}
		msg += "Aborting to avoid holes in the output."
		return nil, fmt.Errorf("%s", msg)
	}

	readers := make([]*Reader, 0, len(paths))
	for _, p := range paths {
		r, err := Open(p)
		if err != nil {
			// Close any already-opened readers.
			for _, rr := range readers {
				rr.Close()
			}
			return nil, fmt.Errorf("failed to open %s: %w", p, err)
		}
		readers = append(readers, r)
	}
	return readers, nil
}

// CoverageGap describes a rectangular region within the merged bounding box
// that is not covered by any input file.
type CoverageGap struct {
	MinX, MinY, MaxX, MaxY float64 // in source CRS coordinates
}

// CheckCoverageGaps analyzes the geographic coverage of the given sources
// and detects holes (areas within the merged bounding box not covered by any file).
// Returns nil if coverage is complete or there is only one source.
func CheckCoverageGaps(sources []*Reader) []CoverageGap {
	if len(sources) <= 1 {
		return nil
	}

	type bbox struct {
		minX, minY, maxX, maxY float64
	}

	boxes := make([]bbox, len(sources))
	mergedMinX, mergedMinY := math.MaxFloat64, math.MaxFloat64
	mergedMaxX, mergedMaxY := -math.MaxFloat64, -math.MaxFloat64
	var totalW, totalH float64

	for i, src := range sources {
		minX, minY, maxX, maxY := src.BoundsInCRS()
		boxes[i] = bbox{minX, minY, maxX, maxY}
		if minX < mergedMinX {
			mergedMinX = minX
		}
		if minY < mergedMinY {
			mergedMinY = minY
		}
		if maxX > mergedMaxX {
			mergedMaxX = maxX
		}
		if maxY > mergedMaxY {
			mergedMaxY = maxY
		}
		totalW += maxX - minX
		totalH += maxY - minY
	}

	avgW := totalW / float64(len(sources))
	avgH := totalH / float64(len(sources))
	if avgW <= 0 || avgH <= 0 {
		return nil
	}

	// Grid cell size: half the average file extent so we can detect
	// single-file-sized holes.
	cellW := avgW / 2
	cellH := avgH / 2

	nx := int(math.Ceil((mergedMaxX - mergedMinX) / cellW))
	ny := int(math.Ceil((mergedMaxY - mergedMinY) / cellH))

	// Cap grid size to keep the check fast.
	const maxGrid = 2000
	if nx > maxGrid {
		cellW = (mergedMaxX - mergedMinX) / maxGrid
		nx = maxGrid
	}
	if ny > maxGrid {
		cellH = (mergedMaxY - mergedMinY) / maxGrid
		ny = maxGrid
	}
	if nx <= 0 || ny <= 0 {
		return nil
	}

	// Build a coverage grid: mark each cell whose center is inside at least one source.
	covered := make([]bool, nx*ny)
	for iy := 0; iy < ny; iy++ {
		cy := mergedMinY + (float64(iy)+0.5)*cellH
		for ix := 0; ix < nx; ix++ {
			cx := mergedMinX + (float64(ix)+0.5)*cellW
			for _, b := range boxes {
				if cx >= b.minX && cx <= b.maxX && cy >= b.minY && cy <= b.maxY {
					covered[iy*nx+ix] = true
					break
				}
			}
		}
	}

	// Flood-fill uncovered cells into contiguous gap regions.
	visited := make([]bool, nx*ny)
	var gaps []CoverageGap

	for iy := 0; iy < ny; iy++ {
		for ix := 0; ix < nx; ix++ {
			idx := iy*nx + ix
			if covered[idx] || visited[idx] {
				continue
			}
			// BFS to find contiguous uncovered region.
			gapMinX, gapMinY := math.MaxFloat64, math.MaxFloat64
			gapMaxX, gapMaxY := -math.MaxFloat64, -math.MaxFloat64
			queue := [][2]int{{ix, iy}}
			visited[idx] = true

			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				cx := cur[0]
				cy := cur[1]

				// Expand the gap bounding box.
				cellMinX := mergedMinX + float64(cx)*cellW
				cellMinY := mergedMinY + float64(cy)*cellH
				cellMaxX := cellMinX + cellW
				cellMaxY := cellMinY + cellH
				if cellMinX < gapMinX {
					gapMinX = cellMinX
				}
				if cellMinY < gapMinY {
					gapMinY = cellMinY
				}
				if cellMaxX > gapMaxX {
					gapMaxX = cellMaxX
				}
				if cellMaxY > gapMaxY {
					gapMaxY = cellMaxY
				}

				// Visit neighbors.
				for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
					nx2 := cx + d[0]
					ny2 := cy + d[1]
					if nx2 >= 0 && nx2 < nx && ny2 >= 0 && ny2 < ny {
						nIdx := ny2*nx + nx2
						if !covered[nIdx] && !visited[nIdx] {
							visited[nIdx] = true
							queue = append(queue, [2]int{nx2, ny2})
						}
					}
				}
			}
			gaps = append(gaps, CoverageGap{gapMinX, gapMinY, gapMaxX, gapMaxY})
		}
	}

	return gaps
}

// MergedBoundsWGS84 computes the WGS84 bounding box that covers all sources.
// Sources with an unknown projection are assumed to already be in WGS84.
// Corners the projection cannot transform are skipped; it panics if no
// corner of any source transforms, since every bound would be garbage.
// MaxLon exceeds 180 when the sources cross the antimeridian (see
// coord.WrapLonRange).
func MergedBoundsWGS84(sources []*Reader) Bounds {
	if len(sources) == 0 {
		return Bounds{}
	}

	// Projected longitudes may lie outside ±180 (UTM zone 60, 0..360 grids).
	merged := Bounds{
		MinLon: math.Inf(1),
		MaxLon: math.Inf(-1),
		MinLat: 90,
		MaxLat: -90,
	}

	// Sources usually share one CRS; build each projection once.
	projs := map[int]coord.Projection{}
	finite := 0

	for _, src := range sources {
		minX, minY, maxX, maxY := src.BoundsInCRS()
		epsg := src.EPSG()
		proj, ok := projs[epsg]
		if !ok {
			proj = coord.ForEPSG(epsg)
			if proj == nil {
				// Assume the coordinates are already in WGS84 as a fallback.
				proj = &coord.WGS84Identity{}
			}
			projs[epsg] = proj
		}

		// Convert corners to WGS84.
		corners := [][2]float64{
			{minX, minY},
			{minX, maxY},
			{maxX, minY},
			{maxX, maxY},
		}

		for _, c := range corners {
			lon, lat := proj.ToWGS84(c[0], c[1])
			if math.IsNaN(lon) || math.IsInf(lon, 0) || math.IsNaN(lat) || math.IsInf(lat, 0) {
				continue
			}
			finite++

			if lon < merged.MinLon {
				merged.MinLon = lon
			}
			if lon > merged.MaxLon {
				merged.MaxLon = lon
			}
			if lat < merged.MinLat {
				merged.MinLat = lat
			}
			if lat > merged.MaxLat {
				merged.MaxLat = lat
			}
		}
	}
	if finite == 0 {
		panic(fmt.Sprintf("cog: no corner of %d source(s) in EPSG:%d transforms to WGS84", len(sources), sources[0].EPSG()))
	}
	merged.MinLon, merged.MaxLon = coord.WrapLonRange(merged.MinLon, merged.MaxLon)

	return merged
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// OverviewForZoom returns the best IFD level to use for the given output pixel size.
// outputPixelSizeCRS must be in the same units as the source CRS (e.g. meters for
// metric projections, degrees for EPSG:4326).
func (r *Reader) OverviewForZoom(outputPixelSizeCRS float64) int {
	bestLevel := 0
	bestRatio := math.Inf(1)

	for i, ifd := range r.ifds {
		// Compute the pixel size at this IFD level (in CRS units).
		levelPixelSize := r.geo.PixelSizeX * float64(r.ifds[0].Width) / float64(ifd.Width)
		ratio := math.Abs(levelPixelSize/outputPixelSizeCRS - 1)
		if ratio < bestRatio {
			bestRatio = ratio
			bestLevel = i
		}
	}

	return bestLevel
}

func (r *Reader) IFDPixelSize(level int) float64 {
	return r.geo.PixelSizeX * float64(r.ifds[0].Width) / float64(r.ifds[level].Width)
}

// IFDPixelSizeY returns the pixel size in the Y direction (CRS units) for the
// given IFD level. Unlike IFDPixelSize which uses Width-based scaling, this
// uses Height-based scaling for non-square pixels.
func (r *Reader) IFDPixelSizeY(level int) float64 {
	return math.Abs(r.geo.PixelSizeY) * float64(r.ifds[0].Height) / float64(r.ifds[level].Height)
}

func (r *Reader) IFDWidth(level int) int {
	return int(r.ifds[level].Width)
}

func (r *Reader) IFDHeight(level int) int {
	return int(r.ifds[level].Height)
}

// IFDTileSize returns [tileWidth, tileHeight] for the given IFD level.
func (r *Reader) IFDTileSize(level int) [2]int {
	return [2]int{int(r.ifds[level].TileWidth), int(r.ifds[level].TileHeight)}
}

// FormatDescription returns a human-readable summary of the raster format,
// e.g. "LZW, 3x uint8" or "Deflate, 1x float32".
func (r *Reader) FormatDescription() string {
	ifd := &r.ifds[0]

	comp := "unknown"
	switch ifd.Compression {
	case 1:
		comp = "uncompressed"
	case 5:
		comp = "LZW"
	case 7:
		comp = "JPEG"
	case 8, 32946:
		comp = "Deflate"
	case 50000:
		comp = "ZSTD"
	}

	spp := int(ifd.SamplesPerPixel)
	bps := 8
	if len(ifd.BitsPerSample) > 0 {
		bps = int(ifd.BitsPerSample[0])
	}

	sampleType := "uint"
	if len(ifd.SampleFormat) > 0 && ifd.SampleFormat[0] == 2 {
		sampleType = "int"
	} else if r.IsFloat() {
		sampleType = "float"
	}

	return fmt.Sprintf("%s, %dx %s%d", comp, spp, sampleType, bps)
}

// IsFloat returns true if the raster is read through the float (elevation)
// path: IEEE floating point, or signed integer (e.g. GEBCO Int16 bathymetry),
// which has no meaningful uint/RGB interpretation.
func (r *Reader) IsFloat() bool {
	ifd := &r.ifds[0]
	if len(ifd.SampleFormat) == 0 {
		return false
	}
	return ifd.SampleFormat[0] == 3 || ifd.SampleFormat[0] == 2 // 3 = IEEE float, 2 = signed int
}

// IsIEEEFloat reports whether the samples are IEEE floating point
// (SampleFormat 3), as opposed to signed integers.
func (r *Reader) IsIEEEFloat() bool {
	sf := r.ifds[0].SampleFormat
	return len(sf) > 0 && sf[0] == 3
}

// NoData returns the GDAL nodata string, or "" if not set.
func (r *Reader) NoData() string {
	return r.ifds[0].NoData
}

// SetBandConfig sets the band selection and rescaling configuration.
// Must be called after OpenAll() and before any ReadTile() calls.
func (r *Reader) SetBandConfig(cfg BandConfig) {
	r.bandCfg = cfg
}

// BitsPerSample returns the bits per sample of the first IFD (e.g. 8, 16).
func (r *Reader) BitsPerSample() int {
	if len(r.ifds[0].BitsPerSample) > 0 {
		return int(r.ifds[0].BitsPerSample[0])
	}
	return 8
}

// SamplesPerPixel returns the samples per pixel of the first IFD.
func (r *Reader) SamplesPerPixel() int {
	return int(r.ifds[0].SamplesPerPixel)
}

// GDALMeta returns the parsed GDAL_METADATA from tag 42112.
// Returns nil if the tag was not present.
func (r *Reader) GDALMeta() *GDALMeta {
	return r.ifds[0].GDALMetadata
}

// Preset describes auto-detected settings derived from the GeoTIFF.
type Preset struct {
	Name    string     // e.g. "multispectral-rgbnir", "float-terrarium"
	Format  string     // suggested output format ("terrarium", ""), empty = no override
	BandCfg BandConfig // fully configured bands, rescale, alpha (zero value for float)
}

// bandRoleKeywords maps canonical color roles to GDAL band DESCRIPTION keywords.
// Matching is case-insensitive. The first keyword match wins for each role.
var bandRoleKeywords = map[string][]string{
	"red":   {"red"},
	"green": {"green"},
	"blue":  {"blue"},
	"nir":   {"nir", "near-infrared", "near infrared", "infrared"},
}

// bandItemRe matches "Band N: BXX (Role)" in a dataset-level "bands" string.
var bandItemRe = regexp.MustCompile(`Band\s+(\d+):\s+\S+\s+\((\w+)\)`)

// DetectPreset examines the GeoTIFF structure and GDAL metadata to return a Preset.
// Detection covers:
//   - Float data (elevation/DEM) → terrarium format
//   - Multi-band with band descriptions → auto band mapping + rescaling
func (r *Reader) DetectPreset() (Preset, bool) {
	// Float data → terrarium encoding.
	if r.IsFloat() {
		return Preset{Name: "float-terrarium", Format: "terrarium"}, true
	}

	// Multi-band with GDAL metadata → auto band mapping + rescaling.
	md := r.GDALMeta()
	if md == nil {
		return Preset{}, false
	}

	roleToFileBand := r.detectBandRoles(md)

	redBand, okR := roleToFileBand["red"]
	greenBand, okG := roleToFileBand["green"]
	blueBand, okB := roleToFileBand["blue"]
	if !okR || !okG || !okB {
		return Preset{}, false
	}

	rescaleMin, rescaleMax := r.detectScale(md)

	name := "multispectral"
	if _, hasNIR := roleToFileBand["nir"]; hasNIR {
		name += "-rgbnir"
	} else {
		name += "-rgb"
	}

	cfg := BandConfig{
		Bands:      [3]int{redBand, greenBand, blueBand},
		AlphaBand:  -1,
		Rescale:    RescaleLinear,
		RescaleMin: rescaleMin,
		RescaleMax: rescaleMax,
	}

	// Include nodata from the GeoTIFF tag so the preset is self-contained.
	if nd := r.ifds[0].NoData; nd != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(nd), 64); err == nil && v >= 0 && v <= 65535 && v == math.Floor(v) {
			cfg.HasNodata = true
			cfg.Nodata = v
		}
	}

	return Preset{Name: name, BandCfg: cfg}, true
}

// detectBandRoles maps color roles (red, green, blue, nir) to 1-indexed file bands
// by examining GDAL metadata. Checks per-band DESCRIPTION items first, then falls
// back to parsing a dataset-level "bands" string.
func (r *Reader) detectBandRoles(md *GDALMeta) map[string]int {
	roleToFileBand := make(map[string]int)

	// Strategy 1: per-band DESCRIPTION items.
	for sample, items := range md.BandItems {
		desc := strings.ToLower(strings.TrimSpace(items["DESCRIPTION"]))
		if desc == "" {
			continue
		}
		for role, keywords := range bandRoleKeywords {
			if _, exists := roleToFileBand[role]; exists {
				continue
			}
			for _, kw := range keywords {
				if strings.Contains(desc, kw) {
					roleToFileBand[role] = sample + 1
					break
				}
			}
		}
	}

	// If we found at least RGB, we're done.
	if _, okR := roleToFileBand["red"]; okR {
		if _, okG := roleToFileBand["green"]; okG {
			if _, okB := roleToFileBand["blue"]; okB {
				return roleToFileBand
			}
		}
	}

	// Strategy 2: dataset-level "bands" string, e.g.
	// "Band 1: B04 (Red), Band 2: B03 (Green), Band 3: B02 (Blue), Band 4: B08 (Infrared)"
	if bandsStr := md.Items["bands"]; bandsStr != "" {
		matches := bandItemRe.FindAllStringSubmatch(bandsStr, -1)
		for _, m := range matches {
			bandIdx, _ := strconv.Atoi(m[1])
			role := strings.ToLower(m[2])
			// Map the role through our keyword table.
			for canonRole, keywords := range bandRoleKeywords {
				if _, exists := roleToFileBand[canonRole]; exists {
					continue
				}
				for _, kw := range keywords {
					if strings.Contains(role, kw) {
						roleToFileBand[canonRole] = bandIdx
						break
					}
				}
			}
		}
	}

	return roleToFileBand
}

// detectScale reads SCALE and OFFSET from GDAL metadata and returns the rescale
// range [min, max] for mapping to [0, 255]. Checks per-band items first (sample 0),
// then dataset-level items.
func (r *Reader) detectScale(md *GDALMeta) (float64, float64) {
	var scaleStr, offsetStr string

	// Per-band SCALE/OFFSET (sample 0) take precedence.
	if items, ok := md.BandItems[0]; ok {
		scaleStr = items["SCALE"]
		offsetStr = items["OFFSET"]
	}
	// Fall back to dataset-level.
	if scaleStr == "" {
		scaleStr = md.Items["SCALE"]
	}
	if offsetStr == "" {
		offsetStr = md.Items["OFFSET"]
	}

	if scaleStr == "" {
		return 0, 10000 // sensible default for reflectance data
	}

	scale, err := strconv.ParseFloat(strings.TrimSpace(scaleStr), 64)
	if err != nil || scale <= 0 {
		return 0, 10000
	}

	maxVal := math.Round(1.0 / scale) // e.g. 0.0001 → 10000

	var offset float64
	if offsetStr != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(offsetStr), 64); err == nil {
			offset = v
		}
	}

	// Offset adjusts the min value: reflectance = (DN + offset) * scale
	// So DN range for [0, 1] reflectance is [-offset/scale, (1-offset*scale)/scale]
	// Simplified: min = -offset/scale (if offset < 0, e.g. Landsat: offset=-0.2, scale=0.0000275)
	minVal := 0.0
	if offset < 0 {
		minVal = math.Round(-offset / scale)
	}

	return minVal, maxVal
}

// buildRescaler returns a function that maps uint16 input values to uint8 output.
func buildRescaler(mode RescaleMode, minVal, maxVal float64) func(uint16) uint8 {
	switch mode {
	case RescaleLinear:
		if maxVal == minVal {
			return func(uint16) uint8 { return 0 }
		}
		scale := 255.0 / (maxVal - minVal)
		return func(v uint16) uint8 {
			f := float64(v)
			if f < minVal {
				return 0
			}
			if f > maxVal {
				return 255
			}
			return uint8(math.Round((f - minVal) * scale))
		}
	case RescaleLog:
		if maxVal == minVal {
			return func(uint16) uint8 { return 0 }
		}
		logRange := math.Log(1 + maxVal - minVal)
		scale := 255.0 / logRange
		return func(v uint16) uint8 {
			f := float64(v)
			if f < minVal {
				return 0
			}
			if f > maxVal {
				return 255
			}
			return uint8(math.Round(math.Log(1+f-minVal) * scale))
		}
	default: // RescaleNone
		return func(v uint16) uint8 { return uint8(v) }
	}
}
