package cog

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
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
	data  []byte // memory-mapped file contents
	bo    binary.ByteOrder
	ifds  []IFD
	geo   GeoInfo
	path  string
	id    int          // unique numeric ID for fast cache keying (set by OpenAll)
	strip *stripLayout // non-nil for strip-based TIFFs promoted to virtual tiles
}

// stripLayout stores the original strip layout for strip-based TIFFs.
// Virtual tiles are composed from multiple strips at read time.
type stripLayout struct {
	offsets       []uint64
	byteCounts    []uint64
	rowsPerStrip  uint32
	stripsPerTile int // number of original strips per virtual tile
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

	ifds, bo, err := parseTIFF(bytes.NewReader(data))
	if err != nil {
		munmapFile(data)
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if len(ifds) == 0 {
		munmapFile(data)
		return nil, fmt.Errorf("%s: no IFDs found", path)
	}

	first := &ifds[0]

	// Strip-based TIFFs: convert the strip layout into virtual tiles.
	var sl *stripLayout
	if first.TileWidth == 0 || first.TileHeight == 0 {
		if len(first.StripOffsets) > 0 {
			sl = promoteStripsToTiles(first)
		} else {
			munmapFile(data)
			return nil, fmt.Errorf("%s: no tile or strip layout found", path)
		}
	}

	switch first.Compression {
	case 1, 5, 7, 8, 32946:
		// Supported: None, LZW, JPEG, Deflate
	default:
		munmapFile(data)
		return nil, fmt.Errorf("%s: unsupported compression type %d", path, first.Compression)
	}

	geo := parseGeoInfo(first)

	// If GeoTIFF tags are absent, try a TFW sidecar.
	if geo.PixelSizeX == 0 && geo.PixelSizeY == 0 {
		if tfwPath := findTFW(path); tfwPath != "" {
			tfw, err := parseTFW(tfwPath)
			if err != nil {
				munmapFile(data)
				return nil, err
			}
			geo = tfw.toGeoInfo()
		}
	}

	// Infer EPSG when GeoKeys didn't provide one.
	if geo.EPSG == 0 && geo.PixelSizeX > 0 {
		geo.EPSG = inferEPSG(geo, first.Width, first.Height)
	}

	return &Reader{
		data:  data,
		bo:    bo,
		ifds:  ifds,
		geo:   geo,
		path:  path,
		strip: sl,
	}, nil
}

// promoteStripsToTiles converts a strip-based IFD into a virtual tile layout.
// Small strips are grouped into larger virtual tiles (>= 256 rows) so that
// resampling kernels (e.g. Lanczos 6x6) never span more than 2 tiles.
// Returns the stripLayout needed to reconstruct virtual tiles at read time.
func promoteStripsToTiles(ifd *IFD) *stripLayout {
	rps := ifd.RowsPerStrip
	if rps == 0 {
		rps = ifd.Height
	}

	const minTileHeight = 256
	stripsPerTile := 1
	if rps < minTileHeight {
		stripsPerTile = int((minTileHeight + rps - 1) / rps)
	}
	virtualTileH := rps * uint32(stripsPerTile)

	totalStrips := len(ifd.StripOffsets)
	numVirtualTiles := (totalStrips + stripsPerTile - 1) / stripsPerTile

	virtualOffsets := make([]uint64, numVirtualTiles)
	virtualByteCounts := make([]uint64, numVirtualTiles)
	for i := 0; i < numVirtualTiles; i++ {
		startStrip := i * stripsPerTile
		virtualOffsets[i] = ifd.StripOffsets[startStrip]
		var totalBytes uint64
		endStrip := startStrip + stripsPerTile
		if endStrip > totalStrips {
			endStrip = totalStrips
		}
		for s := startStrip; s < endStrip; s++ {
			totalBytes += ifd.StripByteCounts[s]
		}
		virtualByteCounts[i] = totalBytes
	}

	sl := &stripLayout{
		offsets:       ifd.StripOffsets,
		byteCounts:    ifd.StripByteCounts,
		rowsPerStrip:  rps,
		stripsPerTile: stripsPerTile,
	}

	ifd.TileWidth = ifd.Width
	ifd.TileHeight = virtualTileH
	ifd.TileOffsets = virtualOffsets
	ifd.TileByteCounts = virtualByteCounts

	return sl
}

// Close unmaps the memory-mapped file.
func (r *Reader) Close() error {
	if r.data != nil {
		err := munmapFile(r.data)
		r.data = nil
		return err
	}
	return nil
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

	end := offset + size
	if end > uint64(len(r.data)) {
		return nil, nil, fmt.Errorf("tile data [%d:%d] exceeds file size %d", offset, end, len(r.data))
	}

	data := r.data[offset:end]

	var decompressed []byte
	switch ifd.Compression {
	case 7: // JPEG — not applicable for float tiles
		return data, ifd, nil
	case 1: // No compression
		decompressed = data
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
	default:
		return nil, nil, fmt.Errorf("unsupported compression: %d", ifd.Compression)
	}

	if ifd.Predictor == 2 {
		undoHorizontalDifferencing(decompressed, int(ifd.TileWidth), int(ifd.SamplesPerPixel))
	}
	return decompressed, ifd, nil
}

// readStripTileRaw reads the strips that compose a virtual tile row and
// returns the concatenated, decompressed bytes.
func (r *Reader) readStripTileRaw(ifd *IFD, tileRow int) ([]byte, *IFD, error) {
	sl := r.strip
	startStrip := tileRow * sl.stripsPerTile
	endStrip := startStrip + sl.stripsPerTile
	if endStrip > len(sl.offsets) {
		endStrip = len(sl.offsets)
	}

	var combined []byte

	for s := startStrip; s < endStrip; s++ {
		offset := sl.offsets[s]
		size := sl.byteCounts[s]
		if size == 0 {
			continue
		}
		end := offset + size
		if end > uint64(len(r.data)) {
			return nil, nil, fmt.Errorf("strip %d data [%d:%d] exceeds file size %d", s, offset, end, len(r.data))
		}

		chunk := r.data[offset:end]

		switch ifd.Compression {
		case 1: // No compression
			combined = append(combined, chunk...)
		case 7: // JPEG
			combined = append(combined, chunk...)
		case 8, 32946: // Deflate / zlib
			dec, err := decompressDeflate(chunk)
			if err != nil {
				return nil, nil, fmt.Errorf("decompressing deflate strip %d: %w", s, err)
			}
			combined = append(combined, dec...)
		case 5: // LZW
			dec, err := decompressLZW(chunk)
			if err != nil {
				return nil, nil, fmt.Errorf("decompressing LZW strip %d: %w", s, err)
			}
			combined = append(combined, dec...)
		default:
			return nil, nil, fmt.Errorf("unsupported compression: %d", ifd.Compression)
		}
	}

	if len(combined) == 0 {
		return nil, ifd, nil
	}

	if ifd.Predictor == 2 {
		undoHorizontalDifferencing(combined, int(ifd.Width), int(ifd.SamplesPerPixel))
	}
	return combined, ifd, nil
}

// undoHorizontalDifferencing reverses TIFF predictor=2 (horizontal differencing).
// Each sample is stored as the difference from the previous sample in the same row.
// This accumulates the deltas to recover the original values.
func undoHorizontalDifferencing(data []byte, width, samplesPerPixel int) {
	rowBytes := width * samplesPerPixel
	for off := 0; off+rowBytes <= len(data); off += rowBytes {
		row := data[off : off+rowBytes]
		for x := samplesPerPixel; x < rowBytes; x++ {
			row[x] += row[x-samplesPerPixel]
		}
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

	return r.decodeRawFloat32Tile(ifd, data)
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

	if len(data) < expectedSize {
		return nil, 0, 0, fmt.Errorf("float tile data too short: got %d, need %d", len(data), expectedSize)
	}

	// We extract just the first band (elevation).
	result := make([]float32, pixelCount)
	for i := 0; i < pixelCount; i++ {
		off := i * spp * bytesPerSample
		switch bps {
		case 32:
			bits := r.bo.Uint32(data[off : off+4])
			result[i] = math.Float32frombits(bits)
		case 64:
			bits := r.bo.Uint64(data[off : off+8])
			result[i] = float32(math.Float64frombits(bits))
		default:
			return nil, 0, 0, fmt.Errorf("unsupported float bits per sample: %d", bps)
		}
	}

	return result, w, h, nil
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

	// Strip-based: compose virtual tile from individual strips.
	if r.strip != nil && level == 0 {
		data, _, err := r.readStripTileRaw(ifd, row)
		if err != nil {
			return nil, err
		}
		if data == nil {
			return image.NewRGBA(image.Rect(0, 0, int(ifd.TileWidth), int(ifd.TileHeight))), nil
		}
		return r.decodeRawTile(ifd, data)
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

	end := offset + size
	if end > uint64(len(r.data)) {
		return nil, fmt.Errorf("tile data [%d:%d] exceeds file size %d", offset, end, len(r.data))
	}

	data := r.data[offset:end]

	switch ifd.Compression {
	case 7: // JPEG
		return r.decodeJPEGTile(ifd, data)
	case 1: // No compression
		if ifd.Predictor == 2 {
			buf := make([]byte, len(data))
			copy(buf, data)
			undoHorizontalDifferencing(buf, int(ifd.TileWidth), int(ifd.SamplesPerPixel))
			return r.decodeRawTile(ifd, buf)
		}
		return r.decodeRawTile(ifd, data)
	case 8, 32946: // Deflate / zlib
		decompressed, err := decompressDeflate(data)
		if err != nil {
			return nil, fmt.Errorf("decompressing deflate tile: %w", err)
		}
		if ifd.Predictor == 2 {
			undoHorizontalDifferencing(decompressed, int(ifd.TileWidth), int(ifd.SamplesPerPixel))
		}
		return r.decodeRawTile(ifd, decompressed)
	case 5: // LZW
		decompressed, err := decompressLZW(data)
		if err != nil {
			return nil, fmt.Errorf("decompressing LZW tile: %w", err)
		}
		if ifd.Predictor == 2 {
			undoHorizontalDifferencing(decompressed, int(ifd.TileWidth), int(ifd.SamplesPerPixel))
		}
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
// For single-band data, pixels matching the GDAL nodata value are set to
// alpha=0 (transparent) so downstream code treats them as empty.
func (r *Reader) decodeRawTile(ifd *IFD, data []byte) (image.Image, error) {
	w := int(ifd.TileWidth)
	h := int(ifd.TileHeight)
	spp := int(ifd.SamplesPerPixel)

	var hasNodata bool
	var nodataVal uint8
	if spp <= 2 {
		nd := r.ifds[0].NoData
		if nd != "" {
			v, err := strconv.ParseFloat(strings.TrimSpace(nd), 64)
			if err == nil && v >= 0 && v <= 255 && v == math.Floor(v) {
				nodataVal = uint8(v)
				hasNodata = true
			}
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := (y*w + x) * spp
			if idx+spp > len(data) {
				break
			}
			var c color.RGBA
			switch spp {
			case 1:
				v := data[idx]
				c.R = v
				c.G = v
				c.B = v
				if hasNodata && v == nodataVal {
					c.A = 0
				} else {
					c.A = 255
				}
			case 2:
				v := data[idx]
				c.R = v
				c.G = v
				c.B = v
				a := data[idx+1]
				if hasNodata && v == nodataVal {
					a = 0
				}
				c.A = a
			default:
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

// DebugIFD returns the raw IFD for debugging purposes.
func (r *Reader) DebugIFD(level int) IFD {
	return r.ifds[level]
}

// RawBytes returns n bytes from the memory-mapped data starting at offset.
func (r *Reader) RawBytes(offset uint64, n int) []byte {
	end := offset + uint64(n)
	if end > uint64(len(r.data)) {
		end = uint64(len(r.data))
	}
	result := make([]byte, end-offset)
	copy(result, r.data[offset:end])
	return result
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
	for i, p := range paths {
		r, err := Open(p)
		if err != nil {
			// Close any already-opened readers.
			for _, rr := range readers {
				rr.Close()
			}
			return nil, fmt.Errorf("failed to open %s: %w", p, err)
		}
		r.id = i
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
	}

	spp := int(ifd.SamplesPerPixel)
	bps := 8
	if len(ifd.BitsPerSample) > 0 {
		bps = int(ifd.BitsPerSample[0])
	}

	sampleType := "uint"
	if r.IsFloat() {
		sampleType = "float"
	}

	return fmt.Sprintf("%s, %dx %s%d", comp, spp, sampleType, bps)
}

// IsFloat returns true if the raster data is floating-point (e.g. Float32 elevation data).
func (r *Reader) IsFloat() bool {
	ifd := &r.ifds[0]
	if len(ifd.SampleFormat) > 0 && ifd.SampleFormat[0] == 3 { // 3 = IEEE floating point
		return true
	}
	return false
}

// NoData returns the GDAL nodata string, or "" if not set.
func (r *Reader) NoData() string {
	return r.ifds[0].NoData
}
