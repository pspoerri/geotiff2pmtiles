# geotiff2pmtiles Makefile

BINARY           := geotiff2pmtiles
BINARY_TRANSFORM := pmtransform
BINARY_CHECK     := checkpmtiles
MODULE           := github.com/pspoerri/geotiff2pmtiles
CMD              := ./cmd/geotiff2pmtiles/
CMD_TRANSFORM    := ./cmd/pmtransform/
CMD_CHECK        := ./cmd/checkpmtiles/
BUILD_DIR        := dist
GO               := go
GOFLAGS          :=
LDFLAGS          :=

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    += -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

OUTPUT           := $(BUILD_DIR)/$(BINARY)
OUTPUT_TRANSFORM := $(BUILD_DIR)/$(BINARY_TRANSFORM)
OUTPUT_CHECK     := $(BUILD_DIR)/$(BINARY_CHECK)

# Default tile format and quality for example targets
FORMAT     ?= webp
QUALITY    ?= 85
MIN_ZOOM   ?= 14
MAX_ZOOM   ?= 18
TILE_SIZE  ?= 512
CONCURRENT ?= $(shell nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)
MEM_LIMIT  ?= 0

# Integration testdata directories (each dataset in its own folder for CLI input)
TESTDATA_DIR     := integration/testdata
COPERNICUS_DIR   := $(TESTDATA_DIR)/copernicus
NATURALEARTH_DIR := $(TESTDATA_DIR)/naturalearth
ESAWORLDCOVER_DIR       := $(TESTDATA_DIR)/esaworldcover
ESAWORLDCOVER_NDVI_DIR  := $(TESTDATA_DIR)/esaworldcover-ndvi
ESAWORLDCOVER_SWIR_DIR  := $(TESTDATA_DIR)/esaworldcover-swir
ESAWORLDCOVER_GAMMA0_DIR := $(TESTDATA_DIR)/esaworldcover-gamma0
SWISSIMAGE_DIR           := $(TESTDATA_DIR)/swissimage

.PHONY: all build build-transform build-check build-all install \
        test test-race test-cover bench \
        test-integration test-integration-download test-integration-real test-integration-all \
        test-integration-copernicus test-integration-naturalearth \
        test-integration-esaworldcover test-integration-esaworldcover-ndvi \
        test-integration-esaworldcover-swir test-integration-esaworldcover-gamma0 \
        test-integration-swissimage \
        lint fmt vet tidy check \
        clean clean-all \
        run example-all pprof-cpu pprof-mem \
        example-swissimage example-swissimage-full-disk example-swissimage-profile \
        example-swissimage-jpeg example-swissimage-png example-swissimage-webp \
        example-swissimage-full-disk-jpeg example-swissimage-full-disk-png example-swissimage-full-disk-webp \
        example-naturalearth example-naturalearth-full-disk \
        example-naturalearth-jpeg example-naturalearth-png example-naturalearth-webp \
        example-naturalearth-full-disk-jpeg example-naturalearth-full-disk-png example-naturalearth-full-disk-webp \
        example-copernicus example-esaworldcover example-esaworldcover-ndvi \
        example-esaworldcover-swir example-esaworldcover-gamma0 \
        example-transform example-transform-reencode example-transform-rebuild \
        cross-linux cross-linux-arm64 cross-darwin cross-darwin-arm64 cross-all \
        help

## all: Build all binaries (default target)
all: build build-transform

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

## build: Compile the binary (requires libwebp: brew install webp / apt-get install libwebp-dev)
build: $(BUILD_DIR)
	CGO_ENABLED=1 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT) $(CMD)

## build-transform: Compile pmtransform binary
build-transform: $(BUILD_DIR)
	CGO_ENABLED=1 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT_TRANSFORM) $(CMD_TRANSFORM)

## build-check: Compile checkpmtiles validation tool
build-check: $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT_CHECK) $(CMD_CHECK)

## build-all: Build geotiff2pmtiles, pmtransform, and checkpmtiles
build-all: build build-transform build-check

