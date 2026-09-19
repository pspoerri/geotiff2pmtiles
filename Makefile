# geotiff2pmtiles Makefile

BINARY           := geotiff2pmtiles
BINARY_TRANSFORM := pmtransform
BINARY_CHECK     := checkpmtiles
BINARY_HEADER    := pmheader
MODULE           := github.com/pspoerri/geotiff2pmtiles
CMD              := ./cmd/geotiff2pmtiles/
CMD_TRANSFORM    := ./cmd/pmtransform/
CMD_CHECK        := ./cmd/checkpmtiles/
CMD_HEADER       := ./cmd/pmheader/
BUILD_DIR        := dist
GO               := go
GOFLAGS          :=
LDFLAGS          :=
# Set CGO=0 to build without libwebp (WebP encoding is then lossless only).
CGO              ?= 1

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    += -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

# Native Windows build (MSYS2 UCRT64 / CLANGARM64): name the outputs .exe and link
# libwebp statically so the binary has no DLL dependencies beyond the system ones.
ifeq ($(OS),Windows_NT)
EXE        := .exe
LDFLAGS    += -extldflags=-static
endif

OUTPUT           := $(BUILD_DIR)/$(BINARY)$(EXE)
OUTPUT_TRANSFORM := $(BUILD_DIR)/$(BINARY_TRANSFORM)$(EXE)
OUTPUT_CHECK     := $(BUILD_DIR)/$(BINARY_CHECK)$(EXE)
OUTPUT_HEADER    := $(BUILD_DIR)/$(BINARY_HEADER)$(EXE)

# Default tile format and quality for example targets
FORMAT     ?= webp
QUALITY    ?= 85
MAX_ZOOM   ?= 18
TILE_SIZE  ?= 512
CONCURRENT ?= $(shell nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)

# Flags shared by the example targets
EXAMPLE_FLAGS = --format $(FORMAT) --quality $(QUALITY) --tile-size $(TILE_SIZE) --concurrency $(CONCURRENT)

# Integration testdata directories (each dataset in its own folder for CLI input)
TESTDATA_DIR     := integration/testdata
COPERNICUS_DIR   := $(TESTDATA_DIR)/copernicus
COPERNICUS_ZSTD_DIR := $(TESTDATA_DIR)/copernicus-zstd
NATURALEARTH_DIR := $(TESTDATA_DIR)/naturalearth
ESAWORLDCOVER_DIR       := $(TESTDATA_DIR)/esaworldcover
ESAWORLDCOVER_NDVI_DIR  := $(TESTDATA_DIR)/esaworldcover-ndvi
ESAWORLDCOVER_SWIR_DIR  := $(TESTDATA_DIR)/esaworldcover-swir
ESAWORLDCOVER_GAMMA0_DIR := $(TESTDATA_DIR)/esaworldcover-gamma0
SWISSIMAGE_DIR           := $(TESTDATA_DIR)/swissimage

.PHONY: \
  all build build-transform build-check build-header build-all install test test-race \
  test-cover bench test-integration test-integration-download test-integration-real \
  test-integration-copernicus test-integration-copernicus-zstd \
  test-integration-naturalearth test-integration-esaworldcover \
  test-integration-esaworldcover-ndvi test-integration-esaworldcover-swir \
  test-integration-esaworldcover-gamma0 test-integration-swissimage \
  test-integration-all fmt vet lint tidy check run example-all example-swissimage \
  example-swissimage-full-disk example-swissimage-profile pprof-cpu pprof-mem \
  example-naturalearth example-naturalearth-full-disk example-copernicus \
  example-copernicus-zstd example-esaworldcover example-esaworldcover-ndvi \
  example-esaworldcover-swir example-esaworldcover-gamma0 example-transform \
  example-transform-reencode example-transform-rebuild cross-linux cross-linux-arm64 \
  cross-darwin cross-darwin-arm64 cross-windows cross-windows-arm64 cross-all clean \
  clean-all help

.DEFAULT_GOAL := help

##@ Build

## all: Build all binaries
all: build-all

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

