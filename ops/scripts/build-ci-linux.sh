#!/bin/bash
set -e

# Expected environment variables:
# TAG: version tag (e.g., v0.0.1)
# GOARCH: amd64 or arm64 (build runs natively on a matching hosted runner, no cross-compile)
# DEB_SUFFIX, LIBWEBKIT, LIBWEBKIT_RPM: set by the workflow matrix for the legacy/modern split

if [ -z "$TAG" ]; then
    if [ -f "VERSION" ]; then
        TAG=$(cat VERSION)
    else
        echo "TAG environment variable is required."
        exit 1
    fi
fi

VERSION_STRING="${BUILD_TAG:-$TAG}"
GOOS="linux"

export GOFLAGS="-buildvcs=false"

# Set by the old ops/docker/build-linux-arm64*.Dockerfile images when
# cross-compiling arm64 from an amd64 host (still used by the local
# `just release build-linux-arm64` dev path) - a no-op on the native
# hosted CI runners, which don't set these.
if [ -n "$CROSS_CGO_CFLAGS" ]; then
    echo "Applying Cross-Compilation Flags..."
    export CGO_CFLAGS="$CROSS_CGO_CFLAGS"
    export CGO_LDFLAGS="$CROSS_CGO_LDFLAGS"
    export PKG_CONFIG_DIR="$CROSS_PKG_CONFIG_DIR"
    export PKG_CONFIG_LIBDIR="$CROSS_PKG_CONFIG_LIBDIR"
    export PKG_CONFIG_SYSROOT_DIR="$CROSS_PKG_CONFIG_SYSROOT_DIR"
fi

echo "Linux Build Script Started. TAG=$TAG GOARCH=$GOARCH"

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

go build -a -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/version.Tag=${VERSION_STRING}'" \
    -o "ops/bin/${OUTPUT_NAME}" ./cmd/http

pushd ops/bin > /dev/null
mv "$OUTPUT_NAME" "$TARGET_BINARY_NAME"
tar -czf "${ARCHIVE_NAME}.tar.gz" "$TARGET_BINARY_NAME"
rm "$TARGET_BINARY_NAME"
popd > /dev/null

# -----------------------------------------------------------------------------
# 3. Desktop Binary + AppDir Staging
# -----------------------------------------------------------------------------
echo "--- Building Desktop Binary ---"

rm -rf build AppDir
mkdir -p build/bin

BASENAME="lokihub-desktop-${GOOS}-${GOARCH}"
LDFLAGS="-X 'github.com/flokiorg/lokihub/version.Tag=${VERSION_STRING}'"

go build -trimpath -tags wails -ldflags "${LDFLAGS} -s -w" \
    -o "build/bin/${BASENAME}" .

echo "--- Staging AppDir ---"
mkdir -p AppDir/usr/bin
mkdir -p AppDir/usr/share/applications
mkdir -p AppDir/usr/share/icons/hicolor/256x256/apps

cp "build/bin/${BASENAME}" "AppDir/usr/bin/lokihub"
cp "ops/appicon.png" "AppDir/usr/share/icons/hicolor/256x256/apps/lokihub.png"
cp "ops/packaging/lokihub.desktop" "AppDir/usr/share/applications/lokihub.desktop"

# AppImage assembly itself happens in the workflow via the
# AppImageCrafters/build-appimage action, reading AppDir staged above.

# -----------------------------------------------------------------------------
# 4. Native Packaging (DEB/RPM) via nfpm
# -----------------------------------------------------------------------------
echo "--- Native Packaging (DEB/RPM) ---"

TARGET_BINARY="lokihub-desktop-linux-${GOARCH}${DEB_SUFFIX}"
cp "build/bin/${BASENAME}" "ops/bin/${TARGET_BINARY}"

# nfpm.yaml uses nfpm's native ${VAR} env-var expansion directly - no
# preprocessing needed, just export the values it references.
export GOARCH TAG
export BINARY_NAME="$TARGET_BINARY"
export LIBWEBKIT LIBWEBKIT_RPM

DEB_NAME="lokihub-desktop-linux${DEB_SUFFIX}-${GOARCH}-${TAG}.deb"
nfpm package --config ops/packaging/nfpm.yaml --packager deb --target "ops/bin/${DEB_NAME}"

RPM_ARCH="x86_64"
if [ "$GOARCH" == "arm64" ]; then
    RPM_ARCH="aarch64"
fi
RPM_NAME="lokihub-desktop-linux${DEB_SUFFIX}-${RPM_ARCH}-${TAG}.rpm"
nfpm package --config ops/packaging/nfpm.yaml --packager rpm --target "ops/bin/${RPM_NAME}"

rm "ops/bin/${TARGET_BINARY}"

echo "Build Complete. Artifacts in ops/bin:"
ls -lh ops/bin
