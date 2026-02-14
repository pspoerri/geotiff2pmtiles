# geotiff2pmtiles Makefile

BINARY     := geotiff2pmtiles
MODULE     := github.com/pspoerri/geotiff2pmtiles
CMD        := ./cmd/geotiff2pmtiles/
BUILD_DIR  := dist
GO         := go
GOFLAGS    :=
LDFLAGS    :=

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    += -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

OUTPUT     := $(BUILD_DIR)/$(BINARY)

# Default tile format and quality for demo targets
FORMAT     ?= webp
QUALITY    ?= 85
MIN_ZOOM   ?= 14
MAX_ZOOM   ?= 16
TILE_SIZE  ?= 512
CONCURRENT ?= $(shell nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)

.PHONY: all build install \
        test test-race test-cover bench \
        lint fmt vet tidy check \
        clean clean-all \
        run demo demo-profile pprof-cpu pprof-mem \
        cross-linux cross-linux-arm64 cross-darwin cross-darwin-arm64 cross-all \
        help

## all: Build the binary (default target)
all: build

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

## build: Compile the binary (pure Go, no CGo)
build: $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT) $(CMD)

## install: Install to $GOPATH/bin
install:
	CGO_ENABLED=0 $(GO) install $(GOFLAGS) -ldflags "$(LDFLAGS)" $(CMD)

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

## run: Build and run with ARGS (e.g. make run ARGS="--verbose data/ out.pmtiles")
run: build
	./$(OUTPUT) $(ARGS)

## demo: Build and run a demonstration with the sample data directory
demo: build
	./$(OUTPUT) --verbose \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		data/ $(BUILD_DIR)/demo-$(FORMAT).pmtiles

## demo-profile: Run demo with CPU and memory profiling
demo-profile: build
	./$(OUTPUT) --verbose \
		--format $(FORMAT) \
		--quality $(QUALITY) \
		--tile-size $(TILE_SIZE) \
		--max-zoom $(MAX_ZOOM) \
		--concurrency $(CONCURRENT) \
		--cpuprofile $(BUILD_DIR)/cpu.prof \
		--memprofile $(BUILD_DIR)/mem.prof \
		data/ $(BUILD_DIR)/demo.pmtiles
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

## demo-jpeg: Demo with JPEG encoding (default quality)
demo-jpeg: FORMAT=jpeg
demo-jpeg: demo

## demo-png: Demo with PNG encoding
demo-png: FORMAT=png
demo-png: demo

## demo-webp: Demo with WebP encoding
demo-webp: FORMAT=webp
demo-webp: demo

# ---------- Cross-compilation ----------

## cross-linux: Build for Linux amd64
cross-linux: $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 $(CMD)

## cross-linux-arm64: Build for Linux arm64
cross-linux-arm64: $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-arm64 $(CMD)

## cross-darwin: Build for macOS amd64
cross-darwin: $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 \
		$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 $(CMD)

## cross-darwin-arm64: Build for macOS arm64
cross-darwin-arm64: $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
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
	@echo "  FORMAT      Tile encoding: jpeg, png, webp     (default: jpeg)"
	@echo "  QUALITY     JPEG/WebP quality 1-100             (default: 85)"
	@echo "  MIN_ZOOM    Minimum zoom level                  (default: 14)"
	@echo "  MAX_ZOOM    Maximum zoom level                  (default: 20)"
	@echo "  TILE_SIZE   Output tile size in pixels           (default: 256)"
	@echo "  CONCURRENT  Number of parallel workers           (default: NumCPU)"
	@echo ""
	@echo "Examples:"
	@echo "  make build                           Build the binary"
	@echo "  make demo MIN_ZOOM=16 MAX_ZOOM=18    Run demo at zoom 16-18"
	@echo "  make demo-png QUALITY=100             Run demo with PNG"
	@echo "  make cross-all                        Cross-compile all platforms"
	@echo "  make run ARGS=\"--verbose data/ o.pmtiles\""
