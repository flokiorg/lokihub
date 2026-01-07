# Justfile for Lokihub

# Default target
default:
    @just --list


# Docker operations
docker-build:
    docker build -t ghcr.io/flokiorg/lokihub:latest .

docker-push:
    docker push ghcr.io/flokiorg/lokihub:latest


# Build Artifacts using Docker (CI-like environment)
build-release:
    ./ops/build-docker.sh all

build-release-http:
    ./ops/build-docker.sh http

build-release-desktop:
    ./ops/build-docker.sh desktop

### Local ###

# Version from file
VERSION := shell('cat VERSION 2>/dev/null || echo "v0.0.1"')

build-front:
    cd ./frontend && yarn build:http

# Run HTTP server locally (Account 1)
run-1:
    WORK_DIR=$(pwd)/data/account-1 \
    PORT=8080 \
    go run -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'" ./cmd/http

# Debug HTTP server locally (Account 1) with Delve
debug-1:
    WORK_DIR=$(pwd)/data/account-1 \
    PORT=8080 \
    dlv debug ./cmd/http --headless --listen=:2345 --api-version=2 --accept-multiclient -- -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'"

# Run HTTP server locally (Account 2)
run-2:
    WORK_DIR=$(pwd)/data/account-2 \
    PORT=9090 \
    go run -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'" ./cmd/http

# Run Wails app locally
run-wails:
    wails dev -tags wails -ldflags "-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'"
