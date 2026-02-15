package tile

import (
	"image"
	"image/color"
	"math"
	"strconv"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
	"github.com/pspoerri/geotiff2pmtiles/internal/encode"
)

// sourceInfo caches per-source metadata used during rendering and prefetching.
type sourceInfo struct {
	reader  *cog.Reader
	minCRSX float64
	minCRSY float64
	maxCRSX float64
	maxCRSY float64
	geo     cog.GeoInfo
}

func buildSourceInfos(sources []*cog.Reader) []sourceInfo {
	infos := make([]sourceInfo, len(sources))
	for i, src := range sources {
		minX, minY, maxX, maxY := src.BoundsInCRS()
		infos[i] = sourceInfo{
			reader:  src,
			minCRSX: minX,
			minCRSY: minY,
			maxCRSX: maxX,
			maxCRSY: maxY,
			geo:     src.GeoInfo(),
		}
	}
	return infos
}

// tileSource is a sourceInfo augmented with per-tile pre-computed data.
// The overview level, pixel size, and image dimensions are constant for all
// pixels within a single output tile, so computing them once per tile instead
// of per pixel eliminates millions of redundant OverviewForZoom iterations.
type tileSource struct {
	reader         *cog.Reader
	geo            cog.GeoInfo
	minCRSX        float64
	minCRSY        float64
	maxCRSX        float64
	maxCRSY        float64
	level          int
	levelPixelSize float64
	imgW           int
	imgH           int
}

// prepareTileSources filters the full source list to only those overlapping
// the output tile's CRS bounding box, and pre-computes the overview level
// and pixel dimensions for each. The returned slice is typically much smaller
// than the full source list, dramatically reducing per-pixel iteration.
func prepareTileSources(srcInfos []sourceInfo, outputResCRS float64, tileMinCRSX, tileMinCRSY, tileMaxCRSX, tileMaxCRSY float64) []tileSource {
	var result []tileSource
	for i := range srcInfos {
		src := &srcInfos[i]
		// Skip sources that don't overlap the output tile.
		if tileMaxCRSX < src.minCRSX || tileMinCRSX > src.maxCRSX ||
			tileMaxCRSY < src.minCRSY || tileMinCRSY > src.maxCRSY {
			continue
		}
		level := src.reader.OverviewForZoom(outputResCRS)
		result = append(result, tileSource{
			reader:         src.reader,
			geo:            src.geo,
			minCRSX:        src.minCRSX,
			minCRSY:        src.minCRSY,
			maxCRSX:        src.maxCRSX,
			maxCRSY:        src.maxCRSY,
			level:          level,
			levelPixelSize: src.reader.IFDPixelSize(level),
			imgW:           src.reader.IFDWidth(level),
			imgH:           src.reader.IFDHeight(level),
		})
	}
	return result
}

// tileCRSBounds computes the CRS bounding box of an output tile by projecting
// all four corners and taking the extremes.
func tileCRSBounds(z, tx, ty int, proj coord.Projection) (minX, minY, maxX, maxY float64) {
	minLon, minLat, maxLon, maxLat := coord.TileBounds(z, tx, ty)
	x1, y1 := proj.FromWGS84(minLon, minLat)
	x2, y2 := proj.FromWGS84(minLon, maxLat)
	x3, y3 := proj.FromWGS84(maxLon, minLat)
	x4, y4 := proj.FromWGS84(maxLon, maxLat)
	minX = math.Min(math.Min(x1, x2), math.Min(x3, x4))
	maxX = math.Max(math.Max(x1, x2), math.Max(x3, x4))
	minY = math.Min(math.Min(y1, y2), math.Min(y3, y4))
	maxY = math.Max(math.Max(y1, y2), math.Max(y3, y4))
	return
}

