package cog

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// TIFF tag IDs.
const (
	tagImageWidth                = 256
	tagImageLength               = 257
	tagBitsPerSample             = 258
	tagCompression               = 259
	tagPhotometric               = 262
	tagStripOffsets               = 273
	tagSamplesPerPixel           = 277
	tagRowsPerStrip              = 278
	tagStripByteCounts           = 279
	tagPlanarConfig              = 284
	tagTileWidth                 = 322
	tagTileLength                = 323
	tagTileOffsets               = 324
	tagTileByteCounts            = 325
	tagSampleFormat              = 339
	tagJPEGTables                = 347
	tagModelTiepointTag          = 33922
	tagModelPixelScaleTag        = 33550
	tagGeoKeyDirectoryTag        = 34735
	tagGeoDoubleParamsTag        = 34736
	tagGeoAsciiParamsTag         = 34737
	tagGDAL_NODATA               = 42113
)

// TIFF data types.
const (
	dtByte      = 1
	dtASCII     = 2
	dtShort     = 3
	dtLong      = 4
	dtRational  = 5
	dtSByte     = 6
	dtUndef     = 7
	dtSShort    = 8
	dtSLong     = 9
	dtSRational = 10
	dtFloat     = 11
	dtDouble    = 12
	dtLong8     = 16
	dtSLong8    = 17
	dtIFD8      = 18
)

// IFD represents a parsed TIFF Image File Directory.
type IFD struct {
	Width            uint32
	Height           uint32
	TileWidth        uint32
	TileHeight       uint32
	BitsPerSample    []uint16
	SamplesPerPixel  uint16
	Compression      uint16
	Photometric      uint16
	PlanarConfig     uint16
	TileOffsets      []uint64
	TileByteCounts   []uint64
	JPEGTables       []byte
	ModelTiepoint    []float64
	ModelPixelScale  []float64
	GeoKeys          []uint16
	GeoDoubleParams  []float64
	GeoAsciiParams   string
}

// TilesAcross returns the number of tiles in the horizontal direction.
func (ifd *IFD) TilesAcross() int {
	return int((ifd.Width + ifd.TileWidth - 1) / ifd.TileWidth)
}

// TilesDown returns the number of tiles in the vertical direction.
func (ifd *IFD) TilesDown() int {
	return int((ifd.Height + ifd.TileHeight - 1) / ifd.TileHeight)
}

// tiffEntry is a raw TIFF directory entry.
type tiffEntry struct {
	Tag      uint16
	DataType uint16
	Count    uint64
	Value    []byte // raw value bytes or inline value
}

// parseTIFF reads all IFDs from a TIFF file.
func parseTIFF(r io.ReadSeeker) ([]IFD, binary.ByteOrder, error) {
	// Read header.
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, nil, fmt.Errorf("reading TIFF header: %w", err)
	}

	var bo binary.ByteOrder
	switch string(header[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, nil, fmt.Errorf("invalid TIFF byte order: %x", header[0:2])
	}

	magic := bo.Uint16(header[2:4])
	isBigTIFF := magic == 43
	if magic != 42 && magic != 43 {
		return nil, nil, fmt.Errorf("invalid TIFF magic: %d", magic)
	}

	var firstIFDOffset uint64
	if isBigTIFF {
		// BigTIFF: bytes 4-5 = offset size (8), bytes 6-7 = always 0, bytes 8-15 = first IFD offset
		var bigHeader [8]byte
		if _, err := io.ReadFull(r, bigHeader[:]); err != nil {
			return nil, nil, fmt.Errorf("reading BigTIFF header: %w", err)
		}
		firstIFDOffset = bo.Uint64(bigHeader[:])
	} else {
		firstIFDOffset = uint64(bo.Uint32(header[4:8]))
	}

	var ifds []IFD
	offset := firstIFDOffset

	for offset != 0 {
		ifd, nextOffset, err := parseOneIFD(r, bo, offset, isBigTIFF)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing IFD at offset %d: %w", offset, err)
		}
		ifds = append(ifds, ifd)
		offset = nextOffset
	}

	return ifds, bo, nil
}

