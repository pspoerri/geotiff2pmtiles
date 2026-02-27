#!/usr/bin/env bash
#
# Downloads real satellite/raster test data for integration tests.
# Usage: bash integration/testdata/download.sh
#
# Files are cached: re-running skips already-downloaded files.
# Each dataset lives in its own subdirectory so it can be used as a
# CLI input directory for geotiff2pmtiles.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- Copernicus DEM GLO-30 (float32, EPSG:4326, ~8 MB) ---
DEM_DIR="$SCRIPT_DIR/copernicus"
DEM_URL="https://copernicus-dem-30m.s3.amazonaws.com/Copernicus_DSM_COG_10_N46_00_E008_00_DEM/Copernicus_DSM_COG_10_N46_00_E008_00_DEM.tif"
DEM_FILE="copernicus_dem_n46_e008.tif"

# --- Natural Earth Hypsometric Raster (8-bit RGB, EPSG:4326 via TFW, ~200 MB) ---
NE_DIR="$SCRIPT_DIR/naturalearth"
NE_URL="https://naciscdn.org/naturalearth/10m/raster/HYP_HR_SR_OB_DR.zip"
NE_ZIP="HYP_HR_SR_OB_DR.zip"
NE_FILE="HYP_HR_SR_OB_DR.tif"

# --- ESA WorldCover S2 RGBNIR composite (16-bit 4-band, EPSG:4326, ~455 MB) ---
ESA_RGBNIR_DIR="$SCRIPT_DIR/esaworldcover"
ESA_RGBNIR_URL="https://esa-worldcover-s2.s3.eu-central-1.amazonaws.com/rgbnir/2021/N00/ESA_WorldCover_10m_2021_v200_N00E009_S2RGBNIR.tif"
ESA_RGBNIR_FILE="ESA_WorldCover_10m_2021_v200_N00E009_S2RGBNIR.tif"

# --- ESA WorldCover S2 NDVI (single-band, EPSG:4326, ~168 MB) ---
ESA_NDVI_DIR="$SCRIPT_DIR/esaworldcover-ndvi"
ESA_NDVI_URL="https://esa-worldcover-s2.s3.eu-central-1.amazonaws.com/ndvi/2021/N00/ESA_WorldCover_10m_2021_v200_N00E009_NDVI.tif"
ESA_NDVI_FILE="ESA_WorldCover_10m_2021_v200_N00E009_NDVI.tif"

# --- ESA WorldCover S2 SWIR (single-band, EPSG:4326, ~20 MB) ---
ESA_SWIR_DIR="$SCRIPT_DIR/esaworldcover-swir"
ESA_SWIR_URL="https://esa-worldcover-s2.s3.eu-central-1.amazonaws.com/swir/2021/N00/ESA_WorldCover_10m_2021_v200_N00E009_SWIR.tif"
ESA_SWIR_FILE="ESA_WorldCover_10m_2021_v200_N00E009_SWIR.tif"

# --- ESA WorldCover S1 VV/VH ratio / gamma0 (SAR, EPSG:4326, ~346 MB) ---
ESA_GAMMA0_DIR="$SCRIPT_DIR/esaworldcover-gamma0"
ESA_GAMMA0_URL="https://esa-worldcover-s1.s3.eu-central-1.amazonaws.com/vvvhratio/2021/N00/ESA_WorldCover_10m_2021_v200_N00E009_S1VVVHratio.tif"
ESA_GAMMA0_FILE="ESA_WorldCover_10m_2021_v200_N00E009_S1VVVHratio.tif"

download_file() {
    local url="$1"
    local output="$2"
    local description="$3"

    if [ -f "$output" ]; then
        echo "✓ $description already exists: $output"
        return 0
    fi

    mkdir -p "$(dirname "$output")"
    echo "⬇ Downloading $description..."
    echo "  URL: $url"
    curl -fSL --progress-bar -o "$output.tmp" "$url"
    mv "$output.tmp" "$output"
    echo "✓ Downloaded: $output ($(du -h "$output" | cut -f1))"
}

# Download Copernicus DEM
download_file "$DEM_URL" "$DEM_DIR/$DEM_FILE" "Copernicus DEM GLO-30 (N46 E008)"

# Download and extract Natural Earth
download_file "$NE_URL" "$NE_DIR/$NE_ZIP" "Natural Earth Hypsometric Raster"

if [ ! -f "$NE_DIR/$NE_FILE" ]; then
    echo "📦 Extracting Natural Earth raster..."
    unzip -o "$NE_DIR/$NE_ZIP" "$NE_FILE" "$(basename "$NE_FILE" .tif).tfw" -d "$NE_DIR" 2>/dev/null || \
    unzip -o "$NE_DIR/$NE_ZIP" -d "$NE_DIR" 2>/dev/null
    if [ ! -f "$NE_DIR/$NE_FILE" ]; then
        echo "ERROR: Expected $NE_FILE after extraction"
        exit 1
    fi
    echo "✓ Extracted: $NE_DIR/$NE_FILE"
fi

# Download ESA WorldCover datasets
download_file "$ESA_RGBNIR_URL" "$ESA_RGBNIR_DIR/$ESA_RGBNIR_FILE" "ESA WorldCover S2 RGBNIR (N00 E009)"
download_file "$ESA_NDVI_URL" "$ESA_NDVI_DIR/$ESA_NDVI_FILE" "ESA WorldCover S2 NDVI (N00 E009)"
download_file "$ESA_SWIR_URL" "$ESA_SWIR_DIR/$ESA_SWIR_FILE" "ESA WorldCover S2 SWIR (N00 E009)"
download_file "$ESA_GAMMA0_URL" "$ESA_GAMMA0_DIR/$ESA_GAMMA0_FILE" "ESA WorldCover S1 Gamma0 VV/VH ratio (N00 E009)"

echo ""
echo "All test data ready in: $SCRIPT_DIR"
find "$SCRIPT_DIR" -name '*.tif' -exec ls -lh {} \;
