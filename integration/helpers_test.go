package integration_test

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
	"github.com/pspoerri/geotiff2pmtiles/internal/pmtiles"
	"github.com/pspoerri/geotiff2pmtiles/internal/tile"
)

const testdataDir = "testdata"

// ---------------------------------------------------------------------------
// Synthetic GeoTIFF writer
// ---------------------------------------------------------------------------

// tiffWriterConfig describes a minimal GeoTIFF for testing.
type tiffWriterConfig struct {
	Width, Height     int
	TileWidth, TileHt int // defaults to 256 if zero
	SamplesPerPixel   int // 1=gray, 3=RGB, 4=RGBA
	BitsPerSample     int // 8 or 16
	OriginLon         float64
	OriginLat         float64
	PixelSizeDeg      float64 // degrees per pixel (WGS84)
	EPSG              int     // 4326 or 3857
	NoData            string  // e.g. "0" or ""
	GDALMetadataXML   string  // tag 42112 (optional)
	// PixelFunc returns the sample value for pixel (x, y) and band index (0-based).
	PixelFunc func(x, y, band int) uint16
}

var tiffSeq atomic.Int64

// writeSyntheticGeoTIFF writes a minimal valid TIFF that cog.Open() can parse.
// It writes classic TIFF, little-endian, uncompressed, tiled organisation.
func writeSyntheticGeoTIFF(t *testing.T, cfg tiffWriterConfig) string {
	t.Helper()

	if cfg.TileWidth == 0 {
		cfg.TileWidth = 256
	}
	if cfg.TileHt == 0 {
		cfg.TileHt = 256
	}
	if cfg.SamplesPerPixel == 0 {
		cfg.SamplesPerPixel = 3
	}
	if cfg.BitsPerSample == 0 {
		cfg.BitsPerSample = 8
	}
	if cfg.EPSG == 0 {
		cfg.EPSG = 4326
	}
	if cfg.PixelFunc == nil {
		cfg.PixelFunc = func(x, y, band int) uint16 { return 128 }
	}

	seq := tiffSeq.Add(1)
	path := filepath.Join(t.TempDir(), fmt.Sprintf("synthetic_%d.tif", seq))

	bo := binary.LittleEndian
	bytesPerSample := cfg.BitsPerSample / 8
	tilesAcross := (cfg.Width + cfg.TileWidth - 1) / cfg.TileWidth
	tilesDown := (cfg.Height + cfg.TileHt - 1) / cfg.TileHt
	numTiles := tilesAcross * tilesDown
	tileBytes := cfg.TileWidth * cfg.TileHt * cfg.SamplesPerPixel * bytesPerSample

	// ---- Collect IFD entries ----
	type ifdEntry struct {
		tag    uint16
		dtype  uint16
		count  uint32
		value  uint32 // value or offset (populated later for external data)
		extern []byte // non-nil if data is stored out-of-band
	}
	var entries []ifdEntry

	add := func(tag, dtype uint16, count uint32, value uint32) {
		entries = append(entries, ifdEntry{tag: tag, dtype: dtype, count: count, value: value})
	}
	addExtern := func(tag, dtype uint16, count uint32, data []byte) {
		entries = append(entries, ifdEntry{tag: tag, dtype: dtype, count: count, extern: data})
	}

	// 256 ImageWidth
	add(256, 3, 1, uint32(cfg.Width))
	// 257 ImageLength
	add(257, 3, 1, uint32(cfg.Height))

	// 258 BitsPerSample (short array)
	{
		buf := make([]byte, 2*cfg.SamplesPerPixel)
		for i := 0; i < cfg.SamplesPerPixel; i++ {
			bo.PutUint16(buf[i*2:], uint16(cfg.BitsPerSample))
		}
		if cfg.SamplesPerPixel <= 2 {
			// Fits in 4 bytes
			var v uint32
			for i := 0; i < len(buf) && i < 4; i++ {
				v |= uint32(buf[i]) << (8 * i)
			}
			add(258, 3, uint32(cfg.SamplesPerPixel), v)
		} else {
			addExtern(258, 3, uint32(cfg.SamplesPerPixel), buf)
		}
	}

	// 259 Compression = 1 (None)
	add(259, 3, 1, 1)

	// 262 Photometric (1=MinIsBlack for gray, 2=RGB)
	if cfg.SamplesPerPixel == 1 {
		add(262, 3, 1, 1)
	} else {
		add(262, 3, 1, 2)
	}

	// 277 SamplesPerPixel
	add(277, 3, 1, uint32(cfg.SamplesPerPixel))

	// 322 TileWidth
	add(322, 3, 1, uint32(cfg.TileWidth))
	// 323 TileLength
	add(323, 3, 1, uint32(cfg.TileHt))

	// 324 TileOffsets (filled later)
	tileOffsetsData := make([]byte, 4*numTiles)
	tileOffsetsIdx := len(entries)
	if numTiles == 1 {
		add(324, 4, 1, 0) // placeholder, filled after layout
	} else {
		addExtern(324, 4, uint32(numTiles), tileOffsetsData)
	}

	// 325 TileByteCounts
	tileByteCountsData := make([]byte, 4*numTiles)
	for i := 0; i < numTiles; i++ {
		bo.PutUint32(tileByteCountsData[i*4:], uint32(tileBytes))
	}
	if numTiles == 1 {
		add(325, 4, 1, uint32(tileBytes))
	} else {
		addExtern(325, 4, uint32(numTiles), tileByteCountsData)
	}

	// 33550 ModelPixelScale: [scaleX, scaleY, 0]
	{
		buf := make([]byte, 24) // 3 doubles
		putFloat64(buf[0:], bo, cfg.PixelSizeDeg)
		putFloat64(buf[8:], bo, cfg.PixelSizeDeg)
		putFloat64(buf[16:], bo, 0)
		addExtern(33550, 12, 3, buf) // dtype DOUBLE
	}

	// 33922 ModelTiepoint: [0, 0, 0, originX, originY, 0]
	{
		buf := make([]byte, 48) // 6 doubles
		putFloat64(buf[0:], bo, 0)
		putFloat64(buf[8:], bo, 0)
		putFloat64(buf[16:], bo, 0)
		putFloat64(buf[24:], bo, cfg.OriginLon) // X
		putFloat64(buf[32:], bo, cfg.OriginLat) // Y
		putFloat64(buf[40:], bo, 0)
		addExtern(33922, 12, 6, buf)
	}

	// 34735 GeoKeyDirectory
	{
		var geoKeys []uint16
		switch cfg.EPSG {
		case 4326:
			// Geographic CRS
			geoKeys = []uint16{
				1, 1, 0, 2, // header: version 1, revision 1, minor 0, 2 keys
				1024, 0, 1, 2, // ModelTypeGeoKey = Geographic
				2048, 0, 1, 4326, // GeographicTypeGeoKey
			}
		case 3857:
			// Projected CRS
			geoKeys = []uint16{
				1, 1, 0, 2,
				1024, 0, 1, 1, // ModelTypeGeoKey = Projected
				3072, 0, 1, 3857, // ProjectedCSTypeGeoKey
			}
		default:
			t.Fatalf("unsupported EPSG for synthetic TIFF: %d", cfg.EPSG)
		}
		buf := make([]byte, 2*len(geoKeys))
		for i, v := range geoKeys {
			bo.PutUint16(buf[i*2:], v)
		}
		addExtern(34735, 3, uint32(len(geoKeys)), buf)
	}

	// 42112 GDAL_METADATA (optional)
	if cfg.GDALMetadataXML != "" {
		data := append([]byte(cfg.GDALMetadataXML), 0) // NUL terminated
		addExtern(42112, 2, uint32(len(data)), data)
	}

	// 42113 GDAL_NODATA (optional)
	if cfg.NoData != "" {
		data := append([]byte(cfg.NoData), 0) // NUL terminated
		if len(data) <= 4 {
			var v uint32
			for i := 0; i < len(data); i++ {
				v |= uint32(data[i]) << (8 * i)
			}
			add(42113, 2, uint32(len(data)), v)
		} else {
			addExtern(42113, 2, uint32(len(data)), data)
		}
	}

	// ---- Layout: Header(8) + IFD + external data + tile data ----
	numEntries := len(entries)
	ifdOffset := uint32(8) // right after TIFF header
	// IFD: 2 bytes (count) + 12 bytes per entry + 4 bytes (next IFD = 0)
	ifdSize := 2 + numEntries*12 + 4
	externalStart := ifdOffset + uint32(ifdSize)

	// Compute extern offsets (single pass — no inline conversion needed
	// because TileOffsets/TileByteCounts are already handled inline for
	// single-tile images, and other small entries are inlined at creation).
	externOffset := externalStart
	for i := range entries {
		if entries[i].extern != nil {
			entries[i].value = externOffset
			externOffset += uint32(len(entries[i].extern))
		}
	}

	// Tile data starts after all external data.
	tileDataStart := externOffset

	// Fill tile offsets.
	if numTiles == 1 {
		entries[tileOffsetsIdx].value = tileDataStart
	} else {
		for i := 0; i < numTiles; i++ {
			bo.PutUint32(tileOffsetsData[i*4:], tileDataStart+uint32(i*tileBytes))
		}
	}

	totalSize := int(tileDataStart) + numTiles*tileBytes
	buf := make([]byte, totalSize)

	// ---- Write TIFF header ----
	buf[0] = 'I'
	buf[1] = 'I' // little-endian
	bo.PutUint16(buf[2:], 42)
	bo.PutUint32(buf[4:], ifdOffset)

	// ---- Write IFD ----
	off := int(ifdOffset)
	bo.PutUint16(buf[off:], uint16(numEntries))
	off += 2
	for _, e := range entries {
		bo.PutUint16(buf[off:], e.tag)
		bo.PutUint16(buf[off+2:], e.dtype)
		bo.PutUint32(buf[off+4:], e.count)
		bo.PutUint32(buf[off+8:], e.value)
		off += 12
	}
	bo.PutUint32(buf[off:], 0) // next IFD offset = 0

	// ---- Write external data ----
	for _, e := range entries {
		if e.extern != nil {
			copy(buf[e.value:], e.extern)
		}
	}

	// ---- Write tile pixel data ----
	for ty := 0; ty < tilesDown; ty++ {
		for tx := 0; tx < tilesAcross; tx++ {
			tileIdx := ty*tilesAcross + tx
			tileOff := int(tileDataStart) + tileIdx*tileBytes
			for py := 0; py < cfg.TileHt; py++ {
				for px := 0; px < cfg.TileWidth; px++ {
					imgX := tx*cfg.TileWidth + px
					imgY := ty*cfg.TileHt + py
					for band := 0; band < cfg.SamplesPerPixel; band++ {
						var val uint16
						if imgX < cfg.Width && imgY < cfg.Height {
							val = cfg.PixelFunc(imgX, imgY, band)
						}
						pixOff := tileOff + (py*cfg.TileWidth+px)*cfg.SamplesPerPixel*bytesPerSample + band*bytesPerSample
						if bytesPerSample == 1 {
							buf[pixOff] = byte(val)
						} else {
							bo.PutUint16(buf[pixOff:], val)
						}
					}
				}
			}
		}
	}

	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatalf("writing synthetic GeoTIFF: %v", err)
	}
	return path
}

