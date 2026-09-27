# Move SWISSIMAGE data into integration test suite

**Date**: 2026-02-27

## Summary

Moved swisstopo SWISSIMAGE DOP10 test data from `data/` into the integration
test suite as `integration/testdata/swissimage/`. Added a dedicated integration
test (`TestSwissImagePipeline`) that exercises multi-source mosaic conversion
from Swiss LV95 (EPSG:2056) projected coordinates.

## Changes

- **Moved data**: `data/*.tif` and download CSV → `integration/testdata/swissimage/`
- **Removed**: `data/` directory (no longer needed)
- **New test**: `integration/satellite_swissimage_test.go` — multi-source 8-bit RGB
  LV95 mosaic → JPEG PMTiles pipeline test
- **Download script**: Updated `integration/testdata/download.sh` to download
  SWISSIMAGE tiles from the CSV URL list
- **Makefile**: Added `test-integration-swissimage` and `demo-swissimage` targets;
  updated `demo`, `demo-full-disk`, `demo-profile`, and `demo-transform*` targets
  to use `$(SWISSIMAGE_DIR)/` instead of `data/`
- **Updated**: `.gitignore`, `README.md`, `ARCHITECTURE.md`, testdata `README.md`
