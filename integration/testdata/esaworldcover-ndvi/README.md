# ESA WorldCover S2 NDVI Composite

Sentinel-2 NDVI temporal percentile composite at 10m resolution.

## File

`ESA_WorldCover_10m_2021_v200_N00E009_NDVI.tif` — 8-bit 3-band COG, EPSG:4326, ~168 MB

3 bands: NDVI-p90 (upper range), NDVI-p50 (median), NDVI-p10 (lower range).
12000x12000 pixels. Tile covers N00 E009 (Gulf of Guinea / Cameroon coast).

## Source

- **Product**: ESA WorldCover 10m v200 — Sentinel-2 NDVI composite
- **Provider**: European Space Agency (ESA) / VITO
- **Website**: https://esa-worldcover.org/en/data-access
- **S3 bucket**: `s3://esa-worldcover-s2/ndvi/`
- **Documentation**: https://esa-worldcover.s3.eu-central-1.amazonaws.com/v200/2021/docs/WorldCover_PUM_V2.0.pdf

## License

CC-BY 4.0 — https://creativecommons.org/licenses/by/4.0/

Attribution: "ESA WorldCover project 2021 / Contains modified Copernicus Sentinel data (2021)
processed by ESA WorldCover consortium."
