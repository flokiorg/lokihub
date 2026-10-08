# Changelog

## [0.5.0]

_Cut as `0.5.0-rc.10` on 2026-10-08._ This section stays open: 0.5.0 itself has
not been released, so everything under it is still pre-release and
accumulating. rc.1 introduced the Cash Hub — bearer and identity-bound
multi-recipient cash tokens, transfer/consolidate/redeem, mint provenance —
on top of 0.4.0's Circles and JIT Wallets. rc.2 through rc.8 are three
rounds of security/financial/disclosure audit against that surface, moving
bill methods onto NIP-CASH's private transport, closing a real order-forgery
gap in LSPS5 notifications and several money-safety and disclosure bugs
around redemption, fees and the cash-to-identity split. rc.9 just picks up
the published `nmilat` SDK the audited wire changes already depend on.
rc.10 gives the hub's own Nostr identity a profile page, surfaces whether
the private-transport announcement actually published, and tidies Debug
Tools and the dashboard overview cards.

### Added

- Cash Hub: bearer (identity-less) multi-recipient cash tokens, paired with a
  bech32 `lokicash1...` token. (#90)
- `cash_transfer` for splitting a slice off a cash wallet, `cash_consolidate`
  for combining several slices this node custodies into one new token,
  `list_recipients` for enumerating a wallet's slices. (#90)
- Per-slice redeem fees (`redeem_fee_ppm`) and a configurable expiry
  ceiling, including no-expiry (`MaxExpSecs == 0`). (#90)
- Optional mint provenance: a recoverable-ECDSA node signature attesting a
  token's minter and amount, across `mint_cash`, `cash_transfer`,
  `cash_consolidate`, and the admin HTTP mint path. (#90)
- New integration suite driving Cash Hub / Circle Hub through the real
  `nmilat` client SDK, proving wire-compatibility with the published
  caller-side library. (#90)
- `cash_consolidate` accepts `pubkey`, `connection_key`, or `bearer`
  targets, and `pubkey` or `connection_key` sources (each independently
  proven; connection_key sources require a live-trusted attestation,
  checked once per call to close a TOCTOU window across a multi-source
  batch). Bearer sources remain deferred.
- Recipients can be addressed by `nconnection1...`, `npub`, or NIP-05 in the
  Cash UI, not just raw hex pubkeys.
- Saved Identity Authorities show their npub, profile picture, display
  name, and NIP-05 instead of a raw hex pubkey.
- Windows desktop app: runs frameless, with its own minimal
  minimize/maximize/close bar instead of the native title bar.
- macOS desktop app: the native title bar is hidden and content extends
  full-size, keeping only the traffic-light buttons — the same look used by
  apps like VS Code and Slack.
- New Cash Hub monitoring view at `/cash-hub/:id`: outstanding liability,
  flow, fees earned and median time to redeem, over charts of liability and
  daily movement, then the bills themselves — one row per bill, expanding
  to its recipients, archived alongside live. (#103)
- New per-hub stats endpoint: outstanding liability, flow, fees earned,
  median time to redeem and a daily series. (#103)
- Settings › Services › NWC relays now exposes whether this hub runs its
  own relays, previously reachable only by editing the database. (#103)
- `get_info` reports the bill methods under a new `private_methods` field,
  since `methods` means "callable here" and these are not — so a client
  could previously only learn they exist by hardcoding the list. Presence
  here is informational and never permission.
- `mint_cash` accepts an optional `idempotency_key`, so a caller that
  retried after a timeout no longer mints and funds a second wallet. A
  repeat is refused rather than replayed, because a cash-mode secret exists
  only in the reply the caller missed; a failed mint releases its key.
- A node profile page for the hub's own Nostr identity: its published
  kind-0 profile (banner, avatar, bio, NIP-05, lightning address) with QR
  codes for its npub and nprofile, its recent notes, and an edit form that
  publishes a new profile. Signing stays on the backend; the node's key
  never reaches the browser.
- Settings › Services shows whether the hub's private-transport
  announcement published — how many relays accepted it, and the last
  error if not. A client could previously see only that it was missing. The
  node's hex, npub and nprofile also appear under Channels.

### Changed

- Migrated onto `nmilat`'s own published `nipcash`/`nipcw` request/response
  types and method-name constants instead of maintaining parallel copies.
  Bumped `github.com/ohstr/nmilat` from v0.3.0 to v0.4.0 (cash-mode rename)
  to v0.5.0-rc.2 (private transport becomes client-usable) to v0.5.0-rc.3
  and on, dropping the temporary local `replace` directive used while
  wire changes were still unpublished at each step. (#102)
- `cash_transfer`'s partial split mints two fresh wallets (carved and
  remainder) instead of shrinking the source in place — a wallet's
  committed amount is immutable everywhere. (#90)
- NIP-CASH's and NIP-CW's per-call proofs moved off the shared, colliding
  kind 35521 onto their own kinds (23198 / 23199). (#90)
- Swap claim/refund transactions broadcast through the node's own FLND
  connection instead of a third-party `lightz` API, and compute the txid
  locally from the signed transaction rather than trusting the
  broadcaster's response. (#93)
- `release.yaml` pulls all platform artifacts from the single aggregated
  `build-all.yaml` run instead of picking up stale artifacts left over from
  earlier ad-hoc `workflow_dispatch` runs. (#82)
- Hyphenated `VERSION` values (`-rc`, `-alpha`, `-beta`, ...) now publish as
  GitHub prereleases instead of stable releases.
- A bearer note's token and secret are merged into a single gift string
  instead of two separate values to copy/track.
- **NIP-CASH's bearer mode is now cash mode:** `identity_type: "bearer"`
  becomes `"cash"` and `bearer_secret` becomes `cash_secret` across every
  cash method and the admin API, so a cash-mode target's
  `new_identity_hash` binds over `"cash:" + commitment + ":"`. A migration
  rewrites stored rows and leaves the secret commitment untouched, so every
  slice minted before the rename stays redeemable with the secret its
  holder already has. Requires `nmilat` v0.4.0. (#102)
- Spent bills are archived instead of erased. The hard delete cascaded
  through the claims and the transaction ledger, so value that passed
  through a hub left no trace an operator could account for. (#103)
- `cash_redeem` and `cash_consolidate` delete a drained bill the way a split
  already did. Deleting on only some paths made the resulting `NOT_FOUND` a
  reliable signal that this hub issued a given bill and it was spent.
  (#103)
- Slice status splits into unclaimed, redeemed, split, expired, reclaimed
  and written-off; `claimed` still works as the legacy umbrella. (#103)
- Payout facts — payment hash, preimage and both fees — are recorded on a
  slice at redeem time, where previously nothing linked a payout back to
  its slice. (#103)
- The hub opens one NIP-47 subscription for every app wallet instead of one
  per wallet, which grew with bills in circulation rather than with users.
  (#103)
- **The four bill methods — `cash_status`, `cash_redeem`, `cash_transfer`,
  `cash_consolidate` — are served over the private transport only, and
  refused on the standard NIP-47 connection.** A standard-transport request
  p-tags the bill's own wallet pubkey, so a holder's bills were a public,
  linkable set however well the payload was encrypted; consolidating fifty
  bills published fifty requests resolving to one.
- `list_recipients` is gone; `cash_status` is the one name, in the
  dispatcher, the permission scopes and `get_info`.
- A redemption that would pay out nothing is refused instead of performed.
  The redeem fee saturates at the amount it is charged on, so a slice at or
  below the flat fee base quoted a payout of zero.
- A redeem fee now has a flat base as well as a proportional part, with the
  route reserve capped so payout plus reserve equals the slice. The hub
  could previously pay up to 10,000 mloki in routing to earn 1, on a route
  the holder chose.
- Converting a bill to cash mode after it has changed hands splits instead
  of reassigning in place, so a previous holder can no longer recover a
  bill they already sold.
- Bumped `nmilat` dependency to v0.5.0-rc.3. lokihub doesn't import the
  packages that version touches (the embeddable relay SDK and its
  `POST /query` HTTP bridge), so there is no behavior change here — this
  just keeps the SDK current.
- Bumped `nmilat` dependency to v0.5.0-rc.11. As above, none of the
  packages lokihub builds in changed between rc.5 and rc.11; the release
  carries relay-side NIP-29/NIP-42/NIP-43 fixes lokihub does not use.
- Analytics sit inside the Cash Hub and Circle Hub overview cards they
  explain, instead of as a separate section further down the page, and
  those cards show a skeleton while loading instead of nothing.
- Debug Tools: "Execute Node Command" is removed; Get App/Node Logs fetch
  straight away, with the maximum log length set from a settings icon on
  the response and remembered across reloads.
- The critical-error page no longer offers an "Update Configuration" form.

### Fixed

- Fixed a duplicate Linux server tarball filename collision across the
  legacy/modern build matrix legs that broke asset upload. (#84)
- 2026-08-31 security/financial/UX audit round (5 independent reviewers):
  fixed a High-severity Circle Wallet spend-cap bypass (a member could
  silently disable their own cap via a sub-loki `max_amount`) plus 7
  additional findings — fee-skim visibility, cash-specific expiry
  messaging, a silently-skipped rate-limit test, `cash_consolidate`
  dedup-guard granularity, 128-bit-safe fee-skim overflow handling, and
  more. (#90)
- Closed a real, currently-exploitable order-forgery gap in the LSPS5
  Nostr notification path: any trusted LSP's signed Nostr event could
  change the state of *any* order, not just ones it created. The ownership
  check that already existed on the HTTP webhook path now lives inside the
  shared function both transports call.
- `mint_cash` wallet creation is atomic — app creation, pubkey
  registration, and claim-row insertion happen in one DB transaction,
  closing a gap where a mid-creation failure could strand a partial,
  unfunded wallet row.
- Bumped `google.golang.org/grpc` to v1.83.2, fixing GO-2026-6348 (heap
  exhaustion via HTTP/2 DATA frame fragmentation), reachable through
  flnd's LND client and the HTTP service's shutdown path.
- Fixed the sidebar's logo/header rendering underneath the window controls
  instead of below them.
- macOS: fixed the native window's own corner background showing a
  mismatched default color instead of the app's actual theme.
- The desktop app no longer exits silently if the backend fails to start —
  it shows a dedicated error screen with the failure message instead of the
  window just vanishing.
- Fixed upgrading straight from a 0.3.0-alpha install to a 0.5.0 release
  candidate failing at startup with "SQL logic error: no such column:
  parent_kind (1)".
- Requests made by the UI's data views now time out instead of hanging
  forever if the backend stops responding.
- Fixed `wails dev` intermittently showing a blank window on cold start,
  caused by a race between Vite's dev server coming up and Wails' asset
  proxy reaching it.
- Fixed `cash_consolidate` delivering the merged wallet's token encrypted
  to the wrong key (`new_identity` instead of the calling connection), so a
  pubkey-mode consolidation's token couldn't be decrypted by its intended
  recipient.
- A cash bill never publishes a kind-13194 info event, which previously
  outlived the bill as a permanent, unauthenticated answer to "did this hub
  serve this pubkey", defeating the deletion that makes a spent bill
  unfindable. Also fixes info-event deletion being silently skipped for
  every app kind. (#103)
- Fixed a concurrent unclaim restoring a slice between the drain check and
  the delete, which could destroy a bill that still backed live value.
  (#103)
- Fixed a failed delete stranding a wallet forever, invisible to both the
  sweep and manual retries. (#103)
- Deleting an app no longer turns a racing request into a 30-second hang.
  (#103)
- Outstanding no longer counts bills whose redeem window has passed but
  which the sweep has not yet collected. (#107)
- The outstanding chart now meets the figure printed above it; it
  previously accumulated from zero over a 30-day window and ignored splits
  and write-offs entirely. (#107)
- Creating a Cash Hub no longer claims its connection can be viewed again
  later — a hub's pairing secret is random and never stored, unlike a
  bill's. (#107)
- `cash_status` no longer answers a caller holding no slice. On a bill
  carrying any `connection_key` recipient, possession of the token alone
  read the whole roster.
- A `connection_key` recipient can read their own row again, via the
  Identity Authority attestation `cash_redeem` already requires.
- `cash_status` honours its scope, and co-recipients are no longer
  disclosed by default.
- Circle join details are nested so co-members cannot read them,
  `sign_message` refuses mint-provenance payloads, and a read-only token
  can no longer read the swap seed.
- An unknown bill, an unverifiable proof and an unserved method are all
  answered by the same silence, and a bill proof is checked before the
  bill is looked up so a miss costs what a hit costs.
- A proof can no longer be lifted out of its envelope and replayed in
  another: a nonce stays burnt as long as a proof bound to it can still
  verify.
- An oversize reply is detected wherever it falls in the batch, not only
  when it happens to come first.
- One envelope can no longer burn a shared bill's whole hourly claim
  budget, nor spend it on items that failed validation.
- A lapsed invoice no longer pins its wallet, hub and identity forever,
  and invoices on hold are counted.
- Both compensating rollbacks check the wallet is actually empty before
  deleting it, rather than trusting that the reversal call returned
  without error.
- A bill reassigned from cash to an identity no longer keeps a stale
  "identity required" hint that made it unreceivable by its rightful
  holder.
- An Identity Authority attestation is verified once per request instead
  of once per claim, so a bill with many recipients no longer costs a
  verification each.
- `db_migrate` copies all 27 tables. It copied six and reported success:
  the schema check only asserts a table exists, and the rest were created
  empty at the destination — so transactions migrated while the claims
  that record who owns what did not.
- Two throughput-measurement tests now skip under `-race`, where the race
  detector's 5-20x slowdown made them compare a measured rate against a
  threshold that assumed full speed, independent of any real regression.
- A backend outage after the app had loaded showed the "Critical Error"
  card twice, stacked on the same page.

## [0.4.0]

This release introduces Circles and JIT Wallets, two new ways to share your
node's Lightning liquidity with other people, along with backup security
improvements, continued localization polish, and several reliability fixes.

### Added

- Circles — give people real wallets, without giving them a node. A Circle
  lets you extend your own node to friends, family, or a team: each person
  gets a real, personal wallet with their own balance, backed by your
  channels and liquidity, with none of the operational burden. Fee-free
  transfers in-circle; self-service membership via allowlist or nostr
  follows; new screens for managing the nostr identities and authorities
  behind your circles.
- JIT (Just-In-Time) Wallets — pay people who aren't ready to collect yet.
  Fund now, claim later: recipients generate their own invoice and claim
  their exact share only when ready. A single JIT Wallet can serve multiple
  recipients, each capped at their own pre-set amount.
- Continued RTL and translation polish across the app, building on the
  multi-language support introduced in 0.3.0-alpha.

### Changed

- Backups now use a stronger, versioned encryption format. Existing backups
  from earlier versions remain fully restorable.
- Redesigned the connection details view with clearer copy-to-clipboard
  actions.

### Fixed

- Fixed the "Deactivate" button on the AutoSwap screen, which was not
  correctly turning off auto-swap-out.
- General stability and correctness improvements across payment and wallet
  handling.

## [0.3.0-alpha]

This release brings background running support, a system tray icon,
improved reliability, and a more polished user experience.

### Added

- Lokihub stays active after you close the main window, and can be fully
  quit at any time from the tray icon menu.
- A system tray icon is available on macOS, Linux, and Windows.
- macOS: the dock icon appears and disappears automatically based on
  whether the window is open.
- A dedicated Help and FAQ section, with content available in additional
  languages.

### Changed

- Navigation, headers, and transaction lists refined for better consistency
  and responsiveness across all screen sizes.

### Fixed

- The app handles unexpected errors in isolated areas without taking down
  the entire interface.
- Payments can be more accurately linked to the app or service that
  generated them.
- Significant testing work across payment processing and exchange services
  to ensure more reliable behavior.

## [0.2.0-alpha]

This release brings full global language support to Lokihub, alongside a
suite of UI and UX improvements designed to deliver a more seamless and
intuitive experience.

### Added

- Multi-language support for 7 languages: English, Spanish, Japanese,
  Korean, Chinese, Arabic and Farsi/Persian.
- Full RTL layout support for Arabic and Farsi, including mirrored
  sidebars, navigation, and directional icons. Bundled `Noto Sans Arabic`
  and `Vazirmatn` fonts for high-quality rendering without external
  dependencies.
- A language switcher to toggle between supported languages.
- A JIT payment explanation dialog, and unique FLC/Loki presets tailored
  for individual apps.

### Changed

- Refactored the global Loading component for better accessibility and
  theme consistency.
- Standardized the use of `AppHeader` across more screens, including
  inbound channel management.
- Integrated `react-i18next` for translation management, with a
  direction-aware CSS engine for RTL/LTR switching.

## [0.1.2-alpha]

Pre-release for testing and feedback. Developers and early adopters are
encouraged to report issues.
