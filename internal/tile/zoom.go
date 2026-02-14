package tile

import (
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
)

// AutoZoomRange computes appropriate min/max zoom levels based on source data.
// pixelSizeMeters is the source ground resolution in meters.
func AutoZoomRange(pixelSizeMeters float64, centerLat float64, tileSize int) (minZoom, maxZoom int) {
	maxZoom = coord.MaxZoomForResolution(pixelSizeMeters, centerLat, tileSize)
	minZoom = maxZoom - 6
	if minZoom < 0 {
		minZoom = 0
	}
	return
}
