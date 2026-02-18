package tile

import (
	"image"
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
	tileW          int // source tile width (pixels per COG tile)
	tileH          int // source tile height (pixels per COG tile)
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
		ifd := src.reader.IFDTileSize(level)
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
			tileW:          ifd[0],
			tileH:          ifd[1],
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
//
// Instead of calling PixelToLonLat for every output pixel (which involves
// expensive trig: Atan, Sinh — 6% of CPU), we precompute longitude per column
// and latitude per row. In web Mercator tiles, longitude is perfectly linear
// with pixel X and latitude depends only on pixel Y, so we reduce trig calls
// from O(tileSize²) to O(tileSize).
func renderTile(z, tx, ty, tileSize int, srcInfos []sourceInfo, proj coord.Projection, cache *cog.TileCache, mode Resampling) *image.RGBA {
	// Pre-compute the output pixel size in CRS units for selecting the best overview level.
	_, midLat, _, _ := coord.TileBounds(z, tx, ty)
	outputResMeters := coord.ResolutionAtLat(midLat, z)
	outputResCRS := coord.MetersToPixelSizeCRS(outputResMeters, proj.EPSG(), midLat)

	// Pre-filter sources to only those overlapping this tile.
	tileMinX, tileMinY, tileMaxX, tileMaxY := tileCRSBounds(z, tx, ty, proj)
	tileSrcs := prepareTileSources(srcInfos, outputResCRS, tileMinX, tileMinY, tileMaxX, tileMaxY)
	if len(tileSrcs) == 0 {
		return nil
	}

	img := GetRGBA(tileSize, tileSize)

	// Precompute lon per column (linear with pixel X) and lat per row
	// (non-linear in Mercator, but independent of X). This reduces
	// expensive PixelToLonLat trig from tileSize² to 2×tileSize calls.
	lons := make([]float64, tileSize)
	lats := make([]float64, tileSize)
	for px := 0; px < tileSize; px++ {
		lons[px], _ = coord.PixelToLonLat(z, tx, ty, tileSize, float64(px)+0.5, 0)
	}
	for py := 0; py < tileSize; py++ {
		_, lats[py] = coord.PixelToLonLat(z, tx, ty, tileSize, 0, float64(py)+0.5)
	}

	hasData := false
	stride := img.Stride

	for py := 0; py < tileSize; py++ {
		lat := lats[py]
		rowOff := py * stride

		for px := 0; px < tileSize; px++ {
			// Convert precomputed WGS84 to source CRS.
			srcX, srcY := proj.FromWGS84(lons[px], lat)

			r, g, b, a, found := sampleFromTileSources(tileSrcs, srcX, srcY, cache, mode)
			if found {
				off := rowOff + px*4
				img.Pix[off+0] = r
				img.Pix[off+1] = g
				img.Pix[off+2] = b
				img.Pix[off+3] = a
				hasData = true
			}
		}
	}

	if !hasData {
		PutRGBA(img)
		return nil
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
		case ResamplingLanczos:
			rr, gg, bb, aa, err = lanczosSampleCached(src.reader, src.level, pixX, pixY, src.imgW, src.imgH, src.tileW, src.tileH, cache)
		case ResamplingBicubic:
			rr, gg, bb, aa, err = bicubicSampleCached(src.reader, src.level, pixX, pixY, src.imgW, src.imgH, src.tileW, src.tileH, cache)
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
// Pixels with alpha == 0 are treated as nodata and excluded from RGB
// interpolation so they don't bleed dark colors into the result.  Alpha is
// interpolated with the standard bilinear weights so edges fade smoothly.
//
// Optimized to do at most 2 cache lookups (instead of 4): pixels in the same
// source tile are extracted directly from the already-fetched image.
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

	// Pre-compute tile coordinates for all four corners.
	ifd := src.IFDTileSize(level)
	tw := ifd[0]
	th := ifd[1]

	col0, row0 := x0/tw, y0/th
	col1, row1 := x1/tw, y1/th

	// Fetch the primary tile (top-left corner) — covers most/all 4 pixels.
	tile00, err := fetchTileCached(src, level, col0, row0, cache)
	var nodata [4]uint8

	// Read p00 and p10 from the primary tile (same row).
	var p00, p10, p01, p11 [4]uint8
	if err != nil {
		p00 = nodata
		p10 = nodata
	} else {
		p00 = pixelFromImage(tile00, x0%tw, y0%th)
		if col1 == col0 {
			// x1 is in the same tile column — no extra lookup.
			p10 = pixelFromImage(tile00, x1%tw, y0%th)
		} else {
			t, e := fetchTileCached(src, level, col1, row0, cache)
			if e != nil {
				p10 = nodata
			} else {
				p10 = pixelFromImage(t, x1%tw, y0%th)
			}
		}
	}

	// Read p01 and p11 from the bottom row.
	if row1 == row0 {
		// Same tile row — reuse tile00 (or the already-fetched secondary tile).
		if err != nil {
			p01 = nodata
			p11 = nodata
		} else {
			p01 = pixelFromImage(tile00, x0%tw, y1%th)
			if col1 == col0 {
				p11 = pixelFromImage(tile00, x1%tw, y1%th)
			} else {
				t, e := fetchTileCached(src, level, col1, row0, cache)
				if e != nil {
					p11 = nodata
				} else {
					p11 = pixelFromImage(t, x1%tw, y1%th)
				}
			}
		}
	} else {
		// Different tile row — need to fetch the bottom-left tile.
		tileBL, errBL := fetchTileCached(src, level, col0, row1, cache)
		if errBL != nil {
			p01 = nodata
			p11 = nodata
		} else {
			p01 = pixelFromImage(tileBL, x0%tw, y1%th)
			if col1 == col0 {
				p11 = pixelFromImage(tileBL, x1%tw, y1%th)
			} else {
				t, e := fetchTileCached(src, level, col1, row1, cache)
				if e != nil {
					p11 = nodata
				} else {
					p11 = pixelFromImage(t, x1%tw, y1%th)
				}
			}
		}
	}

	// Standard bilinear weights.
	w00 := (1 - dx) * (1 - dy)
	w10 := dx * (1 - dy)
	w01 := (1 - dx) * dy
	w11 := dx * dy

	// Alpha: standard bilinear interpolation (nodata pixels naturally
	// contribute 0, giving a smooth fade at data edges).
	aVal := w00*float64(p00[3]) + w10*float64(p10[3]) + w01*float64(p01[3]) + w11*float64(p11[3])

	// For RGB, zero out weights for alpha == 0 (nodata) neighbors so they
	// don't bleed black/garbage color values into the result.
	if p00[3] == 0 {
		w00 = 0
	}
	if p10[3] == 0 {
		w10 = 0
	}
	if p01[3] == 0 {
		w01 = 0
	}
	if p11[3] == 0 {
		w11 = 0
	}

	wSum := w00 + w10 + w01 + w11
	if wSum == 0 {
		// All four neighbors are nodata.
		return 0, 0, 0, 0, nil
	}

	rVal := (w00*float64(p00[0]) + w10*float64(p10[0]) + w01*float64(p01[0]) + w11*float64(p11[0])) / wSum
	gVal := (w00*float64(p00[1]) + w10*float64(p10[1]) + w01*float64(p01[1]) + w11*float64(p11[1])) / wSum
	bVal := (w00*float64(p00[2]) + w10*float64(p10[2]) + w01*float64(p01[2]) + w11*float64(p11[2])) / wSum

	return clampByte(rVal), clampByte(gVal), clampByte(bVal), clampByte(aVal), nil
}

// lanczosSampleCached performs Lanczos-3 interpolation using the tile cache.
// Uses a 6×6 pixel neighborhood for high-quality resampling with sharp detail
// preservation. Pixels with alpha == 0 are excluded from RGB interpolation
// so they don't bleed dark colors into the result. Alpha is interpolated
// with the full kernel weights for smooth edge transitions.
//
// Optimized to batch tile fetches: the 6×6 neighborhood spans at most 4 source
// tiles (2×2 tile grid). We determine which unique tiles are needed, fetch each
// once, and extract all 36 pixels directly — reducing cache lookups from 36 to
// 1-4, which eliminates the dominant bottleneck (readPixelCached was 52.8% of
// CPU time, mostly from redundant cache.Get + IFDTileSize calls).
//
// When all 36 pixels fall within a single YCbCr tile (the common case for JPEG
// COGs), a specialized fast path avoids per-pixel type assertions and method
// calls, inlining YCbCr offset computation and RGB conversion directly.
func lanczosSampleCached(src *cog.Reader, level int, fx, fy float64, imgW, imgH, tw, th int, cache *cog.TileCache) (uint8, uint8, uint8, uint8, error) {
	const a = 3
	const n = 2 * a

	ix0 := int(math.Floor(fx)) - a + 1
	iy0 := int(math.Floor(fy)) - a + 1

	// Precompute 1D weights and clamped pixel coordinates.
	var wxArr, wyArr [n]float64
	var pxArr, pyArr [n]int
	for k := 0; k < n; k++ {
		pxArr[k] = clamp(ix0+k, 0, imgW-1)
		pyArr[k] = clamp(iy0+k, 0, imgH-1)
		wxArr[k] = lanczos3LUT(fx - float64(ix0+k))
		wyArr[k] = lanczos3LUT(fy - float64(iy0+k))
	}

	// Determine the tile column/row range for the 6×6 neighborhood.
	colMin := pxArr[0] / tw
	colMax := pxArr[n-1] / tw
	rowMin := pyArr[0] / th
	rowMax := pyArr[n-1] / th

	// Precompute local coordinates for all kernel positions.
	var localX [n]int
	var localY [n]int
	for k := 0; k < n; k++ {
		localX[k] = pxArr[k] % tw
		localY[k] = pyArr[k] % th
	}

	// Single-tile fast path: when all 36 pixels are in the same tile,
	// skip per-pixel tile lookup and type assertion.
	if colMin == colMax && rowMin == rowMax {
		tile, err := fetchTileCached(src, level, colMin, rowMin, cache)
		if err != nil {
			return 0, 0, 0, 0, err
		}

		// Try YCbCr fast path (most common for JPEG COGs).
		if ycbcr, ok := tile.(*image.YCbCr); ok {
			return lanczosAccumYCbCr(ycbcr, wxArr, wyArr, localX, localY)
		}

		// Try NYCbCrA fast path (JPEG with alpha).
		if nycbcra, ok := tile.(*image.NYCbCrA); ok {
			return lanczosAccumNYCbCrA(nycbcra, wxArr, wyArr, localX, localY)
		}

		// Try RGBA fast path (PNG tiles).
		if rgba, ok := tile.(*image.RGBA); ok {
			return lanczosAccumRGBA(rgba, wxArr, wyArr, localX, localY)
		}

		// Generic fallback for single tile.
		return lanczosAccumGeneric(tile, wxArr, wyArr, localX, localY)
	}

	// Multi-tile path: fetch the unique tiles (at most 2×2 = 4).
	var tiles [2][2]image.Image
	var tileOK [2][2]bool
	for r := rowMin; r <= rowMax; r++ {
		for c := colMin; c <= colMax; c++ {
			tile, err := fetchTileCached(src, level, c, r, cache)
			if err == nil {
				tiles[r-rowMin][c-colMin] = tile
				tileOK[r-rowMin][c-colMin] = true
			}
		}
	}

	// Precompute per-position tile indices.
	var tileColIdx [n]int
	var tileRowIdx [n]int
	for k := 0; k < n; k++ {
		tileColIdx[k] = pxArr[k]/tw - colMin
		tileRowIdx[k] = pyArr[k]/th - rowMin
	}

	var rSum, gSum, bSum, aSum, wTotal, wRGB float64

	for ky := 0; ky < n; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		tr := tileRowIdx[ky]
		ly := localY[ky]

		for kx := 0; kx < n; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			tc := tileColIdx[kx]
			if !tileOK[tr][tc] {
				continue
			}

			p := pixelFromImage(tiles[tr][tc], localX[kx], ly)

			aSum += float64(p[3]) * wt
			wTotal += wt
			if p[3] > 0 {
				rSum += float64(p[0]) * wt
				gSum += float64(p[1]) * wt
				bSum += float64(p[2]) * wt
				wRGB += wt
			}
		}
	}

	if wRGB == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wRGB), clampByte(gSum / wRGB), clampByte(bSum / wRGB), clampByte(aSum / wTotal), nil
}