## build: Build geotiff2pmtiles (needs libwebp; CGO=0 builds without it, WebP lossless only)
build: $(BUILD_DIR)
	CGO_ENABLED=$(CGO) $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT) $(CMD)

## build-transform: Build pmtransform
build-transform: $(BUILD_DIR)
	CGO_ENABLED=$(CGO) $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT_TRANSFORM) $(CMD_TRANSFORM)

## build-check: Build checkpmtiles (archive validator)
build-check: $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT_CHECK) $(CMD_CHECK)

## build-header: Build pmheader (header/metadata patching)
build-header: $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT_HEADER) $(CMD_HEADER)

## build-all: Build all four tools into dist/
build-all: build build-transform build-check build-header

## install: Install geotiff2pmtiles to GOPATH/bin
install:
	CGO_ENABLED=$(CGO) $(GO) install $(GOFLAGS) -ldflags "$(LDFLAGS)" $(CMD)

## run: Build and run geotiff2pmtiles, e.g. make run ARGS="--verbose in/ out.pmtiles"
run: build
	./$(OUTPUT) $(ARGS)

## clean: Remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

## clean-all: Remove build artifacts plus Go build/test caches
clean-all: clean
	$(GO) clean -cache -testcache

##@ Test and code quality

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

##@ Integration tests (real-data tests skip themselves until the data is downloaded)

## test-integration: Synthetic tests only (no download, ~8s)
test-integration:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 120s -v ./integration/

## test-integration-download: Download the real test data (~1.2 GB)
test-integration-download:
	bash integration/testdata/download.sh

## test-integration-real: All integration tests (synthetic + downloaded data)
test-integration-real:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v ./integration/

## test-integration-copernicus: Copernicus DEM (float32 → terrarium)
test-integration-copernicus:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 300s -v -run TestCopernicus ./integration/

## test-integration-copernicus-zstd: Copernicus DEM ZSTD-compressed COG
test-integration-copernicus-zstd:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 300s -v -run TestCopernicusZSTD ./integration/

## test-integration-naturalearth: Natural Earth (8-bit RGB + TFW → JPEG)
test-integration-naturalearth:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 300s -v -run TestNaturalEarth ./integration/

## test-integration-esaworldcover: ESA WorldCover RGBNIR (16-bit RGBNIR → PNG)
test-integration-esaworldcover:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v -run 'TestESAWorldCover(Preset|Pipeline)$$' ./integration/

## test-integration-esaworldcover-ndvi: ESA WorldCover NDVI (8-bit 3-band → PNG)
test-integration-esaworldcover-ndvi:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v -run TestESAWorldCoverNDVI ./integration/

## test-integration-esaworldcover-swir: ESA WorldCover SWIR (8-bit 2-band → PNG)
test-integration-esaworldcover-swir:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 300s -v -run TestESAWorldCoverSWIR ./integration/

## test-integration-esaworldcover-gamma0: ESA WorldCover Gamma0 (SAR VV/VH → PNG)
test-integration-esaworldcover-gamma0:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v -run TestESAWorldCoverGamma0 ./integration/

## test-integration-swissimage: SWISSIMAGE DOP10 (8-bit RGB LV95 multi-source → JPEG)
test-integration-swissimage:
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 600s -v -run TestSwissImage ./integration/

## test-integration-all: Download, then run everything
test-integration-all: test-integration-download test-integration-real

##@ Examples (download test data, convert it to dist/example-*.pmtiles; FORMAT=jpeg|png|webp)

## example-all: Every example below (SWISSIMAGE and Natural Earth in all three formats)
example-all:
	for f in jpeg png webp; do \
		$(MAKE) example-swissimage example-swissimage-full-disk \
		        example-naturalearth example-naturalearth-full-disk FORMAT=$$f || exit 1; \
	done
	$(MAKE) example-copernicus example-copernicus-zstd \
	        example-esaworldcover example-esaworldcover-ndvi example-esaworldcover-swir example-esaworldcover-gamma0 \
	        example-transform example-transform-reencode example-transform-rebuild

