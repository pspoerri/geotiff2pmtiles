# Signed-integer GeoTIFFs (GEBCO bathymetry) as elevation

GEBCO grids are Int16 (SampleFormat=2, -10000..8600 m). The reader only knew
uint and float: `--format terrarium` was refused ("requires float") and the RGB
path read negative depths as wrapped uint16, so bathymetry never showed up.

- `cog.Reader.IsFloat()` is now true for signed-integer rasters too;
  `decodeRawFloat32Tile` decodes int16/int32 samples to float32.
- Such files auto-select Terrarium like float DEMs; nodata (-32767) is handled
  by the existing float nodata path.
- `FormatDescription` reports `int16` instead of `uint16`.
- Test: `internal/cog/signedint_test.go`.

Usage: `geotiff2pmtiles gebco_2026_geotiff/ gebco.pmtiles`