func putFloat64(buf []byte, bo binary.ByteOrder, v float64) {
	bo.PutUint64(buf, math.Float64bits(v))
}

// ---------------------------------------------------------------------------
// Pipeline runners
// ---------------------------------------------------------------------------

// pipelineConfig configures a full GeoTIFF→PMTiles pipeline run.
type pipelineConfig struct {
	InputPaths  []string
	Format      string // jpeg, png, webp
	Quality     int
	MinZoom     int
	MaxZoom     int
	TileSize    int
	Resampling  string
	FillColor   *color.RGBA
	BandCfg     cog.BandConfig
	MemLimitMB  int
	Concurrency int
}

// runPipeline executes the full GeoTIFF→PMTiles pipeline and returns the output path.
func runPipeline(t *testing.T, cfg pipelineConfig) string {
	t.Helper()

	if cfg.Format == "" {
		cfg.Format = "png"
	}
	if cfg.Quality == 0 {
		cfg.Quality = 85
	}
	if cfg.TileSize == 0 {
		cfg.TileSize = 256
	}
	if cfg.Resampling == "" {
		cfg.Resampling = "bilinear"
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 2
	}

	outputPath := filepath.Join(t.TempDir(), "output.pmtiles")

	enc, err := encode.NewEncoder(cfg.Format, cfg.Quality)
	if err != nil {
		t.Fatalf("NewEncoder(%q): %v", cfg.Format, err)
	}

	resamplingMode, err := tile.ParseResampling(cfg.Resampling)
	if err != nil {
		t.Fatalf("ParseResampling(%q): %v", cfg.Resampling, err)
	}

	sources, err := cog.OpenAll(cfg.InputPaths)
	if err != nil {
		t.Fatalf("cog.OpenAll: %v", err)
	}
	defer func() {
		for _, s := range sources {
			s.Close()
		}
	}()

	for _, src := range sources {
		src.SetBandConfig(cfg.BandCfg)
	}

	mergedBounds := cog.MergedBoundsWGS84(sources)

	minZoom := cfg.MinZoom
	maxZoom := cfg.MaxZoom
	if maxZoom < 0 {
		pixelSizeMeters := coord.PixelSizeInGroundMeters(sources[0].PixelSize(), sources[0].EPSG(), mergedBounds.CenterLat())
		maxZoom = coord.MaxZoomForResolution(pixelSizeMeters, mergedBounds.CenterLat(), cfg.TileSize)
	}
	if minZoom < 0 {
		minZoom = maxZoom - 2
		if minZoom < 0 {
			minZoom = 0
		}
	}

	var memoryLimitBytes int64
	if cfg.MemLimitMB > 0 {
		memoryLimitBytes = int64(cfg.MemLimitMB) * 1024 * 1024
	}

	outputDir := filepath.Dir(outputPath)
	genCfg := tile.Config{
		MinZoom:          minZoom,
		MaxZoom:          maxZoom,
		TileSize:         cfg.TileSize,
		Concurrency:      cfg.Concurrency,
		Encoder:          enc,
		Bounds:           mergedBounds,
		Resampling:       resamplingMode,
		FillColor:        cfg.FillColor,
		MemoryLimitBytes: memoryLimitBytes,
		OutputDir:        outputDir,
	}

	writer, err := pmtiles.NewWriter(outputPath, pmtiles.WriterOptions{
		MinZoom:    minZoom,
		MaxZoom:    maxZoom,
		Bounds:     mergedBounds,
		TileFormat: enc.PMTileType(),
		TileSize:   cfg.TileSize,
		TempDir:    outputDir,
		Type:       "baselayer",
	})
	if err != nil {
		t.Fatalf("pmtiles.NewWriter: %v", err)
	}

	_, err = tile.Generate(genCfg, sources, writer)
	if err != nil {
		writer.Abort()
		t.Fatalf("tile.Generate: %v", err)
	}

	if err := writer.Finalize(); err != nil {
		t.Fatalf("writer.Finalize: %v", err)
	}

	return outputPath
}