## example-swissimage: SWISSIMAGE DOP10 (8-bit RGB, EPSG:2056 mosaic)
example-swissimage: build test-integration-download
	./$(OUTPUT) $(EXAMPLE_FLAGS) --max-zoom $(MAX_ZOOM) \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-swissimage-$(FORMAT).pmtiles

## example-swissimage-full-disk: Same with a 1 MB memory limit, forcing disk spill
example-swissimage-full-disk: build test-integration-download
	./$(OUTPUT) $(EXAMPLE_FLAGS) --max-zoom $(MAX_ZOOM) --mem-limit 1 \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-swissimage-full-disk-$(FORMAT).pmtiles

## example-naturalearth: Natural Earth (global raster with TFW sidecar)
example-naturalearth: build test-integration-download
	./$(OUTPUT) $(EXAMPLE_FLAGS) \
		$(NATURALEARTH_DIR)/ $(BUILD_DIR)/example-naturalearth-$(FORMAT).pmtiles

## example-naturalearth-full-disk: Same with a 1 MB memory limit, forcing disk spill
example-naturalearth-full-disk: build test-integration-download
	./$(OUTPUT) $(EXAMPLE_FLAGS) --mem-limit 1 \
		$(NATURALEARTH_DIR)/ $(BUILD_DIR)/example-naturalearth-full-disk-$(FORMAT).pmtiles

## example-copernicus: Copernicus DEM (float32 → terrarium)
example-copernicus: build test-integration-download
	./$(OUTPUT) --format terrarium --tile-size $(TILE_SIZE) --resampling mode --concurrency $(CONCURRENT) \
		$(COPERNICUS_DIR)/ $(BUILD_DIR)/example-copernicus-terrarium.pmtiles

## example-copernicus-zstd: Same DEM as a ZSTD COG (download needs GDAL)
example-copernicus-zstd: build test-integration-download
	./$(OUTPUT) --format terrarium --tile-size $(TILE_SIZE) --resampling mode --concurrency $(CONCURRENT) \
		$(COPERNICUS_ZSTD_DIR)/ $(BUILD_DIR)/example-copernicus-zstd-terrarium.pmtiles

## example-esaworldcover: ESA WorldCover RGBNIR (16-bit 4-band, auto-detected)
example-esaworldcover: build test-integration-download
	./$(OUTPUT) $(EXAMPLE_FLAGS) --resampling-gamma 1.8 \
		$(ESAWORLDCOVER_DIR)/ $(BUILD_DIR)/example-esaworldcover-$(FORMAT).pmtiles

## example-esaworldcover-ndvi: ESA WorldCover NDVI (8-bit 3-band → PNG)
example-esaworldcover-ndvi: build test-integration-download
	./$(OUTPUT) --format png --tile-size $(TILE_SIZE) --concurrency $(CONCURRENT) \
		$(ESAWORLDCOVER_NDVI_DIR)/ $(BUILD_DIR)/example-esaworldcover-ndvi.pmtiles

## example-esaworldcover-swir: ESA WorldCover SWIR (8-bit 2-band → PNG)
example-esaworldcover-swir: build test-integration-download
	./$(OUTPUT) --format png --tile-size $(TILE_SIZE) --concurrency $(CONCURRENT) \
		--bands 1,2,1 --alpha-band -1 \
		$(ESAWORLDCOVER_SWIR_DIR)/ $(BUILD_DIR)/example-esaworldcover-swir.pmtiles

## example-esaworldcover-gamma0: ESA WorldCover Gamma0 (16-bit SAR VV/VH → PNG)
example-esaworldcover-gamma0: build test-integration-download
	./$(OUTPUT) --format png --tile-size $(TILE_SIZE) --concurrency $(CONCURRENT) \
		--alpha-band -1 --rescale-range 0,65535 \
		$(ESAWORLDCOVER_GAMMA0_DIR)/ $(BUILD_DIR)/example-esaworldcover-gamma0.pmtiles

# The transform examples run on the output of example-swissimage.

