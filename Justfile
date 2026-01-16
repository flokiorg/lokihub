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
# Build Artifacts using Docker
# Build Artifacts using Docker
build-linux-amd64:
    ./ops/build-docker.sh amd64

build-linux-arm64:
    ./ops/build-docker.sh arm64

build-linux-amd64-modern:
    ./ops/build-docker.sh amd64 modern

build-linux-arm64-modern:
    ./ops/build-docker.sh arm64 modern

### Local ###

# Version from file
VERSION := shell('cat VERSION 2>/dev/null || echo "v0.0.1"')

build-front:
    cd ./frontend && yarn build:http

# Run HTTP server locally (Account 1)
run-1:
    WORK_DIR=$(pwd)/data/account-1 \
    PORT=1610 \
    go run -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'" ./cmd/http

# Debug HTTP server locally (Account 1) with Delve
debug-1:
    WORK_DIR=$(pwd)/data/account-1 \
    PORT=1610 \
    dlv debug ./cmd/http --headless --listen=:2345 --api-version=2 --accept-multiclient -- -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'"

# Run HTTP server locally (Account 2)
run-2:
    WORK_DIR=$(pwd)/data/account-2 \
    PORT=1611 \
    go run -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'" ./cmd/http

# Run Wails app locally
run-wails:
    wails dev -tags wails -ldflags "-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'"

# Run HTTP server locally (Account 3)
run-3:
    WORK_DIR=$(pwd)/data/account-3 \
    PORT=1612 \
    go run -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag={{VERSION}}'" ./cmd/http

# Start development environment
rundev:
    @if ! tmux has-session -t lokihub-dev 2>/dev/null; then \
        tmux new-session -d -s lokihub-dev -n 'run-1' 'just run-1'; \
        tmux new-window -t lokihub-dev:2 -n 'run-2' 'just run-2'; \
        tmux new-window -t lokihub-dev:3 -n 'run-3' 'just run-3'; \
    fi
    @tmux attach -t lokihub-dev

# Stop development environment
stopdev:
    -tmux kill-session -t lokihub-dev
    -fuser -k 1610/tcp
    -fuser -k 1611/tcp
    -fuser -k 1612/tcp
    -pkill -f "go run .* ./cmd/http"

# Restart development environment
restartdev: stopdev rundev
