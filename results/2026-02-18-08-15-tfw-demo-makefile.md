# TFW Demo Targets in Makefile

## Summary

Added Makefile demo targets for the `data_tfw/` directory containing plain TIFFs with TFW sidecar files (Natural Earth global data).

## Changes

### Modified: `Makefile`
- New variable `TFW_MAX_ZOOM` (default: 6, appropriate for global raster data)
- `demo-tfw`: base demo using `data_tfw/` input
- `demo-tfw-full-disk`: TFW demo with 1 MB memory limit for disk spilling
- Format variants: `demo-tfw-jpeg`, `demo-tfw-png`, `demo-tfw-webp`
- Full-disk format variants: `demo-tfw-full-disk-jpeg`, `demo-tfw-full-disk-png`, `demo-tfw-full-disk-webp`
- Updated `.PHONY` list and `help` target with TFW variable/examples

### Modified: `README.md`
- Mentioned `data_tfw/` sample directory in introduction
- Added TFW usage example

## Usage

```bash
make demo-tfw                         # Default: WebP, zoom 0-6
make demo-tfw-jpeg                    # JPEG encoding
make demo-tfw TFW_MAX_ZOOM=4         # Custom max zoom
make demo-tfw-full-disk               # With disk spilling
```