## install: Install to $GOPATH/bin
install:
	CGO_ENABLED=1 $(GO) install $(GOFLAGS) -ldflags "$(LDFLAGS)" $(CMD)

# ---------- Testing ----------

## test: Run all tests
test:
	$(GO) test $(GOFLAGS) ./...

## test-race: Run all tests with the race detector
test-race:
	$(GO) test $(GOFLAGS) -race ./...

## test-cover: Run tests and generate an HTML coverage report
test-cover:
	$(GO) test $(GOFLAGS) -coverprofile=$(BUILD_DIR)/coverage.out ./...
	$(GO) tool cover -html=$(BUILD_DIR)/coverage.out -o $(BUILD_DIR)/coverage.html
	@echo "Coverage report: $(BUILD_DIR)/coverage.html"

## bench: Run benchmarks
bench:
	$(GO) test $(GOFLAGS) -bench=. -benchmem ./...

## test-integration: Run synthetic integration tests (no download needed)
test-integration:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 120s -v ./integration/

## test-integration-download: Download real satellite test data
test-integration-download:
	bash integration/testdata/download.sh

## test-integration-real: Run all integration tests (synthetic + real satellite data)
test-integration-real:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v ./integration/

## test-integration-copernicus: Run Copernicus DEM integration test (float32 → terrarium)
test-integration-copernicus:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 300s -v -run TestCopernicus ./integration/

## test-integration-naturalearth: Run Natural Earth integration test (8-bit RGB + TFW → JPEG)
test-integration-naturalearth:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 300s -v -run TestNaturalEarth ./integration/

## test-integration-esaworldcover: Run ESA WorldCover RGBNIR integration test (16-bit RGBNIR → PNG)
test-integration-esaworldcover:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v -run 'TestESAWorldCover(Preset|Pipeline)$$' ./integration/

## test-integration-esaworldcover-ndvi: Run ESA WorldCover NDVI integration test (single-band → grayscale PNG)
test-integration-esaworldcover-ndvi:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v -run TestESAWorldCoverNDVI ./integration/

## test-integration-esaworldcover-swir: Run ESA WorldCover SWIR integration test (single-band → grayscale PNG)
test-integration-esaworldcover-swir:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 300s -v -run TestESAWorldCoverSWIR ./integration/

## test-integration-esaworldcover-gamma0: Run ESA WorldCover Gamma0 integration test (SAR VV/VH → PNG)
test-integration-esaworldcover-gamma0:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v -run TestESAWorldCoverGamma0 ./integration/

## test-integration-swissimage: Run SWISSIMAGE DOP10 integration test (8-bit RGB LV95 multi-source → JPEG)
test-integration-swissimage:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v -run TestSwissImage ./integration/

## test-integration-all: Download data and run all integration tests
test-integration-all: test-integration-download test-integration-real

# ---------- Code quality ----------

## fmt: Format all Go source files
fmt:
	$(GO) fmt ./...

## vet: Run go vet
vet:
	$(GO) vet ./...

## lint: Run golangci-lint (install: https://golangci-lint.run)
lint:
	golangci-lint run ./...

## tidy: Tidy and verify module dependencies
tidy:
	$(GO) mod tidy
	$(GO) mod verify

## check: Run fmt, vet, and tests in one shot
check: fmt vet test

# ---------- Run / Examples ----------

## run: Build and run with ARGS (e.g. make run ARGS="--verbose integration/testdata/swissimage/ out.pmtiles")
run: build
	./$(OUTPUT) $(ARGS)

## example-all: Run all examples (all formats, full-disk, Natural Earth, Copernicus, ESA WorldCover, and transform)
example-all: example-swissimage-jpeg example-swissimage-png example-swissimage-webp \
             example-swissimage-full-disk-jpeg example-swissimage-full-disk-png example-swissimage-full-disk-webp \
             example-naturalearth-jpeg example-naturalearth-png example-naturalearth-webp \
             example-naturalearth-full-disk-jpeg example-naturalearth-full-disk-png example-naturalearth-full-disk-webp \
             example-copernicus \
             example-esaworldcover example-esaworldcover-ndvi example-esaworldcover-swir example-esaworldcover-gamma0 \
             example-transform example-transform-reencode example-transform-rebuild