// renderTile renders a single web map tile by reprojecting from source COG data.
// Uses per-pixel inverse projection from the output tile to source CRS coordinates,
// then samples from the source raster using the selected interpolation mode.
func renderTile(z, tx, ty, tileSize int, srcInfos []sourceInfo, proj coord.Projection, cache *cog.TileCache, mode Resampling) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))

	// Pre-compute the output pixel size in CRS units for selecting the best overview level.
	// ResolutionAtLat returns meters/pixel; convert to CRS units for the source projection.
	_, midLat, _, _ := coord.TileBounds(z, tx, ty)
	outputResMeters := coord.ResolutionAtLat(midLat, z)
	outputResCRS := coord.MetersToPixelSizeCRS(outputResMeters, proj.EPSG(), midLat)

	// Pre-filter sources to only those overlapping this tile and pre-compute
	// their overview levels. This avoids calling OverviewForZoom (which loops
	// over all IFD levels) for every pixel — a major hot-spot in the profile.
	tileMinX, tileMinY, tileMaxX, tileMaxY := tileCRSBounds(z, tx, ty, proj)
	tileSrcs := prepareTileSources(srcInfos, outputResCRS, tileMinX, tileMinY, tileMaxX, tileMaxY)
	if len(tileSrcs) == 0 {
		return nil
	}

	hasData := false

	for py := 0; py < tileSize; py++ {
		for px := 0; px < tileSize; px++ {
			// Convert output pixel center to WGS84.
			lon, lat := coord.PixelToLonLat(z, tx, ty, tileSize, float64(px)+0.5, float64(py)+0.5)

			// Convert WGS84 to source CRS.
			srcX, srcY := proj.FromWGS84(lon, lat)

			// Find the best source that covers this point and sample.
			r, g, b, a, found := sampleFromTileSources(tileSrcs, srcX, srcY, cache, mode)
			if found {
				img.SetRGBA(px, py, color.RGBA{R: r, G: g, B: b, A: a})
				hasData = true
			}
		}
	}

	if !hasData {
		return nil // signal: empty tile
	}

	return img
}

// sampleFromTileSources tries each pre-filtered tile source to sample a pixel
// at the given CRS coordinates. Uses pre-computed overview levels and dimensions
// to avoid redundant per-pixel computation.
func sampleFromTileSources(sources []tileSource, srcX, srcY float64, cache *cog.TileCache, mode Resampling) (r, g, b, a uint8, found bool) {
	for i := range sources {
		src := &sources[i]

		// Check if point is within this source's bounds.
		if srcX < src.minCRSX || srcX > src.maxCRSX || srcY < src.minCRSY || srcY > src.maxCRSY {
			continue
		}

		// Convert CRS coordinates to pixel coordinates using pre-computed level data.
		pixX := (srcX - src.geo.OriginX) / src.levelPixelSize
		pixY := (src.geo.OriginY - srcY) / src.levelPixelSize

		// Check bounds.
		if pixX < 0 || pixX >= float64(src.imgW) || pixY < 0 || pixY >= float64(src.imgH) {
			continue
		}

		var rr, gg, bb, aa uint8
		var err error

		switch mode {
		case ResamplingNearest:
			rr, gg, bb, aa, err = nearestSampleCached(src.reader, src.level, pixX, pixY, cache)
		default:
			rr, gg, bb, aa, err = bilinearSampleCached(src.reader, src.level, pixX, pixY, src.imgW, src.imgH, cache)
		}

		if err != nil {
			continue
		}
		return rr, gg, bb, aa, true
	}
	return 0, 0, 0, 0, false
}

// nearestSampleCached reads the nearest (closest) source pixel.
func nearestSampleCached(src *cog.Reader, level int, fx, fy float64, cache *cog.TileCache) (uint8, uint8, uint8, uint8, error) {
	px := int(math.Floor(fx + 0.5))
	py := int(math.Floor(fy + 0.5))

	p, err := readPixelCached(src, level, px, py, cache)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return p[0], p[1], p[2], p[3], nil
}

// bilinearSampleCached performs bilinear interpolation using the tile cache.
func bilinearSampleCached(src *cog.Reader, level int, fx, fy float64, imgW, imgH int, cache *cog.TileCache) (uint8, uint8, uint8, uint8, error) {
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := x0 + 1
	y1 := y0 + 1

	x0 = clamp(x0, 0, imgW-1)
	y0 = clamp(y0, 0, imgH-1)
	x1 = clamp(x1, 0, imgW-1)
	y1 = clamp(y1, 0, imgH-1)

	dx := fx - math.Floor(fx)
	dy := fy - math.Floor(fy)

	// Read pixels using cached tile reads.
	p00, err := readPixelCached(src, level, x0, y0, cache)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	p10, err := readPixelCached(src, level, x1, y0, cache)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	p01, err := readPixelCached(src, level, x0, y1, cache)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	p11, err := readPixelCached(src, level, x1, y1, cache)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	lerp := func(a, b float64, t float64) float64 {
		return a*(1-t) + b*t
	}
	bilerp := func(v00, v10, v01, v11 uint8) uint8 {
		top := lerp(float64(v00), float64(v10), dx)
		bot := lerp(float64(v01), float64(v11), dx)
		v := lerp(top, bot, dy)
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(v)
	}

	return bilerp(p00[0], p10[0], p01[0], p11[0]),
		bilerp(p00[1], p10[1], p01[1], p11[1]),
		bilerp(p00[2], p10[2], p01[2], p11[2]),
		bilerp(p00[3], p10[3], p01[3], p11[3]), nil
}

