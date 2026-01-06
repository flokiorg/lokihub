#!/bin/bash
set -e

# Expected environment variables:
# TAG: version tag (e.g., v0.0.1)

if [ -z "$TAG" ]; then
    if [ -f "VERSION" ]; then
        TAG=$(cat VERSION)
    else 
        echo "TAG environment variable is required."
        exit 1
    fi
fi

# Disable VCS stamping to avoid "dubious ownership" errors in Docker
export GOFLAGS="-buildvcs=false"

echo "Detailed Build Script Started. TAG=$TAG"

mkdir -p ops/bin

# -----------------------------------------------------------------------------
# 1. Frontend Build (Shared)
# -----------------------------------------------------------------------------
echo "--- Building Frontend (HTTP) ---"
cd frontend
yarn install
yarn build:http
cd ..

# -----------------------------------------------------------------------------
# 2. HTTP Server Builds
# -----------------------------------------------------------------------------
echo "--- Building HTTP Servers ---"

build_http() {
    local GOOS=$1
    local GOARCH=$2
    local EXT=$3
    local CC=$4
    local CGO=$5 # 1 or 0

    local TARGET_BINARY_NAME="lokihub${EXT}" # The generic name inside the archive
    local OUTPUT_NAME="lokihub-http-${GOOS}-${GOARCH}${EXT}" # Temp build name
    local ARCHIVE_NAME="lokihub-server-${GOOS}-${GOARCH}-${TAG}"
    
    echo "Building HTTP for $GOOS/$GOARCH..."
    
    # We use 'env' to set variables for the command
    env CGO_ENABLED=$CGO GOOS=$GOOS GOARCH=$GOARCH CC="$CC" \
        go build -a -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/version.Tag=${TAG}'" \
        -o "ops/bin/${OUTPUT_NAME}" ./cmd/http

    # Package
    pushd ops/bin > /dev/null
    # Rename to generic name for archiving
    mv "$OUTPUT_NAME" "$TARGET_BINARY_NAME"
    
    if [ "$GOOS" == "linux" ]; then
        tar -czf "${ARCHIVE_NAME}.tar.gz" "$TARGET_BINARY_NAME"
    else
        # Use zip for windows/darwin
        zip -q "${ARCHIVE_NAME}.zip" "$TARGET_BINARY_NAME"
    fi
    
    # Cleanup
    rm "$TARGET_BINARY_NAME"
    popd > /dev/null

}



# -----------------------------------------------------------------------------
# 3. Desktop Builds (Linux & Windows)
# -----------------------------------------------------------------------------
echo "--- Building Desktop Apps ---"

# Setup Wails Assets
rm -rf build
mkdir -p build
cp ops/appicon.png build/ || true
if [ -d "ops/darwin" ]; then cp -r ops/darwin build/; fi
if [ -d "ops/windows" ]; then cp -r ops/windows build/; fi


