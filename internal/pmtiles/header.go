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
	TileTypeAVIF    = 5
	TileTypeMLT     = 6 // MapLibre Tile (vector)
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
	MinLon              float32
	MinLat              float32
	MaxLon              float32
	MaxLat              float32
	CenterLon           float32
	CenterLat           float32
	Clustered           bool
	InternalCompression uint8
	TileCompression     uint8
	TileType            uint8
	MinZoom             uint8
	MaxZoom             uint8
	CenterZoom          uint8

	// e7 is MinLon, MinLat, MaxLon, MaxLat, CenterLon, CenterLat in the
	// file's E7 units, as NewHeader computed them from float64 degrees or
	// DeserializeHeader read them. float32 cannot hold 1e-7 degree steps
	// (its spacing is 7.6e-6 degrees between 64 and 128), so Serialize
	// writes these for every field still equal to e7ToLonLat of its value,
	// i.e. not changed by the caller since.
	e7 [6]uint32
}

// archiveBounds returns the bounds and centre longitude an archive records
// for b, in its header and its metadata alike. Data crossing the
// antimeridian (MaxLon > 180, see coord.WrapLonRange) is recorded as the
// full longitude range, because TileJSON 3.0 bounds "MUST NOT wrap around
// the ante-meridian", and MapLibre clamps east to 180 and finds no tile at
// all when west > east. The centre stays on the data.
func archiveBounds(b cog.Bounds) (cog.Bounds, float64) {
	centerLon := (b.MinLon + b.MaxLon) / 2
	if b.MaxLon > 180 {
		b.MinLon, b.MaxLon = -180, 180
		centerLon = math.Remainder(centerLon, 360)
	}
	return b, centerLon
}

// archiveCenter returns the centre an archive records for opts, in its
// header and its metadata alike: opts.Center, or else the middle of the
// bounds (see archiveBounds) and of the zoom range.
func archiveCenter(opts WriterOptions) Center {
	if opts.Center != nil {
		return *opts.Center
	}
	b, centerLon := archiveBounds(opts.Bounds)
	return Center{Lon: centerLon, Lat: (b.MinLat + b.MaxLat) / 2, Zoom: (opts.MinZoom + opts.MaxZoom) / 2}
}

// NewHeader creates a header with basic metadata.
func NewHeader(opts WriterOptions) Header {
	b, _ := archiveBounds(opts.Bounds)
	c := archiveCenter(opts)
	h := Header{
		Clustered:           true,
		InternalCompression: CompressionGzip,
		TileCompression:     CompressionNone, // tiles are already compressed (JPEG/PNG/WebP)
		TileType:            opts.TileFormat,
		MinZoom:             uint8(opts.MinZoom),
		MaxZoom:             uint8(opts.MaxZoom),
		CenterZoom:          uint8(c.Zoom),
		// Minimums round down and maximums up, so the bbox contains the data.
		e7: [6]uint32{
			degToE7(b.MinLon, math.Floor), degToE7(b.MinLat, math.Floor),
			degToE7(b.MaxLon, math.Ceil), degToE7(b.MaxLat, math.Ceil),
			degToE7(c.Lon, math.Round), degToE7(c.Lat, math.Round),
		},
	}
	h.setLonLatFromE7()
	return h
}

// setLonLatFromE7 sets the float32 bounds and center fields from h.e7.
func (h *Header) setLonLatFromE7() {
	h.MinLon, h.MinLat = e7ToLonLat(h.e7[0]), e7ToLonLat(h.e7[1])
	h.MaxLon, h.MaxLat = e7ToLonLat(h.e7[2]), e7ToLonLat(h.e7[3])
	h.CenterLon, h.CenterLat = e7ToLonLat(h.e7[4]), e7ToLonLat(h.e7[5])
}

// boundsE7 returns MinLon, MinLat, MaxLon, MaxLat, CenterLon, CenterLat in
// E7 units: the exact recorded value, or the float32 field's value for a
// field the caller has changed.
func (h Header) boundsE7() [6]uint32 {
	e7 := h.e7
	for i, v := range [6]float32{h.MinLon, h.MinLat, h.MaxLon, h.MaxLat, h.CenterLon, h.CenterLat} {
		if e7ToLonLat(e7[i]) != v {
			e7[i] = lonLatToE7(v)
		}
	}
	return e7
}

