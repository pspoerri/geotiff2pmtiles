# Pure-Go WebP fallback for CGO_ENABLED=0 builds

`webp_stub.go` used to return errors. It now decodes WebP with `golang.org/x/image/webp`
and encodes lossless WebP (VP8L) with `github.com/HugoSmits86/nativewebp`, so CGo-free
builds (the released Linux/macOS binaries) can read WebP archives and write WebP tiles.
`--quality` is ignored there; libwebp (CGo) is still needed for lossy WebP.

- `encode.Formats()` reports `webp (lossless only)` in non-CGo builds.
- Removed the jpeg → webp → png fallback in geotiff2pmtiles; webp always exists now.
- Two new pure-Go dependencies: `golang.org/x/image`, `github.com/HugoSmits86/nativewebp`.
