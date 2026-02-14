package tile

import (
	"github.com/pspoerri/geotiff2pmtiles/internal/coord"
)

// AutoZoomRange computes appropriate min/max zoom levels based on source data.
func AutoZoomRange(pixelSize float64, centerLat float64) (minZoom, maxZoom int) {
	maxZoom = coord.MaxZoomForResolution(pixelSize, centerLat)
	minZoom = maxZoom - 6
	if minZoom < 0 {
		minZoom = 0
	}
	return
}
