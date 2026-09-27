package coord

import "github.com/wroge/crs"

// Projection defines the interface for converting between a source CRS and WGS84.
type Projection interface {
	// ToWGS84 converts source CRS coordinates to WGS84 longitude/latitude (degrees).
	ToWGS84(x, y float64) (lon, lat float64)

	// FromWGS84 converts WGS84 longitude/latitude (degrees) to source CRS coordinates.
	FromWGS84(lon, lat float64) (x, y float64)

	// EPSG returns the EPSG code for this projection.
	EPSG() int
}

// ForEPSG returns a Projection for the given EPSG code. Common codes have
// fast native implementations; anything else falls back to CRSFallback.
// Returns nil if the EPSG code is not supported.
func ForEPSG(epsg int) Projection {
	switch epsg {
	case 2056:
		return &SwissLV95{}
	case 4326:
		return &WGS84Identity{}
	case 3857:
		return &WebMercatorProj{}
	}
	if u := utmForEPSG(epsg); u != nil {
		return u
	}
	return crsFallbackForEPSG(epsg)
}

// unitSize returns the length of one coordinate unit of the EPSG code's CRS:
// in degrees for geographic CRSs, otherwise in meters (0.3048 for feet,
// 1200/3937 for US survey feet). Codes wroge/crs does not know are taken to
// be in meters.
func unitSize(epsg int) (size float64, geographic bool) {
	switch epsg {
	case 4326:
		return 1, true
	case 2056, 3857:
		return 1, false
	}
	if utmForEPSG(epsg) != nil {
		return 1, false
	}
	c, err := crs.Load(epsg)
	if err != nil {
		return 1, false
	}
	_, geographic = c.Conversion.(crs.Geographic)
	switch {
	case c.Unit.Name == "": // wroge/crs default: degrees or meters
		return 1, geographic
	case geographic:
		return c.Unit.ToSI / crs.Degree.ToSI, true
	default:
		return c.Unit.ToSI, false
	}
}

// WGS84Identity is a no-op projection for data already in EPSG:4326.
type WGS84Identity struct{}

func (w *WGS84Identity) ToWGS84(x, y float64) (lon, lat float64)   { return x, y }
func (w *WGS84Identity) FromWGS84(lon, lat float64) (x, y float64) { return lon, lat }
func (w *WGS84Identity) EPSG() int                                 { return 4326 }
