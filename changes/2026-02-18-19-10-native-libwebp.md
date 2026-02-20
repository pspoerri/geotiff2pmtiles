# Replace WASM WebP Encoder with Native libwebp

**Date:** 2026-02-18
**Motivation:** WebP encoding via WASM was the #1 CPU bottleneck (~41% of CPU, 81.23s) and caused 51 GB of WASM memory growth allocations.

## What Changed

Replaced the `gen2brain/webp` package (which runs libwebp inside a wazero WASM runtime) with a direct CGo binding to the system's native libwebp library.

### Files Modified

| File | Change |
|------|--------|
| `internal/encode/webp.go` | Rewritten: CGo wrapper around `WebPEncodeRGBA` / `WebPDecodeRGBA` / `WebPFree` |
| `internal/encode/decode.go` | Removed `gen2brain/webp` import; calls `DecodeWebP()` from webp.go |
| `go.mod` | Removed `gen2brain/webp`, `tetratelabs/wazero`, `ebitengine/purego` dependencies |
| `go.sum` | Cleared (zero external Go dependencies) |
| `Makefile` | Removed `CGO_ENABLED=0` from build/install; updated cross-compilation notes |
| `ARCHITECTURE.md` | Updated webp.go description |
| `DESIGN.md` | Added "Native libwebp" design decision section |
| `README.md` | Updated features, added Prerequisites section with libwebp install instructions |

### Technical Approach

Thin CGo wrapper (~90 lines) directly calling libwebp's C API:

- **Encode:** `WebPEncodeRGBA(pix, w, h, stride, quality, &output)` — libwebp allocates the output buffer, we copy it to Go memory with `C.GoBytes`, then free with `WebPFree`
- **Decode:** `WebPDecodeRGBA(data, len, &w, &h)` — returns C-allocated RGBA pixels, copied into `image.RGBA.Pix` with `unsafe.Slice` + `copy`, then freed
- **Linking:** Uses `pkg-config: libwebp` for portable include/library path resolution

### What This Eliminates

| Before (WASM) | After (native CGo) |
|---|---|
| 81.23s in `runtime._ExternalCode` (WASM execution) | Direct C function call, no interpreter overhead |
| 51 GB WASM memory growth (`MemoryInstance.Grow`) | Zero WASM allocations; libwebp manages its own heap |
| 1.1 GB WASM module init per encode (`NewMemoryInstance`) | Single shared libwebp, no per-call init |
| 7.01s Go-side encode overhead | Minimal CGo call overhead (~1μs per call) |
| 3 Go module dependencies | 0 Go module dependencies |

### Expected Performance Impact

- **Encode speed:** 3-5x faster (native C vs WASM interpreter)
- **Memory:** ~52 GB less total allocation (51 GB WASM heap + 1.1 GB module init eliminated)
- **GC pressure:** Dramatically reduced — WASM memory churn was creating extreme GC load

### Tradeoffs

- Builds now require `CGO_ENABLED=1` (Go default) and libwebp installed on the system
- Cross-compilation requires a C cross-compiler and target-platform libwebp
- No longer "pure Go" — but the 41% CPU bottleneck elimination is well worth it

### Build Prerequisites

```bash
# macOS
brew install webp

# Debian/Ubuntu
sudo apt-get install libwebp-dev

# Fedora/RHEL
sudo dnf install libwebp-devel
```

### Verification

- All existing tests pass (`go test ./...`)
- Build succeeds with `make build`
- Zero Go module dependencies remaining
