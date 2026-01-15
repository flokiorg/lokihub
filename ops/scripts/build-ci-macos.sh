#!/bin/bash
set -e

# Expected environment variables:
# TAG: version tag

if [ -z "$TAG" ]; then
    if [ -f "VERSION" ]; then
        TAG=$(cat VERSION)
    else 
        echo "TAG environment variable is required."
        exit 1
    fi
fi

# Use BUILD_TAG for internal version string if provided, otherwise default to TAG
VERSION_STRING="${BUILD_TAG:-$TAG}"

echo "macOS Build Script Started. TAG=$TAG"
mkdir -p ops/bin

# -----------------------------------------------------------------------------
# 1. Setup
# -----------------------------------------------------------------------------
# Assume Go, Node, Wails are installed or setup by Workflow steps/host.
# Check for create-dmg
if ! command -v create-dmg &> /dev/null; then
    echo "create-dmg could not be found, installing..."
    brew install create-dmg
fi

rm -rf build
mkdir -p build
cp ops/appicon.png build/ || true
if [ -d "ops/darwin" ]; then cp -r ops/darwin build/; fi

# -----------------------------------------------------------------------------
# 1.5 Build Frontend (HTTP)
# -----------------------------------------------------------------------------
echo "--- Building Frontend (HTTP) ---"
cd frontend
yarn install
yarn build:http
cd ..

# -----------------------------------------------------------------------------
# 2. Build HTTP Server (Universal)
# -----------------------------------------------------------------------------

# -----------------------------------------------------------------------------
# 2. Build HTTP Server (Universal)
# -----------------------------------------------------------------------------

build_http() {
    local OUTPUT_NAME="lokihub-http-macos"
    local TARGET_BINARY_NAME="lokihub"
    local ARCHIVE_NAME="lokihub-server-macos-${TAG}"
    
    echo "Building HTTP Server (Universal)..."
    
    # Force CGO enabled and set CC for cross-compilation
    echo "Building AMD64 slice..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="clang -arch x86_64" \
        go build -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/version.Tag=${VERSION_STRING}'" \
        -o "ops/bin/lokihub-amd64" ./cmd/http

    echo "Building ARM64 slice..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CC="clang -arch arm64" \
        go build -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/version.Tag=${VERSION_STRING}'" \
        -o "ops/bin/lokihub-arm64" ./cmd/http
    
    echo "Creating Universal Binary..."
    lipo -create -output "ops/bin/${OUTPUT_NAME}" "ops/bin/lokihub-amd64" "ops/bin/lokihub-arm64"
    rm "ops/bin/lokihub-amd64" "ops/bin/lokihub-arm64"

    # Package
    pushd ops/bin > /dev/null
    mv "$OUTPUT_NAME" "$TARGET_BINARY_NAME"
    zip -q "${ARCHIVE_NAME}.zip" "$TARGET_BINARY_NAME"
    rm "$TARGET_BINARY_NAME"
    popd > /dev/null
}

# -----------------------------------------------------------------------------
# 3. Build Desktop (Universal)
# -----------------------------------------------------------------------------