// transformConfig configures a PMTiles→PMTiles transform run.
type transformConfig struct {
	InputPath   string
	Format      string
	Quality     int
	MinZoom     int
	MaxZoom     int
	TileSize    int
	Resampling  string
	Rebuild     bool
	Concurrency int
	FillColor   *color.RGBA
}

// runTransform executes the PMTiles transform pipeline and returns the output path.
func runTransform(t *testing.T, cfg transformConfig) string {
	t.Helper()

	if cfg.Quality == 0 {
		cfg.Quality = 85
	}
	if cfg.Resampling == "" {
		cfg.Resampling = "bilinear"
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 2
	}

	outputPath := filepath.Join(t.TempDir(), "transform.pmtiles")

	reader, err := pmtiles.OpenReader(cfg.InputPath)
	if err != nil {
		t.Fatalf("pmtiles.OpenReader: %v", err)
	}
	defer reader.Close()

	srcHeader := reader.Header()
	srcFormat := pmtiles.TileTypeString(srcHeader.TileType)

	// Resolve format.
	format := cfg.Format
	if format == "" {
		format = srcFormat
	}

	minZoom := cfg.MinZoom
	maxZoom := cfg.MaxZoom
	if minZoom < 0 {
		minZoom = int(srcHeader.MinZoom)
	}
	if maxZoom < 0 {
		maxZoom = int(srcHeader.MaxZoom)
	}

	tileSize := cfg.TileSize
	if tileSize <= 0 {
		tileSize = 256
	}

	enc, err := encode.NewEncoder(format, cfg.Quality)
	if err != nil {
		t.Fatalf("NewEncoder(%q): %v", format, err)
	}

	resamplingMode, err := tile.ParseResampling(cfg.Resampling)
	if err != nil {
		t.Fatalf("ParseResampling: %v", err)
	}

	// Determine mode.
	formatChanged := format != srcFormat
	zoomChanged := minZoom < int(srcHeader.MinZoom)
	mode := tile.TransformPassthrough
	if cfg.Rebuild || zoomChanged {
		mode = tile.TransformRebuild
	} else if formatChanged {
		mode = tile.TransformReencode
	} else if cfg.FillColor != nil {
		mode = tile.TransformReencode
	}

	bounds := [4]float32{srcHeader.MinLon, srcHeader.MinLat, srcHeader.MaxLon, srcHeader.MaxLat}
	outputDir := filepath.Dir(outputPath)

	transformCfg := tile.TransformConfig{
		MinZoom:      minZoom,
		MaxZoom:      maxZoom,
		TileSize:     tileSize,
		Concurrency:  cfg.Concurrency,
		Encoder:      enc,
		SourceFormat: srcFormat,
		Resampling:   resamplingMode,
		Mode:         mode,
		FillColor:    cfg.FillColor,
		Bounds:       bounds,
		OutputDir:    outputDir,
	}

	writer, err := pmtiles.NewWriter(outputPath, pmtiles.WriterOptions{
		MinZoom:    minZoom,
		MaxZoom:    maxZoom,
		Bounds:     cog.Bounds{MinLon: float64(bounds[0]), MinLat: float64(bounds[1]), MaxLon: float64(bounds[2]), MaxLat: float64(bounds[3])},
		TileFormat: enc.PMTileType(),
		TileSize:   tileSize,
		TempDir:    outputDir,
		Type:       "baselayer",
	})
	if err != nil {
		t.Fatalf("pmtiles.NewWriter: %v", err)
	}

	_, err = tile.Transform(transformCfg, reader, writer)
	if err != nil {
		writer.Abort()
		t.Fatalf("tile.Transform: %v", err)
	}

	if err := writer.Finalize(); err != nil {
		t.Fatalf("writer.Finalize: %v", err)
	}

	return outputPath
}

