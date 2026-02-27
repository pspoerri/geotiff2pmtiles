# geotiff2pmtiles Makefile

BINARY           := geotiff2pmtiles
BINARY_TRANSFORM := pmtransform
MODULE           := github.com/pspoerri/geotiff2pmtiles
CMD              := ./cmd/geotiff2pmtiles/
CMD_TRANSFORM    := ./cmd/pmtransform/
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

# Default tile format and quality for demo targets
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

.PHONY: all build build-transform build-all install \
        test test-race test-cover bench \
        test-integration test-integration-download test-integration-real test-integration-all \
        test-integration-copernicus test-integration-naturalearth \
        test-integration-esaworldcover test-integration-esaworldcover-ndvi \
        test-integration-esaworldcover-swir test-integration-esaworldcover-gamma0 \
        test-integration-swissimage \
        lint fmt vet tidy check \
        clean clean-all \
        run demo-all pprof-cpu pprof-mem \
        demo-swissimage demo-swissimage-full-disk demo-swissimage-profile \
        demo-swissimage-jpeg demo-swissimage-png demo-swissimage-webp \
        demo-swissimage-full-disk-jpeg demo-swissimage-full-disk-png demo-swissimage-full-disk-webp \
        demo-tfw demo-tfw-full-disk \
        demo-tfw-jpeg demo-tfw-png demo-tfw-webp \
        demo-tfw-full-disk-jpeg demo-tfw-full-disk-png demo-tfw-full-disk-webp \
        demo-copernicus demo-esaworldcover demo-esaworldcover-ndvi \
        demo-esaworldcover-swir demo-esaworldcover-gamma0 \
        demo-transform demo-transform-reencode demo-transform-rebuild \
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

## build-all: Build both geotiff2pmtiles and pmtransform
build-all: build build-transform

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

# ---------- Run / Demo ----------

## run: Build and run with ARGS (e.g. make run ARGS="--verbose integration/testdata/swissimage/ out.pmtiles")
run: build
	./$(OUTPUT) $(ARGS)

## demo-all: Run all demos (all formats, full-disk, TFW, Copernicus, ESA WorldCover, and transform)
demo-all: demo-swissimage-jpeg demo-swissimage-png demo-swissimage-webp \
          demo-swissimage-full-disk-jpeg demo-swissimage-full-disk-png demo-swissimage-full-disk-webp \
          demo-tfw-jpeg demo-tfw-png demo-tfw-webp \
          demo-tfw-full-disk-jpeg demo-tfw-full-disk-png demo-tfw-full-disk-webp \
          demo-copernicus \
          demo-esaworldcover demo-esaworldcover-ndvi demo-esaworldcover-swir demo-esaworldcover-gamma0 \
          demo-transform demo-transform-reencode demo-transform-rebuild

# ---------- SWISSIMAGE DOP10 Demo (8-bit RGB LV95 multi-source) ----------

## demo-swissimage: Demo with swisstopo SWISSIMAGE DOP10 (8-bit RGB EPSG:2056 → JPEG)
demo-swissimage: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/demo-swissimage-$(FORMAT).pmtiles

## demo-swissimage-full-disk: SWISSIMAGE demo with aggressive disk spilling (1 MB memory limit)
demo-swissimage-full-disk: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		--mem-limit 1 \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/demo-swissimage-full-disk-$(FORMAT).pmtiles

## demo-swissimage-profile: SWISSIMAGE demo with CPU and memory profiling
demo-swissimage-profile: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		--cpuprofile $(BUILD_DIR)/cpu.prof \
		--memprofile $(BUILD_DIR)/mem.prof \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/demo-swissimage.pmtiles
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

## demo-swissimage-jpeg: SWISSIMAGE demo with JPEG encoding
demo-swissimage-jpeg: FORMAT=jpeg
demo-swissimage-jpeg: demo-swissimage

