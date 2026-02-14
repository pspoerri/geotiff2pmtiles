package tile

import (
	"image"
	"image/color"
	"math"

	"github.com/pspoerri/geotiff2pmtiles/internal/cog"
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
)

// sourceInfo caches per-source metadata used during rendering.
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

// renderTile renders a single web map tile by reprojecting from source COG data.
// Uses per-pixel inverse projection from the output tile to source CRS coordinates,
// then bilinear-interpolates from the source raster.
func renderTile(z, tx, ty, tileSize int, sources []*cog.Reader, proj coord.Projection, cache *cog.TileCache) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))

	// Pre-compute the output pixel size in meters for selecting the best overview level.
	_, midLat, _, _ := coord.TileBounds(z, tx, ty)
	outputRes := coord.ResolutionAtLat(midLat, z)

	hasData := false
	srcInfos := buildSourceInfos(sources)

	for py := 0; py < tileSize; py++ {
		for px := 0; px < tileSize; px++ {
			// Convert output pixel center to WGS84.
			lon, lat := coord.PixelToLonLat(z, tx, ty, tileSize, float64(px)+0.5, float64(py)+0.5)

			// Convert WGS84 to source CRS.
			srcX, srcY := proj.FromWGS84(lon, lat)

			// Find the best source that covers this point and sample.
			r, g, b, a, found := sampleFromSources(srcInfos, srcX, srcY, outputRes, cache)
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

// sampleFromSources tries each source to sample a pixel at the given CRS coordinates.
func sampleFromSources(sources []sourceInfo, srcX, srcY, outputRes float64, cache *cog.TileCache) (r, g, b, a uint8, found bool) {
	for i := range sources {
		src := &sources[i]

		// Check if point is within this source's bounds.
		if srcX < src.minCRSX || srcX > src.maxCRSX || srcY < src.minCRSY || srcY > src.maxCRSY {
			continue
		}

		// Choose the best overview level for the output resolution.
		level := src.reader.OverviewForZoom(outputRes)
		levelPixelSize := src.reader.IFDPixelSize(level)

		// Convert CRS coordinates to pixel coordinates at this IFD level.
		pixX := (srcX - src.geo.OriginX) / levelPixelSize
		pixY := (src.geo.OriginY - srcY) / levelPixelSize

		// Check bounds.
		imgW := src.reader.IFDWidth(level)
		imgH := src.reader.IFDHeight(level)
		if pixX < 0 || pixX >= float64(imgW) || pixY < 0 || pixY >= float64(imgH) {
			continue
		}

		// Bilinear interpolation.
		r, g, b, a, err := bilinearSampleCached(src.reader, level, pixX, pixY, imgW, imgH, cache)
		if err != nil {
			continue
		}
		return r, g, b, a, true
	}
	return 0, 0, 0, 0, false
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
		tile = cache.Get(src.Path(), level, col, row)
	}
	if tile == nil {
		var err error
		tile, err = src.ReadTile(level, col, row)
		if err != nil {
			return [4]uint8{}, err
		}
		if cache != nil {
			cache.Put(src.Path(), level, col, row, tile)
		}
	}

	rr, g, b, a := tile.At(localX, localY).RGBA()
	return [4]uint8{uint8(rr >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}, nil
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