// ---------------------------------------------------------------------------
// PMTiles validation helpers
// ---------------------------------------------------------------------------

// pmtilesResult holds validated PMTiles metadata.
type pmtilesResult struct {
	Header     pmtiles.Header
	Metadata   map[string]interface{}
	TileCount  int
	ZoomCounts map[int]int
}

// validatePMTiles opens and validates a PMTiles file, returning summary info.
// It also verifies that the header + root directory fit within the 16 KiB
// initial fetch budget required by pmtiles.io and other HTTP range-request clients.
func validatePMTiles(t *testing.T, path string) pmtilesResult {
	t.Helper()

	reader, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatalf("validatePMTiles: OpenReader(%s): %v", path, err)
	}
	defer reader.Close()

	h := reader.Header()

	// Validate 16 KiB root directory budget.
	const maxInitialFetch = 16384
	initialFetch := h.RootDirOffset + h.RootDirLength
	if initialFetch > maxInitialFetch {
		t.Errorf("validatePMTiles: header + root directory = %d bytes, exceeds %d-byte initial fetch budget",
			initialFetch, maxInitialFetch)
	}

	// Validate section contiguity.
	if h.RootDirOffset+h.RootDirLength != h.MetadataOffset {
		t.Errorf("validatePMTiles: root dir end (%d) != metadata offset (%d)",
			h.RootDirOffset+h.RootDirLength, h.MetadataOffset)
	}
	if h.MetadataOffset+h.MetadataLength != h.LeafDirOffset {
		t.Errorf("validatePMTiles: metadata end (%d) != leaf dir offset (%d)",
			h.MetadataOffset+h.MetadataLength, h.LeafDirOffset)
	}
	if h.LeafDirOffset+h.LeafDirLength != h.TileDataOffset {
		t.Errorf("validatePMTiles: leaf dir end (%d) != tile data offset (%d)",
			h.LeafDirOffset+h.LeafDirLength, h.TileDataOffset)
	}

	meta, err := reader.ReadMetadata()
	if err != nil {
		t.Fatalf("validatePMTiles: ReadMetadata: %v", err)
	}

	zoomCounts := make(map[int]int)
	for z := int(h.MinZoom); z <= int(h.MaxZoom); z++ {
		tiles := reader.TilesAtZoom(z)
		zoomCounts[z] = len(tiles)
	}

	return pmtilesResult{
		Header:     h,
		Metadata:   meta,
		TileCount:  reader.NumTiles(),
		ZoomCounts: zoomCounts,
	}
}