// lanczosAccumYCbCr is the hot inner loop for Lanczos-3 on YCbCr tiles.
// Type assertion and stride lookups happen once; per-pixel work is pure
// integer arithmetic with no interface dispatch.
func lanczosAccumYCbCr(img *image.YCbCr, wxArr, wyArr [6]float64, lx, ly [6]int) (uint8, uint8, uint8, uint8, error) {
	yStride := img.YStride
	cStride := img.CStride
	ratio := img.SubsampleRatio
	yData := img.Y
	cbData := img.Cb
	crData := img.Cr

	var rSum, gSum, bSum, wTotal float64

	for ky := 0; ky < 6; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		y := ly[ky]
		yRowOff := y * yStride
		var cRowOff int
		switch ratio {
		case image.YCbCrSubsampleRatio420:
			cRowOff = (y / 2) * cStride
		case image.YCbCrSubsampleRatio422:
			cRowOff = y * cStride
		default:
			cRowOff = y * cStride
		}

		for kx := 0; kx < 6; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			x := lx[kx]
			yi := yRowOff + x
			var ci int
			switch ratio {
			case image.YCbCrSubsampleRatio420:
				ci = cRowOff + x/2
			case image.YCbCrSubsampleRatio422:
				ci = cRowOff + x/2
			default:
				ci = cRowOff + x
			}

			yy1 := int32(yData[yi]) * 0x10101
			cb1 := int32(cbData[ci]) - 128
			cr1 := int32(crData[ci]) - 128
			r := yy1 + 91881*cr1
			g := yy1 - 22554*cb1 - 46802*cr1
			b := yy1 + 116130*cb1
			if r < 0 {
				r = 0
			} else if r > 0xFF0000 {
				r = 0xFF0000
			}
			if g < 0 {
				g = 0
			} else if g > 0xFF0000 {
				g = 0xFF0000
			}
			if b < 0 {
				b = 0
			} else if b > 0xFF0000 {
				b = 0xFF0000
			}

			rSum += float64(r>>16) * wt
			gSum += float64(g>>16) * wt
			bSum += float64(b>>16) * wt
			wTotal += wt
		}
	}

	if wTotal == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wTotal), clampByte(gSum / wTotal), clampByte(bSum / wTotal), 255, nil
}

