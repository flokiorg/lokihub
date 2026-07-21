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

VERSION := shell('cat VERSION 2>/dev/null || echo "v0.0.1"')
DOCKER_COMPOSE_DEV := "docker compose -f docker-compose.dev.yml"
# flnd stores its network directory as "main" (chaincfg.Params.Name), but
# flncli's --network flag only accepts "mainnet" and assumes the directory
# is named to match — so it looks in .../mainnet/ and never finds the
# macaroon there. All authenticated flncli calls need this explicit path.
FLND_MACAROON_PATH := "/root/.flnd/data/chain/flokicoin/main/admin.macaroon"

default:
    @just --list

# docker-compose dev environment (flnd + hot-reloading backend/frontend) - run `just dev help` for subcommands
[group('dev')]
dev subcommand="" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "$1" in
        setup)
            mkdir -p data/flnd data/hub
            [ -f data/flnd/flnd.conf ] || cp data/flnd/flnd.conf.sample data/flnd/flnd.conf
            sed -i 's/^; flokicoin.mainnet=true/flokicoin.mainnet=true/' data/flnd/flnd.conf
            sed -i 's/^; flokicoin.node=neutrino/flokicoin.node=neutrino/' data/flnd/flnd.conf
            [ -f .env ] || cp .env.example .env
            ;;
        setup-wallet)
            if [ -f "data/flnd/data/chain/flokicoin/main/wallet.db" ]; then
                echo "✅ Existing wallet detected. Skipping creation."
            else
                echo "🔐 Setting up FLND Wallet..."
                {{DOCKER_COMPOSE_DEV}} rm -f flnd &> /dev/null || true
                {{DOCKER_COMPOSE_DEV}} build flnd
                {{DOCKER_COMPOSE_DEV}} run -d --name lokihub-flnd-setup flnd
                echo "⏳ Waiting for FLND to initialize..."
                MAX_RETRIES=30; COUNT=0
                until docker exec lokihub-flnd-setup ls /root/.flnd/tls.cert &> /dev/null || [ $COUNT -eq $MAX_RETRIES ]; do
                    sleep 1; ((COUNT++))
                done
                if docker exec -it lokihub-flnd-setup flncli --network=mainnet create; then
                    echo "✅ Wallet initialized. Cleaning up..."
                    docker stop lokihub-flnd-setup > /dev/null && docker rm lokihub-flnd-setup > /dev/null
                else
                    echo "❌ Wallet creation failed or was cancelled. Container lokihub-flnd-setup left running for you to retry:"
                    echo "   docker exec -it lokihub-flnd-setup flncli --network=mainnet create"
                    exit 1
                fi
            fi
            ;;
        unlock)
            if [ "$(docker inspect -f '{{ "{{" }}.State.Running{{ "}}" }}' lokihub-dev-flnd 2>/dev/null)" != "true" ]; then
                echo "❌ Error: flnd container is not running. Try 'just dev up' first."
                exit 1
            fi
            if docker exec lokihub-dev-flnd flncli --network=mainnet --macaroonpath={{FLND_MACAROON_PATH}} getinfo &> /dev/null; then
                echo "✅ Wallet is already unlocked."
                exit 0
            fi
            if [ ! -f "data/flnd/wallet-password.txt" ]; then
                python3 -c 'import getpass, os; p = getpass.getpass("Enter wallet password: "); f = open("data/flnd/wallet-password.txt", "w"); f.write(p); f.close(); os.chmod("data/flnd/wallet-password.txt", 0o600)'
                if grep -q "^wallet-unlock-password-file=" data/flnd/flnd.conf; then
                    sed -i "s|^wallet-unlock-password-file=.*|wallet-unlock-password-file=/root/.flnd/wallet-password.txt|" data/flnd/flnd.conf
                else
                    awk -v line="wallet-unlock-password-file=/root/.flnd/wallet-password.txt" '!done && /^\[/ { print line; done=1 } { print }' data/flnd/flnd.conf > data/flnd/flnd.conf.tmp && mv data/flnd/flnd.conf.tmp data/flnd/flnd.conf
                fi
                {{DOCKER_COMPOSE_DEV}} restart flnd
            else
                echo "🔐 Auto-unlock is configured but wallet is still locked. Check data/flnd/wallet-password.txt."
            fi
            ;;
        up)
            {{DOCKER_COMPOSE_DEV}} up -d --build
            ;;
        down)
            {{DOCKER_COMPOSE_DEV}} down
            ;;
        logs)
            shift
            {{DOCKER_COMPOSE_DEV}} logs -f "${1:-}"
            ;;
        status)
            {{DOCKER_COMPOSE_DEV}} ps
            ;;
        flncli)
            shift
            docker exec -it lokihub-dev-flnd flncli --network=mainnet --macaroonpath={{FLND_MACAROON_PATH}} "$@"
            ;;
        wails)
            exec wails dev -tags wails,dev -ldflags "-X 'github.com/flokiorg/lokihub/version.Tag={{VERSION}}'"
            ;;
        ""|help)
            cat <<'USAGE'
    usage: just dev <subcommand> [args...]

    subcommands:
      setup                onboard the docker dev environment: create data dirs/config from samples
      setup-wallet          initialize the FLND wallet (run once, after setup, before up)
      unlock                ensure the FLND wallet is unlocked (sets up auto-unlock if needed)
      up                    start the docker dev environment (flnd, backend, frontend w/ hot-reload)
      down                  stop the docker dev environment
      logs [service]        follow logs for the docker dev environment or a specific service
      status                show status of the docker dev environment
      flncli <args...>      run flncli commands against the dev flnd (e.g. `just dev flncli getinfo`)
      wails                 run the Wails desktop app locally (native alternative to the docker dev stack)
    USAGE
            ;;
        *)
            echo "error: unknown dev subcommand '$1'" >&2
            echo "run 'just dev help' to see available subcommands" >&2
            exit 1
            ;;
    esac

