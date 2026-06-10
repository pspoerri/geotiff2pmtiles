# Struct field alignment across all packages

## What

Reordered struct fields in all production packages so that
`golang.org/x/tools/.../fieldalignment` reports zero findings (test files
excluded). No behavioral change; all composite literals of the affected
types were already keyed, and serialization (PMTiles header) is done
field-by-field, so field order is purely an in-memory concern.

Affected structs:

- `internal/cog`: `IFD` (344 → 336 B), `tiffEntry`, `Reader`, `Preset`,
  `tileCacheShard`, `floatCacheShard`
- `internal/pmtiles`: `Header` (128 → 120 B), `WriterOptions`, `Reader`,
  `Writer`
- `internal/tile`: `ioRequest`, `DiskTileStore` (200 → 192 B),
  `DiskTileStoreConfig`, `Config` (144 → 136 B), `TransformConfig`,
  `progressBar`
- `cmd/pmheader`: `patchOptions`

## Why

Two effects:

1. **Size**: mixed small/large fields created padding holes (e.g. `IFD`
   interleaved `uint16` scalars between 24-byte slice headers).
2. **GC scan range**: clustering pointer-bearing fields (maps, slices,
   strings, interfaces) at the front of each struct shrinks the prefix the
   garbage collector must scan (e.g. `Writer` 352 → 200 pointer bytes,
   `ioRequest` 32 → 8 — the latter flows through the disk-spill channel
   once per non-uniform tile).

Individually small, but `tiffEntry` is allocated per TIFF tag during
parsing, `ioRequest` per spilled tile, and cache shards exist 64× per
cache.

## Verification

- `fieldalignment ./...` → no production findings
- `go vet ./...`, `gofmt`, full `go test ./...` pass
