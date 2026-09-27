# Add `demo-all` Makefile target

Added a `demo-all` target that runs every demo variant in sequence:
- Standard demos (JPEG, PNG, WebP)
- Full-disk demos (JPEG, PNG, WebP)
- TFW demos (JPEG, PNG, WebP)
- TFW full-disk demos (JPEG, PNG, WebP)
- Transform demos (passthrough, re-encode, rebuild)

Usage: `make demo-all`