// lanczosAccumNYCbCrA is the hot inner loop for Lanczos-3 on NYCbCrA tiles.
func lanczosAccumNYCbCrA(img *image.NYCbCrA, wxArr, wyArr [6]float64, lx, ly [6]int) (uint8, uint8, uint8, uint8, error) {
	yStride := img.YStride
	cStride := img.CStride
	aStride := img.AStride
	ratio := img.SubsampleRatio
	yData := img.Y
	cbData := img.Cb
	crData := img.Cr
	aData := img.A

	var rSum, gSum, bSum, aSum, wTotal, wRGB float64

	for ky := 0; ky < 6; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		y := ly[ky]
		yRowOff := y * yStride
		aRowOff := y * aStride
		var cRowOff int
		switch ratio {
		case image.YCbCrSubsampleRatio420:
			cRowOff = (y / 2) * cStride
		case image.YCbCrSubsampleRatio422:
			cRowOff = y * cStride
		default:
			cRowOff = y * cStride
		}

		for kx := 0; kx < 6; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			x := lx[kx]
			yi := yRowOff + x
			ai := aRowOff + x
			var ci int
			switch ratio {
			case image.YCbCrSubsampleRatio420:
				ci = cRowOff + x/2
			case image.YCbCrSubsampleRatio422:
				ci = cRowOff + x/2
			default:
				ci = cRowOff + x
			}

			alpha := aData[ai]
			aSum += float64(alpha) * wt
			wTotal += wt

			if alpha > 0 {
				yy1 := int32(yData[yi]) * 0x10101
				cb1 := int32(cbData[ci]) - 128
				cr1 := int32(crData[ci]) - 128
				r := yy1 + 91881*cr1
				g := yy1 - 22554*cb1 - 46802*cr1
				b := yy1 + 116130*cb1
				if r < 0 {
					r = 0
				} else if r > 0xFF0000 {
					r = 0xFF0000
				}
				if g < 0 {
					g = 0
				} else if g > 0xFF0000 {
					g = 0xFF0000
				}
				if b < 0 {
					b = 0
				} else if b > 0xFF0000 {
					b = 0xFF0000
				}

				rSum += float64(r>>16) * wt
				gSum += float64(g>>16) * wt
				bSum += float64(b>>16) * wt
				wRGB += wt
			}
		}
	}

	if wRGB == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wRGB), clampByte(gSum / wRGB), clampByte(bSum / wRGB), clampByte(aSum / wTotal), nil
}

