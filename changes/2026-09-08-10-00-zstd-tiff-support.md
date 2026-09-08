# ZSTD TIFF compression support

Adds decoding of TIFF compression 50000 (GDAL/libtiff ZSTD) in `internal/cog`
for tiled, strip and float paths, with predictor support. Uses
`github.com/klauspost/compress/zstd` (pure Go, so `CGO_ENABLED=0` builds keep
working). This is the project's first external Go dependency; a hand-written
zstd decoder was judged not worth ~1500 lines.

Test: `internal/cog/zstd_test.go` round-trips a predictor-3 float32 tile.

## Example dataset

No public COG dataset we could find ships ZSTD tiles (Copernicus, ESA WorldCover,
swisstopo, USGS 3DEP, Sentinel-2 on AWS, NRCan, Esri LULC and OpenAerialMap were
probed; all use Deflate, LZW or JPEG). `integration/testdata/download.sh` therefore
derives `copernicus-zstd/` from the Copernicus DEM with
`gdal_translate -of COG -co COMPRESS=ZSTD -co PREDICTOR=3`, skipping when GDAL is
absent. New targets: `make example-copernicus-zstd`,
`make test-integration-copernicus-zstd` (`TestCopernicusZSTD`). Output is
byte-identical to the Deflate original.

## Profiling follow-ups

Profiling the ZSTD decode path showed `undoFloatingPointPredictor` at 66% of
decode time, a third of it in `runtime.ifaceeq` from comparing `bo ==
binary.LittleEndian` once per byte. Hoisting the compare out of the loop
halved whole-file float decode for the ZSTD DEM (≈420 ms → ≈186 ms).

Deflate decode is dominated by `compress/flate`. Switching the Deflate/zlib path
to `github.com/klauspost/compress/{flate,zlib}` (already a dependency) cut it by
20–25% (≈570 ms → ≈430 ms) and allocations from ≈8900 to ≈1170 per file.

LZW stays on the in-tree TIFF decoder: klauspost/compress has no LZW package,
and the only TIFF-variant LZW in Go is `golang.org/x/image/tiff/lzw`.
