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

build_http() {
    local OUTPUT_NAME="lokihub-http-macos"
    local TARGET_BINARY_NAME="lokihub"
    local ARCHIVE_NAME="lokihub-server-macos-${TAG}"
    
    echo "Building HTTP Server (Universal)..."
    
    # Go doesn't support 'universal' GOARCH directly in one command easily without toolchain support or lipo.
    # However, for server, we might still want separate or universal?
    # User said "clean the ci from arm or amd". Universal is best for single artifact.
    # We will build both and lipo them.
    
    GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/version.Tag=${TAG}'" -o "ops/bin/lokihub-amd64" ./cmd/http
    GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/version.Tag=${TAG}'" -o "ops/bin/lokihub-arm64" ./cmd/http
    
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

    # Native build - Wails handles universal via -platform darwin/universal
    CGO_ENABLED=1 \
    wails build -platform "darwin/universal" -tags wails -trimpath \
            -ldflags "-s -w -X 'github.com/flokiorg/lokihub/pkg/version.Tag=${TAG}'" \
            -o "${BASENAME}" -clean
            
    local APP_SOURCE="build/bin/${BASENAME}.app"
    
    if [ ! -d "$APP_SOURCE" ]; then
        if [ -d "build/bin/Lokihub.app" ]; then
            echo "Found Lokihub.app, renaming to ${BASENAME}.app to match expectation."
            mv "build/bin/Lokihub.app" "$APP_SOURCE"
        else
            echo "Error: App bundle $APP_SOURCE not found."
            echo "Contents of build/bin:"
            ls -R build/bin
            exit 1
        fi
    fi

    # 2. Verify Architecture
    echo "Verifying architecture for $APP_SOURCE..."
    # Find executable inside the bundle
    local BINARY=$(find "$APP_SOURCE/Contents/MacOS" -maxdepth 1 -type f -perm +111 | head -n 1)
    if [ -n "$BINARY" ]; then
        echo "Binary signatures:"
        file "$BINARY"
    else
        echo "Warning: No executable found in bundle to verify."
    fi

    local APP_DEST="ops/bin/lokihub.app" # Needs to be lokihub.app for nice viewing
    local DMG_Path="ops/bin/${ARCHIVE_NAME}.dmg"
    
    if [ -d "$APP_SOURCE" ]; then
        rm -rf "$APP_DEST"
        mv "$APP_SOURCE" "$APP_DEST"
        
        # Create DMG
        # Ensure icon exists in bundle (Force fix)
        cp "ops/darwin/icon.icns" "$APP_DEST/Contents/Resources/icon.icns" || echo "Warning: Could not copy icon to bundle"
        # Touch bundle to refresh cache
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
