package cog

// GeoTIFF GeoKey IDs.
const (
	gkModelTypeGeoKey       = 1024
	gkRasterTypeGeoKey      = 1025
	gkGeographicTypeGeoKey  = 2048
	gkProjectedCSTypeGeoKey = 3072
)

// GTModelTypeGeoKey values; 32767 also marks a user-defined CRS code.
const (
	modelTypeProjected  = 1
	modelTypeGeographic = 2
	userDefinedGeoKey   = 32767
)

// rasterPixelIsPoint is the GTRasterTypeGeoKey value for rasters whose
// tiepoint refers to a pixel centre (RasterPixelIsArea, 1, is the default).
const rasterPixelIsPoint = 2

// GeoInfo holds parsed GeoTIFF metadata.
type GeoInfo struct {
	EPSG       int     // EPSG code (e.g. 2056); 32767 = user-defined CRS
	OriginX    float64 // easting of upper-left corner
	OriginY    float64 // northing of upper-left corner
	PixelSizeX float64 // pixel width in CRS units (positive)
	PixelSizeY float64 // pixel height in CRS units (positive)
}

// parseGeoInfo extracts geographic metadata from an IFD.
func parseGeoInfo(ifd *IFD) GeoInfo {
	info := GeoInfo{}

	// ModelPixelScale: [ScaleX, ScaleY, ScaleZ]
	if len(ifd.ModelPixelScale) >= 2 {
		info.PixelSizeX = ifd.ModelPixelScale[0]
		info.PixelSizeY = ifd.ModelPixelScale[1]
	}

	// ModelTiepoint: [I, J, K, X, Y, Z] - maps pixel (I,J) to (X,Y)
	if len(ifd.ModelTiepoint) >= 6 {
		// The tiepoint maps pixel (I,J) to world coordinate (X,Y).
		// Origin is at (0,0) pixel, so:
		info.OriginX = ifd.ModelTiepoint[3] - ifd.ModelTiepoint[0]*info.PixelSizeX
		info.OriginY = ifd.ModelTiepoint[4] + ifd.ModelTiepoint[1]*info.PixelSizeY

		// PixelIsPoint: the tiepoint is the centre of pixel (I,J), not its
		// upper-left corner. Move the origin to the corner, as GDAL does.
		if geoKey(ifd.GeoKeys, gkRasterTypeGeoKey) == rasterPixelIsPoint {
			info.OriginX -= info.PixelSizeX / 2
			info.OriginY += info.PixelSizeY / 2
		}
	}

	// Parse GeoKeys for EPSG code.
	info.EPSG = parseEPSG(ifd.GeoKeys)

	return info
}

// geoKey returns the value of a GeoKey stored inline in the key directory
// (TIFFTagLocation 0), or 0 when the key is absent.
func geoKey(geoKeys []uint16, id uint16) int {
	if len(geoKeys) < 4 {
		return 0
	}
	// Header: [KeyDirectoryVersion, KeyRevision, MinorRevision, NumberOfKeys],
	// then one [KeyID, TIFFTagLocation, Count, Value] entry per key.
	numKeys := int(geoKeys[3])
	for i := 0; i < numKeys; i++ {
		base := 4 + i*4
		if base+3 >= len(geoKeys) {
			break
		}
		if geoKeys[base] == id && geoKeys[base+1] == 0 {
			return int(geoKeys[base+3])
		}
	}
	return 0
}

// parseEPSG returns the EPSG code the GeoKeys declare, 0 when they declare
// none, or 32767 for a user-defined CRS, which the pipeline rejects.
// GTModelTypeGeoKey decides which key applies: a projected CRS also names its
// base geographic CRS, and GDAL writes GeographicTypeGeoKey=<datum> next to
// ProjectedCSTypeGeoKey=32767 for custom projections (Albers, LCC), whose
// metres must not be read as degrees.
func parseEPSG(geoKeys []uint16) int {
	pcs := geoKey(geoKeys, gkProjectedCSTypeGeoKey)
	gcs := geoKey(geoKeys, gkGeographicTypeGeoKey)
	switch geoKey(geoKeys, gkModelTypeGeoKey) {
	case modelTypeProjected:
		if pcs == 0 {
			return userDefinedGeoKey // defined by projection parameters only
		}
		return pcs
	case modelTypeGeographic:
		return gcs
	case userDefinedGeoKey:
		return userDefinedGeoKey
	}
	// Model type absent: a projected CRS key wins over its base.
	if pcs != 0 {
		return pcs
	}
	return gcs
}
