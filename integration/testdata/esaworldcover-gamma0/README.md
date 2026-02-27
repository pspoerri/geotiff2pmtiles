# ESA WorldCover S1 Gamma0 VV/VH Ratio Composite

Sentinel-1 SAR median gamma0 backscatter composite at 10m resolution.

## File

`ESA_WorldCover_10m_2021_v200_N46E008_S1VVVHratio.tif` — 16-bit 3-band COG, EPSG:4326, ~346 MB

3 bands: VV, VH, VH/VV ratio. Data is dB-scaled (SCALE=0.001, OFFSET=-45):
physical dB = pixel_value * 0.001 - 45. 12000x12000 pixels.
Tile covers N46 E008 (Swiss Alps, Bernese Oberland region).

## Source

- **Product**: ESA WorldCover 10m v200 — Sentinel-1 gamma0 backscatter composite
- **Provider**: European Space Agency (ESA) / VITO
- **Website**: https://esa-worldcover.org/en/data-access
- **S3 bucket**: `s3://esa-worldcover-s1/vvvhratio/`
- **Documentation**: https://esa-worldcover.s3.eu-central-1.amazonaws.com/v200/2021/docs/WorldCover_PUM_V2.0.pdf

## License

CC-BY 4.0 — https://creativecommons.org/licenses/by/4.0/

Attribution: "ESA WorldCover project 2021 / Contains modified Copernicus Sentinel data (2021)
processed by ESA WorldCover consortium."
