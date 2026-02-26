# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Memory-efficient Go toolset for converting GeoTIFF/COG files to PMTiles v3 archives and transforming existing PMTiles archives. Pure Go stdlib — zero external Go dependencies. Requires libwebp C library for native WebP encoding (`brew install webp` / `apt-get install libwebp-dev`).

## Build & Test Commands

```bash
make build              # Build geotiff2pmtiles (requires CGO_ENABLED=1 + libwebp)
make build-transform    # Build pmtransform
make build-all          # Both binaries
make test               # Run all tests
make test-race          # Tests with race detector (used in CI)
make bench              # Run benchmarks
make fmt                # gofmt
make vet                # go vet
make check              # fmt + vet + test
```

Run a single test: `go test ./internal/coord/ -run TestMercator`

Run a single benchmark: `go test ./internal/tile/ -bench=BenchmarkDownsample -benchmem`

## Architecture

Two CLI tools in `cmd/`:
- **geotiff2pmtiles** — COG → PMTiles conversion
- **pmtransform** — PMTiles → PMTiles transformation (passthrough / re-encode / rebuild pyramid)

Core packages in `internal/`:
- **cog/** — Memory-mapped TIFF/COG reader, IFD parsing, GeoTIFF tags, TFW sidecar, LRU tile cache, strip-to-tile promotion
- **coord/** — Projection interface + implementations (Swiss LV95, WGS84, Web Mercator), Hilbert curve ordering, EPSG inference from coordinate ranges
- **encode/** — Encoder interface + JPEG/PNG/WebP/Terrarium implementations. WebP uses CGo (`webp.go`) with a stub fallback (`webp_stub.go`) for `CGO_ENABLED=0` builds
- **tile/** — Tile generation pipeline: `generator.go` (parallel COG→tile), `transform.go` (PMTiles→PMTiles), `resample.go` (Lanczos-3/bicubic/bilinear/nearest/mode with LUT acceleration), `downsample.go` (pyramid building with gray fast path), `diskstore.go` (disk-backed store with memory backpressure), `tiledata.go` (compact uniform/gray/RGBA representation), `rgbapool.go` (sync.Pool buffer reuse)
- **pmtiles/** — PMTiles v3 reader/writer. Two-pass writer: collect entries → sort by Hilbert ID → cluster tile data. FNV-64a deduplication.

**Pipeline flow** (geotiff2pmtiles): Scan COGs → parse metadata → compute bounds/zoom → generate max-zoom tiles (parallel, Hilbert-batched) → inverse-project + resample per pixel → downsample pyramid → encode → two-pass PMTiles write.

**Transform modes** (pmtransform): Passthrough (raw byte copy), re-encode (decode + encode with new format), rebuild (decode max-zoom + downsample new pyramid).

## Key Design Patterns

- **Memory-mapped I/O** for COG access — tile-level reads without loading entire file
- **Encoded tiles in memory** (5-25x smaller than raw pixels), with disk spilling via dedicated I/O goroutine
- **LUT-accelerated resampling** — 1024-entry precomputed tables for Lanczos-3 and bicubic kernels
- **Precomputed lon/lat arrays** — O(n) trig calls instead of O(n²) in Mercator projection
- **sync.Pool for RGBA buffers** — critical: buffers must be zeroed before reuse
- **Uniform tile compaction** — single-color tiles stored as 4 bytes
- **Nodata-aware source fallthrough** — alpha=0 results skipped so later sources can contribute

## After Completing a Task

Per AGENTS.md:
1. Document changes in `changes/yyyy-mm-dd-hh-mm-title.md`
2. Update design decisions in `DESIGN.md`
3. Update architecture in `ARCHITECTURE.md`
4. Update CLI help text
5. Update `README.md`