# ---------- SWISSIMAGE DOP10 Example (8-bit RGB LV95 multi-source) ----------

## example-swissimage: SWISSIMAGE DOP10 example (8-bit RGB EPSG:2056 → JPEG)
example-swissimage: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-swissimage-$(FORMAT).pmtiles

## example-swissimage-full-disk: SWISSIMAGE example with aggressive disk spilling (1 MB memory limit)
example-swissimage-full-disk: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		--mem-limit 1 \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-swissimage-full-disk-$(FORMAT).pmtiles

## example-swissimage-profile: SWISSIMAGE example with CPU and memory profiling
example-swissimage-profile: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		--cpuprofile $(BUILD_DIR)/cpu.prof \
		--memprofile $(BUILD_DIR)/mem.prof \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-swissimage.pmtiles
	@echo ""
	@echo "Profile files written:"
	@echo "  CPU: $(BUILD_DIR)/cpu.prof"
	@echo "  Mem: $(BUILD_DIR)/mem.prof"
	@echo ""
	@echo "Analyze with:"
	@echo "  go tool pprof -http=:8080 $(BUILD_DIR)/cpu.prof"
	@echo "  go tool pprof -http=:8081 $(BUILD_DIR)/mem.prof"

## pprof-cpu: Open CPU profile in browser (interactive flame graph)
pprof-cpu:
	$(GO) tool pprof -http=:8080 $(BUILD_DIR)/cpu.prof

## pprof-mem: Open memory profile in browser (interactive flame graph)
pprof-mem:
	$(GO) tool pprof -http=:8081 $(BUILD_DIR)/mem.prof

## example-swissimage-jpeg: SWISSIMAGE example with JPEG encoding
example-swissimage-jpeg: FORMAT=jpeg
example-swissimage-jpeg: example-swissimage

## example-swissimage-png: SWISSIMAGE example with PNG encoding
example-swissimage-png: FORMAT=png
example-swissimage-png: example-swissimage

## example-swissimage-webp: SWISSIMAGE example with WebP encoding
example-swissimage-webp: FORMAT=webp
example-swissimage-webp: example-swissimage

## example-swissimage-full-disk-jpeg: SWISSIMAGE full-disk mode with JPEG
example-swissimage-full-disk-jpeg: FORMAT=jpeg
example-swissimage-full-disk-jpeg: example-swissimage-full-disk

## example-swissimage-full-disk-png: SWISSIMAGE full-disk mode with PNG
example-swissimage-full-disk-png: FORMAT=png
example-swissimage-full-disk-png: example-swissimage-full-disk

## example-swissimage-full-disk-webp: SWISSIMAGE full-disk mode with WebP
example-swissimage-full-disk-webp: FORMAT=webp
example-swissimage-full-disk-webp: example-swissimage-full-disk

# ---------- Natural Earth Example (global data with TFW sidecar) ----------

## example-naturalearth: Natural Earth example (global raster with TFW sidecar)
example-naturalearth: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--concurrency $(CONCURRENT) \
		$(NATURALEARTH_DIR)/ $(BUILD_DIR)/example-naturalearth-$(FORMAT).pmtiles

## example-naturalearth-full-disk: Natural Earth example with aggressive disk spilling (1 MB memory limit)
example-naturalearth-full-disk: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--concurrency $(CONCURRENT) \
		--mem-limit 1 \
		$(NATURALEARTH_DIR)/ $(BUILD_DIR)/example-naturalearth-full-disk-$(FORMAT).pmtiles

## example-naturalearth-jpeg: Natural Earth example with JPEG encoding
example-naturalearth-jpeg: FORMAT=jpeg
example-naturalearth-jpeg: example-naturalearth

