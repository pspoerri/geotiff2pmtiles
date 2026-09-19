# README cleanup

Shortened `README.md` from 412 to ~310 lines and fixed remarks that went stale when the
pure-Go WebP fallback landed:

- `CGO_ENABLED=0` / `cross-windows*` builds are no longer "no WebP" — they have lossless
  WebP. Same fix in the `Makefile` comments.
- libwebp is a prerequisite for *lossy* WebP only, not for WebP support.
- `make build-all` builds all four tools, not "both binaries".
- Merged Platform Support / Prerequisites / Installation / Cross-compilation into one
  Installation section with a release-binary table (Windows: static libwebp; Linux/macOS:
  pure-Go lossless).
- Added the undocumented `pmheader` tool to Utilities; collapsed Utilities to one block.
- Condensed Features (dropped implementation details covered by ARCHITECTURE.md), removed
  the duplicated test-data paragraph from the intro, two near-duplicate examples, the
  Performance blurb and the per-dataset test target list.
- Moved Installation to the bottom of the README and split the build-from-source
  instructions into a new `BUILDING.md`, linked from the README.
- Moved the Development section (integration tests, profiling) into a new
  `DEVELOPMENT.md`, linked from the README.

No code changes; DESIGN.md, ARCHITECTURE.md and CLI help are unaffected.
