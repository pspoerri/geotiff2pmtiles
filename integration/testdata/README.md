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

## Datasets

| Directory | Source | EPSG | Depth | Description |
|-----------|--------|------|-------|-------------|
| `copernicus/` | [Copernicus DEM GLO-30](https://spacedata.copernicus.eu/collections/copernicus-digital-elevation-model) | 4326 | Float32 | 30m DEM tile (Swiss Alps) |
| `copernicus-zstd/` | derived from `copernicus/` with `gdal_translate -co COMPRESS=ZSTD -co PREDICTOR=3` | 4326 | Float32 | ZSTD (compression 50000) decode path; needs GDAL |
| `naturalearth/` | [Natural Earth](https://www.naturalearthdata.com/downloads/10m-raster-data/) | 4326 (TFW) | 8-bit RGB | Global hypsometric tints |
| `esaworldcover/` | [ESA WorldCover S2](https://esa-worldcover.org/en/data-access) | 4326 | 16-bit 4-band | Sentinel-2 RGBNIR composite |
| `esaworldcover-ndvi/` | [ESA WorldCover S2](https://esa-worldcover.org/en/data-access) | 4326 | 8-bit 3-band | NDVI percentiles (p90/p50/p10) |
| `esaworldcover-swir/` | [ESA WorldCover S2](https://esa-worldcover.org/en/data-access) | 4326 | 8-bit 2-band | SWIR composite (B11/B12) |
| `esaworldcover-gamma0/` | [ESA WorldCover S1](https://esa-worldcover.org/en/data-access) | 4326 | 16-bit 3-band | Sentinel-1 SAR gamma0 VV/VH ratio |
| `swissimage/` | [swisstopo SWISSIMAGE DOP10](https://www.swisstopo.admin.ch/en/orthoimage-swissimage-10) | 2056 (LV95) | 8-bit RGB | 10cm orthophoto mosaic (36 tiles) |

## Running Tests

See [DEVELOPMENT.md](../../DEVELOPMENT.md). Each dataset has a
`make test-integration-<dir>` and a `make example-<dir>` target.
