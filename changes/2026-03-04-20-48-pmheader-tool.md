# pmheader: fast PMTiles header and metadata patching tool

## Summary

New `pmheader` CLI tool that rewrites PMTiles v3 header fields and metadata
without re-encoding or even reading tile data. This is orders of magnitude
faster than using `pmtransform --rebuild` when all you need is to fix zoom
levels, bounds, center coordinates, tile type, or metadata JSON.

## What it does

- **`--show`**: Inspect header and metadata of any PMTiles file.
- **Header patches** (MinZoom, MaxZoom, CenterZoom, bounds, center, tile type):
  In-place 127-byte overwrite when no output path is given — instant.
- **Metadata patches** (`--set key=value`, `--unset key`, `--metadata-file`):
  Rewrites the pre-tile-data sections (header + root dir + metadata + leaf dirs)
  and streams the tile data verbatim. Fast even for multi-GB files.

## Files

- `cmd/pmheader/main.go` — new CLI tool (no CGo required)
- `Makefile` — added `build-header` target and included in `build-all`

## Design decisions

- **Two code paths**: Header-only changes use `WriteAt(0)` for a 127-byte
  in-place patch. Metadata changes use temp-file + rename for atomicity.
- **No CGo dependency**: Pure Go — builds with `CGO_ENABLED=0`.
- **Directory offsets are tile-data-relative**: Root and leaf directories
  can be copied verbatim; only the header offset fields need recalculation
  when metadata size changes.
