# Changelog

## [0.5.0]

Adds the Cash Hub — multi-recipient cash bills served over NIP-CASH's private
transport — and moves Circles onto member-generated connection keys and NIP-51
follow sets. Hardens money-safety and privacy across both, and restricts the
hub to services the user has explicitly selected.

### Added

- Adds the Cash Hub: it issues multi-recipient cash bills, each held by a secret
  or bound to a Nostr identity and shared as a `lokicash1…` token. Recipients
  can split (`cash_transfer`), merge (`cash_consolidate`), read (`cash_status`)
  and redeem to Lightning (`cash_redeem`), with per-slice redeem fees, a
  configurable expiry ceiling and optional mint provenance. (#90)
- Serves the bill methods over NIP-CASH's private transport, so a holder's bills
  are no longer a public, linkable set on the standard NWC connection. (#111)
- Adds Circle Wallets (NIP-CW): each member generates their own connection key
  and joins from a NIP-51 follow set, so the hub never holds a member's secret.
  Wallets carry per-member budgets and receive ceilings, a lifecycle (Active,
  Suspended, Expired, Closing) with a grace period, operator-driven recovery
  (key rotation and freeze) and self-service `delete_circle_wallet`. (#158)
- Sells a Cash Hub's cash over NIP-CO Order connections with configurable
  Lightning and on-chain terms; a paid order is minted into bills automatically
  and tracked through its lifecycle. (#158)
- Pays a Cash Hub out on-chain: a slice can be redeemed to an on-chain address,
  with per-Hub withdraw fees and an operator screen for the payout engine. (#160)
- Lists a circle as a public NIP-CASH offer (kind-31190) with its own
  `circlehub1` connection, republished on change and retired cleanly. (#158)
- Builds the LSP list from NIP-LSP announcements (kind 10735): the operator ticks
  the LSPs they trust, and every purchase live-checks an LSP's terms against its
  announcement before buying. Replaces the old `services.json`/`LSP`-setting
  list. (#158)
- Adds a built-in fiat-rate oracle: the fiat rate comes from a signed NIP-78
  event published by the oracle in the seed, replacing the `rates.json` fetch and
  degrading to sats/FLC until a fresh rate is available. (#193)
- Folds the former flspd LSP daemon into lokihub as an optional standalone
  `lspd`, with a Hub attach bridge and a backoffice UI. (#172)
- Adds zap transfers (NIP-CZ): a `cash_transfer` can spin a slice off as a
  zap. (#158)
- Adds operator screens for cash and circle hubs: a per-Cash-Hub monitoring page
  (outstanding liability, flow, fees earned, median time to redeem), settings
  editors, and a delete-check that lists what blocks a hub from being
  deleted. (#103)
- Adds a profile page for the hub's own Nostr identity (kind-0 profile, QR
  codes, recent notes, an edit form and plain-text posting); signing stays on
  the backend so the node key never reaches the browser. (#129)
- Accepts `nconnection1…`, npub or NIP-05 for recipients, and shows saved
  Identity Authorities by profile instead of a hex pubkey. (a3ae647d)
- Warns at start when flnd runs without `keysend-hold-time`, which Circle
  Wallets need to refuse a keysend over a member's cap. (#170)
- Runs the Windows desktop app frameless with its own window controls, and hides
  the macOS title bar. (8a3e6e7b)
- Adds an "Open data folder" button to the desktop app's startup-error and
  critical-error screens, so the app-data folder (database, config, logs) can
  be opened when something goes wrong. (#215)
- Accepts NIP-47 Pre-Authorisation grants for read methods on the private
  transport, so a holder can delegate read access such as `cash_status` to
  another key without handing over its own slice proof; a grant marked
  single-use is spent on first use and a replay is answered with silence. (#N)

### Changed

- Reorganizes the Cash Hub creation form into clearer sections — the per-bill
  cap and expiry stay prominent while the economics knobs (min transfer, redeem
  fee, retention) move under one Advanced section — and keeps the Create button
  disabled until a name and budget are set.
- Reorganizes the Circle Wallet creation form so the wallet budget, renewal,
  expiry and fee limits move out of the collapsed Advanced section into a
  visible Wallet limits panel, leaving only the hub-wide caps as advanced.
- Adds a live membership preview beside the Circle Wallet creation form that
  summarizes who publishes the follow set and lists its members as you edit.
- Relabels the Cash Hub surface from "wallet" to "Cash Bill" across its
  dashboard, creation and edit views, so the operator UI matches the cash-bill
  model it serves. (#N)
- Drives on-chain amount and unit labels from the hub's native-asset descriptor
  (now seeded and served on `/api/hub/info`) instead of a hardcoded Flokicoin
  unit, and de-brands the block-explorer label and chain-name strings to
  `{{chain}}`/`{{unit}}` tokens, so the UI follows the configured chain. (#N)
- Pages the startup wallet seeders and bounds the stale-outgoing transaction
  sweep to a fixed batch per tick, so a hub with many apps or a large pending
  backlog starts and sweeps in bounded work instead of loading every app at
  once. (#N)
- References nmilat's NIP-47 kind constants directly instead of redeclaring
  them, keeping lokihub's method and notification kinds aligned with the
  protocol library. (#N)
- Breaking: renames the chain-branded hub-info/rate API to chain-neutral names
  (`/api/loki/rates` → `/api/rates`, `/api/loki/info` → `/api/hub/info`,
  `/api/loki/faq` → `/api/hub/faq`) so lokihub reads generically across
  chains. (#204, #205)
- Breaking: moves Circles to NIP-CW — members join from a NIP-51 follow set with
  their own connection keys, so one account can run several circles. Circles on
  the old `following`/kind:3 policy are deleted on upgrade. (#139, #124)
- Breaking: refuses to start on a database created before 0.5.0's final schema
  and no longer migrates older databases; reset the database before
  upgrading. (#158)
- Breaking: renames NIP-CASH's bearer mode to cash mode across every cash method
  and the admin API. A migration rewrites stored rows, so existing slices stay
  redeemable with the secret their holder already has. (#102)
- Breaking: moves the NIP-CASH and NIP-CW per-call proofs onto their own event
  kinds (23198 and 23199), off the shared kind 35521. (#90)
- Renames the built-in "Ohstr Relay" relay suggestion to "Ohstr" and lists it
  last in the setup relay picker. (#215)
- Redesigns the desktop tray/menu-bar icon as a larger black-and-white Loki
  face, dropping the sunburst so it reads bigger and stays legible on both light
  and dark menu bars. (#216)
- Updates `flnd` to v0.2.6-rc.1, `flndecodepay` to v1.0.6-rc.1 and `go-flokicoin`
  to v0.26.4-rc.1, and pins `google.golang.org/grpc` to v1.83.2. (#217)

### Removed

- Removes the on-chain ⇄ Lightning swap feature entirely, including its screens,
  the `/api/swaps` and `/api/autoswap` routes, the swap seed and the
  `SWAP_SERVICE_URL`/`ENABLE_SWAP` settings. (#189)
- Removes the Lightning message-board feature entirely, including its widget,
  setup and `MESSAGEBOARD_NWC_URL`/`ENABLE_MESSAGEBOARD_NWC` settings. (#191)
- Removes the remote `lokihub-services` fetch: the explorer/relay suggestions,
  currencies, FAQ and info now ship embedded in the binary, so a fresh hub works
  offline and the catalogue cannot drift from its release
  (`LOKIHUB_SEED_FILE` overrides them). (#193)

### Fixed

- Fixes the LSP order-detail timeline and the LSPS1 `get_order` `funded_at`,
  which stamped every lifecycle event with the order's creation time; the
  per-state transition times (paid, funded, completed) are now persisted and
  reported as the real times.
- Falls back to the LSPS1 payment state when an LSP returns an empty
  `order_state`, so the channel-order screen never shows a blank status.
- Shows the LSP dashboard's liquidity as unavailable (—) when the node or its
  wallet balance cannot be reached, instead of a placeholder 5 FLC figure.
- Shows a loading indicator instead of a raw "TODO" debug string on the channel
  order screen when an order is in an unhandled transitional state.
- Returns a proper internal error from the LSP's LSPS1 `get_order` when the order
  lookup fails for a reason other than the order not existing, instead of always
  reporting "order not found".
- Lets a `connection_key` recipient read its own `cash_status` row by presenting
  its slice attestation; such a recipient previously got `NOT_FOUND` under every
  scope because its signer is never the slice's identity value. (#N)
- Spends a `cash_transfer`/`cash_consolidate` `request_id` before applying the
  rate limit, so a rate-limited retry can no longer replay the same request. (#N)
- Omits `create_circle_wallet` from a connection's offered methods when the
  target is not a circle hub, so a client is not shown an action the hub would
  reject. (#N)
- Retries a paid or minted on-chain order lookup during a chain-scan outage
  instead of reporting a stale "window not ended" result, so a transient failure
  to read the chain is surfaced as retryable rather than a wrong answer. (#N)
- Fixes the desktop tray/menu-bar icon rendering clipped into a corner at small
  sizes (a non-zero SVG viewBox origin), and renders it as a template image that
  tints to both light and dark macOS menu bars. (#N)
- Connects to a node that reports its network as `main` or `test` (the short
  names lokid and Bitcoin Core use); such a node was refused at startup with a
  spurious "the node is on another chain" error. (#213)
- Surfaces a hint and an escape (open the data folder, reload) on the desktop
  "Starting…" screen after a couple of minutes, so a backend that stays
  reachable but never reports started or failed — e.g. a node connection that
  hangs — no longer leaves the screen spinning with no feedback. (#215)
- Fixes upgrading from 0.3.0-alpha failing at startup with "no such column:
  parent_kind". (#98)
- Shows a desktop error screen when the backend fails to start instead of
  exiting silently, and times out UI data requests instead of hanging
  forever. (#98)
- Encrypts the flnd admin macaroon at rest, strengthens backup-key derivation to
  argon2id, and refuses an unverified-TLS connection to a non-loopback
  flnd. (#174)
- Makes logout actually end a session by revoking the token server-side, and
  caps a token's lifetime, so a leaked token can be killed without rotating the
  unlock password. (#176)
- Stops a read-only token from escalating to full access through the app log and
  from reading the swap seed. (#176)
- Gates the desktop (Wails) IPC router behind the unlock session, so a read-only
  or locked app can no longer move funds or export a seed. (#180)
- Scopes `cancel_hold_invoice` and `settle_hold_invoice` to the app that owns the
  hold invoice, so one connection can no longer settle another app's hold invoice
  by its public payment hash. (#178)
- Rate-limits `/api/unlock`, `/api/start`, `/api/backup` and `/logout` by the
  connection's own address, instead of a spoofable `X-Forwarded-For`. (#160)
- Publishes the dev stack's ports on the loopback only unless `DEV_BIND` says
  otherwise; they had been open on every interface. (#160)
- Refuses to start on a chain other than the one `NETWORK` names, or on an
  unknown `NETWORK` value. (#160)
- Lets the operator rotate the private-transport inbox key, so a leaked inbox
  key can be replaced. (#160)
- Fixes a Circle Wallet spend-cap bypass where a member could disable their own
  cap with a sub-loki `max_amount`, plus smaller audit findings. (#90)
- Moves every member wallet's balance and the hub's own balance to your main
  balance as recorded transfers when you delete a Circle Hub with "Delete All";
  before, those funds vanished with no record. (#148)
- Updates dependencies for published advisories, including react-router v7 (an
  open redirect, GHSA-wrjc-x8rr-h8h6) and Go 1.26.10 (a certificate-verification
  flaw, GO-2026-6809). (#134, #152)

### Security

- Breaking: lokihub makes no outbound connection to an external URL unless the
  user selected it under Settings → Services — nothing is hardcoded, not even
  relays, and a fresh hub connects to nothing until the user picks services.
  Profile images are off by default, NIP-05 verifies only on an explicit action,
  and the app store and explorer links use only user-selected URLs. (#208, #209, #210)
- Posts the app-connection success message to an explicit origin, never a
  wildcard, so connection metadata cannot leak to an unintended opener. (#201)
- Routes externally-supplied navigation (an LNURL `successAction.url`) through
  the scheme guard, so a `javascript:` or `data:` URL is shown as text rather
  than navigated. (#201)
- Authenticates the migration backup archive with AES-256-GCM keyed by argon2id,
  so a tampered archive or wrong password fails cleanly instead of restoring
  corrupt state; existing backups still restore. (#195)
- Hardens the at-rest key-encryption KDF to Argon2id (RFC 9106); secrets wrapped
  with the old parameters re-wrap transparently on the next unlock. (#195)
- Rejects stale or replayed LSPS5 webhook notifications, so a captured
  notification can no longer re-trigger a wallet sync. (#197)
- Treats order and circle-hub connections as scope- and budget-immutable in the
  admin edit path, so an admin edit cannot grant a fund-moving scope to a
  connection whose secret is held by an external party. (#197)
- Bounds every lspd attach response the Hub buffers, so a compromised or MITM'd
  lspd can no longer return a huge response and OOM the Hub. (#197)

## [0.4.0]

Adds Circles and JIT Wallets, two ways to share your node's Lightning liquidity
with other people.

### Added

- Adds Circles: each member gets a personal wallet with its own balance,
  backed by your node's channels. Transfers inside a circle are fee-free, and
  membership is self-service through an allowlist or nostr follows.
- Adds JIT Wallets: fund once, and each recipient claims their capped share
  with their own invoice when they are ready.
- Continues RTL and translation polish across the app.

### Changed

- Moves backups to a stronger, versioned encryption format. Backups from
  earlier versions still restore.
- Redesigns the connection details view with clearer copy buttons.

### Fixed

- Fixes the AutoSwap "Deactivate" button not turning off auto swap-out.

## [0.3.0-alpha]

Lets Lokihub keep running in the background, with a system tray icon.

### Added

- Keeps Lokihub running after you close the main window. Quit it from the
  tray icon menu.
- Adds a system tray icon on macOS, Linux and Windows.
- Shows the macOS dock icon only while the window is open.
- Adds a Help and FAQ section, translated into additional languages.

### Changed

- Refines navigation, headers and transaction lists for consistency across
  screen sizes.

### Fixed

- Keeps an unexpected error in one area from taking down the whole interface.
- Links payments more accurately to the app or service that made them.

## [0.2.0-alpha]

Adds seven languages, with full right-to-left layouts for Arabic and Farsi.

### Added

- Adds English, Spanish, Japanese, Korean, Chinese, Arabic and Farsi, with a
  language switcher.
- Adds full RTL layouts for Arabic and Farsi, with bundled Noto Sans Arabic and
  Vazirmatn fonts.
- Adds a JIT payment explanation dialog and per-app FLC/Loki amount presets.

## [0.1.2-alpha]

Pre-release for testing and feedback.
