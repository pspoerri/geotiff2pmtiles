# Recursive directory scanning for TIFF files

## What changed
`collectTIFFs()` in `cmd/geotiff2pmtiles/main.go` now walks directories
recursively using `filepath.WalkDir` instead of only listing immediate
directory entries with `os.ReadDir`. TIFF files in subfolders are now
discovered automatically.

## Why
Users organizing large datasets into subdirectories (e.g., by region or
acquisition date) previously had to list each subfolder individually or
flatten the structure. Recursive scanning lets a single top-level directory
path find all `.tif`/`.tiff` files regardless of nesting depth.

## Files modified
- `cmd/geotiff2pmtiles/main.go` — `collectTIFFs()`: `os.ReadDir` → `filepath.WalkDir`; added `io/fs` import; updated CLI help text
- `README.md` — noted recursive scanning in directory example
