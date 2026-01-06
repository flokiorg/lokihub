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
# 2. Build HTTP Server (Native)
# -----------------------------------------------------------------------------
echo "--- Building HTTP Servers ---"

build_http() {
    local ARCH=$1
    local OUTPUT_NAME="lokihub-http-darwin-${ARCH}"
    local TARGET_BINARY_NAME="lokihub"
    local ARCHIVE_NAME="lokihub-server-darwin-${ARCH}-${TAG}"
    
    echo "Building HTTP for darwin/$ARCH..."
    
    # Native build
    CGO_ENABLED=1 GOOS=darwin GOARCH=$ARCH \
        go build -a -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/version.Tag=${TAG}'" \
        -o "ops/bin/${OUTPUT_NAME}" ./cmd/http

    # Package
    pushd ops/bin > /dev/null
    mv "$OUTPUT_NAME" "$TARGET_BINARY_NAME"
    zip -q "${ARCHIVE_NAME}.zip" "$TARGET_BINARY_NAME"
    rm "$TARGET_BINARY_NAME"
    popd > /dev/null
}

# Build both architectures
build_http "amd64"
build_http "arm64"


# -----------------------------------------------------------------------------
# 3. Build Desktop (x64 and arm64 handling)
# -----------------------------------------------------------------------------
# We typically want a Universal binary or separate. The original workflow had separate matrix jobs.
# Here we can build both if the host supports it (Go supports cross-arch easily on mac).
# Wails supports 'darwin/universal' or specific.
# Let's keep it simple: Use the HOST architecture or build specific if we want both.
# If the runner is M1 (arm64), we can target arm64 easily.
# If we want both, we run wails twice.

build_macos_desktop() {
    local ARCH=$1
    local WAILS_PLATFORM="darwin/$ARCH"
    
    local BASENAME="lokihub-desktop-darwin-${ARCH}"
    local ARCHIVE_NAME="lokihub-desktop-darwin-${ARCH}-${TAG}"
    
    echo "Building macOS Desktop for $ARCH..."
    
    # 1. Enforce Clean Build
    rm -rf build/bin

    wails build -platform "$WAILS_PLATFORM" -tags wails -trimpath \
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

echo "--- Building Darwin AMD64 ---"
build_macos_desktop "amd64"

echo "--- Building Darwin ARM64 ---"
build_macos_desktop "arm64"

echo "macOS Build Script Complete."
ls -lh ops/bin
