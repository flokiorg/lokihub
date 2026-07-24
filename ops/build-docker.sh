#!/bin/bash
set -e

ARCH=$1
VARIANT=$2

if [ -z "$ARCH" ]; then
    ARCH="amd64"
fi

DOCKERFILE="ops/docker/build-linux-${ARCH}.Dockerfile"
CONTAINER_SUFFIX="${ARCH}"

if [ "$VARIANT" == "modern" ]; then
    DOCKERFILE="ops/docker/build-linux-${ARCH}-modern.Dockerfile"
    CONTAINER_SUFFIX="${ARCH}-modern"
fi

if [ ! -f "VERSION" ]; then
    echo "Error: VERSION file not found"
    exit 1
fi
TAG=$(cat VERSION | tr -d '[:space:]')
if [ -z "$TAG" ]; then
    echo "Error: VERSION file is empty"
    exit 1
fi

echo "Building Release for TAG=$TAG (Arch: $ARCH, Variant: ${VARIANT:-legacy})"

# Ensure scripts are executable
chmod +x ops/scripts/*.sh

CONTAINER_NAME="lokihub-builder-${CONTAINER_SUFFIX}"

# Build Builder Docker Image
echo "Building Docker Builder Image (${CONTAINER_SUFFIX})..."
docker build -t "${CONTAINER_NAME}:latest" -f "${DOCKERFILE}" .

# Run Build in Docker
echo "Running Build in Docker..."

docker run --rm \
    -v $(pwd):/build \
    -e TAG=${TAG} \
    "${CONTAINER_NAME}:latest" \
    /bin/bash /build/ops/scripts/build-ci-linux.sh

echo "Build Complete. Artifacts are in ops/bin/"
