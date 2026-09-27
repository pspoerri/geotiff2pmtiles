package cog

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// TIFF tag IDs.
const (
	tagNewSubfileType     = 254
	tagImageWidth         = 256
	tagImageLength        = 257
	tagBitsPerSample      = 258
	tagCompression        = 259
	tagPhotometric        = 262
	tagStripOffsets       = 273
	tagSamplesPerPixel    = 277
	tagRowsPerStrip       = 278
	tagStripByteCounts    = 279
	tagPlanarConfig       = 284
	tagColorMap           = 320
	tagTileWidth          = 322
	tagTileLength         = 323
	tagTileOffsets        = 324
	tagTileByteCounts     = 325
	tagPredictor          = 317
	tagSampleFormat       = 339
	tagJPEGTables         = 347
	tagModelTiepointTag   = 33922
	tagModelPixelScaleTag = 33550
	tagGeoKeyDirectoryTag = 34735
	tagGeoDoubleParamsTag = 34736
	tagGeoAsciiParamsTag  = 34737
	tagGDALMetadata       = 42112
	tagGDAL_NODATA        = 42113
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
	dtIFD       = 13
	dtLong8     = 16
	dtSLong8    = 17
	dtIFD8      = 18
)

// IFD represents a parsed TIFF Image File Directory.
type IFD struct {
	GDALMetadata    *GDALMeta // parsed GDAL_METADATA XML (tag 42112), nil if absent
	GeoAsciiParams  string
	NoData          string
	BitsPerSample   []uint16
	SampleFormat    []uint16
	ColorMap        []uint16 // palette (Photometric 3): 2^bits reds, then greens, then blues
	TileOffsets     []uint64
	TileByteCounts  []uint64
	StripOffsets    []uint64
	StripByteCounts []uint64
	JPEGTables      []byte
	ModelTiepoint   []float64
	ModelPixelScale []float64
	GeoKeys         []uint16
	GeoDoubleParams []float64
	Width           uint32
	Height          uint32
	TileWidth       uint32
	TileHeight      uint32
	RowsPerStrip    uint32
	NewSubfileType  uint32 // tag 254: subfilePage, subfileMask bits
	SamplesPerPixel uint16
	Compression     uint16
	Photometric     uint16
	PlanarConfig    uint16
	Predictor       uint16
	// whiteIsZero is Photometric 0 as read from the tag. Photometric's zero
	// value is WhiteIsZero too, so an IFD without the (required) tag, or one
	// built in code, would otherwise render inverted; libtiff assumes
	// BlackIsZero there.
	whiteIsZero bool
}

// NewSubfileType bits that mark an IFD as something other than an image or
// its reduced-resolution version.
const (
	subfilePage = 2 // one page of a multi-page file
	subfileMask = 4 // a transparency mask for another image
)

// GDALMeta holds parsed GDAL_METADATA XML items from tag 42112.
// Dataset-level items go in Items; per-band items (with sample=N) go in BandItems.
type GDALMeta struct {
	Items     map[string]string         // dataset-level: name → value
	BandItems map[int]map[string]string // per-band: sample (0-indexed) → name → value
}

// signBias16 maps signed 16-bit samples onto the unsigned range while keeping
// their order: XOR-ing the raw bits with it equals adding 32768. The uint16
// rescale/nodata machinery then handles Int16 data (e.g. GEBCO) unchanged.
const signBias16 = 0x8000

// sampleBias returns signBias16 for signed data 9..16 bits deep, 0 otherwise.
//
// Keyed on the declared depth rather than on bytesPerSample, so that a
// bit-packed signed depth such as 15 is recognised; bytesPerSample truncates
// that to 1 and would report no bias. Depths above 16 get 0.
func (ifd *IFD) sampleBias() int {
	if b := ifd.bitsPerSample(); b > 8 && b <= 16 && len(ifd.SampleFormat) > 0 && ifd.SampleFormat[0] == 2 {
		return signBias16
	}
	return 0
}

// bitsPerSample returns the declared sample depth, defaulting to 8.
func (ifd *IFD) bitsPerSample() int {
	if len(ifd.BitsPerSample) > 0 {
		return int(ifd.BitsPerSample[0])
	}
	return 8
}