## demo-swissimage-png: SWISSIMAGE demo with PNG encoding
demo-swissimage-png: FORMAT=png
demo-swissimage-png: demo-swissimage

## demo-swissimage-webp: SWISSIMAGE demo with WebP encoding
demo-swissimage-webp: FORMAT=webp
demo-swissimage-webp: demo-swissimage

## demo-swissimage-full-disk-jpeg: SWISSIMAGE full-disk mode with JPEG
demo-swissimage-full-disk-jpeg: FORMAT=jpeg
demo-swissimage-full-disk-jpeg: demo-swissimage-full-disk

## demo-swissimage-full-disk-png: SWISSIMAGE full-disk mode with PNG
demo-swissimage-full-disk-png: FORMAT=png
demo-swissimage-full-disk-png: demo-swissimage-full-disk

## demo-swissimage-full-disk-webp: SWISSIMAGE full-disk mode with WebP
demo-swissimage-full-disk-webp: FORMAT=webp
demo-swissimage-full-disk-webp: demo-swissimage-full-disk

# ---------- TFW Demo (Natural Earth global data) ----------

## demo-tfw: Demo with TFW world-file data (global Natural Earth raster)
demo-tfw: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--concurrency $(CONCURRENT) \
		$(NATURALEARTH_DIR)/ $(BUILD_DIR)/demo-tfw-$(FORMAT).pmtiles

## demo-tfw-full-disk: TFW demo with aggressive disk spilling (1 MB memory limit)
demo-tfw-full-disk: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--concurrency $(CONCURRENT) \
		--mem-limit 1 \
		$(NATURALEARTH_DIR)/ $(BUILD_DIR)/demo-tfw-full-disk-$(FORMAT).pmtiles

## demo-tfw-jpeg: TFW demo with JPEG encoding
demo-tfw-jpeg: FORMAT=jpeg
demo-tfw-jpeg: demo-tfw

## demo-tfw-png: TFW demo with PNG encoding
demo-tfw-png: FORMAT=png
demo-tfw-png: demo-tfw

## demo-tfw-webp: TFW demo with WebP encoding
demo-tfw-webp: FORMAT=webp
demo-tfw-webp: demo-tfw

## demo-tfw-full-disk-jpeg: TFW full-disk mode with JPEG
demo-tfw-full-disk-jpeg: FORMAT=jpeg
demo-tfw-full-disk-jpeg: demo-tfw-full-disk

## demo-tfw-full-disk-png: TFW full-disk mode with PNG
demo-tfw-full-disk-png: FORMAT=png
demo-tfw-full-disk-png: demo-tfw-full-disk

## demo-tfw-full-disk-webp: TFW full-disk mode with WebP
demo-tfw-full-disk-webp: FORMAT=webp
demo-tfw-full-disk-webp: demo-tfw-full-disk

# ---------- Copernicus DEM Demo (float32 → terrarium) ----------

## demo-copernicus: Demo with Copernicus DEM (float32 → terrarium PNG)
demo-copernicus: build test-integration-download
	./$(OUTPUT) \
		--format terrarium \
		--tile-size $(TILE_SIZE) \
		--max-zoom 10 \
		--concurrency $(CONCURRENT) \
		$(COPERNICUS_DIR)/ $(BUILD_DIR)/demo-copernicus-terrarium.pmtiles

# ---------- ESA WorldCover Demo (16-bit RGBNIR → PNG) ----------

## demo-esaworldcover: Demo with ESA WorldCover S2 RGBNIR (16-bit 4-band → PNG with auto-detect)
demo-esaworldcover: build test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom 9 \
		--concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_DIR)/ $(BUILD_DIR)/demo-esaworldcover-$(FORMAT).pmtiles

## demo-esaworldcover-ndvi: Demo with ESA WorldCover NDVI (3-band p90/p50/p10 composite → PNG)
demo-esaworldcover-ndvi: build test-integration-download
	./$(OUTPUT) \
		--format png \
		--tile-size $(TILE_SIZE) \
		--max-zoom 9 \
		--concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_NDVI_DIR)/ $(BUILD_DIR)/demo-esaworldcover-ndvi.pmtiles