// lanczosAccumRGBA is the hot inner loop for Lanczos-3 on RGBA tiles.
func lanczosAccumRGBA(img *image.RGBA, wxArr, wyArr [6]float64, lx, ly [6]int) (uint8, uint8, uint8, uint8, error) {
	pix := img.Pix
	stride := img.Stride

	var rSum, gSum, bSum, aSum, wTotal, wRGB float64

	for ky := 0; ky < 6; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		rowOff := ly[ky] * stride

		for kx := 0; kx < 6; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			off := rowOff + lx[kx]*4
			alpha := pix[off+3]
			aSum += float64(alpha) * wt
			wTotal += wt
			if alpha > 0 {
				rSum += float64(pix[off+0]) * wt
				gSum += float64(pix[off+1]) * wt
				bSum += float64(pix[off+2]) * wt
				wRGB += wt
			}
		}
	}

	if wRGB == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wRGB), clampByte(gSum / wRGB), clampByte(bSum / wRGB), clampByte(aSum / wTotal), nil
}

// lanczosAccumGeneric is a fallback for rare tile types using the image.Image interface.
func lanczosAccumGeneric(tile image.Image, wxArr, wyArr [6]float64, lx, ly [6]int) (uint8, uint8, uint8, uint8, error) {
	var rSum, gSum, bSum, aSum, wTotal, wRGB float64

	for ky := 0; ky < 6; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		for kx := 0; kx < 6; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}
			p := pixelFromImage(tile, lx[kx], ly[ky])

			aSum += float64(p[3]) * wt
			wTotal += wt
			if p[3] > 0 {
				rSum += float64(p[0]) * wt
				gSum += float64(p[1]) * wt
				bSum += float64(p[2]) * wt
				wRGB += wt
			}
		}
	}

	if wRGB == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wRGB), clampByte(gSum / wRGB), clampByte(bSum / wRGB), clampByte(aSum / wTotal), nil
}

// bicubicSampleCached performs Catmull-Rom bicubic interpolation using the
// tile cache. Uses a 4×4 pixel neighborhood — sharper than bilinear with less
// ringing than Lanczos-3. Pixels with alpha == 0 are excluded from RGB
// interpolation. Optimized with batched tile fetches: the 4×4 neighborhood
// spans at most 2×2 source tiles.
func bicubicSampleCached(src *cog.Reader, level int, fx, fy float64, imgW, imgH, tw, th int, cache *cog.TileCache) (uint8, uint8, uint8, uint8, error) {
	const n = 4

	ix0 := int(math.Floor(fx)) - 1
	iy0 := int(math.Floor(fy)) - 1

	var wxArr, wyArr [n]float64
	var pxArr, pyArr [n]int
	for k := 0; k < n; k++ {
		pxArr[k] = clamp(ix0+k, 0, imgW-1)
		pyArr[k] = clamp(iy0+k, 0, imgH-1)
		wxArr[k] = bicubicLUT(fx - float64(ix0+k))
		wyArr[k] = bicubicLUT(fy - float64(iy0+k))
	}

	colMin := pxArr[0] / tw
	colMax := pxArr[n-1] / tw
	rowMin := pyArr[0] / th
	rowMax := pyArr[n-1] / th

	var localX, localY [n]int
	for k := 0; k < n; k++ {
		localX[k] = pxArr[k] % tw
		localY[k] = pyArr[k] % th
	}

	// Single-tile fast path.
	if colMin == colMax && rowMin == rowMax {
		tile, err := fetchTileCached(src, level, colMin, rowMin, cache)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		if ycbcr, ok := tile.(*image.YCbCr); ok {
			return bicubicAccumYCbCr(ycbcr, wxArr, wyArr, localX, localY)
		}
		if nycbcra, ok := tile.(*image.NYCbCrA); ok {
			return bicubicAccumNYCbCrA(nycbcra, wxArr, wyArr, localX, localY)
		}
		if rgba, ok := tile.(*image.RGBA); ok {
			return bicubicAccumRGBA(rgba, wxArr, wyArr, localX, localY)
		}
		return bicubicAccumGeneric(tile, wxArr, wyArr, localX, localY)
	}

	// Multi-tile path: fetch the unique tiles (at most 2×2 = 4).
	var tiles [2][2]image.Image
	var tileOK [2][2]bool
	for r := rowMin; r <= rowMax; r++ {
		for c := colMin; c <= colMax; c++ {
			tile, err := fetchTileCached(src, level, c, r, cache)
			if err == nil {
				tiles[r-rowMin][c-colMin] = tile
				tileOK[r-rowMin][c-colMin] = true
			}
		}
	}

	var tileColIdx, tileRowIdx [n]int
	for k := 0; k < n; k++ {
		tileColIdx[k] = pxArr[k]/tw - colMin
		tileRowIdx[k] = pyArr[k]/th - rowMin
	}

	var rSum, gSum, bSum, aSum, wTotal, wRGB float64

	for ky := 0; ky < n; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		tr := tileRowIdx[ky]
		ly := localY[ky]

		for kx := 0; kx < n; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}
			tc := tileColIdx[kx]
			if !tileOK[tr][tc] {
				continue
			}
			p := pixelFromImage(tiles[tr][tc], localX[kx], ly)

			aSum += float64(p[3]) * wt
			wTotal += wt
			if p[3] > 0 {
				rSum += float64(p[0]) * wt
				gSum += float64(p[1]) * wt
				bSum += float64(p[2]) * wt
				wRGB += wt
			}
		}
	}

	if wRGB == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wRGB), clampByte(gSum / wRGB), clampByte(bSum / wRGB), clampByte(aSum / wTotal), nil
}

