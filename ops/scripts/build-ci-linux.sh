#!/bin/bash
set -e

# Expected environment variables:
# TAG: version tag (e.g., v0.0.1)
# GOOS, GOARCH, CC: Set by Docker environment
# LINUXDEPLOY_BIN: Path to linuxdeploy AppImage

if [ -z "$TAG" ]; then
    if [ -f "VERSION" ]; then
        TAG=$(cat VERSION)
    else 
        echo "TAG environment variable is required."
        exit 1
    fi
fi

# Disable VCS stamping
export GOFLAGS="-buildvcs=false"

# Apply Cross-Compilation Flags if present
if [ -n "$CROSS_CGO_CFLAGS" ]; then
    echo "Applying Cross-Compilation Flags..."
    export CGO_CFLAGS="$CROSS_CGO_CFLAGS"
    export CGO_LDFLAGS="$CROSS_CGO_LDFLAGS"
    export PKG_CONFIG_DIR="$CROSS_PKG_CONFIG_DIR"
    export PKG_CONFIG_LIBDIR="$CROSS_PKG_CONFIG_LIBDIR"
    export PKG_CONFIG_SYSROOT_DIR="$CROSS_PKG_CONFIG_SYSROOT_DIR"
fi

echo "Generic Build Script Started. TAG=$TAG"
echo "Target: $GOOS/$GOARCH using CC=$CC"

mkdir -p ops/bin

# -----------------------------------------------------------------------------
# 1. Frontend Build
# -----------------------------------------------------------------------------
echo "--- Building Frontend ---"
cd frontend
yarn install
yarn build:http
cd ..

# -----------------------------------------------------------------------------
# 2. HTTP Server Build
# -----------------------------------------------------------------------------
echo "--- Building HTTP Server ---"
TARGET_BINARY_NAME="lokihub"
OUTPUT_NAME="lokihub-http-${GOOS}-${GOARCH}"
ARCHIVE_NAME="lokihub-server-${GOOS}-${GOARCH}-${TAG}"

go build -a -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/version.Tag=${TAG}'" \
    -o "ops/bin/${OUTPUT_NAME}" ./cmd/http

# Package HTTP
pushd ops/bin > /dev/null
mv "$OUTPUT_NAME" "$TARGET_BINARY_NAME"
tar -czf "${ARCHIVE_NAME}.tar.gz" "$TARGET_BINARY_NAME"
rm "$TARGET_BINARY_NAME"
popd > /dev/null

# -----------------------------------------------------------------------------
# 3. Desktop Build
# -----------------------------------------------------------------------------
echo "--- Building Desktop App ---"

# Assets
rm -rf build
mkdir -p build
cp ops/appicon.png build/ || true

BASENAME="lokihub-desktop-${GOOS}-${GOARCH}"
ARCHIVE_NAME="lokihub-desktop-${GOOS}-${GOARCH}-${TAG}"
echo "DEBUG: ARCHIVE_NAME=${ARCHIVE_NAME}"

