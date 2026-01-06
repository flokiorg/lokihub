#!/bin/bash
set -e

TARGET=$1
if [ -z "$TARGET" ]; then
    TARGET="all"
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

echo "Building Release for TAG=$TAG (Target: $TARGET)"

# Ensure scripts are executable
chmod +x ops/scripts/*.sh

# Build Builder Docker Image
echo "Building Docker Builder Image..."
docker build -t lokihub-builder:latest -f ops/docker/builder.Dockerfile .

# Run Build in Docker
echo "Running Build in Docker..."
# Note: Currently build-ci-linux.sh builds everything. 
# We ignore the TARGET arg for selective building as the underlying script doesn't support it yet.
# If splitting is required, we would pass $TARGET to build-ci-linux.sh and handle it there.

docker run --rm \
    -v $(pwd):/build \
    -e TAG=${TAG} \
    lokihub-builder:latest \
    /bin/bash /build/ops/scripts/build-ci-linux.sh

echo "Build Complete. Artifacts are in ops/bin/"