## example-transform: pmtransform passthrough (copy tiles, no re-encode)
example-transform: example-swissimage build-transform
	./$(OUTPUT_TRANSFORM) --verbose \
		$(BUILD_DIR)/example-swissimage-$(FORMAT).pmtiles $(BUILD_DIR)/example-transform-passthrough.pmtiles

## example-transform-reencode: pmtransform re-encode to PNG
example-transform-reencode: example-swissimage build-transform
	./$(OUTPUT_TRANSFORM) --verbose --format png \
		$(BUILD_DIR)/example-swissimage-$(FORMAT).pmtiles $(BUILD_DIR)/example-transform-png.pmtiles

## example-transform-rebuild: pmtransform pyramid rebuild down to zoom 10
example-transform-rebuild: example-swissimage build-transform
	./$(OUTPUT_TRANSFORM) --verbose --rebuild --min-zoom 10 \
		$(BUILD_DIR)/example-swissimage-$(FORMAT).pmtiles $(BUILD_DIR)/example-transform-rebuild.pmtiles

##@ Profiling

## example-swissimage-profile: Run the SWISSIMAGE example, writing dist/cpu.prof and dist/mem.prof
example-swissimage-profile: build test-integration-download
	./$(OUTPUT) $(EXAMPLE_FLAGS) --max-zoom $(MAX_ZOOM) \
		--cpuprofile $(BUILD_DIR)/cpu.prof --memprofile $(BUILD_DIR)/mem.prof \
		$(SWISSIMAGE_DIR)/ $(BUILD_DIR)/example-swissimage.pmtiles
	@echo "Profiles written to $(BUILD_DIR)/cpu.prof and $(BUILD_DIR)/mem.prof — view with: make pprof-cpu / make pprof-mem"

## pprof-cpu: Open the CPU profile in a browser
pprof-cpu:
	$(GO) tool pprof -http=:8080 $(BUILD_DIR)/cpu.prof

## pprof-mem: Open the memory profile in a browser
pprof-mem:
	$(GO) tool pprof -http=:8081 $(BUILD_DIR)/mem.prof

##@ Cross-compilation (geotiff2pmtiles only, into dist/)

# Requires a C cross-compiler (CC) and libwebp built for the target platform.
# The Windows targets build with CGO_ENABLED=0 instead, so they need no toolchain
# but WebP encoding is lossless only.
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

## cross-windows: Build for Windows amd64 (CGO_ENABLED=0: WebP lossless only)
cross-windows: $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe $(CMD)

## cross-windows-arm64: Build for Windows arm64 (CGO_ENABLED=0: WebP lossless only)
cross-windows-arm64: $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-windows-arm64.exe $(CMD)

## cross-all: All six of the above
cross-all: cross-linux cross-linux-arm64 cross-darwin cross-darwin-arm64 cross-windows cross-windows-arm64

# help is the default goal. Sections come from '##@' lines, targets from '## name: text'.
help:
	@echo "Usage: make <target> [VAR=value ...]"
	@echo ""
	@echo "Common"
	@echo "  make build-all                         Build all tools into dist/"
	@echo "  make build CGO=0                       Build without libwebp"
	@echo "  make check                             fmt + vet + tests, before committing"
	@echo "  make test-integration                  End-to-end tests, no download needed"
	@echo "  make example-swissimage FORMAT=png     Download sample data and convert it"
	@awk '/^##@ /{printf "\n%s\n", substr($$0,5)} \
	      /^## /{t=substr($$2,1,length($$2)-1); sub(/^## [^ ]+ /,""); printf "  %-38s %s\n", t, $$0}' $(MAKEFILE_LIST)
	@echo ""
	@echo "Variables"
	@echo "  CGO=1         0 builds without libwebp (WebP lossless only)"
	@echo "  ARGS=         Arguments for 'make run'"
	@echo "  FORMAT=webp   Example tile format: jpeg, png, webp"
	@echo "  QUALITY=85    Example JPEG/WebP quality"
	@echo "  TILE_SIZE=512 Example tile size in pixels"
	@echo "  MAX_ZOOM=18   SWISSIMAGE example maximum zoom"
	@echo "  CONCURRENT=   Example worker count (default: number of CPUs)"
