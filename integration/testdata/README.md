# Integration Test Data

This directory holds real satellite/raster data for integration tests.
Each dataset lives in its own subdirectory so it can be passed directly
as a CLI input directory to `geotiff2pmtiles`.

Data files (`.tif`, `.tfw`, `.zip`) are git-ignored and must be downloaded before running real-data tests.

## Download

```bash
make test-integration-download
# or directly:
bash integration/testdata/download.sh
```

## Directory Layout

```
testdata/
  copernicus/            # Copernicus DEM GLO-30 (float32, ~8 MB)
  naturalearth/          # Natural Earth raster + TFW (8-bit RGB, ~200 MB)
  esaworldcover/         # ESA WorldCover S2 RGBNIR (16-bit 4-band, ~455 MB)
  esaworldcover-ndvi/    # ESA WorldCover S2 NDVI (single-band, ~168 MB)
  esaworldcover-swir/    # ESA WorldCover S2 SWIR (single-band, ~20 MB)
  esaworldcover-gamma0/  # ESA WorldCover S1 VV/VH ratio (SAR, ~346 MB)
  swissimage/            # swisstopo SWISSIMAGE DOP10 (8-bit RGB LV95, 36 tiles)
```

## Datasets

| Directory | Source | EPSG | Depth | Description |
|-----------|--------|------|-------|-------------|
| `copernicus/` | [Copernicus DEM GLO-30](https://spacedata.copernicus.eu/collections/copernicus-digital-elevation-model) | 4326 | Float32 | 30m DEM tile (Swiss Alps) |
| `naturalearth/` | [Natural Earth](https://www.naturalearthdata.com/downloads/10m-raster-data/) | 4326 (TFW) | 8-bit RGB | Global hypsometric tints |
| `esaworldcover/` | [ESA WorldCover S2](https://esa-worldcover.org/en/data-access) | 4326 | 16-bit 4-band | Sentinel-2 RGBNIR composite |
| `esaworldcover-ndvi/` | [ESA WorldCover S2](https://esa-worldcover.org/en/data-access) | 4326 | 16-bit 1-band | NDVI (vegetation index) |
| `esaworldcover-swir/` | [ESA WorldCover S2](https://esa-worldcover.org/en/data-access) | 4326 | 16-bit 1-band | SWIR (shortwave infrared) |
| `esaworldcover-gamma0/` | [ESA WorldCover S1](https://esa-worldcover.org/en/data-access) | 4326 | 16-bit multi-band | Sentinel-1 SAR VV/VH ratio |
| `swissimage/` | [swisstopo SWISSIMAGE DOP10](https://www.swisstopo.admin.ch/en/orthoimage-swissimage-10) | 2056 (LV95) | 8-bit RGB | 10cm orthophoto mosaic (36 tiles) |

## Running Tests

```bash
# Synthetic tests only (no download needed, runs in CI)
make test-integration

# All tests including real data
make test-integration-all

# Per-dataset tests
make test-integration-copernicus           # Float32 DEM -> terrarium PNG
make test-integration-naturalearth         # 8-bit RGB + TFW -> JPEG
make test-integration-esaworldcover        # 16-bit 4-band RGBNIR -> PNG
make test-integration-esaworldcover-ndvi   # Single-band NDVI -> grayscale PNG
make test-integration-esaworldcover-swir   # Single-band SWIR -> grayscale PNG
make test-integration-esaworldcover-gamma0 # SAR VV/VH ratio -> PNG
make test-integration-swissimage          # 8-bit RGB LV95 mosaic -> JPEG
```

## Examples (CLI)

```bash
make example-naturalearth            # Natural Earth -> PMTiles (TFW sidecar)
make example-copernicus             # Copernicus DEM -> terrarium PMTiles
make example-esaworldcover          # ESA WorldCover RGBNIR -> PMTiles
make example-esaworldcover-ndvi     # ESA WorldCover NDVI -> grayscale PMTiles
make example-esaworldcover-swir     # ESA WorldCover SWIR -> grayscale PMTiles
make example-esaworldcover-gamma0   # ESA WorldCover Gamma0 SAR -> PMTiles
make example-swissimage             # SWISSIMAGE DOP10 -> JPEG PMTiles
```