// bicubicAccumYCbCr is the inner loop for bicubic on YCbCr tiles.
func bicubicAccumYCbCr(img *image.YCbCr, wxArr, wyArr [4]float64, lx, ly [4]int) (uint8, uint8, uint8, uint8, error) {
	yStride := img.YStride
	cStride := img.CStride
	ratio := img.SubsampleRatio
	yData := img.Y
	cbData := img.Cb
	crData := img.Cr

	var rSum, gSum, bSum, wTotal float64

	for ky := 0; ky < 4; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		y := ly[ky]
		yRowOff := y * yStride
		var cRowOff int
		switch ratio {
		case image.YCbCrSubsampleRatio420:
			cRowOff = (y / 2) * cStride
		case image.YCbCrSubsampleRatio422:
			cRowOff = y * cStride
		default:
			cRowOff = y * cStride
		}

		for kx := 0; kx < 4; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			x := lx[kx]
			yi := yRowOff + x
			var ci int
			switch ratio {
			case image.YCbCrSubsampleRatio420:
				ci = cRowOff + x/2
			case image.YCbCrSubsampleRatio422:
				ci = cRowOff + x/2
			default:
				ci = cRowOff + x
			}

			yy1 := int32(yData[yi]) * 0x10101
			cb1 := int32(cbData[ci]) - 128
			cr1 := int32(crData[ci]) - 128
			r := yy1 + 91881*cr1
			g := yy1 - 22554*cb1 - 46802*cr1
			b := yy1 + 116130*cb1
			if r < 0 {
				r = 0
			} else if r > 0xFF0000 {
				r = 0xFF0000
			}
			if g < 0 {
				g = 0
			} else if g > 0xFF0000 {
				g = 0xFF0000
			}
			if b < 0 {
				b = 0
			} else if b > 0xFF0000 {
				b = 0xFF0000
			}

			rSum += float64(r>>16) * wt
			gSum += float64(g>>16) * wt
			bSum += float64(b>>16) * wt
			wTotal += wt
		}
	}

	if wTotal == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wTotal), clampByte(gSum / wTotal), clampByte(bSum / wTotal), 255, nil
}

// bicubicAccumNYCbCrA is the inner loop for bicubic on NYCbCrA tiles.
func bicubicAccumNYCbCrA(img *image.NYCbCrA, wxArr, wyArr [4]float64, lx, ly [4]int) (uint8, uint8, uint8, uint8, error) {
	yStride := img.YStride
	cStride := img.CStride
	aStride := img.AStride
	ratio := img.SubsampleRatio
	yData := img.Y
	cbData := img.Cb
	crData := img.Cr
	aData := img.A

	var rSum, gSum, bSum, aSum, wTotal, wRGB float64

	for ky := 0; ky < 4; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		y := ly[ky]
		yRowOff := y * yStride
		aRowOff := y * aStride
		var cRowOff int
		switch ratio {
		case image.YCbCrSubsampleRatio420:
			cRowOff = (y / 2) * cStride
		case image.YCbCrSubsampleRatio422:
			cRowOff = y * cStride
		default:
			cRowOff = y * cStride
		}

		for kx := 0; kx < 4; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			x := lx[kx]
			yi := yRowOff + x
			ai := aRowOff + x
			var ci int
			switch ratio {
			case image.YCbCrSubsampleRatio420:
				ci = cRowOff + x/2
			case image.YCbCrSubsampleRatio422:
				ci = cRowOff + x/2
			default:
				ci = cRowOff + x
			}

			alpha := aData[ai]
			aSum += float64(alpha) * wt
			wTotal += wt

			if alpha > 0 {
				yy1 := int32(yData[yi]) * 0x10101
				cb1 := int32(cbData[ci]) - 128
				cr1 := int32(crData[ci]) - 128
				r := yy1 + 91881*cr1
				g := yy1 - 22554*cb1 - 46802*cr1
				b := yy1 + 116130*cb1
				if r < 0 {
					r = 0
				} else if r > 0xFF0000 {
					r = 0xFF0000
				}
				if g < 0 {
					g = 0
				} else if g > 0xFF0000 {
					g = 0xFF0000
				}
				if b < 0 {
					b = 0
				} else if b > 0xFF0000 {
					b = 0xFF0000
				}

				rSum += float64(r>>16) * wt
				gSum += float64(g>>16) * wt
				bSum += float64(b>>16) * wt
				wRGB += wt
			}
		}
	}

	if wRGB == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wRGB), clampByte(gSum / wRGB), clampByte(bSum / wRGB), clampByte(aSum / wTotal), nil
}

// bicubicAccumRGBA is the inner loop for bicubic on RGBA tiles.
func bicubicAccumRGBA(img *image.RGBA, wxArr, wyArr [4]float64, lx, ly [4]int) (uint8, uint8, uint8, uint8, error) {
	pix := img.Pix
	stride := img.Stride

	var rSum, gSum, bSum, aSum, wTotal, wRGB float64

	for ky := 0; ky < 4; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		rowOff := ly[ky] * stride

		for kx := 0; kx < 4; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			off := rowOff + lx[kx]*4
			alpha := pix[off+3]
			aSum += float64(alpha) * wt
			wTotal += wt
			if alpha > 0 {
				rSum += float64(pix[off+0]) * wt
				gSum += float64(pix[off+1]) * wt
				bSum += float64(pix[off+2]) * wt
				wRGB += wt
			}
		}
	}

	if wRGB == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wRGB), clampByte(gSum / wRGB), clampByte(bSum / wRGB), clampByte(aSum / wTotal), nil
}

