package cog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"syscall"
)

// Bounds represents geographic bounds in WGS84.
type Bounds struct {
	MinLon, MaxLon float64
	MinLat, MaxLat float64
}

// CenterLat returns the center latitude.
func (b Bounds) CenterLat() float64 {
	return (b.MinLat + b.MaxLat) / 2
}

// Reader provides tile-level access to a COG/GeoTIFF file.
// The file is memory-mapped for lock-free concurrent access.
type Reader struct {
	data     []byte // memory-mapped file contents
	bo       binary.ByteOrder
	ifds     []IFD
	geo      GeoInfo
	path     string
}

// Open opens a COG/GeoTIFF file by memory-mapping it and parsing its structure.
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

	// Memory-map the entire file read-only. The fd can be closed after mmap.
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("mmap %s: %w", path, err)
	}

	// Parse TIFF structure from the memory-mapped data.
	ifds, bo, err := parseTIFF(bytes.NewReader(data))
	if err != nil {
		syscall.Munmap(data)
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if len(ifds) == 0 {
		syscall.Munmap(data)
		return nil, fmt.Errorf("%s: no IFDs found", path)
	}

	// Validate that the first IFD has tile layout.
	first := &ifds[0]
	if first.TileWidth == 0 || first.TileHeight == 0 {
		syscall.Munmap(data)
		return nil, fmt.Errorf("%s: not a tiled TIFF (no TileWidth/TileHeight)", path)
	}

	geo := parseGeoInfo(first)

	return &Reader{
		data: data,
		bo:   bo,
		ifds: ifds,
		geo:  geo,
		path: path,
	}, nil
}

// Close unmaps the memory-mapped file.
func (r *Reader) Close() error {
	if r.data != nil {
		err := syscall.Munmap(r.data)
		r.data = nil
		return err
	}
	return nil
}

// Path returns the file path.
func (r *Reader) Path() string {
	return r.path
}

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

// NumOverviews returns the number of overview levels (IFDs beyond the first).
func (r *Reader) NumOverviews() int {
	return len(r.ifds) - 1
}

// IFDCount returns the total number of IFDs.
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

// EPSG returns the detected EPSG code.
func (r *Reader) EPSG() int {
	return r.geo.EPSG
}

// ReadTile reads and decodes a single tile at the given column and row from the specified IFD level.
// Level 0 is the full resolution; higher levels are overviews.
// This is safe for concurrent use — the underlying data is memory-mapped read-only.
func (r *Reader) ReadTile(level, col, row int) (image.Image, error) {
	if level < 0 || level >= len(r.ifds) {
		return nil, fmt.Errorf("invalid IFD level %d (have %d)", level, len(r.ifds))
	}

	ifd := &r.ifds[level]
	tilesAcross := ifd.TilesAcross()
	tilesDown := ifd.TilesDown()

	if col < 0 || col >= tilesAcross || row < 0 || row >= tilesDown {
		return nil, fmt.Errorf("tile (%d,%d) out of range (%dx%d)", col, row, tilesAcross, tilesDown)
	}

	tileIdx := row*tilesAcross + col
	if tileIdx >= len(ifd.TileOffsets) || tileIdx >= len(ifd.TileByteCounts) {
		return nil, fmt.Errorf("tile index %d out of range", tileIdx)
	}

	offset := ifd.TileOffsets[tileIdx]
	size := ifd.TileByteCounts[tileIdx]

	if size == 0 {
		// Empty tile, return a blank image.
		return image.NewRGBA(image.Rect(0, 0, int(ifd.TileWidth), int(ifd.TileHeight))), nil
	}

	// Bounds-check the slice into memory-mapped data.
	end := offset + size
	if end > uint64(len(r.data)) {
		return nil, fmt.Errorf("tile data [%d:%d] exceeds file size %d", offset, end, len(r.data))
	}

	// Direct slice from memory-mapped region — no syscall, no lock.
	data := r.data[offset:end]

	// Decode based on compression.
	switch ifd.Compression {
	case 7: // JPEG
		return r.decodeJPEGTile(ifd, data)
	case 1: // No compression
		return r.decodeRawTile(ifd, data)
	case 8, 32946: // Deflate
		return nil, fmt.Errorf("deflate compression not yet supported")
	default:
		return nil, fmt.Errorf("unsupported compression: %d", ifd.Compression)
	}
}