// readPixelCached reads a single pixel using the tile cache.
// Uses type-specific pixel reads to avoid the interface boxing overhead from
// image.At() returning color.Color (which was 10% of total CPU time).
func readPixelCached(src *cog.Reader, level, px, py int, cache *cog.TileCache) ([4]uint8, error) {
	ifd := src.IFDTileSize(level)
	tw := ifd[0]
	th := ifd[1]

	col := px / tw
	row := py / th
	localX := px % tw
	localY := py % th

	// Try cache first.
	var tile image.Image
	if cache != nil {
		tile = cache.Get(src.ID(), level, col, row)
	}
	if tile == nil {
		var err error
		tile, err = src.ReadTile(level, col, row)
		if err != nil {
			return [4]uint8{}, err
		}
		if cache != nil {
			cache.Put(src.ID(), level, col, row, tile)
		}
	}

	// Type-switch to avoid interface boxing: image.At() returns color.Color
	// which heap-allocates the concrete color value. Using the type-specific
	// methods (YCbCrAt, RGBAAt) returns by value with no allocation.
	switch img := tile.(type) {
	case *image.YCbCr:
		c := img.YCbCrAt(localX, localY)
		rr, g, b, _ := c.RGBA()
		return [4]uint8{uint8(rr >> 8), uint8(g >> 8), uint8(b >> 8), 255}, nil
	case *image.RGBA:
		c := img.RGBAAt(localX, localY)
		return [4]uint8{c.R, c.G, c.B, c.A}, nil
	default:
		rr, g, b, a := tile.At(localX, localY).RGBA()
		return [4]uint8{uint8(rr >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}, nil
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// renderTileTerrarium renders a single web map tile from float GeoTIFF data,
// converting elevation values to Terrarium RGB encoding.
func renderTileTerrarium(z, tx, ty, tileSize int, srcInfos []sourceInfo, proj coord.Projection, cache *cog.FloatTileCache, mode Resampling) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))

	_, midLat, _, _ := coord.TileBounds(z, tx, ty)
	outputResMeters := coord.ResolutionAtLat(midLat, z)
	outputResCRS := coord.MetersToPixelSizeCRS(outputResMeters, proj.EPSG(), midLat)

	// Pre-filter sources and pre-compute overview levels for this tile.
	tileMinX, tileMinY, tileMaxX, tileMaxY := tileCRSBounds(z, tx, ty, proj)
	tileSrcs := prepareTileSources(srcInfos, outputResCRS, tileMinX, tileMinY, tileMaxX, tileMaxY)
	if len(tileSrcs) == 0 {
		return nil
	}

	hasData := false

	// Parse nodata values from the active sources.
	nodataValues := make([]float64, len(tileSrcs))
	for i := range tileSrcs {
		nd := tileSrcs[i].reader.NoData()
		if nd != "" {
			v, err := strconv.ParseFloat(nd, 64)
			if err == nil {
				nodataValues[i] = v
			} else {
				nodataValues[i] = math.NaN()
			}
		} else {
			nodataValues[i] = math.NaN()
		}
	}

	for py := 0; py < tileSize; py++ {
		for px := 0; px < tileSize; px++ {
			lon, lat := coord.PixelToLonLat(z, tx, ty, tileSize, float64(px)+0.5, float64(py)+0.5)
			srcX, srcY := proj.FromWGS84(lon, lat)

			elevation, found := sampleFromTileSourcesFloat(tileSrcs, nodataValues, srcX, srcY, cache, mode)
			if found && !math.IsNaN(elevation) {
				img.SetRGBA(px, py, encode.ElevationToTerrarium(elevation))
				hasData = true
			}
			// nodata pixels remain transparent (zero RGBA)
		}
	}

	if !hasData {
		return nil
	}
	return img
}