// bytesPerSample returns the number of bytes per sample based on BitsPerSample.
//
// Note that this is only meaningful for depths that are a multiple of eight.
// A depth such as 15 may be bit-packed on disk, in which case no whole number
// of bytes describes a sample; samples16 determines the real layout from the
// tile length instead of asking this.
func (ifd *IFD) bytesPerSample() int {
	if len(ifd.BitsPerSample) > 0 {
		return int(ifd.BitsPerSample[0]) / 8
	}
	return 1
}

// TilesAcross returns the number of tiles in the horizontal direction.
func (ifd *IFD) TilesAcross() int {
	return int((ifd.Width + ifd.TileWidth - 1) / ifd.TileWidth)
}

// TilesDown returns the number of tiles in the vertical direction.
func (ifd *IFD) TilesDown() int {
	return int((ifd.Height + ifd.TileHeight - 1) / ifd.TileHeight)
}

// supportedCompression reports whether the tile decoders handle a TIFF
// compression scheme: none, LZW, JPEG, Deflate (either code) or ZSTD.
func supportedCompression(c uint16) bool {
	switch c {
	case 1, 5, 7, 8, 32946, 50000:
		return true
	}
	return false
}

// imageIFDs keeps IFD 0 and the later IFDs that can serve as its overviews,
// filtering ifds in place, and returns the GDAL internal mask of each kept
// level (see levelMasks). GDAL interleaves 1-bit transparency masks with the
// overviews, and other writers add pages, thumbnails or striped reduced
// images; picked as a level by OverviewForZoom or ValueRange, those decode as
// garbage or divide by zero. NewSubfileType is not required to mark an
// overview, since some writers omit it.
func imageIFDs(ifds []IFD) (levels, masks []IFD) {
	first := &ifds[0]
	kept := ifds[:1]
	for _, ifd := range ifds[1:] {
		switch {
		case ifd.usableMask():
			masks = append(masks, ifd)
		case ifd.NewSubfileType&(subfilePage|subfileMask) != 0, ifd.Photometric == 4: // page, or a mask we cannot apply
		case ifd.TileWidth == 0 || ifd.TileHeight == 0: // striped: only IFD 0 is promoted to tiles
		case ifd.Width == 0 || ifd.Height == 0 || ifd.Width > first.Width || ifd.Height > first.Height: // not a reduced image
		case ifd.SamplesPerPixel != first.SamplesPerPixel || ifd.bitsPerSample() != first.bitsPerSample(): // thumbnail
		case !supportedCompression(ifd.Compression):
		default:
			kept = append(kept, ifd)
		}
	}
	return levelMasks(kept, masks)
}

// usableMask reports whether ifd is a transparency mask the reader can
// apply: NewSubfileType bit 2 and Photometric 4, as GDAL writes its internal
// masks, tiled, one sample of 1..8 bits, compressed by other means than
// JPEG, and with a tile at every position.
func (ifd *IFD) usableMask() bool {
	if ifd.NewSubfileType&subfileMask == 0 || ifd.Photometric != 4 || ifd.TileWidth == 0 || ifd.TileHeight == 0 {
		return false
	}
	tiles := ifd.TilesAcross() * ifd.TilesDown()
	bits := ifd.bitsPerSample()
	return tiles > 0 && len(ifd.TileOffsets) >= tiles && len(ifd.TileByteCounts) >= tiles &&
		ifd.SamplesPerPixel <= 1 && bits >= 1 && bits <= 8 && (ifd.Predictor <= 1 || bits == 8) &&
		supportedCompression(ifd.Compression) && ifd.Compression != 7
}

// levelMasks returns, for each level, the mask of the same size and tiling
// (a zero IFD if there is none), or nil when the file has no masks at all.
// GDAL writes one per level, though not always right after its image.
//
// Once the full-resolution image has a mask, an overview without one is
// dropped, filtering levels in place: read unmasked, it would turn the
// masked area opaque at the zooms it serves, hiding the sources behind it,
// while the next finer level masks it correctly and costs only speed.
func levelMasks(levels, masks []IFD) ([]IFD, []IFD) {
	if len(masks) == 0 {
		return levels, nil
	}
	kept, paired := levels[:0], make([]IFD, 0, len(levels))
	for _, l := range levels {
		var m IFD
		for _, c := range masks {
			if c.Width == l.Width && c.Height == l.Height && c.TileWidth == l.TileWidth && c.TileHeight == l.TileHeight {
				m = c
				break
			}
		}
		if m.Width == 0 && len(paired) > 0 && paired[0].Width != 0 {
			continue // an overview of a masked image, without a mask
		}
		kept = append(kept, l)
		paired = append(paired, m)
	}
	return kept, paired
}

