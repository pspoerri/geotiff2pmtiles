package coord

// Projection defines the interface for converting between a source CRS and WGS84.
type Projection interface {
	// ToWGS84 converts source CRS coordinates to WGS84 longitude/latitude (degrees).
	ToWGS84(x, y float64) (lon, lat float64)

	// FromWGS84 converts WGS84 longitude/latitude (degrees) to source CRS coordinates.
	FromWGS84(lon, lat float64) (x, y float64)

	// EPSG returns the EPSG code for this projection.
	EPSG() int
}

// ForEPSG returns a Projection for the given EPSG code.
// Returns nil if the EPSG code is not supported.
func ForEPSG(epsg int) Projection {
	switch epsg {
	case 2056:
		return &SwissLV95{}
	case 4326:
		return &WGS84Identity{}
	case 3857:
		return &WebMercatorProj{}
	default:
		return nil
	}
}

// WGS84Identity is a no-op projection for data already in EPSG:4326.
type WGS84Identity struct{}

func (w *WGS84Identity) ToWGS84(x, y float64) (lon, lat float64) { return x, y }
func (w *WGS84Identity) FromWGS84(lon, lat float64) (x, y float64) { return lon, lat }
func (w *WGS84Identity) EPSG() int { return 4326 }