## demo-esaworldcover-swir: Demo with ESA WorldCover SWIR (2-band B11/B12 composite → PNG)
demo-esaworldcover-swir: build test-integration-download
	./$(OUTPUT) \
		--format png \
		--tile-size $(TILE_SIZE) \
		--max-zoom 9 \
		--bands 1,2,1 \
		--alpha-band -1 \
		--concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_SWIR_DIR)/ $(BUILD_DIR)/demo-esaworldcover-swir.pmtiles

## demo-esaworldcover-gamma0: Demo with ESA WorldCover S1 Gamma0 VV/VH ratio (SAR → PNG)
demo-esaworldcover-gamma0: build test-integration-download
	./$(OUTPUT) \
		--format png \
		--tile-size $(TILE_SIZE) \
		--max-zoom 9 \
		--alpha-band -1 \
		--rescale-range 0,65535 \
		--concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_GAMMA0_DIR)/ $(BUILD_DIR)/demo-esaworldcover-gamma0.pmtiles

# ---------- PMTiles Transform Demo ----------

## demo-transform: Demo passthrough (copy tiles, no re-encode)
demo-transform: build build-transform test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/demo-$(FORMAT).pmtiles
	./$(OUTPUT_TRANSFORM) --verbose \
		$(BUILD_DIR)/demo-$(FORMAT).pmtiles $(BUILD_DIR)/demo-transform-passthrough.pmtiles

## demo-transform-reencode: Demo format conversion (WebP → PNG)
demo-transform-reencode: build build-transform test-integration-download
	./$(OUTPUT) \
		--format webp \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/demo-webp.pmtiles
	./$(OUTPUT_TRANSFORM) --verbose --format png \
		$(BUILD_DIR)/demo-webp.pmtiles $(BUILD_DIR)/demo-transform-png.pmtiles

## demo-transform-rebuild: Demo pyramid rebuild with extended zoom range
demo-transform-rebuild: build build-transform test-integration-download
	./$(OUTPUT) \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/demo-$(FORMAT).pmtiles
	./$(OUTPUT_TRANSFORM) --verbose --rebuild --min-zoom 10 \
		$(BUILD_DIR)/demo-$(FORMAT).pmtiles $(BUILD_DIR)/demo-transform-rebuild.pmtiles

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
	@echo "  make demo-all                        Run every demo target"
	@echo "  make demo-swissimage                  SWISSIMAGE DOP10 demo (LV95 mosaic)"
	@echo "  make demo-swissimage-png              SWISSIMAGE demo with PNG"
	@echo "  make demo-swissimage-full-disk        SWISSIMAGE full disk spilling (1 MB limit)"
	@echo "  make demo-swissimage-profile          SWISSIMAGE demo with profiling"
	@echo "  make demo-tfw                         Natural Earth TFW demo"
	@echo "  make demo-tfw-webp                    Natural Earth TFW demo with WebP"
	@echo "  make demo-copernicus                  Copernicus DEM demo (terrarium)"
	@echo "  make demo-esaworldcover               ESA WorldCover RGBNIR demo"
	@echo "  make demo-esaworldcover-ndvi          ESA WorldCover NDVI demo"
	@echo "  make demo-esaworldcover-swir          ESA WorldCover SWIR demo"
	@echo "  make demo-esaworldcover-gamma0        ESA WorldCover Gamma0 SAR demo"
	@echo "  make demo-transform                   Transform demo (passthrough)"
	@echo "  make demo-transform-reencode          Transform demo (WebP → PNG)"
	@echo "  make demo-transform-rebuild           Transform demo (rebuild pyramid)"
	@echo "  make cross-all                        Cross-compile all platforms"
	@echo "  make run ARGS=\"--verbose $(SWISSIMAGE_DIR)/ o.pmtiles\""
