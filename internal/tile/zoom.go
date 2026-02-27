package tile

import (
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
)

// AutoZoomRange computes appropriate min/max zoom levels based on source data.
// pixelSizeMeters is the source ground resolution in meters.
// The min zoom is the highest zoom level at which the entire image fits in a
// single tile, so the output always has a useful overview.
func AutoZoomRange(pixelSizeMeters float64, centerLat float64, tileSize int,
	minLon, minLat, maxLon, maxLat float64) (minZoom, maxZoom int) {
	maxZoom = coord.MaxZoomForResolution(pixelSizeMeters, centerLat, tileSize)
	minZoom = coord.MinZoomForSingleTile(minLon, minLat, maxLon, maxLat)
	if minZoom > maxZoom {
		minZoom = maxZoom
	}
	return
}
