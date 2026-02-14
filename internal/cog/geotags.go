package cog

// GeoTIFF GeoKey IDs.
const (
	gkModelTypeGeoKey         = 1024
	gkRasterTypeGeoKey        = 1025
	gkGeographicTypeGeoKey    = 2048
	gkProjectedCSTypeGeoKey   = 3072
)

// GeoInfo holds parsed GeoTIFF metadata.
type GeoInfo struct {
	EPSG       int     // EPSG code (e.g. 2056)
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
	}

	// Parse GeoKeys for EPSG code.
	info.EPSG = parseEPSG(ifd.GeoKeys)

	return info
}

// parseEPSG extracts the EPSG code from GeoKey directory entries.
func parseEPSG(geoKeys []uint16) int {
	if len(geoKeys) < 4 {
		return 0
	}

	// GeoKey directory header: [KeyDirectoryVersion, KeyRevision, MinorRevision, NumberOfKeys]
	numKeys := int(geoKeys[3])

	for i := 0; i < numKeys; i++ {
		base := 4 + i*4
		if base+3 >= len(geoKeys) {
			break
		}
		keyID := geoKeys[base]
		// tiffTagLocation := geoKeys[base+1]
		// count := geoKeys[base+2]
		valueOffset := geoKeys[base+3]

		switch keyID {
		case gkProjectedCSTypeGeoKey:
			if valueOffset > 0 {
				return int(valueOffset)
			}
		case gkGeographicTypeGeoKey:
			if valueOffset > 0 {
				return int(valueOffset)
			}
		}
	}

	return 0
}
