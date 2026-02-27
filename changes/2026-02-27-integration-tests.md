# Integration Test Suite

Added end-to-end integration tests that exercise the full pipeline (GeoTIFF → tile
generation → PMTiles output) and transform pipeline (PMTiles → PMTiles).

## Files

- `integration/helpers_test.go` — Synthetic GeoTIFF writer (binary TIFF with GeoTIFF
  tags), pipeline runners (`runPipeline`, `runTransform`), and PMTiles validation
  helpers (`validatePMTiles`, `assertTileDecodesAsImage`, `assertTilePixel`)
- `integration/synthetic_test.go` — 12 test cases covering RGB, RGBA transparency,
  grayscale with nodata, 16-bit rescaling, 16-bit auto-detection, multi-source mosaic,
  fill-color, disk spilling, transform passthrough/reencode/rebuild, and WebP encoding
- `integration/satellite_copernicus_test.go` — Copernicus DEM float32 → terrarium PMTiles
- `integration/satellite_naturalearth_test.go` — Natural Earth 8-bit RGB + TFW → JPEG PMTiles
- `integration/satellite_esaworldcover_test.go` — ESA WorldCover 16-bit RGBNIR preset detection
  and full pipeline (auto-detected bands/rescaling → PNG PMTiles)
- `integration/satellite_esaworldcover_ndvi_test.go` — ESA WorldCover NDVI 3-band composite
  (p90/p50/p10 → RGB PNG PMTiles)
- `integration/satellite_esaworldcover_swir_test.go` — ESA WorldCover SWIR 2-band composite
  (B11/B12 false-color → PNG PMTiles)
- `integration/satellite_esaworldcover_gamma0_test.go` — ESA WorldCover S1 gamma0 3-band 16-bit
  (VV/VH/ratio with linear rescaling → PNG PMTiles)
- `integration/testdata/download.sh` — Script to fetch real satellite test data
- `integration/testdata/README.md` — Documents data sources and URLs

Each dataset lives in its own subdirectory under `integration/testdata/` so it can
be passed directly as a CLI input directory to `geotiff2pmtiles`:
- `integration/testdata/copernicus/` — Copernicus DEM GLO-30 (float32)
- `integration/testdata/naturalearth/` — Natural Earth raster + TFW (8-bit RGB)
- `integration/testdata/esaworldcover/` — ESA WorldCover S2 RGBNIR (16-bit 4-band)
- `integration/testdata/esaworldcover-ndvi/` — ESA WorldCover S2 NDVI (8-bit 3-band)
- `integration/testdata/esaworldcover-swir/` — ESA WorldCover S2 SWIR (8-bit 2-band)
- `integration/testdata/esaworldcover-gamma0/` — ESA WorldCover S1 VV/VH ratio (16-bit 3-band)

All satellite tests are skipped when data files are absent.

## Makefile Targets

### Testing
- `make test-integration` — Run synthetic integration tests (~8s)
- `make test-integration-download` — Download real satellite test data (~1.2 GB)
- `make test-integration-real` — Run all integration tests (synthetic + real)
- `make test-integration-all` — Download data and run all integration tests
- `make test-integration-copernicus` — Copernicus DEM only (float32 → terrarium)
- `make test-integration-naturalearth` — Natural Earth only (8-bit RGB + TFW → JPEG)
- `make test-integration-esaworldcover` — ESA WorldCover RGBNIR only (16-bit RGBNIR → PNG)
- `make test-integration-esaworldcover-ndvi` — ESA WorldCover NDVI only
- `make test-integration-esaworldcover-swir` — ESA WorldCover SWIR only
- `make test-integration-esaworldcover-gamma0` — ESA WorldCover Gamma0 only

### Demos (CLI)
- `make demo-tfw` — Natural Earth raster (uses `integration/testdata/naturalearth/`)
- `make demo-copernicus` — Copernicus DEM → terrarium PMTiles
- `make demo-esaworldcover` — ESA WorldCover RGBNIR → PMTiles
- `make demo-esaworldcover-ndvi` — ESA WorldCover NDVI → PMTiles
- `make demo-esaworldcover-swir` — ESA WorldCover SWIR → PMTiles
- `make demo-esaworldcover-gamma0` — ESA WorldCover Gamma0 SAR → PMTiles

## CI

- Split unit and integration tests into separate steps
- Added `workflow_dispatch` trigger
- Added optional `integration-real` job for satellite data tests (manual trigger only)

## Synthetic GeoTIFF Writer

The writer produces valid uncompressed tiled TIFFs with proper GeoTIFF tags (ModelPixelScale,
ModelTiepoint, GeoKeyDirectory) for EPSG 4326 and 3857. Handles the TIFF spec requirement
that IFD values fitting in 4 bytes must be stored inline (critical for single-tile images
where TileOffsets/TileByteCounts have count=1).