// tiffEntry is a raw TIFF directory entry.
type tiffEntry struct {
	Value    []byte // raw value bytes or inline value
	Count    uint64
	Tag      uint16
	DataType uint16
}

// maxIFDs bounds the IFD chain. A COG has one IFD per overview and mask, so
// the limit only stops a corrupt chain from growing without end.
const maxIFDs = 1 << 16

// parseTIFF reads all IFDs from a TIFF file.
//
// Every count and offset in the file is untrusted: the chain is checked for
// loops, and tag data is checked against the file size before anything is
// allocated for it.
func parseTIFF(r io.ReadSeeker) ([]IFD, binary.ByteOrder, error) {
	fileSize, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, nil, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}

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
	seen := make(map[uint64]bool)

	for offset != 0 {
		if seen[offset] {
			return nil, nil, fmt.Errorf("IFD chain loops back to offset %d", offset)
		}
		if len(ifds) == maxIFDs {
			return nil, nil, fmt.Errorf("more than %d IFDs", maxIFDs)
		}
		seen[offset] = true
		ifd, nextOffset, err := parseOneIFD(r, bo, offset, isBigTIFF, uint64(fileSize))
		if err != nil {
			return nil, nil, fmt.Errorf("parsing IFD at offset %d: %w", offset, err)
		}
		ifds = append(ifds, ifd)
		offset = nextOffset
	}

	return ifds, bo, nil
}

func parseOneIFD(r io.ReadSeeker, bo binary.ByteOrder, offset uint64, bigTIFF bool, fileSize uint64) (IFD, uint64, error) {
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
	// Tags are distinct 16-bit numbers, so a larger BigTIFF count is garbage
	// and must not size the allocation below.
	if numEntries > 1<<16 {
		return IFD{}, 0, fmt.Errorf("directory claims %d entries", numEntries)
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
		if err := resolveEntry(r, bo, &entries[i], bigTIFF, fileSize); err != nil {
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
	case dtLong, dtSLong, dtFloat, dtIFD:
		return 4
	case dtRational, dtSRational, dtDouble, dtLong8, dtSLong8, dtIFD8:
		return 8
	default:
		return 1
	}
}

// resolveEntry reads the actual data for an entry if it doesn't fit inline.
func resolveEntry(r io.ReadSeeker, bo binary.ByteOrder, e *tiffEntry, bigTIFF bool, fileSize uint64) error {
	// Bound the count before multiplying: a garbage BigTIFF count would
	// otherwise wrap to a size that looks inline, or size a huge allocation.
	size := uint64(dataTypeSize(e.DataType))
	if e.Count > fileSize/size {
		return fmt.Errorf("%d values of %d bytes exceed the file size %d", e.Count, size, fileSize)
	}
	totalSize := e.Count * size

	inlineSize := uint64(4)
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
	if dataOffset > fileSize-totalSize {
		return fmt.Errorf("data [%d:+%d] exceeds the file size %d", dataOffset, totalSize, fileSize)
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
		case tagNewSubfileType:
			ifd.NewSubfileType = getUint32(e, bo)
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
			ifd.whiteIsZero = ifd.Photometric == 0
		case tagPlanarConfig:
			ifd.PlanarConfig = getUint16Val(e, bo)
		case tagColorMap:
			ifd.ColorMap = getUint16Slice(e, bo)
		case tagTileOffsets:
			ifd.TileOffsets = getUint64Slice(e, bo)
		case tagTileByteCounts:
			ifd.TileByteCounts = getUint64Slice(e, bo)
		case tagStripOffsets:
			ifd.StripOffsets = getUint64Slice(e, bo)
		case tagStripByteCounts:
			ifd.StripByteCounts = getUint64Slice(e, bo)
		case tagRowsPerStrip:
			ifd.RowsPerStrip = getUint32(e, bo)
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
		case tagPredictor:
			ifd.Predictor = getUint16Val(e, bo)
		case tagSampleFormat:
			ifd.SampleFormat = getUint16Slice(e, bo)
		case tagGDAL_NODATA:
			// GDAL_NODATA is stored as an ASCII string.
			s := string(e.Value[:e.Count])
			// Trim null bytes.
			for len(s) > 0 && s[len(s)-1] == 0 {
				s = s[:len(s)-1]
			}
			ifd.NoData = s
		case tagGeoAsciiParamsTag:
			ifd.GeoAsciiParams = string(e.Value[:e.Count])
		case tagGDALMetadata:
			s := string(e.Value[:e.Count])
			for len(s) > 0 && s[len(s)-1] == 0 {
				s = s[:len(s)-1]
			}
			ifd.GDALMetadata = parseGDALMetadataXML(s)
		}
	}

	return ifd
}

