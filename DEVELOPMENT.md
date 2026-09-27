# Development

Build instructions: [BUILDING.md](BUILDING.md). Code structure: [ARCHITECTURE.md](ARCHITECTURE.md).

`make check` runs `go fmt` (which rewrites files), `go vet` and `go test ./...`; plain
`make` lists every target, grouped by purpose.

## Tests

Unit tests live next to the code in `internal/` and `cmd/` (the CLIs' flag handling,
validation and helper functions). `internal/cog/tiffbuild_test.go` builds TIFF directories
byte by byte, malformed ones included, for the parser tests, and provides `bytesSource`, a
naive `ByteSource` that panics when the reader breaks the range contract.

## Reproducing CI locally

`make check` is a quick pre-commit pass, not what CI runs: CI fails on unformatted files
instead of fixing them, uses the race detector, disables the test cache and checks the
module files. The Linux job runs:

```bash
test -z "$(gofmt -l .)"                                  # formatting (lists offending files)
go vet ./...
go test -race -count=1 ./internal/... ./cmd/...          # unit tests
go test -race -count=1 -timeout 120s -v ./integration/   # synthetic integration tests
go mod tidy && git diff --exit-code go.mod go.sum        # module tidiness
```

The Windows jobs run the same tests without `-race` (mingw has no race runtime), and the
release build cross-compiles the Linux and macOS binaries at `CGO_ENABLED=0`. To check that
those configurations still compile:

```bash
CGO_ENABLED=0 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
```

## Integration Tests

End-to-end tests exercise the full pipeline with synthetic and real satellite data:

```bash
make test-integration            # Synthetic tests only (~8s, no download needed)
make test-integration-download   # Download all real satellite data (~1.2 GB total)
make test-integration-all        # Download + run all tests
```

Real-data tests skip themselves until their data is downloaded; once it is, `make test`
and `make test-race` run them too.

Eight real-data datasets are used, each exercising a different input type:

| Dataset | Size | EPSG | Type | Description |
|---------|------|------|------|-------------|
| `copernicus/` | ~8 MB | 4326 | Float32 | Copernicus DEM GLO-30 — 30m elevation tile (Swiss Alps) |
| `copernicus-zstd/` | ~39 MB | 4326 | Float32, ZSTD | Same tile recompressed with `gdal_translate -co COMPRESS=ZSTD` (needs GDAL) |
| `naturalearth/` | ~200 MB | 4326 | 8-bit RGB + TFW | Natural Earth hypsometric tints (global, TFW sidecar) |
| `esaworldcover/` | ~455 MB | 4326 | 16-bit 4-band | ESA WorldCover S2 RGBNIR composite (Sentinel-2) |
| `esaworldcover-ndvi/` | ~168 MB | 4326 | 8-bit 3-band | ESA WorldCover S2 NDVI percentiles (p10/p50/p90) |
| `esaworldcover-swir/` | ~20 MB | 4326 | 8-bit 2-band | ESA WorldCover S2 SWIR composite (B11/B12) |
| `esaworldcover-gamma0/` | ~346 MB | 4326 | 16-bit 3-band | ESA WorldCover S1 SAR gamma0 VV/VH ratio |
| `swissimage/` | varies | 2056 | 8-bit RGB | swisstopo SWISSIMAGE DOP10 — 10cm orthophoto mosaic (LV95) |

Each dataset has its own target: `make test-integration-<dataset>` (e.g. `make test-integration-copernicus-zstd`).

## Profiling

```bash
make example-swissimage-profile             # Run with CPU + memory profiling
go tool pprof -http=:8080 dist/cpu.prof    # Interactive flame graph
go tool pprof -http=:8081 dist/mem.prof    # Memory profile
```

Any run can write profiles with `--cpu-profile <file>` and `--mem-profile <file>`
(geotiff2pmtiles and pmtransform).
