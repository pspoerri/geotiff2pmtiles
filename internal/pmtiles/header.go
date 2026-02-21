package pmtiles

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
)

// PMTiles v3 constants.
const (
	HeaderSize = 127

	// Internal compression for directories.
	CompressionUnknown = 0
	CompressionNone    = 1
	CompressionGzip    = 2
	CompressionBrotli  = 3
	CompressionZstd    = 4

	// Tile types.
	TileTypeUnknown = 0
	TileTypeMVT     = 1
	TileTypePNG     = 2
	TileTypeJPEG    = 3
	TileTypeWebP    = 4
)

// Header represents the PMTiles v3 header (127 bytes).
type Header struct {
	RootDirOffset       uint64
	RootDirLength       uint64
	MetadataOffset      uint64
	MetadataLength      uint64
	LeafDirOffset       uint64
	LeafDirLength       uint64
	TileDataOffset      uint64
	TileDataLength      uint64
	NumAddressedTiles   uint64
	NumTileEntries      uint64
	NumTileContents     uint64
	Clustered           bool
	InternalCompression uint8
	TileCompression     uint8
	TileType            uint8
	MinZoom             uint8
	MaxZoom             uint8
	MinLon              float32
	MinLat              float32
	MaxLon              float32
	MaxLat              float32
	CenterZoom          uint8
	CenterLon           float32
	CenterLat           float32
}

// NewHeader creates a header with basic metadata.
func NewHeader(opts WriterOptions) Header {
	h := Header{
		Clustered:           true,
		InternalCompression: CompressionGzip,
		TileCompression:     CompressionNone, // tiles are already compressed (JPEG/PNG/WebP)
		TileType:            opts.TileFormat,
		MinZoom:             uint8(opts.MinZoom),
		MaxZoom:             uint8(opts.MaxZoom),
		MinLon:              float32(opts.Bounds.MinLon),
		MinLat:              float32(opts.Bounds.MinLat),
		MaxLon:              float32(opts.Bounds.MaxLon),
		MaxLat:              float32(opts.Bounds.MaxLat),
		CenterZoom:          uint8((opts.MinZoom + opts.MaxZoom) / 2),
		CenterLon:           float32((opts.Bounds.MinLon + opts.Bounds.MaxLon) / 2),
		CenterLat:           float32((opts.Bounds.MinLat + opts.Bounds.MaxLat) / 2),
	}
	return h
}

// Serialize writes the 127-byte header.
func (h *Header) Serialize() []byte {
	buf := make([]byte, HeaderSize)

	// Magic number: "PMTiles" + version 3
	copy(buf[0:7], "PMTiles")
	buf[7] = 3

	binary.LittleEndian.PutUint64(buf[8:16], h.RootDirOffset)
	binary.LittleEndian.PutUint64(buf[16:24], h.RootDirLength)
	binary.LittleEndian.PutUint64(buf[24:32], h.MetadataOffset)
	binary.LittleEndian.PutUint64(buf[32:40], h.MetadataLength)
	binary.LittleEndian.PutUint64(buf[40:48], h.LeafDirOffset)
	binary.LittleEndian.PutUint64(buf[48:56], h.LeafDirLength)
	binary.LittleEndian.PutUint64(buf[56:64], h.TileDataOffset)
	binary.LittleEndian.PutUint64(buf[64:72], h.TileDataLength)
	binary.LittleEndian.PutUint64(buf[72:80], h.NumAddressedTiles)
	binary.LittleEndian.PutUint64(buf[80:88], h.NumTileEntries)
	binary.LittleEndian.PutUint64(buf[88:96], h.NumTileContents)

	if h.Clustered {
		buf[96] = 1
	}
	buf[97] = h.InternalCompression
	buf[98] = h.TileCompression
	buf[99] = h.TileType
	buf[100] = h.MinZoom
	buf[101] = h.MaxZoom

	// Bounds as E7 (int32 * 1e7) encoded in little-endian
	binary.LittleEndian.PutUint32(buf[102:106], lonLatToE7(h.MinLon))
	binary.LittleEndian.PutUint32(buf[106:110], lonLatToE7(h.MinLat))
	binary.LittleEndian.PutUint32(buf[110:114], lonLatToE7(h.MaxLon))
	binary.LittleEndian.PutUint32(buf[114:118], lonLatToE7(h.MaxLat))

	buf[118] = h.CenterZoom
	binary.LittleEndian.PutUint32(buf[119:123], lonLatToE7(h.CenterLon))
	binary.LittleEndian.PutUint32(buf[123:127], lonLatToE7(h.CenterLat))

	return buf
}