build_macos_desktop() {
    local BASENAME="lokihub-desktop-macos"
    local ARCHIVE_NAME="lokihub-desktop-macos-${TAG}"

    echo "Building macOS Desktop (Universal)..."
    
    # 1. Enforce Clean Build
    rm -rf build/bin

    # Wails universal build can be tricky with CGO constraints.
    # It is safer to build two native slices and lipo them if the automated 'darwin/universal' fails CGO.
    # However, Wails 'darwin/universal' flag should handle it IF we setup environment correctly.
    # But since we saw CGO errors, let's try explicit manual universal build for robustness.

    echo "Building Desktop AMD64 slice..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="clang -arch x86_64" \
    wails build -platform "darwin/amd64" -tags wails -trimpath \
            -ldflags "-s -w -X 'github.com/flokiorg/lokihub/pkg/version.Tag=${VERSION_STRING}'" \
            -o "${BASENAME}-amd64" -clean
    
    # Wails might output "Lokihub.app" instead of the target name
    if [ -d "build/bin/Lokihub.app" ]; then
        mv "build/bin/Lokihub.app" "build/bin/${BASENAME}-amd64.app"
    fi

    echo "Building Desktop ARM64 slice..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CC="clang -arch arm64" \
    wails build -platform "darwin/arm64" -tags wails -trimpath \
            -ldflags "-s -w -X 'github.com/flokiorg/lokihub/pkg/version.Tag=${VERSION_STRING}'" \
            -o "${BASENAME}-arm64"
            
    # Rename again for ARM64
    if [ -d "build/bin/Lokihub.app" ]; then
        mv "build/bin/Lokihub.app" "build/bin/${BASENAME}-arm64.app"
    fi
            
    echo "Debugging: Contents of build/bin:"
    ls -R build/bin
            
    # Lipo the binaries inside the app bundles? No, Wails produces a .app.
    # We need to construct a Universal .app from the two slices.
    
    # Path to binaries
    local APP_AMD64="build/bin/${BASENAME}-amd64.app"
    local APP_ARM64="build/bin/${BASENAME}-arm64.app"
    
    # We will use ARM64 as the base (since we are on arm runner usually, or just pick one)
    # and replace the binary with the lipo'd one.
    local FINAL_APP="build/bin/${BASENAME}.app"
    
    echo "Creating Universal App Bundle..."
    cp -r "$APP_ARM64" "$FINAL_APP"
    
    local BIN_AMD64=$(find "$APP_AMD64/Contents/MacOS" -type f -perm +111 | head -n 1)
    local BIN_ARM64=$(find "$APP_ARM64/Contents/MacOS" -type f -perm +111 | head -n 1)
    local BIN_DEST="$FINAL_APP/Contents/MacOS/$(basename "$BIN_ARM64")"
    
    if [[ -z "$BIN_AMD64" || -z "$BIN_ARM64" ]]; then
       echo "Error: Could not find binaries to lipo."
       exit 1
    fi

    # Lipo them
    lipo -create -output "$BIN_DEST" "$BIN_AMD64" "$BIN_ARM64"
    
    # Verify
    echo "Verifying Universal Binary:"
    file "$BIN_DEST"

    # Clean up slices
    rm -rf "$APP_AMD64" "$APP_ARM64"

    local APP_SOURCE="$FINAL_APP"
    local APP_DEST="ops/bin/lokihub.app" 
    local DMG_Path="ops/bin/${ARCHIVE_NAME}.dmg"
    
    if [ -d "$APP_SOURCE" ]; then
        rm -rf "$APP_DEST"
        mv "$APP_SOURCE" "$APP_DEST"
        
        # Create DMG
        cp "ops/darwin/icon.icns" "$APP_DEST/Contents/Resources/icon.icns" || echo "Warning: Could not copy icon to bundle"
        touch "$APP_DEST"

        if [ -d "$APP_DEST" ]; then
              create-dmg \
                --volname "Lokihub" \
                --volicon "ops/darwin/icon.icns" \
                --background "ops/darwin/dmg-background.png" \
                --window-pos 200 120 \
                --window-size 800 400 \
                --icon-size 100 \
                --icon "lokihub.app" 200 190 \
                --hide-extension "lokihub.app" \
                --app-drop-link 600 185 \
                "$DMG_Path" \
                "$APP_DEST"
        fi
        
        # Cleanup .app
        rm -rf "$APP_DEST"
    else
        echo "Error: App bundle $APP_SOURCE not found."
        exit 1
    fi
}

echo "--- Building HTTP Servers ---"
build_http

echo "--- Building Darwin Desktop ---"
build_macos_desktop

echo "macOS Build Script Complete."
ls -lh ops/bin
