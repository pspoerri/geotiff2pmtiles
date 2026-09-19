# Development

Build instructions: [BUILDING.md](BUILDING.md). Code structure: [ARCHITECTURE.md](ARCHITECTURE.md).

`make check` runs fmt, vet and the unit tests; `make help` lists every target.

## Integration Tests

End-to-end tests exercise the full pipeline with synthetic and real satellite data:

```bash
make test-integration            # Synthetic tests only (~8s, no download needed)
make test-integration-download   # Download all real satellite data (~1.2 GB total)
make test-integration-all        # Download + run all tests
```

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