build_desktop() {
    local PLATFORM=$1   # e.g., linux/amd64
    local WAILS_PLATFORM=$2
    local COMPILER=$3   # env var CC value
    local SLUG=$4 # linux-amd64
    
    local OS="${PLATFORM%/*}"
    local ARCH="${PLATFORM#*/}"
    local BASENAME="lokihub-desktop-${OS}-${ARCH}"
    local ARCHIVE_NAME="lokihub-desktop-${OS}-${ARCH}-${TAG}"

    echo "Building Desktop for $PLATFORM..."

    # Environment
    export CGO_ENABLED=1
    export CC="$COMPILER"
    
    # 1. Enforce Clean Build
    rm -rf build/bin
    mkdir -p build/bin

    
    # Specific setup for Linux ARM64 cross-compilation
    if [ "$PLATFORM" == "linux/arm64" ]; then
        # Use simple pkg-config but point it to the sysroot
        export PKG_CONFIG_DIR=""
        export PKG_CONFIG_LIBDIR="/sysroot/arm64/usr/lib/aarch64-linux-gnu/pkgconfig:/sysroot/arm64/usr/share/pkgconfig"
        export PKG_CONFIG_SYSROOT_DIR="/sysroot/arm64"
        export CGO_CFLAGS="--sysroot=/sysroot/arm64"
        export CGO_LDFLAGS="--sysroot=/sysroot/arm64"
    else
        unset PKG_CONFIG_LIBDIR
        unset PKG_CONFIG_SYSROOT_DIR
        unset CGO_CFLAGS
        unset CGO_LDFLAGS
    fi

    LDFLAGS="-X 'github.com/flokiorg/lokihub/pkg/version.Tag=${TAG}'"
    
    # Wails Build
    # Note: -o output name logic in wails can be tricky.
    wails build -platform "$WAILS_PLATFORM" -webview2 embed -tags wails \
        -ldflags "${LDFLAGS}" \
        -o "${BASENAME}" -clean -nopackage

    # 2. Verify Architecture
    local GENERATED_BIN="build/bin/${BASENAME}"
    echo "Verifying architecture for $GENERATED_BIN..."
    if [ -f "$GENERATED_BIN" ]; then
        file "$GENERATED_BIN"
    else
        echo "Error: Binary $GENERATED_BIN not found!"
        exit 1
    fi


    # Packaging
    pushd ops/bin > /dev/null

    if [ "$OS" == "linux" ]; then
        echo "Packaging Linux AppImage for $ARCH..."
        mkdir -p tools

        if [ "$ARCH" == "amd64" ]; then
            # Use pre-installed tool
            echo "Using pre-installed linuxdeploy-x86_64.AppImage"
        elif [ "$ARCH" == "arm64" ]; then
            # Use pre-installed tool
            echo "Using pre-installed linuxdeploy-aarch64.AppImage"
        fi

        # 2. Setup AppDir
        rm -rf AppDir
        mkdir -p AppDir/usr/bin
        mkdir -p AppDir/usr/share/applications
        mkdir -p AppDir/usr/share/icons/hicolor/256x256/apps
        
        if [ -f "../../build/bin/${BASENAME}" ]; then
            cp "../../build/bin/${BASENAME}" "AppDir/usr/bin/lokihub"
        else
            echo "Error: Binary ../../build/bin/${BASENAME} not found!"
            exit 1
        fi
        
        cp "../../ops/appicon.png" "AppDir/usr/share/icons/hicolor/256x256/apps/lokihub.png"

        # 3. Create Desktop File
        cat > AppDir/usr/share/applications/lokihub.desktop <<EOF
[Desktop Entry]
Type=Application
Name=Lokihub
Comment=The Flokicoin Developers
Exec=lokihub
Icon=lokihub
Categories=Utility;
Terminal=false
EOF

        # 4. Generate AppImage
        # Make sure we export version for AppImage name
        export VERSION="${TAG}"
        export APPIMAGE_EXTRACT_AND_RUN=1
        
        if [ "$ARCH" == "amd64" ]; then
            # Use absolute path to pre-installed tool
            /usr/local/bin/tools/linuxdeploy-x86_64.AppImage \
                --appdir AppDir \
                --output appimage \
                --icon-file AppDir/usr/share/icons/hicolor/256x256/apps/lokihub.png \
                --desktop-file AppDir/usr/share/applications/lokihub.desktop
            
            # Move Result
            mv Lokihub-*.AppImage "${ARCHIVE_NAME}.AppImage"
            
        elif [ "$ARCH" == "arm64" ]; then
            export ARCH=arm64
            # We must use QEMU
            
            # 1. Run linuxdeploy ONLY to populate AppDir (no --output appimage) to avoid plugin crash
            # Extract linuxdeploy
            rm -rf squashfs-root
            qemu-aarch64-static /usr/local/bin/tools/linuxdeploy-aarch64.AppImage --appimage-extract > /dev/null
            
            # DELETE THE APPIMAGE PLUGIN TO PREVENT CRASH
            rm -f squashfs-root/usr/bin/linuxdeploy-plugin-appimage
            
            # REPLACE PATCHELF WITH DUMMY TO AVOID QEMU ISSUES
            # linuxdeploy requires patchelf to exist, but we don't need it for Go binaries.
            # The real patchelf crashes/fails in QEMU.
            rm -f squashfs-root/usr/bin/patchelf
            echo '#!/bin/sh' > squashfs-root/usr/bin/patchelf
            echo 'exit 0' >> squashfs-root/usr/bin/patchelf
            chmod +x squashfs-root/usr/bin/patchelf
            
            echo "Running linuxdeploy (AppDir only)..."
            qemu-aarch64-static squashfs-root/AppRun \
                --appdir AppDir \
                --icon-file AppDir/usr/share/icons/hicolor/256x256/apps/lokihub.png \
                --desktop-file AppDir/usr/share/applications/lokihub.desktop
            
            rm -rf squashfs-root
            
            rm -rf squashfs-root
            
            # 2. Manual AppImage Assembly (The Robust Way)
            # appimagetool fails to embed MD5 digest into cross-arch runtime.
            # So we manually create the squashfs and concatenate.
            
            echo "Extracting mksquashfs..."
            rm -rf squashfs-root-tool
            # Use pre-installed tool
            /usr/local/bin/tools/appimagetool-x86_64.AppImage --appimage-extract > /dev/null
            mv squashfs-root squashfs-root-tool
            
            # Find mksquashfs (usually in usr/bin or similar)
            MKSQUASHFS=$(find squashfs-root-tool -name mksquashfs | head -n 1)
            
            echo "Creating squashfs payload..."
            # -comp zstd is standard for modern AppImages
            "$MKSQUASHFS" AppDir filesystem.squashfs -root-owned -noappend -comp zstd -Xcompression-level 1
            
            echo "Assembling AppImage..."
            cat /usr/local/bin/tools/runtime-aarch64 filesystem.squashfs > "${ARCHIVE_NAME}.AppImage"
            chmod +x "${ARCHIVE_NAME}.AppImage"
            
            rm -rf squashfs-root-tool filesystem.squashfs
        fi

        # Clean up
        rm -rf AppDir

    elif [ "$OS" == "windows" ]; then
        # Wails on Linux might not append .exe automatically when -o is used
        SOURCE_BIN="../../build/bin/${BASENAME}"
        if [ -f "${SOURCE_BIN}.exe" ]; then
            SOURCE_BIN="${SOURCE_BIN}.exe"
        fi

        if [ -f "${SOURCE_BIN}" ]; then
             mv "${SOURCE_BIN}" "lokihub.exe"
             7z a -tzip "${ARCHIVE_NAME}.zip" "lokihub.exe"
             rm "lokihub.exe"
        else
             echo "Warning: Output ../../build/bin/${BASENAME}[.exe] not found"
        fi
    fi
    popd > /dev/null
}

# -----------------------------------------------------------------------------
# 4. Build Execution Order
# -----------------------------------------------------------------------------

# 1. Linux Desktop ARM64 (Most likely to fail/crash)
# Using aarch64-linux-gnu-gcc provided by crossbuild-essential-arm64
build_desktop "linux/arm64" "linux/arm64" "aarch64-linux-gnu-gcc" "linux-arm64"

# 2. Linux Desktop AMD64
build_desktop "linux/amd64" "linux/amd64" "gcc" "linux-amd64"

# 3. Linux HTTP AMD64
build_http "linux" "amd64" "" "gcc" "1"

# 4. Linux HTTP ARM64
build_http "linux" "arm64" "" "zig cc -target aarch64-linux-gnu" "1"

echo "Linux Build Script Complete."
ls -lh ops/bin
