// Package coord converts coordinates between WGS84 and source CRSs: native
// UTM, Swiss LV95, WGS84 and Web Mercator projections, and a wroge/crs
// fallback for other EPSG codes. It also holds the Web Mercator tile math,
// longitude wrapping at the antimeridian, CRS unit conversion and the
// Hilbert ordering of tiles.
package coord

import (
	"math"

	"github.com/wroge/crs"
)

// Projection defines the interface for converting between a source CRS and WGS84.
type Projection interface {
	// ToWGS84 converts source CRS coordinates to WGS84 longitude/latitude (degrees).
	// The longitude is continuous across the source and may lie outside
	// [-180, 180], e.g. 182 east of the antimeridian in UTM zone 60.
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
type WGS84Identity struct {
	// Lon360 is for grids whose longitudes run 0..360: FromWGS84 then
	// returns longitudes west of Lon360Min as lon+360, inside the grid.
	Lon360 bool
	// Lon360Min is the grid's western edge if it lies west of Greenwich, as
	// for a grid of pixel centres from 0 (-0.125 for 0.25° GFS or ERA5
	// data); longitudes from there to 0 are inside the grid as they are.
	Lon360Min float64
}

func (w *WGS84Identity) ToWGS84(x, y float64) (lon, lat float64) { return x, y }
func (w *WGS84Identity) EPSG() int                               { return 4326 }

// WGS84ForGrid returns the projection for an EPSG:4326 grid spanning
// longitudes minX..maxX with pixels pixelSizeX wide. A grid that runs past
// 180° from at most one pixel west of 0 stores the western hemisphere at
// lon+360: 0..360, pixel centres from 0 (GFS/ERA5: -0.125..359.875) or a
// Pacific 100..260 grid. Any other grid gets the plain identity; the 1e-6°
// margin keeps a world raster ending at 180.0000001 in the usual convention.
func WGS84ForGrid(minX, maxX, pixelSizeX float64) WGS84Identity {
	const eps = 1e-6
	if minX < -pixelSizeX-eps || maxX <= 180+eps {
		return WGS84Identity{}
	}
	return WGS84Identity{Lon360: true, Lon360Min: min(minX, 0)}
}

// wraps reports whether lon is stored at lon+360 in the grid.
func (w *WGS84Identity) wraps(lon float64) bool { return w.Lon360 && lon < w.Lon360Min }

func (w *WGS84Identity) FromWGS84(lon, lat float64) (x, y float64) {
	if w.wraps(lon) {
		lon += 360
	}
	return lon, lat
}

// LonRange returns the grid x range holding the longitudes minLon..maxLon.
// Projecting the two ends is not enough on a Lon360 grid, which jumps by 360
// at Lon360Min: a range across that point (such as the z0 tile) needs the
// whole grid width, and one ending on it stays in one piece.
func (w *WGS84Identity) LonRange(minLon, maxLon float64) (minX, maxX float64) {
	switch {
	case !w.wraps(minLon):
		return minLon, maxLon
	case maxLon > w.Lon360Min:
		return w.Lon360Min, w.Lon360Min + 360
	default:
		return minLon + 360, maxLon + 360
	}
}

// WrapLonRange shifts the longitude range [minLon, maxLon] by whole turns
// so that minLon lies in [-180, 180). maxLon then exceeds 180 if the range
// crosses the antimeridian, as for a UTM zone 60 raster or a 100..260 grid;
// a range of a full turn or more becomes [-180, 180]. Overshoots below
// 1e-6° (~0.1 m) are rounding noise, as in -180.0000000001.
func WrapLonRange(minLon, maxLon float64) (float64, float64) {
	const eps = 1e-6
	if maxLon-minLon >= 360-eps {
		return -180, 180
	}
	turns := math.Floor((minLon + 180 + eps) / 360)
	minLon, maxLon = max(minLon-360*turns, -180), maxLon-360*turns
	if maxLon < 180+eps {
		maxLon = min(maxLon, 180)
	}
	return minLon, maxLon
}
