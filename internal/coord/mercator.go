package coord

import "math"

const (
	// EarthCircumference is the equatorial circumference in meters at zoom 0.
	EarthCircumference = 40075016.685578488
	// OriginShift is half the earth's circumference.
	OriginShift = EarthCircumference / 2.0
	// TileSize is the standard web map tile dimension.
	DefaultTileSize = 256
)

// WebMercatorProj implements the Projection interface for EPSG:3857.
type WebMercatorProj struct{}

func (w *WebMercatorProj) EPSG() int { return 3857 }

func (w *WebMercatorProj) ToWGS84(x, y float64) (lon, lat float64) {
	lon = (x / OriginShift) * 180.0
	lat = (y / OriginShift) * 180.0
	lat = 180.0 / math.Pi * (2.0*math.Atan(math.Exp(lat*math.Pi/180.0)) - math.Pi/2.0)
	return
}

func (w *WebMercatorProj) FromWGS84(lon, lat float64) (x, y float64) {
	x = lon * OriginShift / 180.0
	y = math.Log(math.Tan((90.0+lat)*math.Pi/360.0)) / (math.Pi / 180.0)
	y = y * OriginShift / 180.0
	return
}

// pow2 returns 2^z as a float64 using bit shifting (much faster than math.Pow).
func pow2(z int) float64 {
	return float64(uint64(1) << uint(z))
}

// maxMercatorLat is the maximum latitude representable in Web Mercator.
// Beyond this, the Mercator projection diverges to infinity.
const maxMercatorLat = 85.0511287798

// LonLatToTile converts WGS84 lon/lat to tile coordinates at the given zoom level.
func LonLatToTile(lon, lat float64, zoom int) (x, y int) {
	// Clamp latitude to the valid Web Mercator range to avoid Inf/NaN
	// from the Mercator projection at the poles.
	if lat > maxMercatorLat {
		lat = maxMercatorLat
	} else if lat < -maxMercatorLat {
		lat = -maxMercatorLat
	}

	n := pow2(zoom)
	x = int(math.Floor((lon + 180.0) / 360.0 * n))
	latRad := lat * math.Pi / 180.0
	y = int(math.Floor((1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n))

	maxTile := int(n) - 1
	if x < 0 {
		x = 0
	}
	if x > maxTile {
		x = maxTile
	}
	if y < 0 {
		y = 0
	}
	if y > maxTile {
		y = maxTile
	}
	return
}

// TileBounds returns the WGS84 bounding box of a tile at the given zoom level.
func TileBounds(z, x, y int) (minLon, minLat, maxLon, maxLat float64) {
	n := pow2(z)
	minLon = float64(x)/n*360.0 - 180.0
	maxLon = float64(x+1)/n*360.0 - 180.0
	minLat = math.Atan(math.Sinh(math.Pi*(1.0-2.0*float64(y+1)/n))) * 180.0 / math.Pi
	maxLat = math.Atan(math.Sinh(math.Pi*(1.0-2.0*float64(y)/n))) * 180.0 / math.Pi
	return
}

// TilePixelBounds returns the fractional pixel coordinates within a tile
// for a given WGS84 lon/lat and tile (z,x,y), using the given tile size.
func TilePixelCoords(lon, lat float64, z, tileX, tileY, tileSize int) (px, py float64) {
	n := pow2(z)

	// Global pixel coordinates.
	globalX := (lon + 180.0) / 360.0 * n * float64(tileSize)
	latRad := lat * math.Pi / 180.0
	globalY := (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n * float64(tileSize)

	// Pixel within this tile.
	px = globalX - float64(tileX)*float64(tileSize)
	py = globalY - float64(tileY)*float64(tileSize)
	return
}

// PixelToLonLat converts a pixel position within a tile to WGS84 lon/lat.
func PixelToLonLat(z, tileX, tileY, tileSize int, px, py float64) (lon, lat float64) {
	n := pow2(z)

	globalX := float64(tileX)*float64(tileSize) + px
	globalY := float64(tileY)*float64(tileSize) + py

	lon = globalX/(n*float64(tileSize))*360.0 - 180.0
	lat = math.Atan(math.Sinh(math.Pi*(1.0-2.0*globalY/(n*float64(tileSize))))) * 180.0 / math.Pi
	return
}

// ResolutionAtLat returns the ground resolution in meters/pixel at the given latitude and zoom level.
func ResolutionAtLat(lat float64, zoom int) float64 {
	return EarthCircumference * math.Cos(lat*math.Pi/180.0) / pow2(zoom) / float64(DefaultTileSize)
}

// PixelSizeInGroundMeters converts a pixel size from CRS units to ground meters.
// For geographic CRS (EPSG:4326), the pixel size is in degrees.
// For Web Mercator (EPSG:3857), it's in projected meters (stretched by 1/cos(lat)).
// For metric projections (e.g. EPSG:2056), it's already in meters.
func PixelSizeInGroundMeters(pixelSizeCRS float64, epsg int, lat float64) float64 {
	switch epsg {
	case 4326:
		// Degrees of longitude to ground meters at the given latitude.
		return pixelSizeCRS * EarthCircumference * math.Cos(lat*math.Pi/180.0) / 360.0
	case 3857:
		// Web Mercator meters to ground meters at the given latitude.
		return pixelSizeCRS * math.Cos(lat*math.Pi/180.0)
	default:
		// Assume metric CRS (e.g. EPSG:2056).
		return pixelSizeCRS
	}
}

// MetersToPixelSizeCRS converts a ground-meter pixel size to CRS units.
// This is the inverse of PixelSizeInGroundMeters.
func MetersToPixelSizeCRS(meters float64, epsg int, lat float64) float64 {
	switch epsg {
	case 4326:
		// Ground meters to degrees of longitude at the given latitude.
		return meters * 360.0 / (EarthCircumference * math.Cos(lat*math.Pi/180.0))
	case 3857:
		// Ground meters to Web Mercator meters at the given latitude.
		return meters / math.Cos(lat*math.Pi/180.0)
	default:
		// Assume metric CRS (e.g. EPSG:2056).
		return meters
	}
}

// MaxZoomForResolution calculates the maximum zoom level whose ground resolution
// is at least as coarse as the given pixel size (in ground meters).
// tileSize is the number of pixels per tile edge (e.g. 256 or 512).
func MaxZoomForResolution(pixelSizeMeters float64, centerLat float64, tileSize int) int {
	if pixelSizeMeters <= 0 || tileSize <= 0 {
		return 0
	}
	cosLat := math.Cos(centerLat * math.Pi / 180.0)
	z := math.Log2(EarthCircumference * cosLat / (pixelSizeMeters * float64(tileSize)))
	iz := int(math.Floor(z))
	if iz < 0 {
		return 0
	}
	if iz > 28 {
		return 28
	}
	return iz
}

// TilesInBounds returns all tile coordinates at the given zoom level that intersect the given WGS84 bounds.
func TilesInBounds(zoom int, minLon, minLat, maxLon, maxLat float64) [][3]int {
	minTX, minTY := LonLatToTile(minLon, maxLat, zoom) // note: maxLat -> minTY
	maxTX, maxTY := LonLatToTile(maxLon, minLat, zoom)

	var tiles [][3]int
	for ty := minTY; ty <= maxTY; ty++ {
		for tx := minTX; tx <= maxTX; tx++ {
			tiles = append(tiles, [3]int{zoom, tx, ty})
		}
	}
	return tiles
}
