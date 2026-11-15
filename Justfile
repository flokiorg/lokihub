# Justfile for Lokihub
#
# Commands are grouped as `just <group> <subcommand> [args...]` (e.g. `just
# dev up`, `just test integration`). Just's own module system needs a
# separate file per group, so groups here are single recipes that dispatch
# on their first argument instead - everything stays in this one Justfile.
#
# Run `just <group>` with no subcommand (or `just <group> help`) to see that
# group's subcommands. Run `just --list` for the top-level group list.

set shell := ["bash", "-c"]
set positional-arguments
# Makes .env visible to `just` recipes and to the tools they run; docker
# compose reads the same .env for its own interpolation.
set dotenv-load := true

VERSION := shell('cat VERSION 2>/dev/null || echo "v0.0.1"')

default:
    @just --list

# unit tests: go vet + go test -race across the module (hermetic, no deps). The
# NWC integration suite and regtest stack now live in the cash-e2e repo.
[group('test')]
test subcommand="unit" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "$1" in
        ""|unit)
            # go vet + race tests across the module. Hermetic, no dependencies.
            go vet ./...
            go test -race -count=1 ./...
            ;;
        help)
            cat <<'USAGE'
    usage: just test <subcommand>

    subcommands:
      unit   (default) go vet + go test -race ./... - hermetic, no dependencies

    The NWC integration suite and regtest stack have moved to the cash-e2e repo.
    USAGE
            ;;
        *)
            echo "error: unknown test subcommand '$1'" >&2
            echo "run 'just test help' to see available subcommands" >&2
            exit 1
            ;;
    esac

[group('security')]
security subcommand="" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "$1" in
        lint)
            golangci-lint run ./...
            ;;
        govulncheck)
            # Reports known-vulnerable dependencies (via govulncheck's
            # advisory database) actually reachable from lokihub's own
            # code, not just present in go.sum.
            go run golang.org/x/vuln/cmd/govulncheck@latest ./...
            ;;
        gosec)
            # Static security analysis (weak crypto, unhandled errors on
            # Close/Write, SQL string concatenation, hardcoded credentials,
            # etc). Overlaps partially with golangci-lint's own integrated
            # gosec linter (lint above) - this standalone run exists to
            # hand auditors a focused, gosec-only report.
            go run github.com/securego/gosec/v2/cmd/gosec@latest ./...
            ;;
        all)
            just -f "{{justfile()}}" security lint
            just -f "{{justfile()}}" security govulncheck
            just -f "{{justfile()}}" security gosec
            ;;
        ""|help)
            cat <<'USAGE'
    usage: just security <subcommand>

    subcommands:
      lint          golangci-lint run ./... (.golangci.yml)
      govulncheck   dependency vulnerability scan (govulncheck)
      gosec         standalone gosec-only static security report
      all           run every static/dependency security check in one go
    USAGE
            ;;
        *)
            echo "error: unknown security subcommand '$1'" >&2
            echo "run 'just security help' to see available subcommands" >&2
            exit 1
            ;;
    esac

# production docker image + cross-platform build artifacts - run `just release help` for subcommands
[group('release')]
release subcommand="" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "$1" in
        build-front)
            cd ./frontend && yarn build:http
            ;;
        docker-build)
            docker build -t ghcr.io/flokiorg/lokihub:latest .
            ;;
        docker-push)
            docker push ghcr.io/flokiorg/lokihub:latest
            ;;
        build-linux-amd64)
            ./ops/build-docker.sh amd64
            ;;
        build-linux-arm64)
            ./ops/build-docker.sh arm64
            ;;
        build-linux-amd64-modern)
            ./ops/build-docker.sh amd64 modern
            ;;
        build-linux-arm64-modern)
            ./ops/build-docker.sh arm64 modern
            ;;
        ""|help)
            cat <<'USAGE'
    usage: just release <subcommand>

    subcommands:
      build-front                 build the frontend production bundle
      docker-build                build the production docker image (ghcr.io/flokiorg/lokihub:latest)
      docker-push                 push the production docker image
      build-linux-amd64           build linux/amd64 artifacts using docker (CI-like environment)
      build-linux-arm64           build linux/arm64 artifacts using docker (CI-like environment)
      build-linux-amd64-modern    build linux/amd64 artifacts (modern baseline) using docker
      build-linux-arm64-modern    build linux/arm64 artifacts (modern baseline) using docker

    note: these local docker builds produce the server tarball + desktop
    binary + deb/rpm packages, but not the .AppImage - that's assembled by
    the AppImageCrafters/build-appimage GitHub Action in CI, which isn't
    invoked by this local path.
    USAGE
            ;;
        *)
            echo "error: unknown release subcommand '$1'" >&2
            echo "run 'just release help' to see available subcommands" >&2
            exit 1
            ;;
    esac
