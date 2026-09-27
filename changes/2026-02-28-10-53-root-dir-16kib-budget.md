# Root directory 16 KiB budget enforcement

## Problem

PMTiles files with many tile entries (e.g. ESA WorldCover at zoom 13 with 60M+ tiles)
produced a root directory exceeding 16 KiB. The pmtiles.io web viewer fetches the first
16,384 bytes and attempts to decompress the root directory from them. When the root
directory extends beyond that, the gzip stream is truncated, causing:

    "Failed to read data from the ReadableStream: TypeError: The input is ended
     without reaching the stream end"

## Root cause

`buildDirectory` checked only the entry count (≤16,384 → flat root) but not the
compressed size. With realistic Hilbert-curve tile IDs and varying tile sizes, the
varint-encoded + gzip-compressed directory easily exceeds 16 KiB even with fewer
than 16,384 entries.

## Fix

After serializing the root directory, check its compressed size against the budget
(16,384 − 127 = 16,257 bytes). If exceeded, fall through to leaf directories where
the root contains only small leaf pointers (well within budget).

## Files changed

- `internal/pmtiles/directory.go` — size-based fallback to leaf directories
- `internal/pmtiles/directory_test.go` — test with realistic Hilbert IDs verifying budget
- `DESIGN.md` — new section documenting the design decision
- `ARCHITECTURE.md` — updated directory.go description
