<img alt="Lokihub Logo" src="./frontend/src/assets/loki-light-head.svg" width="200" align="right" />



Lokihub allows you to control your Flokicoin Lightning node or wallet from any other application that supports [NWC](https://nwc.dev/).

The application can run in two modes:

- Desktop (Wails app): Mac, Windows, Linux
- HTTP (Web app): Docker, Linux, Mac, Windows

Ideally the app runs 24/7 (on a node, VPS or always-online desktop/laptop machine) so it can be connected to a lightning address and receive online payments.

## Self Hosted
 
Go to the [Deploy it yourself](#deploy-it-yourself) section below.


## Development

### Required Software

- Go
- Node
- NPM
- Yarn

### Environment setup

    $ cp .env.example .env
    # edit the config for your needs (Read further down for all the available env options)
    $ vim .env

### Server (HTTP mode)

1. Run `tWallet` (Flokicoin Terminal Wallet). FLND (Flokicoin Lightning Network Daemon) is built-in to `tWallet` and is the only way to access it. Uncomment the FLND section in your `.env` file to connect.

2. Compile the frontend or run `touch frontend/dist/tmp` to ensure there are embeddable files available.

3. `go run cmd/http/main.go`

### React Frontend (HTTP mode)

Go to `/frontend`

1. `yarn install`
2. `yarn dev`

### HTTP Production build

    $ yarn build:http

If you plan to run Lokihub on a subpath behind a reverse proxy, you can do:

    $ BASE_PATH="/hub" yarn build:http

### Wails (Backend + Frontend)

_Make sure to have [wails](https://wails.io/docs/gettingstarted/installation) installed and all platform-specific dependencies installed (see wails doctor)_

    $ wails dev -tags "wails"

_If you get a blank screen, try running in your normal terminal (outside of vscode, and make sure HTTP frontend is not running)_

#### Wails Production build

    $ wails build -tags "wails"

### Build and run locally (HTTP mode)

    $ mkdir tmp
    $ go build -o main cmd/http/main.go
    $ cp main tmp
    $ cp .env tmp
    $ cd tmp
    $ ./main

### Run dockerfile locally (HTTP mode)

    $ docker build . -t nwc-local --progress=plain
    $ docker run -v $(pwd)/.data/docker:/data -e WORK_DIR='/data' -p 1610:1610 nwc-local

### Testing

    $ go test ./...

#### Test matching regular expression

    $ go test ./... -run TestHandleGetInfoEvent

#### Testing with PostgreSQL

By default, sqlite is used for testing. It is also possible to run the tests with PostgreSQL.

The tests use [pgtestdb](https://github.com/peterldowns/pgtestdb) to set up a temporary PostgreSQL database, which requires a running PostgreSQL server. Follow your OS instructions to install PostgreSQL, or use the official [Docker image](https://hub.docker.com/_/postgres).

See the [docker compose file](./tests/db/postgres/docker-compose.yml) for an easy way to get started.

When PostgreSQL is installed and running, set the `TEST_DATABASE_URI` environment variable to the PostgreSQL connection string. For example:

    $ export TEST_DATABASE_URI="postgresql://user:password@localhost:5432/postgres"

Note that the PostgreSQL user account must be granted appropriate permissions to create new databases. When the tests complete, the temporary database will be removed.

**Do not** use a production database. It is preferable to launch a dedicated PostgreSQL instance for testing purposes.

#### Mocking

We use [testify/mock](https://github.com/stretchr/testify) to facilitate mocking in tests. Instead of writing mocks manually, we generate them using [vektra/mockery](https://github.com/vektra/mockery). To regenerate them, [install mockery](https://vektra.github.io/mockery/latest/installation) and run it in the project's root directory:

    $ mockery

Mockery loads its configuration from the .mockery.yaml file in the root directory of this project. To add mocks for new interfaces, add them to the configuration file and run mockery.

### Profiling

The application supports both the Go pprof library and the DataDog profiler.

#### Go pprof

To enable Go pprof, set the `GO_PROFILER_ADDR` environment variable to the address you want the profiler to be available on (e.g. `localhost:6060`).

Now, you should be able to access the pprof web interface at `http://localhost:6060/debug/pprof`.

You can use the `go tool pprof` command to collect and inspect the profiling data. For example, to profile the application for 30 seconds and then open the pprof web UI, run:

```sh
go tool pprof -http=localhost:8081 -seconds=30 http://localhost:6060/debug/pprof/profile
```

For more information on the Go pprof library, see the [official documentation](https://pkg.go.dev/net/http/pprof).

### Versioning

    $ go run -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag=v0.1.0'" cmd/http/main.go

## Optional configuration parameters

The following configuration options can be set as environment variables or in a .env file

- `RELAY`: can support multiple separated by commas
- `DATABASE_URI`: A sqlite filename or postgres URL.
- `PORT`: The port on which the app should listen on
- `WORK_DIR`: Directory to store NWC data files.
- `LOG_LEVEL`: Log level for the main application. Higher is more verbose.
- `NETWORK`: On-chain network used for the node.
- `AUTO_UNLOCK_PASSWORD`: Provide unlock password to auto-unlock Lokihub on startup (e.g. after a machine restart). Unlock password still be required to access the interface.
- `LOKIHUB_SERVICES_URL`: The URL for Lokihub's backend services API.
- `LOKIHUB_STORE_URL`: The URL for Lokihub's App Store.
- `ESPLORA_SERVER`: The Esplora server URL.
- `SWAP_SERVICE_URL`: The swap service URL.
- `REBALANCE_SERVICE_URL`: The rebalance service URL.
- `ENABLE_REBALANCE`: Enable rebalance feature (default: true).
- `ENABLE_SWAP`: Enable swap feature (default: true).
- `MESSAGEBOARD_NWC_URL`: The Nostr Wallet Connect URL for the messageboard.
- `MEMPOOL_API`: The Flokicoin Explorer API URL.
- `LOG_TO_FILE`: Whether to log to a file.
- `ENABLE_ADVANCED_SETUP`: Enable advanced setup options.
- `LOG_DB_QUERIES`: Log database queries.
- `BASE_URL`: Base URL for the application.
- `FRONTEND_URL`: URL for the frontend.
- `GO_PROFILER_ADDR`: Address for the Go profiler.


### Migrating the database (Sqlite <-> Postgres)

Migration of the database is currently experimental. Please make a backup before continuing.

#### Migration from Sqlite to Postgres

1. Stop the running hub
2. Update the `DATABASE_URI` to your destination e.g. `postgresql://myuser:mypass@localhost:5432/nwc`
3. Run the migration:

   go run cmd/db_migrate/main.go -from .data/nwc.db -to postgresql://myuser:mypass@localhost:5432/nwc

## Node-specific backend parameters

### FLND Backend parameters

_To configure via env, the following parameters must be provided:_

- `FLND_ADDRESS`: the FLND gRPC address, eg. `localhost:10005`
- `FLND_CERT_FILE`: the location where FLND's `tls.cert` file can be found
- `FLND_MACAROON_FILE`: the location where FLND's `admin.macaroon` file can be found

## Application deeplink options

### `/apps/new` deeplink options

Clients can use a deeplink to allow the user to add a new connection. Depending on the client this URL has different query options:

#### NWC created secret

The default option is that the NWC app creates a secret and the user uses the nostr wallet connect URL string to enable the client application.

##### Query parameter options

- `name`: the name of the client app

Example:

`/apps/new?name=myapp`

#### Client created secret

If the client creates the secret the client only needs to share the public key of that secret for authorization. The user authorized that pubkey and no sensitivate data needs to be shared.

##### Query parameter options for /new

- `name`: the name of the client app
- `pubkey`: the public key of the client's secret for the user to authorize
- `return_to`: (optional) if a `return_to` URL is provided the user will be redirected to that URL after authorization. The `lud16`, `relay` and `pubkey` query parameters will be added to the URL.
- `expires_at` (optional) connection cannot be used after this date. Unix timestamp in seconds.
- `max_amount` (optional) maximum amount in millis that can be sent per renewal period
- `budget_renewal` (optional) reset the budget at the end of the given budget renewal. Can be `never` (default), `daily`, `weekly`, `monthly`, `yearly`
- `request_methods` (optional) url encoded, space separated list of request types that you need permission for: `pay_invoice` (default), `get_balance` (see NIP47). For example: `..&request_methods=pay_invoice%20get_balance`
- `notification_types` (optional) url encoded, space separated list of notification types that you need permission for: For example: `..&notification_types=payment_received%20payment_sent`
- `isolated` (optional) makes an isolated app connection with its own balance and only access to its own transaction list. e.g. `&isolated=true`. If using this option, you should not pass any custom request methods or notification types, nor set a budget or expiry.

Example:

`/apps/new?name=myapp&pubkey=47c5a21...&return_to=https://example.com`

#### Web-flow: client created secret

Web clients can open a new prompt popup to load the authorization page.
Once the user has authorized the app connection a `nwc:success` message is sent to the webview (using `dispatchEvent`) or opening page (using `postMessage`) to indicate that the connection is authorized.

## Help

If you need help, please visit our community chat on Discord: [flokicoin.org/discord](https://flokicoin.org/discord).


## NIP-47 Supported Methods

✅ NIP-47 info event

❌ `expiration` tag in requests


✅ `get_info`

✅ `get_balance`

✅ `pay_invoice`

- ⚠️ amount not supported (for amountless invoices)
- ⚠️ PAYMENT_FAILED error code not supported

✅ `pay_keysend`

- ⚠️ PAYMENT_FAILED error code not supported

✅ `make_invoice`

✅ `lookup_invoice`

- ⚠️ NOT_FOUND error code not supported

✅ `list_transactions`

- ⚠️ from and until in request not supported
- ⚠️ failed payments will not be returned

✅ `multi_pay_invoice`

- ⚠️ amount not supported (for amountless invoices)
- ⚠️ PAYMENT_FAILED error code not supported

✅ `multi_pay_keysend`

- ⚠️ PAYMENT_FAILED error code not supported


## Lokihub Architecture

### NWC Wallet Service

At a high level Lokihub is an [NWC](https://nwc.dev) wallet service which allows users to use their single wallet seamlessly within a multitude of apps(clients). Any client that supports NWC and has a valid connection secret can communicate with the wallet service to execute commands on the underlying wallet (internally called LNClient).

### LNClient

The LNClient interface abstracts the differences between wallet implementations and allows users to run Lokihub with their preferred wallet, which currently is FLND.

### Transactions Service

Lokihub maintains its own database of transactions to enable features like self-payments for isolated app connections (sub-wallets), additional metadata (that apps can provide when creating invoices or making keysend payments), and to associate transactions with apps, providing additional context to users about how their wallet is being used across apps.

The transactions service sits between the LNClient and two possible entry points: the NIP-47 handlers, and our internal API which is used by the Lokihub frontend.

### Event Publisher

Internally Lokihub uses a basic implementation of the pubsub messaging pattern which allows different parts of the system to fire or consume events. For example, the LNClients can fire events when they asynchronously receive or send a payment, which is consumed by the transaction service to update our internal transaction database, and then fire its own events which can be consumed by the NIP-47 notifier to publish notification events to subscribing apps.

#### Published Events

    - `nwc_started` - when Lokihub process starts
    - `nwc_stopped` - when Lokihub process gracefully exits
    - `nwc_node_started` - when Lokihub successfully starts or connects to the configured LNClient.
    - `nwc_node_start_failed` - The LNClient failed to sync or could not be connected to (e.g. network error, or incorrect configuration for an external node)
    - `nwc_node_stopped` the LNClient was gracefully stopped
    - `nwc_node_stop_failed` - failed to request the node to stop. Ideally this never happens.
    - `nwc_node_sync_failed` - the node failed to sync onchain, wallet or fee estimates.
    - `nwc_unlocked` - when user enters correct password (HTTP only)
    - `nwc_channel_ready` - a new channel is opened, active and ready to use
    - `nwc_channel_closed` - a channel was closed (could be co-operatively or a force closure)
    - `nwc_backup_channels` - send a list of channels that can be used as a SCB.
    - `nwc_outgoing_liquidity_required` - when user tries to pay an invoice more than their current outgoing liquidity across active channels
    - `nwc_incoming_liquidity_required` - when user tries to creates an invoice more than their current incoming liquidity across active channels
    - `nwc_permission_denied` - a NIP-47 request was denied - either due to the app connection not having permission for a certain command, or the app does not have insufficient balance or budget to make the payment.
    - `nwc_payment_failed` - failed to make a lightning payment
    - `nwc_payment_sent` - successfully made a lightning payment
    - `nwc_payment_received` - received a lightning payment
    - `nwc_hold_invoice_accepted` - accepted a lightning payment, but it needs to be cancelled or settled
    - `nwc_hold_invoice_canceled` - accepted hold payment was explicitly cancelled
    - `nwc_budget_warning` - successfully made a lightning payment, but budget is nearly exceeded
    - `nwc_app_created` - a new app connection was created
    - `nwc_app_deleted` - a new app connection was deleted
    - `nwc_lnclient_*` - underlying LNClient events, consumed only by the transactions service.
    - `nwc_swap_succeeded` - successfully made a boltz swap
    - `nwc_rebalance_succeeded` - successfully rebalanced channels
    - `nwc_payment_forwarded` - successfully forwarded a payment and earned routing fees

### NIP-47 Handlers

Lokihub subscribes to a standard Nostr relay and listens for whitelisted events from known pubkeys and handles these requests in a similar way as a standard HTTP API controller, and either doing requests to the underling LNClient, or to the transactions service in the case of payments and invoices.

### Frontend

The Lokihub frontend is a standard React app that can run in one of two modes: as an HTTP server, or desktop app, built by Wails. To abstract away, both the HTTP service and Wails handlers pass requests through to the API, where the business logic is located, for direct requests from user interactions.

#### Authentication

Lokihub uses simple JWT auth in HTTP mode, which also allows the HTTP API to be exposed to external apps, which can use Lokihub's API to have access to extra functionality currently not covered by the NIP-47 spec, however there are downsides - this API is not a public spec, and only works over HTTP. Therefore, apps are recommended to use NIP-47 where possible.

### Encryption

Sensitive data such as the seed phrase are saved AES-encrypted by the user's unlock password, and only decrypted in-memory in order to run the lightning node. This data is not logged and is only transferred over encrypted channels, and always requires the user's unlock password to access.

All requests to the wallet service are made with one of the following ways:

- NIP-47 - requests encrypted by NIP-04 using randomly-generated keypairs (one per app connection) and sent via websocket through the configured relay.
- HTTP - requests encrypted by JWT and ideally HTTPS (except self-hosted, which can be protected by firewall)
- Desktop mode - requests are made internally through the Wails router, without any kind of network traffic.

## Attribution

This project is based on Alby Hub (https://github.com/getAlby/hub), licensed under the Apache License, Version 2.0. Modifications have been made.

