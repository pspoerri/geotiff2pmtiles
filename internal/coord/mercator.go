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

// LonLatToTile converts WGS84 lon/lat to tile coordinates at the given zoom level.
func LonLatToTile(lon, lat float64, zoom int) (x, y int) {
	n := math.Pow(2, float64(zoom))
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
	n := math.Pow(2, float64(z))
	minLon = float64(x)/n*360.0 - 180.0
	maxLon = float64(x+1)/n*360.0 - 180.0
	minLat = math.Atan(math.Sinh(math.Pi*(1.0-2.0*float64(y+1)/n))) * 180.0 / math.Pi
	maxLat = math.Atan(math.Sinh(math.Pi*(1.0-2.0*float64(y)/n))) * 180.0 / math.Pi
	return
}

// TilePixelBounds returns the fractional pixel coordinates within a tile
// for a given WGS84 lon/lat and tile (z,x,y), using the given tile size.
func TilePixelCoords(lon, lat float64, z, tileX, tileY, tileSize int) (px, py float64) {
	n := math.Pow(2, float64(z))

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
	n := math.Pow(2, float64(z))

	globalX := float64(tileX)*float64(tileSize) + px
	globalY := float64(tileY)*float64(tileSize) + py

	lon = globalX/(n*float64(tileSize))*360.0 - 180.0
	lat = math.Atan(math.Sinh(math.Pi*(1.0-2.0*globalY/(n*float64(tileSize))))) * 180.0 / math.Pi
	return
}

// ResolutionAtLat returns the ground resolution in meters/pixel at the given latitude and zoom level.
func ResolutionAtLat(lat float64, zoom int) float64 {
	return EarthCircumference * math.Cos(lat*math.Pi/180.0) / math.Pow(2, float64(zoom)) / float64(DefaultTileSize)
}

// MaxZoomForResolution calculates the maximum zoom level that matches a given ground resolution.
func MaxZoomForResolution(pixelSize float64, centerLat float64) int {
	for z := 30; z >= 0; z-- {
		res := ResolutionAtLat(centerLat, z)
		if res >= pixelSize {
			return z
		}
	}
	return 0
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
