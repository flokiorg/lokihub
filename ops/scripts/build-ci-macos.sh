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
        
        # Finalize the bundle, then RE-SIGN it. The universal binary was lipo'd
        # over the arm64 bundle's already-signed binary, so the app's code
        # signature no longer matches it -> macOS reports the app as "damaged".
        # Strip quarantine xattrs and re-sign the whole bundle ad-hoc so the
        # signature is valid. (A Developer-ID identity could be slotted in here
        # for notarization later; ad-hoc is enough to stop "damaged" on a build
        # that isn't downloaded with a quarantine flag.)
        cp "ops/darwin/icon.icns" "$APP_DEST/Contents/Resources/icon.icns" || echo "Warning: Could not copy icon to bundle"
        xattr -cr "$APP_DEST" 2>/dev/null || true
        codesign --remove-signature "$APP_DEST" 2>/dev/null || true
        codesign --force --deep --sign - "$APP_DEST"
        codesign --verify --deep --strict "$APP_DEST" && echo "codesign: ad-hoc signature valid" || echo "Warning: codesign --verify failed"

        if [ -d "$APP_DEST" ]; then
              # Build the styled drag-to-Applications dmg with appdmg, which writes
              # the window layout directly (no Finder), so it works on the headless
              # Codemagic builder too -- create-dmg's Finder AppleScript times out
              # there (-1712), which is why the Codemagic-built dmg had no styling.
              # Fall back to a plain hdiutil dmg only if appdmg is unavailable.
              command -v gm >/dev/null 2>&1 || brew install graphicsmagick >/dev/null 2>&1 || true
              command -v appdmg >/dev/null 2>&1 || npm install -g appdmg >/dev/null 2>&1 || true
              cat > /tmp/appdmg.json <<JSONEOF
{
  "title": "Lokihub",
  "icon": "$PWD/ops/darwin/icon.icns",
  "background": "$PWD/ops/darwin/dmg-background.png",
  "icon-size": 100,
  "window": { "position": { "x": 200, "y": 120 }, "size": { "width": 800, "height": 400 } },
  "contents": [
    { "x": 200, "y": 190, "type": "file", "path": "$PWD/$APP_DEST" },
    { "x": 600, "y": 185, "type": "link", "path": "/Applications" }
  ]
}
JSONEOF
              rm -f "$DMG_Path" ops/bin/rw.*.dmg
              if command -v appdmg >/dev/null 2>&1 && appdmg /tmp/appdmg.json "$DMG_Path"; then
                echo "appdmg: styled dmg created"
              else
                echo "Warning: appdmg unavailable/failed; building a plain hdiutil dmg"
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