func parseOneIFD(r io.ReadSeeker, bo binary.ByteOrder, offset uint64, bigTIFF bool) (IFD, uint64, error) {
	if _, err := r.Seek(int64(offset), io.SeekStart); err != nil {
		return IFD{}, 0, err
	}

	var numEntries uint64
	if bigTIFF {
		var buf [8]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return IFD{}, 0, err
		}
		numEntries = bo.Uint64(buf[:])
	} else {
		var buf [2]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return IFD{}, 0, err
		}
		numEntries = uint64(bo.Uint16(buf[:]))
	}

	entrySize := 12
	if bigTIFF {
		entrySize = 20
	}

	entries := make([]tiffEntry, numEntries)
	for i := uint64(0); i < numEntries; i++ {
		buf := make([]byte, entrySize)
		if _, err := io.ReadFull(r, buf); err != nil {
			return IFD{}, 0, err
		}
		entries[i] = parseTiffEntry(buf, bo, bigTIFF)
	}

	// Read next IFD offset.
	var nextOffset uint64
	if bigTIFF {
		var buf [8]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return IFD{}, 0, err
		}
		nextOffset = bo.Uint64(buf[:])
	} else {
		var buf [4]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return IFD{}, 0, err
		}
		nextOffset = uint64(bo.Uint32(buf[:]))
	}

	// Resolve entries that point to external data.
	for i := range entries {
		if err := resolveEntry(r, bo, &entries[i], bigTIFF); err != nil {
			return IFD{}, 0, fmt.Errorf("resolving entry tag %d: %w", entries[i].Tag, err)
		}
	}

	ifd := buildIFD(entries, bo)
	return ifd, nextOffset, nil
}

func parseTiffEntry(buf []byte, bo binary.ByteOrder, bigTIFF bool) tiffEntry {
	tag := bo.Uint16(buf[0:2])
	dt := bo.Uint16(buf[2:4])

	var count uint64
	var valueBytes []byte

	if bigTIFF {
		count = bo.Uint64(buf[4:12])
		valueBytes = make([]byte, 8)
		copy(valueBytes, buf[12:20])
	} else {
		count = uint64(bo.Uint32(buf[4:8]))
		valueBytes = make([]byte, 4)
		copy(valueBytes, buf[8:12])
	}

	return tiffEntry{
		Tag:      tag,
		DataType: dt,
		Count:    count,
		Value:    valueBytes,
	}
}

func dataTypeSize(dt uint16) int {
	switch dt {
	case dtByte, dtASCII, dtSByte, dtUndef:
		return 1
	case dtShort, dtSShort:
		return 2
	case dtLong, dtSLong, dtFloat, dtIFD8:
		return 4
	case dtRational, dtSRational, dtDouble, dtLong8, dtSLong8:
		return 8
	default:
		return 1
	}
}

// resolveEntry reads the actual data for an entry if it doesn't fit inline.
func resolveEntry(r io.ReadSeeker, bo binary.ByteOrder, e *tiffEntry, bigTIFF bool) error {
	totalSize := int(e.Count) * dataTypeSize(e.DataType)

	inlineSize := 4
	if bigTIFF {
		inlineSize = 8
	}

	if totalSize <= inlineSize {
		// Data fits inline in the value field.
		return nil
	}

	// Data is stored externally; value field holds an offset.
	var dataOffset uint64
	if bigTIFF {
		dataOffset = bo.Uint64(e.Value)
	} else {
		dataOffset = uint64(bo.Uint32(e.Value))
	}

	if _, err := r.Seek(int64(dataOffset), io.SeekStart); err != nil {
		return err
	}

	data := make([]byte, totalSize)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	e.Value = data
	return nil
}