// sampleFromTileSourcesFloat tries each pre-filtered tile source to sample a
// float elevation at the given CRS coordinates.
func sampleFromTileSourcesFloat(sources []tileSource, nodataValues []float64, srcX, srcY float64, cache *cog.FloatTileCache, mode Resampling) (float64, bool) {
	for i := range sources {
		src := &sources[i]

		if srcX < src.minCRSX || srcX > src.maxCRSX || srcY < src.minCRSY || srcY > src.maxCRSY {
			continue
		}

		pixX := (srcX - src.geo.OriginX) / src.levelPixelSize
		pixY := (src.geo.OriginY - srcY) / src.levelPixelSize

		if pixX < 0 || pixX >= float64(src.imgW) || pixY < 0 || pixY >= float64(src.imgH) {
			continue
		}

		var val float64
		var err error

		switch mode {
		case ResamplingNearest:
			val, err = nearestSampleFloat(src.reader, src.level, pixX, pixY, cache)
		default:
			val, err = bilinearSampleFloat(src.reader, src.level, pixX, pixY, src.imgW, src.imgH, cache)
		}

		if err != nil {
			continue
		}

		// Check nodata.
		if math.IsNaN(val) {
			continue
		}
		nd := nodataValues[i]
		if !math.IsNaN(nd) && val == nd {
			continue
		}

		return val, true
	}
	return math.NaN(), false
}

// nearestSampleFloat reads the nearest float pixel.
func nearestSampleFloat(src *cog.Reader, level int, fx, fy float64, cache *cog.FloatTileCache) (float64, error) {
	px := int(math.Floor(fx + 0.5))
	py := int(math.Floor(fy + 0.5))
	return readFloatPixelCached(src, level, px, py, cache)
}

// bilinearSampleFloat performs bilinear interpolation on float data.
func bilinearSampleFloat(src *cog.Reader, level int, fx, fy float64, imgW, imgH int, cache *cog.FloatTileCache) (float64, error) {
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := x0 + 1
	y1 := y0 + 1

	x0 = clamp(x0, 0, imgW-1)
	y0 = clamp(y0, 0, imgH-1)
	x1 = clamp(x1, 0, imgW-1)
	y1 = clamp(y1, 0, imgH-1)

	dx := fx - math.Floor(fx)
	dy := fy - math.Floor(fy)

	v00, err := readFloatPixelCached(src, level, x0, y0, cache)
	if err != nil {
		return math.NaN(), err
	}
	v10, err := readFloatPixelCached(src, level, x1, y0, cache)
	if err != nil {
		return math.NaN(), err
	}
	v01, err := readFloatPixelCached(src, level, x0, y1, cache)
	if err != nil {
		return math.NaN(), err
	}
	v11, err := readFloatPixelCached(src, level, x1, y1, cache)
	if err != nil {
		return math.NaN(), err
	}

	// If any neighbor is NaN, fall back to nearest.
	if math.IsNaN(v00) || math.IsNaN(v10) || math.IsNaN(v01) || math.IsNaN(v11) {
		// Use the center pixel (nearest).
		cx := int(math.Floor(fx + 0.5))
		cy := int(math.Floor(fy + 0.5))
		cx = clamp(cx, 0, imgW-1)
		cy = clamp(cy, 0, imgH-1)
		return readFloatPixelCached(src, level, cx, cy, cache)
	}

	lerp := func(a, b, t float64) float64 {
		return a*(1-t) + b*t
	}

	top := lerp(v00, v10, dx)
	bot := lerp(v01, v11, dx)
	return lerp(top, bot, dy), nil
}

// readFloatPixelCached reads a single float pixel using the tile cache.
func readFloatPixelCached(src *cog.Reader, level, px, py int, cache *cog.FloatTileCache) (float64, error) {
	tileSize := src.IFDTileSize(level)
	tw := tileSize[0]
	th := tileSize[1]

	col := px / tw
	row := py / th
	localX := px % tw
	localY := py % th

	// Try cache first.
	var tileData []float32
	var tileW int
	if cache != nil {
		tileData, tileW, _ = cache.Get(src.ID(), level, col, row)
	}
	if tileData == nil {
		var err error
		var w, h int
		tileData, w, h, err = src.ReadFloatTile(level, col, row)
		if err != nil {
			return math.NaN(), err
		}
		if tileData == nil {
			return math.NaN(), nil // empty tile
		}
		tileW = w
		if cache != nil {
			cache.Put(src.ID(), level, col, row, tileData, w, h)
		}
	}

	idx := localY*tileW + localX
	if idx < 0 || idx >= len(tileData) {
		return math.NaN(), nil
	}

	return float64(tileData[idx]), nil
}