## example-naturalearth-png: Natural Earth example with PNG encoding
example-naturalearth-png: FORMAT=png
example-naturalearth-png: example-naturalearth

## example-naturalearth-webp: Natural Earth example with WebP encoding
example-naturalearth-webp: FORMAT=webp
example-naturalearth-webp: example-naturalearth

## example-naturalearth-full-disk-jpeg: Natural Earth full-disk mode with JPEG
example-naturalearth-full-disk-jpeg: FORMAT=jpeg
example-naturalearth-full-disk-jpeg: example-naturalearth-full-disk

## example-naturalearth-full-disk-png: Natural Earth full-disk mode with PNG
example-naturalearth-full-disk-png: FORMAT=png
example-naturalearth-full-disk-png: example-naturalearth-full-disk

## example-naturalearth-full-disk-webp: Natural Earth full-disk mode with WebP
example-naturalearth-full-disk-webp: FORMAT=webp
example-naturalearth-full-disk-webp: example-naturalearth-full-disk

# ---------- Copernicus DEM Example (float32 → terrarium) ----------

## example-copernicus: Copernicus DEM example (float32 → terrarium PNG)
example-copernicus: build test-integration-download
	./$(OUTPUT) \
		--format terrarium \
		--tile-size $(TILE_SIZE) \
		--resampling mode \
		--concurrency $(CONCURRENT) \
		$(COPERNICUS_DIR)/ $(BUILD_DIR)/example-copernicus-terrarium.pmtiles

# ---------- ESA WorldCover Example (16-bit RGBNIR → PNG) ----------

## example-esaworldcover: ESA WorldCover S2 RGBNIR example (16-bit 4-band → PNG with auto-detect)
example-esaworldcover: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--resampling-gamma 1.8 \
		--concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_DIR)/ $(BUILD_DIR)/example-esaworldcover-$(FORMAT).pmtiles

## example-esaworldcover-ndvi: ESA WorldCover NDVI example (3-band p90/p50/p10 composite → PNG)
example-esaworldcover-ndvi: build test-integration-download
	./$(OUTPUT) \
		--format png \
		--tile-size $(TILE_SIZE) \
		--concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_NDVI_DIR)/ $(BUILD_DIR)/example-esaworldcover-ndvi.pmtiles

## example-esaworldcover-swir: ESA WorldCover SWIR example (2-band B11/B12 composite → PNG)
example-esaworldcover-swir: build test-integration-download
	./$(OUTPUT) \
		--format png \
		--tile-size $(TILE_SIZE) \
		--bands 1,2,1 \
		--alpha-band -1 \
		--concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_SWIR_DIR)/ $(BUILD_DIR)/example-esaworldcover-swir.pmtiles

## example-esaworldcover-gamma0: ESA WorldCover S1 Gamma0 VV/VH ratio example (SAR → PNG)
example-esaworldcover-gamma0: build test-integration-download
	./$(OUTPUT) \
		--format png \
		--tile-size $(TILE_SIZE) \
		--alpha-band -1 \
		--rescale-range 0,65535 \
		--concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_GAMMA0_DIR)/ $(BUILD_DIR)/example-esaworldcover-gamma0.pmtiles

# ---------- PMTiles Transform Example ----------

## example-transform: Transform passthrough example (copy tiles, no re-encode)
example-transform: build build-transform test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-$(FORMAT).pmtiles
	./$(OUTPUT_TRANSFORM) --verbose \
		$(BUILD_DIR)/example-$(FORMAT).pmtiles $(BUILD_DIR)/example-transform-passthrough.pmtiles

## example-transform-reencode: Transform format conversion example (WebP → PNG)
example-transform-reencode: build build-transform test-integration-download
	./$(OUTPUT) \
		--format webp \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-webp.pmtiles
	./$(OUTPUT_TRANSFORM) --verbose --format png \
		$(BUILD_DIR)/example-webp.pmtiles $(BUILD_DIR)/example-transform-png.pmtiles