# Cleanup previous artifacts to prevent confusion
rm -f *.AppImage*
rm -f ops/bin/*.AppImage*

# Ensure clean build
rm -rf build/bin
mkdir -p build/bin

LDFLAGS="-X 'github.com/flokiorg/lokihub/pkg/version.Tag=${TAG}'"

# Wails Build
echo "--- Building Desktop Binary (Direct Go) ---"
go build -trimpath -tags wails -ldflags "${LDFLAGS} -s -w" \
    -o "build/bin/${BASENAME}" .

# Packaging (AppImage)
# Packaging (AppImage)
# pushd ops/bin > /dev/null # REMOVED: Stay in root
echo "Packaging AppImage..."

mkdir -p tools # Placeholder

# Setup AppDir
rm -rf AppDir
mkdir -p AppDir/usr/bin
mkdir -p AppDir/usr/share/applications
mkdir -p AppDir/usr/share/icons/hicolor/256x256/apps

if [ -f "build/bin/${BASENAME}" ]; then
    cp "build/bin/${BASENAME}" "AppDir/usr/bin/lokihub"
else
    echo "Error: Binary build/bin/${BASENAME} not found!"
    exit 1
fi

cp "ops/appicon.png" "AppDir/usr/share/icons/hicolor/256x256/apps/lokihub.png"

# Create Desktop File
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

export VERSION="${TAG}"
export APPIMAGE_EXTRACT_AND_RUN=1

if [ "$GOARCH" == "amd64" ]; then
    # Standard LinuxDeploy
    set -x # Debug ON
    "$LINUXDEPLOY_BIN" \
        --appdir AppDir \
        --output appimage \
        --icon-file AppDir/usr/share/icons/hicolor/256x256/apps/lokihub.png \
        --desktop-file AppDir/usr/share/applications/lokihub.desktop
    
    mv Lokihub-*.AppImage "ops/bin/${ARCHIVE_NAME}.AppImage"

elif [ "$GOARCH" == "arm64" ]; then
    # Cross-Arch Manual Assembly
    # We use QEMU to populate AppDir, then manually squashfs
    
    # 1. Run linuxdeploy (AppDir only)
    # Extract linuxdeploy (since running directly via QEMU might fail on plugins)
    rm -rf squashfs-root
    qemu-aarch64-static "$LINUXDEPLOY_BIN" --appimage-extract > /dev/null
    
    # Remove conflicting plugins/patchelf
    rm -f squashfs-root/usr/bin/linuxdeploy-plugin-appimage
    rm -f squashfs-root/usr/bin/patchelf
    echo '#!/bin/sh' > squashfs-root/usr/bin/patchelf
    echo 'exit 0' >> squashfs-root/usr/bin/patchelf
    chmod +x squashfs-root/usr/bin/patchelf
    
    qemu-aarch64-static squashfs-root/AppRun \
        --appdir AppDir \
        --icon-file AppDir/usr/share/icons/hicolor/256x256/apps/lokihub.png \
        --desktop-file AppDir/usr/share/applications/lokihub.desktop
    
    rm -rf squashfs-root

    # 2. Manual Assembly
    echo "Creating squashfs payload..."
    # Extract appimagetool to get mksquashfs if needed, OR use installed tool
    # In ARM64 Dockerfile we have 'appimagetool-x86_64' but we need to run it.
    
    rm -rf squashfs-root-tool
    "$APPIMAGETOOL_BIN" --appimage-extract > /dev/null
    mv squashfs-root squashfs-root-tool
    MKSQUASHFS=$(find squashfs-root-tool -name mksquashfs | head -n 1)
    
    "$MKSQUASHFS" AppDir filesystem.squashfs -root-owned -noappend -comp zstd -Xcompression-level 1
    
    echo "Assembling AppImage..."
    cat "$RUNTIME_BIN" filesystem.squashfs > "ops/bin/${ARCHIVE_NAME}.AppImage"
    chmod +x "ops/bin/${ARCHIVE_NAME}.AppImage"
    
    rm -rf squashfs-root-tool filesystem.squashfs
fi

# Cleanup
rm -rf AppDir
# popd > /dev/null # REMOVED: Matched pushd removal

# -----------------------------------------------------------------------------
# 4. Native Packaging (DEB/RPM) via NFPM
# -----------------------------------------------------------------------------
# -----------------------------------------------------------------------------
# 4. Native Packaging (DEB/RPM) via NFPM
# -----------------------------------------------------------------------------
echo "--- Native Packaging (DEB/RPM) ---"
if command -v nfpm >/dev/null 2>&1; then
    # Detect Build Environment (Legacy vs Modern)
    OS_ID=$(grep '^VERSION_CODENAME=' /etc/os-release | cut -d= -f2)
    echo "Detected Build Environment: $OS_ID"

    # Defaults (Legacy / Bullseye)
    DEB_SUFFIX="" # e.g. lokihub-desktop-linux-amd64-vX.Y.Z.deb
    LIBWEBKIT="libwebkit2gtk-4.0-37"
    LIBWEBKIT_RPM="webkit2gtk3"

    if [ "$OS_ID" == "bookworm" ]; then
        # Modern (Ubuntu 24.04+)
        echo "Configuring for Modern (Ubuntu 24.04+) naming..."
        DEB_SUFFIX="-ubuntu24.04"
        LIBWEBKIT="libwebkit2gtk-4.1-0"
        LIBWEBKIT_RPM="webkit2gtk4.1" # Fedora 40+ package name usually
    fi

    TARGET_BINARY="lokihub-desktop-linux-${GOARCH}${DEB_SUFFIX}"
    cp "build/bin/${BASENAME}" "ops/bin/${TARGET_BINARY}"
    
    # Generate effective config
    # We substitute:
    # BINARY_NAME -> The actual binary file in ops/bin
    # GOARCH -> Architecture
    # TAG -> Version
    # LIBWEBKIT -> Deb dependency
    # LIBWEBKIT_RPM -> Rpm dependency
    sed -e "s|\${GOARCH}|$GOARCH|g" \
        -e "s|\${TAG}|$TAG|g" \
        -e "s|\${BINARY_NAME}|$TARGET_BINARY|g" \
        -e "s|\${LIBWEBKIT}|$LIBWEBKIT|g" \
        -e "s|\${LIBWEBKIT_RPM}|$LIBWEBKIT_RPM|g" \
        ops/packaging/nfpm.yaml > ops/packaging/nfpm_eff.yaml
    
    # 1. DEB Package
    # Naming: lokihub-desktop-linux[-ubuntu24.04]-amd64-vX.Y.Z.deb
    DEB_NAME="lokihub-desktop-linux${DEB_SUFFIX}-${GOARCH}-${TAG}.deb"
    nfpm package --config ops/packaging/nfpm_eff.yaml --packager deb --target "ops/bin/${DEB_NAME}"
    
    # 2. RPM Package
    # RPM arch naming: amd64 -> x86_64, arm64 -> aarch64
    RPM_ARCH="x86_64"
    if [ "$GOARCH" == "arm64" ]; then
        RPM_ARCH="aarch64"
    fi
    RPM_NAME="lokihub-desktop-linux${DEB_SUFFIX}-${RPM_ARCH}-${TAG}.rpm"
    nfpm package --config ops/packaging/nfpm_eff.yaml --packager rpm --target "ops/bin/${RPM_NAME}"

    # 3. Rename AppImage to match scheme
    # Legacy: lokihub-desktop-linux-amd64-vX.Y.Z.AppImage
    # Modern: lokihub-desktop-linux-ubuntu24.04-amd64-vX.Y.Z.AppImage
    
    APPIMAGE_ORIG="ops/bin/${ARCHIVE_NAME}.AppImage" # Now in ops/bin
    
    # If we are Modern, we want to rename the generated AppImage
    if [ -n "$DEB_SUFFIX" ]; then
        NEW_APPIMAGE_NAME="lokihub-desktop-linux${DEB_SUFFIX}-${GOARCH}-${TAG}.AppImage"
        # Source is ops/bin/..., Dest is ops/bin/...
        mv "${APPIMAGE_ORIG}" "ops/bin/${NEW_APPIMAGE_NAME}"
    else 
        # Already in right place and name (mostly)
        # mv "${APPIMAGE_ORIG}" "ops/bin/${APPIMAGE_ORIG}" # Redundant if already there
        echo "AppImage already correctly named in ops/bin."
    fi
    
    # Cleanup temp setup
    rm "ops/bin/${TARGET_BINARY}"
    rm ops/packaging/nfpm_eff.yaml
else
    echo "Warning: nfpm not found using $(command -v nfpm). Skipping native packaging."
    ls -l $(go env GOPATH)/bin/nfpm || true
    mv "${ARCHIVE_NAME}.AppImage" "ops/bin/${ARCHIVE_NAME}.AppImage"
fi

echo "Build Complete. Artifacts in ops/bin:"
ls -lh ops/bin