// Bounds returns the bounds at the file format's full 1e-7 degree
// precision; the float32 fields are off by up to 7.6e-6 degrees (~0.85 m).
func (h Header) Bounds() cog.Bounds {
	e7 := h.boundsE7()
	return cog.Bounds{MinLon: e7ToDeg(e7[0]), MinLat: e7ToDeg(e7[1]), MaxLon: e7ToDeg(e7[2]), MaxLat: e7ToDeg(e7[3])}
}

// Center returns the centre at the file format's full precision, as Bounds
// does the bounds.
func (h Header) Center() Center {
	e7 := h.boundsE7()
	return Center{Lon: e7ToDeg(e7[4]), Lat: e7ToDeg(e7[5]), Zoom: int(h.CenterZoom)}
}

func e7ToDeg(v uint32) float64 { return float64(int32(v)) / 1e7 }

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
	e7 := h.boundsE7()
	binary.LittleEndian.PutUint32(buf[102:106], e7[0])
	binary.LittleEndian.PutUint32(buf[106:110], e7[1])
	binary.LittleEndian.PutUint32(buf[110:114], e7[2])
	binary.LittleEndian.PutUint32(buf[114:118], e7[3])

	buf[118] = h.CenterZoom
	binary.LittleEndian.PutUint32(buf[119:123], e7[4])
	binary.LittleEndian.PutUint32(buf[123:127], e7[5])

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
		CenterZoom:          buf[118],
		e7: [6]uint32{
			binary.LittleEndian.Uint32(buf[102:106]),
			binary.LittleEndian.Uint32(buf[106:110]),
			binary.LittleEndian.Uint32(buf[110:114]),
			binary.LittleEndian.Uint32(buf[114:118]),
			binary.LittleEndian.Uint32(buf[119:123]),
			binary.LittleEndian.Uint32(buf[123:127]),
		},
	}
	h.setLonLatFromE7()

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
	case TileTypeAVIF:
		return "avif"
	case TileTypeMLT:
		return "mlt"
	default:
		return "unknown"
	}
}

// lonLatToE7 converts degrees to E7 units, clamped to ±180: past ±214.7
// the product overflows int32, and the conversion is platform-dependent.
func lonLatToE7(v float32) uint32 {
	return uint32(int32(math.Round(clampDeg(float64(v)) * 1e7)))
}

// degToE7 converts degrees to E7 units with the given rounding, clamped to
// ±180 as in lonLatToE7. v*1e7 can land a hair off an integer for decimal
// input such as 45.82; snapping that first keeps floor/ceil from turning it
// into 45.8199999/45.8200001.
func degToE7(v float64, round func(float64) float64) uint32 {
	e := clampDeg(v) * 1e7
	if r := math.Round(e); math.Abs(e-r) < 1e-3 {
		e = r
	}
	return uint32(int32(round(e)))
}

func clampDeg(v float64) float64 { return max(-180, min(180, v)) }

func e7ToLonLat(v uint32) float32 {
	return float32(float64(int32(v)) / 1e7)
}

// Center is the position and zoom a viewer opens an archive at.
type Center struct {
	Lon, Lat float64
	Zoom     int
}

// WriterOptions holds configuration for the PMTiles writer.
type WriterOptions struct {
	// TempDir is the directory for temporary tile data files.
	// Defaults to the output file's directory when empty.
	TempDir string
	// Name is the archive name for the metadata JSON.
	// Defaults to "geotiff2pmtiles" when empty.
	Name string
	// Description is stored in the PMTiles metadata JSON.
	// When empty, defaults to "Generated from GeoTIFF files".
	Description string
	// Attribution is a string crediting data sources, displayed by map renderers.
	Attribution string
	// Type categorizes the tileset: "baselayer" or "overlay".
	// Defaults to "baselayer" when empty.
	Type string
	// Encoding names the pixel encoding when it isn't plain imagery
	// (e.g. "terrarium" for elevation-encoded PNG). Stored in the metadata
	// JSON so tools like pmtransform can handle the tiles correctly.
	Encoding   string
	MinZoom    int
	MaxZoom    int
	TileSize   int
	Bounds     cog.Bounds
	TileFormat uint8
	// Center, when set, is the centre recorded instead of the one derived
	// from Bounds and the zoom range. A copy of an archive passes its
	// source's Header.Center: the recorded bounds of data crossing the
	// antimeridian are -180..180 (see archiveBounds), whose middle is 0.
	Center *Center
	// Extra is merged into the metadata JSON, overriding the derived keys.
	// It is for what the fixed keys have no room for -- a provenance record
	// of the sources behind the archive, say.
	Extra map[string]any
}
