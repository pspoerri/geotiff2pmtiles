# Fix: Nodata/Transparent Pixels Blocking Source Fallthrough

**Date**: 2026-02-20

## Problem

PMTiles output shows issues in areas with no data (holes). Tiles within a source's
bounding box but containing only nodata pixels are incorrectly emitted as non-empty,
and holes in one source prevent valid data from subsequent sources from being used.

## Root Cause

`sampleFromTileSources` returned `found=true` for any successfully-read pixel,
including fully transparent (alpha=0) ones. This caused:

1. **Source blocking**: When source A has nodata at a coordinate but source B has
   valid data, source A's transparent pixel was returned as "found" and source B
   was never checked — leaving holes in the output.

2. **False non-empty tiles**: `renderTile` set `hasData=true` for every "found"
   pixel regardless of alpha. Tiles entirely within a source's bounding box but
   in a nodata region (e.g. ocean in ESA WorldCover) were encoded and stored as
   all-transparent tiles through the entire zoom pyramid. For JPEG output (no
   alpha support), these appeared as solid black tiles.

The float/terrarium path (`sampleFromTileSourcesFloat`) already handled this
correctly by skipping NaN/nodata values and trying the next source.

## Fix

In `sampleFromTileSources`: after reading a pixel, check `alpha == 0` and
`continue` to the next source instead of returning. Only pixels with alpha > 0
are returned as "found". Semi-transparent pixels from resampling near data edges
(e.g. bilinear interpolation between data and nodata) are correctly preserved
since they have alpha > 0.

## Changes

- `internal/tile/resample.go`: Skip alpha=0 results in `sampleFromTileSources`
- `DESIGN.md`: Added "Nodata-aware source fallthrough" design decision
