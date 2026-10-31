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
DOCKER_COMPOSE_DEV := "docker compose -f docker-compose.dev.yml"
# The regtest stack's data (git-ignored). `just dev wipe` deletes it.
DATA_DIR := "data/regtest"
# The stack's tool (integration/cmd/regtest). GOWORK=off so it builds in a
# worktree too, which the org go.work does not list.
REGTEST := "GOWORK=off go run ./integration/cmd/regtest"

default:
    @just --list

# the regtest docker dev stack (chain, two flnd, relay, hot-reloading backend/frontend) - run `just dev help` for subcommands
[group('dev')]
dev subcommand="" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    # Refuses anything but regtest: a tracked node config or .env naming another
    # network, or the old mainnet data dir, which may hold a funded wallet.
    guard() {
        if grep -HniE 'mainnet|testnet|neutrino' dev/regtest/*.conf; then
            echo "refusing: a dev/regtest config names a network other than regtest" >&2; exit 1
        fi
        if [ -f .env ]; then
            settings=$(grep -v '^[[:space:]]*#' .env || true)
            if grep -iE '(^|[^a-z0-9_/.-])(mainnet|testnet[0-9]*|signet)([^a-z0-9_/.-]|$)|flokicoin\.(mainnet|testnet|signet)|node=neutrino' <<<"$settings" \
                || grep -E '^[[:space:]]*NETWORK=' <<<"$settings" | grep -vqE '^[[:space:]]*NETWORK=["]?regtest["]?[[:space:]]*$'; then
                echo "refusing: .env names a network other than regtest" >&2; exit 1
            fi
        fi
        if [ -d data/flnd ]; then
            echo "refusing: data/flnd/ exists. It is the old mainnet dev node's data and may hold a funded wallet." >&2
            echo "Sweep its funds and remove it by hand; the regtest stack never reads, moves or deletes it." >&2
            exit 1
        fi
    }
    # frontend.go embeds frontend/dist, so the backend cannot compile without at
    # least one file there; a real `yarn build:http` overwrites this placeholder.
    dist_placeholder() {
        mkdir -p frontend/dist
        [ -e frontend/dist/index.html ] || echo '<!doctype html><title>lokihub dev placeholder</title>' > frontend/dist/index.html
    }
    # flspd, the test LSP, runs in the compose "lsp" profile, on when its image
    # exists (built in an flspd checkout: `just image-regtest <nmilat checkout>`).
    lsp_image() {
        local img="${FLSPD_IMAGE:-}"
        [ -n "$img" ] || img=$(grep -E '^FLSPD_IMAGE=' .env 2>/dev/null | tail -1 | cut -d= -f2- || true)
        echo "${img:-flspd:regtest}"
    }
    lsp_profile() {
        if docker image inspect "$(lsp_image)" >/dev/null 2>&1; then
            export COMPOSE_PROFILES=lsp
        elif [ "${1:-}" = "warn" ]; then
            echo "note: no $(lsp_image) image, so the stack runs without flspd (the LSP catalogue tests skip)" >&2
        fi
    }
    wait_backend() {
        for i in $(seq 1 90); do
            {{DOCKER_COMPOSE_DEV}} logs backend --since "$1" 2>&1 | grep -q "http server started" && return 0
            sleep 2
        done
        echo "backend did not start" >&2; return 1
    }
    case "$1" in
        setup)
            mkdir -p "{{DATA_DIR}}"/{chain,flnd,payer,hub,relay,relay-ctl,flnd-lsp,flspd-creds,flspd-config,flspd-db,flspd-admin}
            [ -f .env ] || cp .env.example .env
            if [ ! -f "{{DATA_DIR}}/flspd.env" ]; then
                # flspd's own key (not the node key): any 32 random bytes in hex.
                key=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
                sed "s/^FLSPD_PRIVATE_KEY=.*/FLSPD_PRIVATE_KEY=$key/" dev/regtest/flspd/flspd.env.example > "{{DATA_DIR}}/flspd.env"
            fi
            # The dashboard admin the backend's flspd test hooks log in as.
            if grep -q '^DASHBOARD_ADMIN_PUBKEY=$' "{{DATA_DIR}}/flspd.env"; then
                admin=$({{REGTEST}} flspd-admin-key)
                sed -i "s/^DASHBOARD_ADMIN_PUBKEY=$/DASHBOARD_ADMIN_PUBKEY=$admin/" "{{DATA_DIR}}/flspd.env"
            fi
            if [ ! -f "{{DATA_DIR}}/relay/relay.yaml" ]; then
                key=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
                sed "s/__RELAY_PRIVKEY__/$key/" dev/regtest/relay.yaml.tmpl > "{{DATA_DIR}}/relay/relay.yaml"
            fi
            {{REGTEST}} certs
            ;;
        up)
            guard
            just -f "{{justfile()}}" dev setup
            dist_placeholder
            lsp_profile warn
            {{DOCKER_COMPOSE_DEV}} up -d --build
            just -f "{{justfile()}}" dev bootstrap
            ;;
        bootstrap)
            guard
            {{REGTEST}} bootstrap
            just -f "{{justfile()}}" dev seed-relay
            ;;
        down)
            COMPOSE_PROFILES=lsp {{DOCKER_COMPOSE_DEV}} down
            ;;
        wipe)
            if [ "${2:-}" != "--yes" ]; then
                read -r -p "Delete {{DATA_DIR}}/ (chain, both nodes, the hub and the relay)? [y/N] " answer
                [ "$answer" = "y" ] || [ "$answer" = "Y" ] || exit 1
            fi
            COMPOSE_PROFILES=lsp {{DOCKER_COMPOSE_DEV}} down
            # The containers write as root, so remove through one if a plain rm cannot.
            rm -rf "{{DATA_DIR}}" 2>/dev/null || docker run --rm -v "$PWD/data:/d" alpine rm -rf /d/regtest
            ;;
        mine)
            shift
            {{REGTEST}} mine "$@"
            ;;
        fund)
            shift
            {{REGTEST}} fund "$@"
            ;;
        channels)
            {{REGTEST}} channels
            ;;
        restart)
            shift
            lsp_profile
            dist_placeholder
            {{DOCKER_COMPOSE_DEV}} restart "$@"
            ;;
        logs)
            shift
            lsp_profile
            {{DOCKER_COMPOSE_DEV}} logs -f "${1:-}"
            ;;
        status)
            lsp_profile
            {{DOCKER_COMPOSE_DEV}} ps
            {{REGTEST}} status
            ;;
        reset)
            # Restarts relay then backend so a run starts from a fresh relay and
            # backend session, then reseeds the relay. Chain and nodes are untouched,
            # apart from one new block: flnd counts itself synced only while the tip
            # is under two hours old.
            {{REGTEST}} mine 1
            {{DOCKER_COMPOSE_DEV}} restart relay
            since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
            {{DOCKER_COMPOSE_DEV}} restart backend
            echo "waiting for backend to come back up..."
            wait_backend "$since"
            just -f "{{justfile()}}" dev seed-relay
            ;;
        restart-empty-relay)
            # For INTEGRATION_RESTART_CMD: stops the backend, empties the relay and
            # starts both again, returning once the backend is up.
            {{DOCKER_COMPOSE_DEV}} stop backend relay
            rm -rf "{{DATA_DIR}}/relay/db" 2>/dev/null || docker run --rm -v "$PWD/data:/d" alpine rm -rf /d/regtest/relay/db
            since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
            {{DOCKER_COMPOSE_DEV}} start relay backend
            wait_backend "$since"
            ;;
        seed-relay)
            # Publishes integration/testdata/social_graph.json's follow set to the dev
            # relay (integration/cmd/seedrelay). A follow set is addressable, so this
            # is safe to rerun.
            GOWORK=off go run ./integration/cmd/seedrelay "${RELAY:-ws://relay:5500}"
            ;;
        flncli)
            shift
            node=flnd; port=10005
            if [ "${1:-}" = "payer" ]; then node=payer; port=10006; shift
            elif [ "${1:-}" = "lsp" ]; then node=flnd-lsp; port=10007; lsp_profile; shift
            elif [ "${1:-}" = "hub" ]; then shift; fi
            {{DOCKER_COMPOSE_DEV}} exec "$node" flncli --network=regtest --rpcserver=localhost:$port "$@"
            ;;
        lokid-cli)
            shift
            GOWORK=off go run github.com/flokiorg/go-flokicoin/cmd/lokid-cli --regtest \
                -s "${DEV_HOST:-localhost}:${CHAIN_RPC_PORT:-25213}" -u dev -P dev \
                --rpccert "{{DATA_DIR}}/chain/rpc.cert" "$@"
            ;;
        wails)
            exec wails dev -tags wails,dev -ldflags "-X 'github.com/flokiorg/lokihub/appversion.Tag={{VERSION}}'"
            ;;
        ""|help)
            cat <<'USAGE'
    usage: just dev <subcommand> [args...]

    The dev and test stack runs on a regtest chain only: lokid (`chain`), the
    Hub's flnd (`flnd`), a second flnd that tests pay from and to (`payer`), the
    dev relay, the hot-reloading backend and frontend, and Caddy; with the
    flspd:regtest image present, also flspd, the test LSP, on its own flnd
    (`flnd-lsp`, compose profile "lsp").

    subcommands:
      setup                    create data/regtest/, .env from .env.example, the relay config and the chain RPC cert
      up                       setup, start the stack, then bootstrap
      bootstrap                fund the nodes, open a channel each way (and flnd-lsp -> payer), set lokihub up, write
                               integration/config.local.yaml, seed the relay (idempotent; `up` runs it)
      down                     stop the stack (data kept)
      wipe [--yes]             stop the stack and delete data/regtest/ - the only way to a fresh chain
      mine [N] [address]       mine N blocks (default 1) to the payer, or to address
      fund <hub|payer|address> <loki>   pay from the payer's wallet, then mine 1
      channels                 reopen either channel if it is missing (after wiping one node)
      restart [service...]     restart everything, or the named services
      logs [service]           follow logs
      status                   containers, tip height, wallet and channel balances, lokihub state
      reset                    mine 1, restart relay and backend, reseed the relay (`just test integration` runs it)
      restart-empty-relay      restart the backend on an emptied relay (INTEGRATION_RESTART_CMD)
      seed-relay               publish the integration suite's fixed follow set to the dev relay
      flncli [hub|payer|lsp] <args...>   flncli --network=regtest against a node
      lokid-cli <args...>      lokid-cli --regtest against the chain node
      wails                    run the Wails desktop app locally (not the docker stack)

    Never point this stack or the integration suite at a mainnet node. To run a
    second stack from a worktree, set COMPOSE_PROJECT_NAME, the *_PORT values and
    the DEV_*_DIR checkout paths in its .env.
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
            # Runs against the regtest dev stack (`just dev up`, which writes
            # integration/config.local.yaml). Skips 4 tests: the 3 that need
            # real (nonzero) NWC rate limits (.env disables them - see
            # integration-ratelimits below), and the opt-in
            # TestZZZZ_SweepLeakedEphemeralFixtures.
            #
            # `dev reset` first so every run starts from the same server-side
            # baseline (fresh relay/backend session and subscription state)
            # regardless of what a previous run's own load left behind - see
            # issues.md's "too many concurrent REQs" investigation, where
            # stale in-memory relay state (on top of a DB-level leak, now
            # fixed - see aaa_preflight_cleanup_test.go/zz_leak_check_test.go)
            # made re-running the suite non-reproducible.
            just -f "{{justfile()}}" dev reset
            go vet -tags integration ./integration/...
            # 20m, not 8m. The timeout is a safety net against a hung test,
            # not a performance budget — but at 8m it was the budget: three
            # consecutive full runs took 387s, 435s and 477s against a 480s
            # cap, and a fourth died at exactly 480.033s inside a test that
            # deliberately sleeps. Nothing had failed; the suite simply ran
            # out of clock. A cap that close to the real runtime fires on
            # ordinary variance instead of on hangs, which is the opposite of
            # what it is for, and turns a green suite red at random.
            #
            # Six concurrency/race tests account for roughly three quarters of
            # the runtime (the slowest alone is ~137s), and they are meant to
            # be slow. Speeding the suite up is a separate piece of work.
            go test -tags integration -count=1 -timeout 20m ./integration/...
            ;;
        integration-ratelimits)
            # Runs only the 3 rate-limit tests, with dev's NWC rate limits
            # temporarily switched on (they're 0/disabled by default -
            # handy for manual frontend testing, but it means the
            # 11th/21st call in these tests never actually gets rejected).
            # The backend-ready wait only accepts a "http server started"
            # logged after the restart: an unfiltered `docker logs` matches
            # the previous start's line at once, so it never really waited.
            # Restores .env and restarts the backend again afterwards
            # regardless of outcome (trap on EXIT), so dev is never left
            # with limits on.
            restore() {
                sed -i \
                    -e 's/^CASH_WALLET_RATE_LIMIT_PER_HOUR=.*/CASH_WALLET_RATE_LIMIT_PER_HOUR=0/' \
                    -e 's/^CASH_CLAIM_CALLS_PER_HOUR=.*/CASH_CLAIM_CALLS_PER_HOUR=0/' \
                    -e 's/^CIRCLE_WALLET_RATE_LIMIT_PER_HOUR=.*/CIRCLE_WALLET_RATE_LIMIT_PER_HOUR=0/' \
                    .env
                {{DOCKER_COMPOSE_DEV}} restart backend >/dev/null
                echo "restored .env rate limits to disabled and restarted backend"
            }
            trap restore EXIT

            sed -i \
                -e 's/^CASH_WALLET_RATE_LIMIT_PER_HOUR=.*/CASH_WALLET_RATE_LIMIT_PER_HOUR=10/' \
                -e 's/^CASH_CLAIM_CALLS_PER_HOUR=.*/CASH_CLAIM_CALLS_PER_HOUR=120/' \
                -e 's/^CIRCLE_WALLET_RATE_LIMIT_PER_HOUR=.*/CIRCLE_WALLET_RATE_LIMIT_PER_HOUR=3/' \
                .env
            restarted_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
            {{DOCKER_COMPOSE_DEV}} restart backend >/dev/null
            echo "waiting for backend to come back up with real rate limits..."
            for i in $(seq 1 60); do
                {{DOCKER_COMPOSE_DEV}} logs backend --since "$restarted_at" 2>&1 | grep -q "http server started" && break
                sleep 2
            done

            INTEGRATION_RUN_RATE_LIMIT_TESTS=1 go test -tags integration -count=1 -timeout 3m \
                -run 'TestCashHubs/RateLimiting_EleventhCreateIsRateLimited|TestClaimFunds/RateLimiting_TwentyFirstClaimIsRateLimited|TestAudit_CashTransferRateLimit_PerWallet' \
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
      integration-ratelimits  the 3 tests needing real (nonzero) NWC rate limits
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
