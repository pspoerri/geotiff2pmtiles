# satcomposite patches: bit-packed depths, raw integer reads, ByteSource

Seven commits ported back from the satcomposite project (vendored copy), applied with
`git am` as d497ffa..91edf6a, then reviewed; the fixes below are on top.

## Patches

1. Decode bit-packed sample depths (e.g. 15-bit Sentinel-2 L2A from Planetary Computer);
   `ReadUint16Tile`; `ValueRange` scans through it.
2. `ReadUint16Region` and `Uint16TileCache` (explicit hit flag so empty tiles stay cached).
3. Unpack bit-packed samples through a 32-bit window (~6.5× faster per 15-bit tile).
4. Hit/miss counters on `Uint16TileCache`.
5. `WriterOptions.Extra`: caller-supplied metadata keys.
6. `ByteSource`: all byte access goes through `Size`/`Slice`/`Close`; `OpenSource`.
7. `Reader.SetID` for readers opened one at a time.

## Review fixes

- `ReadUint16Tile`: the short last virtual tile of a 16-bit little-endian strip TIFF was
  sent through the MSB-first bit unpacker and came back byte-swapped, so the auto rescale
  range was wrong (e.g. [100, 30956] instead of [-5000, 100]). Whole-byte depths now always
  use the file's byte order.
- `ReadUint16Tile`: single-band `PlanarConfiguration=2` tiles were rejected, so auto
  rescale aborted on files that worked before. Planar layout is only rejected for spp > 1.
- `ReadUint16Tile`: depths 1-7 panicked in the padded branch; a length tie between
  packed and padded 9-15 bit rows was read as padded. Sub-byte depths are always
  unpacked, and a tie goes to packed.
- `pmtiles.Writer`: `json.Marshal` errors from `Extra` were ignored, shipping an archive with
  empty metadata. `NewWriter` now rejects a non-encodable `Extra`; `Finalize` returns the error.
- CLI: 9-16 bit input is rescaled like 16-bit (15-bit input rendered near-black).
- Regression tests in `internal/cog/uint16region_test.go`.

## Not fixed (low severity, see review)

Signed 9-15 bit packed samples are not sign-extended before the sign bias; `Stats` on a
`Local` view returns zeros; a read after `Close` panics instead of erroring; the three
sharded LRU caches are near-copies (a generic `shardedLRU[V]` would remove ~120 lines).
