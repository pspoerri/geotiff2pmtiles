# Copernicus DEM GLO-30, ZSTD-compressed

The same N46 E008 tile as `../copernicus/`, recompressed locally by
`download.sh` into a COG with TIFF compression 50000 (ZSTD) and the
floating-point predictor:

```bash
gdal_translate -of COG -co COMPRESS=ZSTD -co PREDICTOR=3 -co BLOCKSIZE=512 \
  ../copernicus/copernicus_dem_n46_e008.tif copernicus_dem_n46_e008_zstd.tif
```

No public dataset we know of ships ZSTD tiles, so this exercises the ZSTD decode
path with a real GDAL-produced file. Requires GDAL (`brew install gdal` /
`apt-get install gdal-bin`) at download time; skipped otherwise.

License and attribution: see `../copernicus/README.md`.
