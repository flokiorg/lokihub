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
# 2. Build Desktop (Universal)
# -----------------------------------------------------------------------------
#
# macOS ships ONLY the desktop .dmg -- there is no macOS HTTP-server release
# (headless Hub on macOS is built from source; Linux/Windows still ship the
# server). Dropping the server build also halves this job's two universal CGO
# builds, so it grabs a hosted macOS runner faster and is less exposed to
# capacity cancellation -- which matters because the release now requires the
# .dmg (see .github/workflows/release.yaml).

build_macos_desktop() {
    local BASENAME="lokihub-desktop-macos"
    local ARCHIVE_NAME="lokihub-desktop-macos-${TAG}"

    echo "Building macOS Desktop (Universal)..."
    
    # 1. Enforce Clean Build
    rm -rf build/bin

    # Wails' own 'darwin/universal' build hits CGO cross-compile errors, so
    # build native amd64/arm64 slices separately and lipo them manually.

    echo "Building Desktop AMD64 slice..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="clang -arch x86_64" \
    wails build -platform "darwin/amd64" -tags wails -trimpath \
            -ldflags "-s -w -X 'github.com/flokiorg/lokihub/internal/buildinfo.Tag=${VERSION_STRING}'" \
            -o "${BASENAME}-amd64" -clean
    
    # Wails might output "Lokihub.app" instead of the target name
    if [ -d "build/bin/Lokihub.app" ]; then
        mv "build/bin/Lokihub.app" "build/bin/${BASENAME}-amd64.app"
    fi

    echo "Building Desktop ARM64 slice..."
    CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CC="clang -arch arm64" \
    wails build -platform "darwin/arm64" -tags wails -trimpath \
            -ldflags "-s -w -X 'github.com/flokiorg/lokihub/internal/buildinfo.Tag=${VERSION_STRING}'" \
            -o "${BASENAME}-arm64"
            
    # Rename again for ARM64
    if [ -d "build/bin/Lokihub.app" ]; then
        mv "build/bin/Lokihub.app" "build/bin/${BASENAME}-arm64.app"
    fi

    # Wails produces a per-slice .app bundle; build the universal one by
    # taking the ARM64 bundle as the base and lipo-ing in the amd64 binary.
    local APP_AMD64="build/bin/${BASENAME}-amd64.app"
    local APP_ARM64="build/bin/${BASENAME}-arm64.app"
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
              # create-dmg styles the DMG window via a Finder AppleScript, which
              # times out (-1712) on a headless CI builder. Try the styled dmg a
              # couple of times, then fall back to a plain but fully functional
              # dmg so a Finder hiccup never blocks the release.
              styled_dmg() {
                rm -f "$DMG_Path" ops/bin/rw.*.dmg
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
              }
              if ! styled_dmg && ! { sleep 10; styled_dmg; }; then
                echo "Warning: styled create-dmg failed (Finder automation); building a plain dmg"
                rm -f "$DMG_Path" ops/bin/rw.*.dmg
                hdiutil create -volname "Lokihub" -srcfolder "$APP_DEST" -ov -format UDZO "$DMG_Path"
              fi
              rm -f ops/bin/rw.*.dmg
        fi
        
        # Cleanup .app
        rm -rf "$APP_DEST"
    else
        echo "Error: App bundle $APP_SOURCE not found."
        exit 1
    fi
}

echo "--- Building Darwin Desktop ---"
build_macos_desktop

echo "macOS Build Script Complete."
ls -lh ops/bin