// bicubicAccumGeneric is a fallback for rare tile types using the image.Image interface.
func bicubicAccumGeneric(tile image.Image, wxArr, wyArr [4]float64, lx, ly [4]int) (uint8, uint8, uint8, uint8, error) {
	var rSum, gSum, bSum, aSum, wTotal, wRGB float64

	for ky := 0; ky < 4; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		for kx := 0; kx < 4; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}
			p := pixelFromImage(tile, lx[kx], ly[ky])

			aSum += float64(p[3]) * wt
			wTotal += wt
			if p[3] > 0 {
				rSum += float64(p[0]) * wt
				gSum += float64(p[1]) * wt
				bSum += float64(p[2]) * wt
				wRGB += wt
			}
		}
	}

	if wRGB == 0 {
		return 0, 0, 0, 0, nil
	}

	return clampByte(rSum / wRGB), clampByte(gSum / wRGB), clampByte(bSum / wRGB), clampByte(aSum / wTotal), nil
}

// fetchTileCached retrieves a decoded tile image using the cache.
// Callers extract pixels directly from the returned image to avoid per-pixel
// cache lookups (the bilinear case needs 4 pixels from potentially the same tile).
func fetchTileCached(src *cog.Reader, level, col, row int, cache *cog.TileCache) (image.Image, error) {
	if cache != nil {
		if tile := cache.Get(src.ID(), level, col, row); tile != nil {
			return tile, nil
		}
	}
	tile, err := src.ReadTile(level, col, row)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		cache.Put(src.ID(), level, col, row, tile)
	}
	return tile, nil
}