func buildIFD(entries []tiffEntry, bo binary.ByteOrder) IFD {
	var ifd IFD
	ifd.SamplesPerPixel = 1
	ifd.PlanarConfig = 1

	for _, e := range entries {
		switch e.Tag {
		case tagImageWidth:
			ifd.Width = getUint32(e, bo)
		case tagImageLength:
			ifd.Height = getUint32(e, bo)
		case tagTileWidth:
			ifd.TileWidth = getUint32(e, bo)
		case tagTileLength:
			ifd.TileHeight = getUint32(e, bo)
		case tagBitsPerSample:
			ifd.BitsPerSample = getUint16Slice(e, bo)
		case tagSamplesPerPixel:
			ifd.SamplesPerPixel = getUint16Val(e, bo)
		case tagCompression:
			ifd.Compression = getUint16Val(e, bo)
		case tagPhotometric:
			ifd.Photometric = getUint16Val(e, bo)
		case tagPlanarConfig:
			ifd.PlanarConfig = getUint16Val(e, bo)
		case tagTileOffsets:
			ifd.TileOffsets = getUint64Slice(e, bo)
		case tagTileByteCounts:
			ifd.TileByteCounts = getUint64Slice(e, bo)
		case tagJPEGTables:
			ifd.JPEGTables = make([]byte, len(e.Value))
			copy(ifd.JPEGTables, e.Value)
		case tagModelTiepointTag:
			ifd.ModelTiepoint = getFloat64Slice(e, bo)
		case tagModelPixelScaleTag:
			ifd.ModelPixelScale = getFloat64Slice(e, bo)
		case tagGeoKeyDirectoryTag:
			ifd.GeoKeys = getUint16Slice(e, bo)
		case tagGeoDoubleParamsTag:
			ifd.GeoDoubleParams = getFloat64Slice(e, bo)
		case tagGeoAsciiParamsTag:
			ifd.GeoAsciiParams = string(e.Value[:e.Count])
		}
	}

	return ifd
}

func getUint16Val(e tiffEntry, bo binary.ByteOrder) uint16 {
	switch e.DataType {
	case dtShort:
		return bo.Uint16(e.Value)
	case dtLong:
		return uint16(bo.Uint32(e.Value))
	default:
		return uint16(e.Value[0])
	}
}

func getUint32(e tiffEntry, bo binary.ByteOrder) uint32 {
	switch e.DataType {
	case dtShort:
		return uint32(bo.Uint16(e.Value))
	case dtLong:
		return bo.Uint32(e.Value)
	case dtLong8:
		return uint32(bo.Uint64(e.Value))
	default:
		return uint32(e.Value[0])
	}
}

func getUint16Slice(e tiffEntry, bo binary.ByteOrder) []uint16 {
	n := int(e.Count)
	result := make([]uint16, n)
	for i := 0; i < n; i++ {
		result[i] = bo.Uint16(e.Value[i*2 : i*2+2])
	}
	return result
}

func getUint64Slice(e tiffEntry, bo binary.ByteOrder) []uint64 {
	n := int(e.Count)
	result := make([]uint64, n)
	switch e.DataType {
	case dtLong:
		for i := 0; i < n; i++ {
			result[i] = uint64(bo.Uint32(e.Value[i*4 : i*4+4]))
		}
	case dtLong8:
		for i := 0; i < n; i++ {
			result[i] = bo.Uint64(e.Value[i*8 : i*8+8])
		}
	case dtShort:
		for i := 0; i < n; i++ {
			result[i] = uint64(bo.Uint16(e.Value[i*2 : i*2+2]))
		}
	}
	return result
}

func getFloat64Slice(e tiffEntry, bo binary.ByteOrder) []float64 {
	n := int(e.Count)
	result := make([]float64, n)
	size := dataTypeSize(e.DataType)
	for i := 0; i < n; i++ {
		off := i * size
		switch e.DataType {
		case dtDouble:
			bits := bo.Uint64(e.Value[off : off+8])
			result[i] = float64FromBits(bits)
		case dtFloat:
			bits := bo.Uint32(e.Value[off : off+4])
			result[i] = float64(float32FromBits(bits))
		}
	}
	return result
}

func float64FromBits(bits uint64) float64 {
	return math.Float64frombits(bits)
}

func float32FromBits(bits uint32) float32 {
	return math.Float32frombits(bits)
}
