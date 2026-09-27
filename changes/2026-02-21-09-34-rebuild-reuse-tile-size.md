# Reuse source tile size when rebuilding pyramid (2026-02-21)

## Summary

When `pmtransform --rebuild` runs without `--tile-size`, the output now reuses the
source archive's tile size instead of defaulting to 256px.

## Problem

The PMTiles v3 header does not store tile size (only format via `TileType`). The
previous implementation always defaulted to 256 when `--tile-size` was omitted,
even for archives built with 512px tiles. Rebuilding with `--resampling lanczos`
would change the resampling but inadvertently reduce tile size from 512→256.

## Solution

When `--tile-size` is -1 (default/“keep source”), pmtransform discovers the source
tile size by reading and decoding one tile from the max zoom level. The decoded
image dimensions are used as the output tile size. If no tile can be decoded
(e.g. all empty), falls back to 256.

## Changes

- `cmd/pmtransform/main.go`: Added `discoverSourceTileSize(reader, format)` which
  decodes the first available non-empty tile and returns its width. Replaced the
  previous incorrect `srcHeader.TileType` usage (format code, not size) with this
  discovery logic.