// parseGDALMetadataXML extracts <Item name="key" sample="N">value</Item> pairs
// from the GDAL_METADATA XML tag (tag 42112). Items with a sample attribute are
// stored as per-band metadata; items without are dataset-level.
func parseGDALMetadataXML(xmlStr string) *GDALMeta {
	meta := &GDALMeta{
		Items:     make(map[string]string),
		BandItems: make(map[int]map[string]string),
	}
	decoder := xml.NewDecoder(strings.NewReader(xmlStr))
	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Item" {
			continue
		}
		var name string
		sample := -1
		for _, attr := range se.Attr {
			switch attr.Name.Local {
			case "name":
				name = attr.Value
			case "sample":
				if v, err := strconv.Atoi(attr.Value); err == nil {
					sample = v
				}
			}
		}
		if name == "" {
			continue
		}
		var value string
		if err := decoder.DecodeElement(&value, &se); err != nil {
			continue
		}
		if sample >= 0 {
			if meta.BandItems[sample] == nil {
				meta.BandItems[sample] = make(map[string]string)
			}
			meta.BandItems[sample][name] = value
		} else {
			meta.Items[name] = value
		}
	}
	if len(meta.Items) == 0 && len(meta.BandItems) == 0 {
		return nil
	}
	return meta
}

// getUint16Val returns the first value of an unsigned integer entry, or 0.
func getUint16Val(e tiffEntry, bo binary.ByteOrder) uint16 {
	return uint16(getUint32(e, bo))
}

// getUint32 returns the first value of an unsigned integer entry, or 0.
func getUint32(e tiffEntry, bo binary.ByteOrder) uint32 {
	if v := getUint64Slice(e, bo); len(v) > 0 {
		return uint32(v[0])
	}
	return 0
}

// getUint16Slice is getUint64Slice narrowed to uint16, for SHORT arrays that
// a writer may also store as BYTE or LONG.
func getUint16Slice(e tiffEntry, bo binary.ByteOrder) []uint16 {
	v := getUint64Slice(e, bo)
	if v == nil {
		return nil
	}
	result := make([]uint16, len(v))
	for i, x := range v {
		result[i] = uint16(x)
	}
	return result
}

// getUint64Slice decodes an unsigned integer entry of any width, dispatching
// on its declared type; other types give nil.
func getUint64Slice(e tiffEntry, bo binary.ByteOrder) []uint64 {
	var get func([]byte) uint64
	switch e.DataType {
	case dtByte:
		get = func(b []byte) uint64 { return uint64(b[0]) }
	case dtShort:
		get = func(b []byte) uint64 { return uint64(bo.Uint16(b)) }
	case dtLong, dtIFD:
		get = func(b []byte) uint64 { return uint64(bo.Uint32(b)) }
	case dtLong8, dtIFD8:
		get = bo.Uint64
	default:
		return nil
	}
	size := dataTypeSize(e.DataType)
	result := make([]uint64, valueCount(e))
	for i := range result {
		result[i] = get(e.Value[i*size:])
	}
	return result
}

// getFloat64Slice decodes a FLOAT or DOUBLE entry; other types give nil.
func getFloat64Slice(e tiffEntry, bo binary.ByteOrder) []float64 {
	var get func([]byte) float64
	switch e.DataType {
	case dtDouble:
		get = func(b []byte) float64 { return math.Float64frombits(bo.Uint64(b)) }
	case dtFloat:
		get = func(b []byte) float64 { return float64(math.Float32frombits(bo.Uint32(b))) }
	default:
		return nil
	}
	size := dataTypeSize(e.DataType)
	result := make([]float64, valueCount(e))
	for i := range result {
		result[i] = get(e.Value[i*size:])
	}
	return result
}

// valueCount returns how many values of the entry's type its Value holds:
// Count, unless a malformed entry claims more than its data has.
func valueCount(e tiffEntry) int {
	n := len(e.Value) / dataTypeSize(e.DataType)
	if e.Count < uint64(n) {
		return int(e.Count)
	}
	return n
}