// assertTileDecodesAsImage reads and decodes a tile, asserting it is valid.
func assertTileDecodesAsImage(t *testing.T, path string, z, x, y int) image.Image {
	t.Helper()

	reader, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatalf("assertTileDecodesAsImage: OpenReader: %v", err)
	}
	defer reader.Close()

	h := reader.Header()
	format := pmtiles.TileTypeString(h.TileType)

	data, err := reader.ReadTile(z, x, y)
	if err != nil {
		t.Fatalf("assertTileDecodesAsImage: ReadTile(%d/%d/%d): %v", z, x, y, err)
	}
	if data == nil {
		t.Fatalf("assertTileDecodesAsImage: tile %d/%d/%d not found", z, x, y)
	}

	img, err := encode.DecodeImage(data, format)
	if err != nil {
		t.Fatalf("assertTileDecodesAsImage: DecodeImage(%d/%d/%d): %v", z, x, y, err)
	}
	return img
}

// assertTilePixel checks that a pixel in a tile matches expected RGBA values within tolerance.
func assertTilePixel(t *testing.T, path string, z, x, y int, px, py int, wantR, wantG, wantB, wantA uint8, tolerance int) {
	t.Helper()

	img := assertTileDecodesAsImage(t, path, z, x, y)
	r, g, b, a := img.At(px, py).RGBA()
	gotR, gotG, gotB, gotA := uint8(r>>8), uint8(g>>8), uint8(b>>8), uint8(a>>8)

	if absDiff(int(gotR), int(wantR)) > tolerance ||
		absDiff(int(gotG), int(wantG)) > tolerance ||
		absDiff(int(gotB), int(wantB)) > tolerance ||
		absDiff(int(gotA), int(wantA)) > tolerance {
		t.Errorf("tile %d/%d/%d pixel (%d,%d): got rgba(%d,%d,%d,%d), want rgba(%d,%d,%d,%d) ±%d",
			z, x, y, px, py, gotR, gotG, gotB, gotA, wantR, wantG, wantB, wantA, tolerance)
	}
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// ---------------------------------------------------------------------------
// Plausibility validation for satellite integration tests
// ---------------------------------------------------------------------------

// plausibilityExpectation defines expected properties of a PMTiles output
// for comprehensive plausibility validation.
type plausibilityExpectation struct {
	MinZoom  int
	MaxZoom  int
	TileType uint8

	// Geographic bounds (approximate).
	MinLon, MaxLon float64
	MinLat, MaxLat float64
	BoundsTol      float64 // tolerance in degrees for each bound

	MinTotalTiles int // minimum expected total tile count
}

// assertPlausiblePMTiles performs comprehensive plausibility checks on a
// PMTiles output file against the given expectations.
func assertPlausiblePMTiles(t *testing.T, path string, exp plausibilityExpectation) {
	t.Helper()

	result := validatePMTiles(t, path)
	h := result.Header

	// 1. Zoom levels
	if int(h.MinZoom) != exp.MinZoom {
		t.Errorf("MinZoom = %d, want %d", h.MinZoom, exp.MinZoom)
	}
	if int(h.MaxZoom) != exp.MaxZoom {
		t.Errorf("MaxZoom = %d, want %d", h.MaxZoom, exp.MaxZoom)
	}

	// 2. Tile type
	if h.TileType != exp.TileType {
		t.Errorf("TileType = %d, want %d", h.TileType, exp.TileType)
	}

	// 3. Geographic bounds — valid and within tolerance of expected area
	if h.MinLon >= h.MaxLon {
		t.Errorf("invalid bounds: MinLon (%f) >= MaxLon (%f)", h.MinLon, h.MaxLon)
	}
	if h.MinLat >= h.MaxLat {
		t.Errorf("invalid bounds: MinLat (%f) >= MaxLat (%f)", h.MinLat, h.MaxLat)
	}
	if math.Abs(float64(h.MinLon)-exp.MinLon) > exp.BoundsTol {
		t.Errorf("MinLon = %f, want ~%f (±%f)", h.MinLon, exp.MinLon, exp.BoundsTol)
	}
	if math.Abs(float64(h.MaxLon)-exp.MaxLon) > exp.BoundsTol {
		t.Errorf("MaxLon = %f, want ~%f (±%f)", h.MaxLon, exp.MaxLon, exp.BoundsTol)
	}
	if math.Abs(float64(h.MinLat)-exp.MinLat) > exp.BoundsTol {
		t.Errorf("MinLat = %f, want ~%f (±%f)", h.MinLat, exp.MinLat, exp.BoundsTol)
	}
	if math.Abs(float64(h.MaxLat)-exp.MaxLat) > exp.BoundsTol {
		t.Errorf("MaxLat = %f, want ~%f (±%f)", h.MaxLat, exp.MaxLat, exp.BoundsTol)
	}

	// 4. Center point — within bounds and CenterZoom within zoom range
	if h.CenterLon < h.MinLon || h.CenterLon > h.MaxLon {
		t.Errorf("CenterLon %f outside bounds [%f, %f]", h.CenterLon, h.MinLon, h.MaxLon)
	}
	if h.CenterLat < h.MinLat || h.CenterLat > h.MaxLat {
		t.Errorf("CenterLat %f outside bounds [%f, %f]", h.CenterLat, h.MinLat, h.MaxLat)
	}
	if int(h.CenterZoom) < exp.MinZoom || int(h.CenterZoom) > exp.MaxZoom {
		t.Errorf("CenterZoom %d outside range [%d, %d]", h.CenterZoom, exp.MinZoom, exp.MaxZoom)
	}

	// 5. Tile counts — total, per-zoom non-zero, non-decreasing across zoom levels
	if result.TileCount < exp.MinTotalTiles {
		t.Errorf("TileCount = %d, want >= %d", result.TileCount, exp.MinTotalTiles)
	}
	prevCount := 0
	for z := exp.MinZoom; z <= exp.MaxZoom; z++ {
		count := result.ZoomCounts[z]
		if count == 0 {
			t.Errorf("expected tiles at zoom %d, got 0", z)
		}
		if count < prevCount {
			t.Errorf("tile count decreased from zoom %d (%d) to zoom %d (%d)", z-1, prevCount, z, count)
		}
		prevCount = count
	}

	// 6. Tile decoding — first tile at MaxZoom decodes as a valid image
	reader, err := pmtiles.OpenReader(path)
	if err != nil {
		t.Fatalf("plausibility: OpenReader: %v", err)
	}
	defer reader.Close()

	maxZoomTiles := reader.TilesAtZoom(exp.MaxZoom)
	if len(maxZoomTiles) > 0 {
		tile := maxZoomTiles[0]
		img := assertTileDecodesAsImage(t, path, tile[0], tile[1], tile[2])
		bounds := img.Bounds()
		if bounds.Dx() == 0 || bounds.Dy() == 0 {
			t.Errorf("decoded tile %d/%d/%d has zero dimensions", tile[0], tile[1], tile[2])
		}
	}

	// 7. Clustering
	if !h.Clustered {
		t.Error("expected Clustered = true")
	}

	// 8. Metadata — non-empty with required keys
	if result.Metadata == nil {
		t.Error("metadata is nil")
	} else {
		for _, key := range []string{"name", "format", "bounds", "minzoom", "maxzoom"} {
			if _, ok := result.Metadata[key]; !ok {
				t.Errorf("metadata missing required key %q", key)
			}
		}
	}

	t.Logf("plausibility: %d tiles across zoom %d-%d, bounds [%.2f,%.2f,%.2f,%.2f]",
		result.TileCount, exp.MinZoom, exp.MaxZoom,
		h.MinLon, h.MinLat, h.MaxLon, h.MaxLat)
}