// decodeJPEGTile decodes a JPEG-compressed tile, optionally prepending JPEG tables.
func (r *Reader) decodeJPEGTile(ifd *IFD, data []byte) (image.Image, error) {
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

// decodeRawTile decodes an uncompressed tile.
func (r *Reader) decodeRawTile(ifd *IFD, data []byte) (image.Image, error) {
	w := int(ifd.TileWidth)
	h := int(ifd.TileHeight)
	spp := int(ifd.SamplesPerPixel)

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := (y*w + x) * spp
			if idx+spp > len(data) {
				break
			}
			var c color.RGBA
			c.R = data[idx]
			if spp > 1 {
				c.G = data[idx+1]
			}
			if spp > 2 {
				c.B = data[idx+2]
			}
			if spp > 3 {
				c.A = data[idx+3]
			} else {
				c.A = 255
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img, nil
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

// OpenAll opens multiple COG files and returns their readers.
func OpenAll(paths []string) ([]*Reader, error) {
	readers := make([]*Reader, 0, len(paths))
	for _, p := range paths {
		r, err := Open(p)
		if err != nil {
			// Close any already-opened readers.
			for _, rr := range readers {
				rr.Close()
			}
			return nil, err
		}
		readers = append(readers, r)
	}
	return readers, nil
}

// MergedBoundsWGS84 computes the WGS84 bounding box that covers all sources.
// Requires that sources have a known projection (currently supports EPSG:2056).
func MergedBoundsWGS84(sources []*Reader) Bounds {
	if len(sources) == 0 {
		return Bounds{}
	}

	merged := Bounds{
		MinLon: 180,
		MaxLon: -180,
		MinLat: 90,
		MaxLat: -90,
	}

	for _, src := range sources {
		minX, minY, maxX, maxY := src.BoundsInCRS()
		epsg := src.EPSG()

		// Convert corners to WGS84.
		corners := [][2]float64{
			{minX, minY},
			{minX, maxY},
			{maxX, minY},
			{maxX, maxY},
		}

		for _, c := range corners {
			var lon, lat float64
			switch epsg {
			case 2056:
				lon, lat = lv95ToWGS84(c[0], c[1])
			case 4326:
				lon, lat = c[0], c[1]
			case 3857:
				lon, lat = webMercatorToWGS84(c[0], c[1])
			default:
				// Assume the coordinates are already in WGS84 as a fallback.
				lon, lat = c[0], c[1]
			}

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

	return merged
}

// lv95ToWGS84 converts Swiss LV95 (EPSG:2056) coordinates to WGS84 lon/lat.
// Uses swisstopo approximate formulas.
func lv95ToWGS84(easting, northing float64) (lon, lat float64) {
	y := (easting - 2_600_000) / 1_000_000
	x := (northing - 1_200_000) / 1_000_000

	lonSec := 2.6779094 + 4.728982*y + 0.791484*y*x + 0.1306*y*x*x - 0.0436*y*y*y
	latSec := 16.9023892 + 3.238272*x - 0.270978*y*y - 0.002528*x*x - 0.0447*y*y*x - 0.0140*x*x*x

	lon = lonSec * 100 / 36
	lat = latSec * 100 / 36
	return
}

// webMercatorToWGS84 converts Web Mercator (EPSG:3857) to WGS84.
func webMercatorToWGS84(x, y float64) (lon, lat float64) {
	lon = x * 180 / 20037508.342789244
	lat = (math.Atan(math.Exp(y*math.Pi/20037508.342789244))*360/math.Pi - 90)
	return
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

// OverviewForZoom returns the best IFD level to use for a given output zoom level.
// It picks the overview whose resolution most closely matches the output resolution.
func (r *Reader) OverviewForZoom(outputPixelSize float64) int {
	bestLevel := 0
	bestRatio := math.Inf(1)

	for i, ifd := range r.ifds {
		// Compute the pixel size at this IFD level.
		levelPixelSize := r.geo.PixelSizeX * float64(r.ifds[0].Width) / float64(ifd.Width)
		ratio := math.Abs(levelPixelSize/outputPixelSize - 1)
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