// pixelFromImage extracts an RGBA pixel from a decoded tile without bounds
// checks (the caller must guarantee valid coordinates). Avoids the interface
// boxing overhead of image.At() → color.Color and the redundant bounds check
// inside YCbCrAt, which together consumed ~12% of CPU time.
func pixelFromImage(tile image.Image, x, y int) [4]uint8 {
	switch img := tile.(type) {
	case *image.YCbCr:
		// Direct slice access: skip the bounds check in YCbCrAt.
		yi := img.YOffset(x, y)
		ci := img.COffset(x, y)
		yy := img.Y[yi]
		cb := img.Cb[ci]
		cr := img.Cr[ci]
		// Inline YCbCr→RGB conversion matching image/color.YCbCr.RGBA(),
		// but returning uint8 directly without the 16-bit intermediate.
		yy1 := int32(yy) * 0x10101
		cb1 := int32(cb) - 128
		cr1 := int32(cr) - 128
		r := yy1 + 91881*cr1
		g := yy1 - 22554*cb1 - 46802*cr1
		b := yy1 + 116130*cb1
		if r < 0 {
			r = 0
		} else if r > 0xFF0000 {
			r = 0xFF0000
		}
		if g < 0 {
			g = 0
		} else if g > 0xFF0000 {
			g = 0xFF0000
		}
		if b < 0 {
			b = 0
		} else if b > 0xFF0000 {
			b = 0xFF0000
		}
		return [4]uint8{uint8(r >> 16), uint8(g >> 16), uint8(b >> 16), 255}
	case *image.NYCbCrA:
		yi := img.YOffset(x, y)
		ci := img.COffset(x, y)
		ai := img.AOffset(x, y)
		yy := img.Y[yi]
		cb := img.Cb[ci]
		cr := img.Cr[ci]
		a := img.A[ai]
		yy1 := int32(yy) * 0x10101
		cb1 := int32(cb) - 128
		cr1 := int32(cr) - 128
		r := yy1 + 91881*cr1
		g := yy1 - 22554*cb1 - 46802*cr1
		b := yy1 + 116130*cb1
		if r < 0 {
			r = 0
		} else if r > 0xFF0000 {
			r = 0xFF0000
		}
		if g < 0 {
			g = 0
		} else if g > 0xFF0000 {
			g = 0xFF0000
		}
		if b < 0 {
			b = 0
		} else if b > 0xFF0000 {
			b = 0xFF0000
		}
		return [4]uint8{uint8(r >> 16), uint8(g >> 16), uint8(b >> 16), a}
	case *image.RGBA:
		i := img.PixOffset(x, y)
		return [4]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
	default:
		rr, gg, bb, aa := tile.At(x, y).RGBA()
		return [4]uint8{uint8(rr >> 8), uint8(gg >> 8), uint8(bb >> 8), uint8(aa >> 8)}
	}
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

	tile, err := fetchTileCached(src, level, col, row, cache)
	if err != nil {
		return [4]uint8{}, err
	}
	return pixelFromImage(tile, localX, localY), nil
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

// clampByte rounds a float64 to the nearest uint8, clamping to [0, 255].
// Defined at package level so the compiler can inline it (closures are not inlined).
func clampByte(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// lanczos3 computes the Lanczos-3 kernel value. The kernel is a windowed sinc:
//
//	L₃(x) = sinc(x) · sinc(x/3)   for |x| < 3
//	       = 0                      for |x| ≥ 3
//
// where sinc(x) = sin(πx)/(πx). At x = 0 the limit is 1.
// Simplified: L₃(x) = 3·sin(πx)·sin(πx/3) / (π²x²).
func lanczos3(x float64) float64 {
	if x == 0 {
		return 1
	}
	if x < -3 || x > 3 {
		return 0
	}
	xPi := x * math.Pi
	return 3 * math.Sin(xPi) * math.Sin(xPi/3) / (xPi * xPi)
}

// lanczos3LUTSize is the number of entries in each half of the lookup table.
// 1024 entries over [0, 3] gives a step of ~0.00293, which is more than
// sufficient for sub-pixel resampling accuracy.
const lanczos3LUTSize = 1024

// lanczos3Table stores precomputed Lanczos-3 kernel values for x in [0, 3).
// The kernel is symmetric so we only store the positive half.
var lanczos3Table [lanczos3LUTSize]float64

func init() {
	for i := 0; i < lanczos3LUTSize; i++ {
		x := float64(i) * 3.0 / float64(lanczos3LUTSize)
		lanczos3Table[i] = lanczos3(x)
	}
}

// lanczos3LUT evaluates the Lanczos-3 kernel via table lookup with linear
// interpolation. Eliminates math.Sin calls (which were 7.56% of CPU time)
// while maintaining high accuracy.
func lanczos3LUT(x float64) float64 {
	if x < 0 {
		x = -x
	}
	if x >= 3 {
		return 0
	}
	// Map x from [0, 3) to table index.
	pos := x * (lanczos3LUTSize / 3.0)
	idx := int(pos)
	if idx >= lanczos3LUTSize-1 {
		return lanczos3Table[lanczos3LUTSize-1]
	}
	frac := pos - float64(idx)
	return lanczos3Table[idx]*(1-frac) + lanczos3Table[idx+1]*frac
}

// bicubic computes the Catmull-Rom (a = -0.5) bicubic kernel value:
//
//	W(x) = 1.5|x|³ - 2.5|x|² + 1         for |x| ≤ 1
//	W(x) = -0.5|x|³ + 2.5|x|² - 4|x| + 2 for 1 < |x| ≤ 2
//	W(x) = 0                                for |x| > 2
func bicubic(x float64) float64 {
	if x < 0 {
		x = -x
	}
	if x >= 2 {
		return 0
	}
	x2 := x * x
	x3 := x2 * x
	if x <= 1 {
		return 1.5*x3 - 2.5*x2 + 1
	}
	return -0.5*x3 + 2.5*x2 - 4*x + 2
}

// bicubicLUTSize is the number of entries in each half of the lookup table.
// 1024 entries over [0, 2] gives a step of ~0.00195.
const bicubicLUTSize = 1024

// bicubicTable stores precomputed Catmull-Rom kernel values for x in [0, 2).
// The kernel is symmetric so we only store the positive half.
var bicubicTable [bicubicLUTSize]float64

func init() {
	for i := 0; i < bicubicLUTSize; i++ {
		x := float64(i) * 2.0 / float64(bicubicLUTSize)
		bicubicTable[i] = bicubic(x)
	}
}

// bicubicLUT evaluates the Catmull-Rom bicubic kernel via table lookup with
// linear interpolation. Eliminates repeated polynomial evaluation in the
// inner resampling loops.
func bicubicLUT(x float64) float64 {
	if x < 0 {
		x = -x
	}
	if x >= 2 {
		return 0
	}
	pos := x * (bicubicLUTSize / 2.0)
	idx := int(pos)
	if idx >= bicubicLUTSize-1 {
		return bicubicTable[bicubicLUTSize-1]
	}
	frac := pos - float64(idx)
	return bicubicTable[idx]*(1-frac) + bicubicTable[idx+1]*frac
}

// renderTileTerrarium renders a single web map tile from float GeoTIFF data,
// converting elevation values to Terrarium RGB encoding.
func renderTileTerrarium(z, tx, ty, tileSize int, srcInfos []sourceInfo, proj coord.Projection, cache *cog.FloatTileCache, mode Resampling) *image.RGBA {
	_, midLat, _, _ := coord.TileBounds(z, tx, ty)
	outputResMeters := coord.ResolutionAtLat(midLat, z)
	outputResCRS := coord.MetersToPixelSizeCRS(outputResMeters, proj.EPSG(), midLat)

	// Pre-filter sources and pre-compute overview levels for this tile.
	tileMinX, tileMinY, tileMaxX, tileMaxY := tileCRSBounds(z, tx, ty, proj)
	tileSrcs := prepareTileSources(srcInfos, outputResCRS, tileMinX, tileMinY, tileMaxX, tileMaxY)
	if len(tileSrcs) == 0 {
		return nil
	}

	img := GetRGBA(tileSize, tileSize)
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

	// Precompute lon per column and lat per row to avoid per-pixel trig.
	lons := make([]float64, tileSize)
	lats := make([]float64, tileSize)
	for px := 0; px < tileSize; px++ {
		lons[px], _ = coord.PixelToLonLat(z, tx, ty, tileSize, float64(px)+0.5, 0)
	}
	for py := 0; py < tileSize; py++ {
		_, lats[py] = coord.PixelToLonLat(z, tx, ty, tileSize, 0, float64(py)+0.5)
	}

	for py := 0; py < tileSize; py++ {
		lat := lats[py]
		for px := 0; px < tileSize; px++ {
			srcX, srcY := proj.FromWGS84(lons[px], lat)
			elevation, found := sampleFromTileSourcesFloat(tileSrcs, nodataValues, srcX, srcY, cache, mode)
			if found && !math.IsNaN(elevation) {
				img.SetRGBA(px, py, encode.ElevationToTerrarium(elevation))
				hasData = true
			}
			// nodata pixels remain transparent (zero RGBA)
		}
	}

	if !hasData {
		PutRGBA(img)
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
		case ResamplingLanczos:
			val, err = lanczosSampleFloat(src.reader, src.level, pixX, pixY, src.imgW, src.imgH, src.tileW, src.tileH, cache)
		case ResamplingBicubic:
			val, err = bicubicSampleFloat(src.reader, src.level, pixX, pixY, src.imgW, src.imgH, src.tileW, src.tileH, cache)
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

// lanczosSampleFloat performs Lanczos-3 interpolation on float data.
// NaN pixels are excluded from the weighted sum; if all neighbors are NaN,
// falls back to nearest-neighbor.
//
// Optimized with batched tile fetches (same approach as lanczosSampleCached)
// and LUT-based kernel evaluation.
func lanczosSampleFloat(src *cog.Reader, level int, fx, fy float64, imgW, imgH, tw, th int, cache *cog.FloatTileCache) (float64, error) {
	const a = 3
	const n = 2 * a

	ix0 := int(math.Floor(fx)) - a + 1
	iy0 := int(math.Floor(fy)) - a + 1

	var pxArr, pyArr [n]int
	var wxArr, wyArr [n]float64
	for k := 0; k < n; k++ {
		pxArr[k] = clamp(ix0+k, 0, imgW-1)
		pyArr[k] = clamp(iy0+k, 0, imgH-1)
		wxArr[k] = lanczos3LUT(fx - float64(ix0+k))
		wyArr[k] = lanczos3LUT(fy - float64(iy0+k))
	}

	// Determine the tile column/row range for the 6×6 neighborhood.
	colMin := pxArr[0] / tw
	colMax := pxArr[n-1] / tw
	rowMin := pyArr[0] / th
	rowMax := pyArr[n-1] / th

	// Fetch the unique float tiles (at most 2×2 = 4).
	var ftData [2][2][]float32
	var ftW [2][2]int
	var ftOK [2][2]bool
	srcID := src.ID()
	for r := rowMin; r <= rowMax; r++ {
		for c := colMin; c <= colMax; c++ {
			dr := r - rowMin
			dc := c - colMin
			var tileData []float32
			var tileW int
			if cache != nil {
				tileData, tileW, _ = cache.Get(srcID, level, c, r)
			}
			if tileData == nil {
				var w, h int
				var err error
				tileData, w, h, err = src.ReadFloatTile(level, c, r)
				if err != nil || tileData == nil {
					continue
				}
				tileW = w
				if cache != nil {
					cache.Put(srcID, level, c, r, tileData, w, h)
				}
			}
			ftData[dr][dc] = tileData
			ftW[dr][dc] = tileW
			ftOK[dr][dc] = true
		}
	}

	var sum, wTotal float64
	hasNaN := false

	for ky := 0; ky < n; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		py := pyArr[ky]
		tileRow := py/th - rowMin
		localY := py % th

		for kx := 0; kx < n; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			px := pxArr[kx]
			tileCol := px/tw - colMin
			if !ftOK[tileRow][tileCol] {
				continue
			}

			localX := px % tw
			idx := localY*ftW[tileRow][tileCol] + localX
			data := ftData[tileRow][tileCol]
			if idx < 0 || idx >= len(data) {
				continue
			}
			val := float64(data[idx])

			if math.IsNaN(val) {
				hasNaN = true
				continue
			}

			sum += val * wt
			wTotal += wt
		}
	}

	if wTotal == 0 {
		if hasNaN {
			cx := clamp(int(math.Floor(fx+0.5)), 0, imgW-1)
			cy := clamp(int(math.Floor(fy+0.5)), 0, imgH-1)
			return readFloatPixelCached(src, level, cx, cy, cache)
		}
		return math.NaN(), nil
	}

	return sum / wTotal, nil
}

// bicubicSampleFloat performs Catmull-Rom bicubic interpolation on float data.
// NaN pixels are excluded from the weighted sum; if all neighbors are NaN,
// falls back to nearest-neighbor.
func bicubicSampleFloat(src *cog.Reader, level int, fx, fy float64, imgW, imgH, tw, th int, cache *cog.FloatTileCache) (float64, error) {
	const n = 4

	ix0 := int(math.Floor(fx)) - 1
	iy0 := int(math.Floor(fy)) - 1

	var pxArr, pyArr [n]int
	var wxArr, wyArr [n]float64
	for k := 0; k < n; k++ {
		pxArr[k] = clamp(ix0+k, 0, imgW-1)
		pyArr[k] = clamp(iy0+k, 0, imgH-1)
		wxArr[k] = bicubicLUT(fx - float64(ix0+k))
		wyArr[k] = bicubicLUT(fy - float64(iy0+k))
	}

	colMin := pxArr[0] / tw
	colMax := pxArr[n-1] / tw
	rowMin := pyArr[0] / th
	rowMax := pyArr[n-1] / th

	// Fetch the unique float tiles (at most 2×2 = 4).
	var ftData [2][2][]float32
	var ftW [2][2]int
	var ftOK [2][2]bool
	srcID := src.ID()
	for r := rowMin; r <= rowMax; r++ {
		for c := colMin; c <= colMax; c++ {
			dr := r - rowMin
			dc := c - colMin
			var tileData []float32
			var tileW int
			if cache != nil {
				tileData, tileW, _ = cache.Get(srcID, level, c, r)
			}
			if tileData == nil {
				var w, h int
				var err error
				tileData, w, h, err = src.ReadFloatTile(level, c, r)
				if err != nil || tileData == nil {
					continue
				}
				tileW = w
				if cache != nil {
					cache.Put(srcID, level, c, r, tileData, w, h)
				}
			}
			ftData[dr][dc] = tileData
			ftW[dr][dc] = tileW
			ftOK[dr][dc] = true
		}
	}

	var sum, wTotal float64
	hasNaN := false

	for ky := 0; ky < n; ky++ {
		wyVal := wyArr[ky]
		if wyVal == 0 {
			continue
		}
		py := pyArr[ky]
		tileRow := py/th - rowMin
		localY := py % th

		for kx := 0; kx < n; kx++ {
			wt := wxArr[kx] * wyVal
			if wt == 0 {
				continue
			}

			px := pxArr[kx]
			tileCol := px/tw - colMin
			if !ftOK[tileRow][tileCol] {
				continue
			}

			localX := px % tw
			idx := localY*ftW[tileRow][tileCol] + localX
			data := ftData[tileRow][tileCol]
			if idx < 0 || idx >= len(data) {
				continue
			}
			val := float64(data[idx])

			if math.IsNaN(val) {
				hasNaN = true
				continue
			}

			sum += val * wt
			wTotal += wt
		}
	}

	if wTotal == 0 {
		if hasNaN {
			cx := clamp(int(math.Floor(fx+0.5)), 0, imgW-1)
			cy := clamp(int(math.Floor(fy+0.5)), 0, imgH-1)
			return readFloatPixelCached(src, level, cx, cy, cache)
		}
		return math.NaN(), nil
	}

	return sum / wTotal, nil
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