## example-transform-rebuild: Transform pyramid rebuild example with extended zoom range
example-transform-rebuild: build build-transform test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-$(FORMAT).pmtiles
	./$(OUTPUT_TRANSFORM) --verbose --rebuild --min-zoom 10 \
		$(BUILD_DIR)/example-$(FORMAT).pmtiles $(BUILD_DIR)/example-transform-rebuild.pmtiles

# ---------- Cross-compilation ----------
# Requires a C cross-compiler (CC) and libwebp built for the target platform.
# Example: CC=x86_64-linux-musl-gcc PKG_CONFIG_PATH=/path/to/linux-amd64/lib/pkgconfig make cross-linux

## cross-linux: Build for Linux amd64
cross-linux: $(BUILD_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 $(CMD)

## cross-linux-arm64: Build for Linux arm64
cross-linux-arm64: $(BUILD_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-arm64 $(CMD)

## cross-darwin: Build for macOS amd64
cross-darwin: $(BUILD_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 $(CMD)

## cross-darwin-arm64: Build for macOS arm64
cross-darwin-arm64: $(BUILD_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 $(CMD)

## cross-all: Build for all supported platforms
cross-all: cross-linux cross-linux-arm64 cross-darwin cross-darwin-arm64

# ---------- Cleanup ----------

## clean: Remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

## clean-all: Remove build artifacts plus Go build/test caches
clean-all: clean
	$(GO) clean -cache -testcache

# ---------- Help ----------

## help: Show this help message
help:
	@echo "Usage: make [target] [VAR=value ...]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | column -t -s ':'
	@echo ""
	@echo "Variables (override with VAR=value):"
	@echo "  FORMAT      Tile encoding: jpeg, png, webp     (default: webp)"
	@echo "  QUALITY     JPEG/WebP quality 1-100             (default: 85)"
	@echo "  MIN_ZOOM    Minimum zoom level                  (default: 14)"
	@echo "  MAX_ZOOM    Maximum zoom level                  (default: 18)"
	@echo "  TILE_SIZE   Output tile size in pixels           (default: 512)"
	@echo "  CONCURRENT  Number of parallel workers           (default: NumCPU)"
	@echo "  MEM_LIMIT   Memory limit in MB for disk spill    (default: 0 = auto)"
	@echo ""
	@echo "Examples:"
	@echo "  make build                           Build geotiff2pmtiles"
	@echo "  make build-transform                 Build pmtransform"
	@echo "  make build-all                       Build both binaries"
	@echo "  make example-all                      Run every example target"
	@echo "  make example-swissimage               SWISSIMAGE DOP10 example (LV95 mosaic)"
	@echo "  make example-swissimage-png           SWISSIMAGE example with PNG"
	@echo "  make example-swissimage-full-disk     SWISSIMAGE full disk spilling (1 MB limit)"
	@echo "  make example-swissimage-profile       SWISSIMAGE example with profiling"
	@echo "  make example-naturalearth              Natural Earth example"
	@echo "  make example-naturalearth-webp         Natural Earth example with WebP"
	@echo "  make example-copernicus               Copernicus DEM example (terrarium)"
	@echo "  make example-esaworldcover            ESA WorldCover RGBNIR example"
	@echo "  make example-esaworldcover-ndvi       ESA WorldCover NDVI example"
	@echo "  make example-esaworldcover-swir       ESA WorldCover SWIR example"
	@echo "  make example-esaworldcover-gamma0     ESA WorldCover Gamma0 SAR example"
	@echo "  make example-transform                Transform example (passthrough)"
	@echo "  make example-transform-reencode       Transform example (WebP → PNG)"
	@echo "  make example-transform-rebuild        Transform example (rebuild pyramid)"
	@echo "  make cross-all                        Cross-compile all platforms"
	@echo "  make run ARGS=\"--verbose $(SWISSIMAGE_DIR)/ o.pmtiles\""