# unit tests + black-box NWC integration suite (see integration/README.md) - run `just test help` for subcommands
[group('test')]
test subcommand="unit" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "$1" in
        ""|unit)
            # Everything except the integration/ black-box suite below,
            # which needs a live dev stack. Hermetic, no dependencies.
            go vet ./...
            go test -race -count=1 ./...
            ;;
        integration)
            # Runs against dev's default config. Skips 16 subtests: 2 need
            # real (nonzero) NWC rate limits to exercise (dev's .env
            # disables them by default - see integration-ratelimits below),
            # and 14 are deliberately-redundant policy variants left as
            # documented skips elsewhere in the suite. Requires: dev stack
            # up (`just dev up`) with a real LN backend, and
            # integration/config.local.yaml pointing at an admin API token
            # (see integration/README.md).
            go vet -tags integration ./integration/...
            go test -tags integration -count=1 -timeout 8m ./integration/...
            ;;
        integration-ratelimits)
            # Runs only the 2 rate-limit tests, with dev's NWC rate limits
            # temporarily switched on (they're 0/disabled by default -
            # handy for manual frontend testing, but it means the
            # 11th/21st call in these tests never actually gets rejected).
            # Restores .env and restarts the backend again afterwards
            # regardless of outcome (trap on EXIT), so dev is never left
            # with limits on.
            restore() {
                sed -i \
                    -e 's/^JIT_WALLET_RATE_LIMIT_PER_HOUR=.*/JIT_WALLET_RATE_LIMIT_PER_HOUR=0/' \
                    -e 's/^JIT_WALLET_CLAIM_RATE_LIMIT_PER_HOUR=.*/JIT_WALLET_CLAIM_RATE_LIMIT_PER_HOUR=0/' \
                    -e 's/^CIRCLE_WALLET_RATE_LIMIT_PER_HOUR=.*/CIRCLE_WALLET_RATE_LIMIT_PER_HOUR=0/' \
                    .env
                {{DOCKER_COMPOSE_DEV}} restart backend >/dev/null
                echo "restored .env rate limits to disabled and restarted backend"
            }
            trap restore EXIT

            sed -i \
                -e 's/^JIT_WALLET_RATE_LIMIT_PER_HOUR=.*/JIT_WALLET_RATE_LIMIT_PER_HOUR=10/' \
                -e 's/^JIT_WALLET_CLAIM_RATE_LIMIT_PER_HOUR=.*/JIT_WALLET_CLAIM_RATE_LIMIT_PER_HOUR=20/' \
                -e 's/^CIRCLE_WALLET_RATE_LIMIT_PER_HOUR=.*/CIRCLE_WALLET_RATE_LIMIT_PER_HOUR=3/' \
                .env
            {{DOCKER_COMPOSE_DEV}} restart backend >/dev/null
            echo "waiting for backend to come back up with real rate limits..."
            for i in $(seq 1 30); do
                docker logs lokihub-dev-backend 2>&1 | grep -q "http server started" && break
                sleep 1
            done

            INTEGRATION_RUN_RATE_LIMIT_TESTS=1 go test -tags integration -count=1 -timeout 3m \
                -run 'TestJITHubs/RateLimiting_EleventhCreateIsRateLimited|TestClaimFunds/RateLimiting_TwentyFirstClaimIsRateLimited' \
                -v ./integration/...
            ;;
        integration-all)
            # Runs the full suite twice - once with dev's default config,
            # once with NWC rate limits temporarily enabled - so every test
            # in the suite actually runs and passes at least once, with
            # zero skipped tests overall.
            just -f "{{justfile()}}" test integration
            just -f "{{justfile()}}" test integration-ratelimits
            ;;
        help)
            cat <<'USAGE'
    usage: just test <subcommand>

    subcommands:
      unit                    (default) go vet + go test -race ./... - hermetic, no dependencies
      integration             black-box NWC integration suite against dev's default config
      integration-ratelimits  the 2 tests needing real (nonzero) NWC rate limits
      integration-all         both integration suites back to back - zero skips overall
    USAGE
            ;;
        *)
            echo "error: unknown test subcommand '$1'" >&2
            echo "run 'just test help' to see available subcommands" >&2
            exit 1
            ;;
    esac

# static analysis + vulnerability scanning (see SECURITY_AUDIT_SCOPE.md) - run `just security help` for subcommands
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
    USAGE
            ;;
        *)
            echo "error: unknown release subcommand '$1'" >&2
            echo "run 'just release help' to see available subcommands" >&2
            exit 1
            ;;
    esac
