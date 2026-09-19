package coord

import "math"

// UTM implements the Projection interface for the UTM zones on the WGS84
// ellipsoid (EPSG:326xx north, EPSG:327xx south) and ETRS89 (EPSG:258xx).
// Uses the Krüger series to order n³ (sub-millimeter within a zone).
//
// ponytail: ETRS89 is treated as WGS84 (<1 m offset, drifting ~2.5 cm/year);
// add a datum shift if sub-meter accuracy is ever needed.
//
// Reference: https://en.wikipedia.org/wiki/Universal_Transverse_Mercator_coordinate_system#Simplified_formulae
type UTM struct {
	epsg  int
	lon0  float64 // central meridian (radians)
	north float64 // false northing (meters)
}

// utmForEPSG returns the UTM projection for the given EPSG code, or nil if
// the code is not a UTM zone.
func utmForEPSG(epsg int) *UTM {
	var zone int
	var north float64
	switch {
	case epsg >= 32601 && epsg <= 32660:
		zone = epsg - 32600
	case epsg >= 32701 && epsg <= 32760:
		zone, north = epsg-32700, 10_000_000
	case epsg >= 25828 && epsg <= 25838:
		zone = epsg - 25800
	default:
		return nil
	}
	return &UTM{epsg: epsg, lon0: float64(zone*6-183) * math.Pi / 180, north: north}
}

const (
	utmK0    = 0.9996
	utmEast  = 500_000.0
	wgs84A   = 6378137.0
	wgs84F   = 1 / 298.257223563
	utmN     = wgs84F / (2 - wgs84F)
	utmN2    = utmN * utmN
	utmN3    = utmN2 * utmN
	utmScale = utmK0 * wgs84A / (1 + utmN) * (1 + utmN2/4 + utmN2*utmN2/64)

	utmAlpha1 = utmN/2 - 2*utmN2/3 + 5*utmN3/16
	utmAlpha2 = 13*utmN2/48 - 3*utmN3/5
	utmAlpha3 = 61 * utmN3 / 240

	utmBeta1 = utmN/2 - 2*utmN2/3 + 37*utmN3/96
	utmBeta2 = utmN2/48 + utmN3/15
	utmBeta3 = 17 * utmN3 / 480

	utmDelta1 = 2*utmN - 2*utmN2/3 - 2*utmN3
	utmDelta2 = 7*utmN2/3 - 8*utmN3/5
	utmDelta3 = 56 * utmN3 / 15
)

var utmC = 2 * math.Sqrt(utmN) / (1 + utmN)

func (u *UTM) EPSG() int { return u.epsg }

// FromWGS84 converts WGS84 longitude/latitude (degrees) to UTM easting/northing.
func (u *UTM) FromWGS84(lon, lat float64) (easting, northing float64) {
	sinPhi := math.Sin(lat * math.Pi / 180)
	dLon := lon*math.Pi/180 - u.lon0

	t := math.Sinh(math.Atanh(sinPhi) - utmC*math.Atanh(utmC*sinPhi))
	xi := math.Atan2(t, math.Cos(dLon))
	eta := math.Atanh(math.Sin(dLon) / math.Sqrt(1+t*t))

	easting = utmEast + utmScale*(eta+
		utmAlpha1*math.Cos(2*xi)*math.Sinh(2*eta)+
		utmAlpha2*math.Cos(4*xi)*math.Sinh(4*eta)+
		utmAlpha3*math.Cos(6*xi)*math.Sinh(6*eta))
	northing = u.north + utmScale*(xi+
		utmAlpha1*math.Sin(2*xi)*math.Cosh(2*eta)+
		utmAlpha2*math.Sin(4*xi)*math.Cosh(4*eta)+
		utmAlpha3*math.Sin(6*xi)*math.Cosh(6*eta))
	return
}

// ToWGS84 converts UTM easting/northing to WGS84 longitude/latitude (degrees).
func (u *UTM) ToWGS84(easting, northing float64) (lon, lat float64) {
	xi := (northing - u.north) / utmScale
	eta := (easting - utmEast) / utmScale

	xiP := xi -
		utmBeta1*math.Sin(2*xi)*math.Cosh(2*eta) -
		utmBeta2*math.Sin(4*xi)*math.Cosh(4*eta) -
		utmBeta3*math.Sin(6*xi)*math.Cosh(6*eta)
	etaP := eta -
		utmBeta1*math.Cos(2*xi)*math.Sinh(2*eta) -
		utmBeta2*math.Cos(4*xi)*math.Sinh(4*eta) -
		utmBeta3*math.Cos(6*xi)*math.Sinh(6*eta)

	chi := math.Asin(math.Sin(xiP) / math.Cosh(etaP))
	phi := chi +
		utmDelta1*math.Sin(2*chi) +
		utmDelta2*math.Sin(4*chi) +
		utmDelta3*math.Sin(6*chi)

	lon = (u.lon0 + math.Atan2(math.Sinh(etaP), math.Cos(xiP))) * 180 / math.Pi
	lat = phi * 180 / math.Pi
	return
}