// DeserializeHeader parses a 127-byte PMTiles v3 header.
func DeserializeHeader(buf []byte) (Header, error) {
	if len(buf) < HeaderSize {
		return Header{}, fmt.Errorf("header too short: %d bytes (need %d)", len(buf), HeaderSize)
	}

	if string(buf[0:7]) != "PMTiles" {
		return Header{}, fmt.Errorf("invalid magic bytes: %q", buf[0:7])
	}
	if buf[7] != 3 {
		return Header{}, fmt.Errorf("unsupported PMTiles version: %d (expected 3)", buf[7])
	}

	h := Header{
		RootDirOffset:       binary.LittleEndian.Uint64(buf[8:16]),
		RootDirLength:       binary.LittleEndian.Uint64(buf[16:24]),
		MetadataOffset:      binary.LittleEndian.Uint64(buf[24:32]),
		MetadataLength:      binary.LittleEndian.Uint64(buf[32:40]),
		LeafDirOffset:       binary.LittleEndian.Uint64(buf[40:48]),
		LeafDirLength:       binary.LittleEndian.Uint64(buf[48:56]),
		TileDataOffset:      binary.LittleEndian.Uint64(buf[56:64]),
		TileDataLength:      binary.LittleEndian.Uint64(buf[64:72]),
		NumAddressedTiles:   binary.LittleEndian.Uint64(buf[72:80]),
		NumTileEntries:      binary.LittleEndian.Uint64(buf[80:88]),
		NumTileContents:     binary.LittleEndian.Uint64(buf[88:96]),
		Clustered:           buf[96] == 1,
		InternalCompression: buf[97],
		TileCompression:     buf[98],
		TileType:            buf[99],
		MinZoom:             buf[100],
		MaxZoom:             buf[101],
		MinLon:              e7ToLonLat(binary.LittleEndian.Uint32(buf[102:106])),
		MinLat:              e7ToLonLat(binary.LittleEndian.Uint32(buf[106:110])),
		MaxLon:              e7ToLonLat(binary.LittleEndian.Uint32(buf[110:114])),
		MaxLat:              e7ToLonLat(binary.LittleEndian.Uint32(buf[114:118])),
		CenterZoom:          buf[118],
		CenterLon:           e7ToLonLat(binary.LittleEndian.Uint32(buf[119:123])),
		CenterLat:           e7ToLonLat(binary.LittleEndian.Uint32(buf[123:127])),
	}

	return h, nil
}

// TileTypeString returns a human-readable name for a tile type constant.
func TileTypeString(t uint8) string {
	switch t {
	case TileTypeMVT:
		return "mvt"
	case TileTypePNG:
		return "png"
	case TileTypeJPEG:
		return "jpeg"
	case TileTypeWebP:
		return "webp"
	default:
		return "unknown"
	}
}

func lonLatToE7(v float32) uint32 {
	return uint32(int32(math.Round(float64(v) * 1e7)))
}

func e7ToLonLat(v uint32) float32 {
	return float32(float64(int32(v)) / 1e7)
}

// WriterOptions holds configuration for the PMTiles writer.
type WriterOptions struct {
	MinZoom    int
	MaxZoom    int
	Bounds     cog.Bounds
	TileFormat uint8
	TileSize   int
	// TempDir is the directory for temporary tile data files.
	// Defaults to the output file's directory when empty.
	TempDir string
}
