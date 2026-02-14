package coord

// SwissLV95 implements the Projection interface for EPSG:2056 (CH1903+ / LV95).
// Uses swisstopo's published polynomial approximation formulas.
// Accuracy: ~1 meter, sufficient for tile boundary computation and pixel reprojection.
//
// Reference: https://www.swisstopo.admin.ch/en/knowledge-facts/surveying-geodesy/reference-frames/local/lv95.html
type SwissLV95 struct{}

func (s *SwissLV95) EPSG() int { return 2056 }

// ToWGS84 converts Swiss LV95 easting/northing to WGS84 longitude/latitude (degrees).
func (s *SwissLV95) ToWGS84(easting, northing float64) (lon, lat float64) {
	// Auxiliary values: differences from Bern reference in 1000 km units
	y := (easting - 2_600_000) / 1_000_000
	x := (northing - 1_200_000) / 1_000_000

	// Longitude in 10000" units
	lonSec := 2.6779094 +
		4.728982*y +
		0.791484*y*x +
		0.1306*y*x*x -
		0.0436*y*y*y

	// Latitude in 10000" units
	latSec := 16.9023892 +
		3.238272*x -
		0.270978*y*y -
		0.002528*x*x -
		0.0447*y*y*x -
		0.0140*x*x*x

	// Convert from 10000" to degrees
	lon = lonSec * 100.0 / 36.0
	lat = latSec * 100.0 / 36.0
	return
}

// FromWGS84 converts WGS84 longitude/latitude (degrees) to Swiss LV95 easting/northing.
func (s *SwissLV95) FromWGS84(lon, lat float64) (easting, northing float64) {
	// Convert to sexagesimal seconds, then to 10000" auxiliary values
	phiSec := lat * 3600
	lambdaSec := lon * 3600

	phiAux := (phiSec - 169028.66) / 10000
	lambdaAux := (lambdaSec - 26782.5) / 10000

	// Easting
	easting = 2_600_072.37 +
		211_455.93*lambdaAux -
		10_938.51*lambdaAux*phiAux -
		0.36*lambdaAux*phiAux*phiAux -
		44.54*lambdaAux*lambdaAux*lambdaAux

	// Northing
	northing = 1_200_147.07 +
		308_807.95*phiAux +
		3_745.25*lambdaAux*lambdaAux +
		76.63*phiAux*phiAux -
		194.56*lambdaAux*lambdaAux*phiAux +
		119.79*phiAux*phiAux*phiAux

	return
}
