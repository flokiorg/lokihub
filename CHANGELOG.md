# Changelog

## [0.5.0]

Adds the Cash Hub, multi-recipient cash bills served over NIP-CASH's private
transport, and moves Circles onto member-generated connection keys and NIP-51
follow sets. Also fixes the money-safety and privacy findings from several
audits of both.

### Added

- Adds `CASH_MAX_REQUEST_AGE_SECS`, the horizon H a request's deadline may lie
  ahead (default 300, 60 to 86400); it can be raised, never lowered.
- Adds the Cash Hub, which issues multi-recipient cash bills held by a secret
  or bound to an identity, shared as a `lokicash1...` token. (#90)
- Adds `cash_transfer` to split a slice off a bill, `cash_consolidate` to merge
  slices this node holds into one new bill, and `cash_status` to read a bill's
  slices. (#90)
- Adds per-slice redeem fees (`redeem_fee_ppm`) and a configurable expiry
  ceiling, including no expiry. (#90)
- Adds optional mint provenance: a node signature attesting a bill's minter and
  amount on every mint, transfer and consolidate. (#90)
- Lets `cash_consolidate` target a pubkey, a connection key or cash mode, and
  take pubkey or connection-key sources, each proven separately. Cash-mode
  sources are not supported yet. (8432fb6f)
- Accepts `nconnection1...`, npub or NIP-05 for recipients in the Cash UI, not
  just hex pubkeys. (a3ae647d)
- Shows saved Identity Authorities by npub, picture, display name and NIP-05
  instead of a hex pubkey. (1f30f8cb, c7456b60)
- Runs the Windows desktop app frameless, with its own minimize, maximize and
  close buttons. (8a3e6e7b)
- Hides the macOS title bar and extends content to the window edge, keeping
  only the traffic-light buttons. (8a3e6e7b, #97)
- Adds a Cash Hub monitoring page at `/cash-hub/:id`: outstanding liability,
  flow, fees earned, median time to redeem, and every bill with its
  recipients, archived ones included. The same stats come from a new per-hub
  stats endpoint. (#103)
- Adds a setting under Settings › Services › NWC relays for whether this hub
  runs its own relays. It was only reachable by editing the database. (#103)
- Lists the bill methods in `get_info` under a new `private_methods` field.
  Being listed there is informational, never permission. (#111)
- Adds an optional `idempotency_key` to `mint_cash`, so a caller retrying after
  a timeout no longer mints and funds a second bill. A repeat is refused, not
  replayed, since a cash secret exists only in the first reply. (#111)
- Adds a profile page for the hub's own Nostr identity, with its kind-0
  profile, npub and nprofile QR codes, recent notes and an edit form. Signing
  stays on the backend, so the node key never reaches the browser. (593775ea)
- Lets the node profile page post plain-text notes as the hub's identity,
  shown in the feed right away. (05b50513)
- Shows in Settings › Services whether the private-transport announcement
  published, how many relays accepted it and the last error. The node's hex,
  npub and nprofile also appear under Channels. (1d0e1fcd)
- Reports `circle_wallet.identity` in `get_info` so clients can join a circle
  privately. It is omitted while the private transport is not running. (#129)
- Adds follow-set circles the hub publishes as its node identity, with public
  and hidden members, a title and a description. Hidden members are encrypted
  so only the hub can read them. (#139)
- Marks a member's wallet as frozen in the hub's wallet list once they leave
  the circle. (#148)
- Reports a Circle Hub's per-wallet ceiling and renewal floor in `get_info`
  (`per_wallet_max_millis`, `min_budget_renewal`), so clients can check a join
  before sending it. (#150)

### Changed

- Takes a `request_id` on `mint_cash` and, for a cash recipient, the
  caller's commitment; the Hub no longer generates or returns a cash secret.
  `idempotency_key`, `min_transfer_millis` and a recipient `cash_secret` are
  refused, and the reply carries `cash_token` without `pairing_uri`.
- Signs every bill under `cash-mint:v1` and every `cashhub1` string with the
  node identity and chain, as the final NIP-CASH requires.
- Refuses to sign text starting with `cash-mint:`, `cash-zap:`, `cash-hub:`,
  `cash-order:` or `circle-hub:` over NWC (`RESTRICTED`).
- Answers `cash_status` on a cash-mode bill from the bill proof alone and
  refuses one that carries a `cash_secret`; a destroyed bill's answer is
  `{"status":"spent"}`.
- Refuses to start on a database created before 0.5.0's final schema, and no
  longer converts older databases. Reset the database before upgrading.
- Breaking: renames NIP-CASH's bearer mode to cash mode (`identity_type:
  "cash"`, `cash_secret`) across every cash method and the admin API. A
  migration rewrites stored rows, so existing slices stay redeemable with the
  secret their holder has. (#102)
- Breaking: renames `list_recipients` to `cash_status` in the dispatcher, the
  permission scopes and `get_info`. (#111)
- Breaking: serves the four bill methods (`cash_status`, `cash_redeem`,
  `cash_transfer`, `cash_consolidate`) only over the private transport. On the
  standard NIP-47 connection each request p-tags the bill's wallet, which made
  a holder's bills a public, linkable set. (#111)
- Breaking: issues each Circle Wallet to a connection key the member
  generates, so `create_circle_wallet` requires `connection_pubkey` and a
  matching `connection` tag on its proof. The hub never holds the member's
  secret, and `encrypted_details` carries `wallet_pubkey` and `relays` instead
  of a `pairing_uri`. (#124)
- Breaking: admits circle members from a NIP-51 follow set (`circle_policy:
  "follow_set"`) instead of the host's kind:3 list, so one account can run
  several circles. Circles on the old `following` policy are deleted on
  upgrade. (#139)
- Makes a `cash_transfer` split mint two new bills, carved-off and remainder,
  instead of shrinking the source, so a bill's amount never changes. (#90)
- Moves the NIP-CASH and NIP-CW per-call proofs off the shared kind 35521 onto
  their own kinds, 23198 and 23199. (#90)
- Broadcasts swap claim and refund transactions through the node's own FLND
  connection instead of the third-party `lightz` API, and computes the txid
  locally. (#93)
- Merges a cash bill's token and secret into one gift string, so there is one
  value to copy. (9affcdfe)
- Archives spent bills instead of deleting them, so value that passed through
  a hub stays accountable. (#103)
- Deletes a drained bill on `cash_redeem` and `cash_consolidate` too, as a
  split already did. Deleting on only some paths revealed which bills this hub
  issued. (#103)
- Splits slice status into unclaimed, redeemed, split, expired, reclaimed and
  written-off; `claimed` still works as the umbrella. (#103)
- Records the payment hash, preimage and both fees on a slice when it is
  redeemed. (#103)
- Opens one NIP-47 subscription for all app wallets instead of one per wallet,
  which grew with the number of bills in circulation. (#103)
- Refuses a redemption that would pay out nothing. (#111)
- Adds a flat base to the redeem fee and caps the route reserve, so payout plus
  reserve equals the slice. Before, the hub could pay up to 10,000 mloki in
  routing to earn 1. (#111)
- Splits a bill converted to cash mode after it has changed hands, instead of
  reassigning it, so a previous holder can't recover a bill they sold. (#111)
- Keeps a Circle Wallet on the fee rate in force when its member joined; a hub
  fee change applies only to later joins (NIP-CW §Fees). (#122)
- Makes removing a recipient from a Cash bill all-or-nothing: if returning
  their amount to the hub fails, their slice stays as it was. (#122)
- Answers `create_circle_wallet` with NIP-CW error codes (`WALLET_EXISTS`,
  `NOT_READY`) and reports the resolved `max_amount` in `encrypted_details`.
  (#124)
- Moves analytics into the Cash Hub and Circle Hub overview cards they
  explain, and shows a skeleton while those cards load. (17fd8a62)
- Removes "Execute Node Command" from Debug Tools. Get App/Node Logs now fetch
  right away, with the maximum log length set from a settings icon and
  remembered. (1d0e1fcd)
- Removes the "Update Configuration" form from the critical-error page.
  (c01e74ea)
- Moves the frontend to react-router v7, which fixes an open redirect through a
  backslash in a link (GHSA-wrjc-x8rr-h8h6). (#134)
- Builds with Go 1.26.10, which fixes a certificate-verification flaw in the
  standard library (GO-2026-6809) reachable from backup restore. (#152)
- Moves to `nmilat` v0.5.0-rc.32, the published client SDK with the NIP-CASH
  and NIP-CW wire changes in this release. (#153)

### Fixed

- Fixes a Circle Wallet spend-cap bypass where a member could disable their
  own cap with a sub-loki `max_amount`, plus seven smaller audit findings.
  (#90)
- Fixes LSPS5 Nostr notifications letting any trusted LSP change the state of
  any order, not just its own. (205d91ec)
- Creates a `mint_cash` wallet in one database transaction, so a failure
  midway no longer leaves a partial, unfunded wallet. (6f12eac7)
- Moves `google.golang.org/grpc` to v1.83.2, fixing GO-2026-6348 (heap
  exhaustion via HTTP/2 DATA frames), reachable through flnd's client and the
  HTTP service. (f32928a6)
- Moves `golang.org/x/net` to v0.60.0, fixing four reachable HTTP/2 server
  advisories: GO-2026-6617, GO-2026-6612, GO-2026-6611 and GO-2026-6603.
  (f02d0c22)
- Uses flokiorg's fork of `flokicoinjs-lib` (v7.1.1), which moves `valibot` to
  1.5.0 and clears GHSA-vqpr-j7v3-hqw9 and GHSA-5qjj-4xww-7phc. (#117)
- Fixes publishing the node profile from the desktop app, which failed with
  "Unhandled route". (05b50513)
- Fixes the sidebar logo and header rendering under the window controls.
  (164a5f8f)
- Fixes the macOS window corner showing a default background color instead of
  the app theme. (a991bc9b)
- Shows an error screen with the failure message when the desktop app's
  backend fails to start, instead of exiting silently. (#98)
- Fixes upgrading from 0.3.0-alpha to a 0.5.0 release candidate failing at
  startup with "no such column: parent_kind". (#98)
- Times out the UI's data requests instead of hanging forever when the backend
  stops responding. (#98)
- Fixes `cash_consolidate` encrypting the merged bill's token to
  `new_identity` instead of the calling connection, so its recipient couldn't
  decrypt it. (#100)
- Stops publishing a kind-13194 info event for cash bills, which outlived the
  bill and revealed which pubkeys this hub served. Also fixes info-event
  deletion being skipped for every app kind. (#103)
- Fixes a concurrent unclaim restoring a slice between the drain check and the
  delete, which could destroy a bill still backing live value. (#103)
- Fixes a failed delete leaving a wallet stranded, invisible to the sweep and
  to manual retries. (#103)
- Stops a request racing an app deletion from hanging for 30 seconds. (#103)
- Stops counting bills past their redeem window as outstanding before the
  sweep collects them. (#107)
- Fixes the outstanding chart to match the figure above it; it accumulated
  from zero over 30 days and ignored splits and write-offs. (#107)
- Stops claiming a new Cash Hub's connection can be viewed again later; a
  hub's pairing secret is random and never stored. (#107)
- Stops `cash_status` answering a caller holding no slice. With a
  `connection_key` recipient on the bill, the token alone read the whole
  roster. (#111)
- Lets a `connection_key` recipient read their own row again, using the
  Identity Authority attestation `cash_redeem` already requires. (#111)
- Makes `cash_status` honor its scope and stop disclosing co-recipients by
  default. (#111)
- Nests circle join details so co-members can't read them. (#111)
- Refuses mint-provenance payloads in `sign_message`. (#111)
- Stops a read-only token from reading the swap seed. (#111)
- Answers an unknown bill, an unverifiable proof and an unserved method with
  the same silence, and checks a bill proof before looking up the bill so a
  miss costs what a hit costs. (#111)
- Stops a proof being lifted out of its envelope and replayed in another: a
  nonce stays burnt while any proof bound to it can still verify. (#111)
- Detects an oversize reply wherever it falls in a batch, not only when it
  comes first. (#111)
- Stops one envelope from burning a shared bill's whole hourly claim budget or
  spending it on items that failed validation. (#111)
- Stops a lapsed invoice pinning its wallet, hub and identity forever, and
  counts invoices on hold. (#111)
- Checks a wallet is actually empty before either compensating rollback
  deletes it. (#111)
- Clears the stale "identity required" hint on a bill reassigned from cash to
  an identity, which made it unreceivable by its holder. (#111)
- Verifies an Identity Authority attestation once per request instead of once
  per claim. (#111)
- Fixes `db_migrate` copying 6 of its 27 tables and reporting success, which
  left the ownership claims behind. (#111)
- Fixes the "Critical Error" card showing twice after a backend outage.
  (c01e74ea)
- Fixes `cash_redeem` when the node can't report a payment's outcome: the
  slice now stays claimed until the payment settles or fails, instead of
  being released at once. (#119)
- Stops writing NIP-47 request parameters to the log: a request that fails
  to decode is now logged with its method and error only. (#119)
- Fixes `cash_status` on a cash-mode bill: an item without a slice proof is
  now answered only when its `cash_secret` matches one of the bill's slices. (#119)
- Makes refusing a request for a recently destroyed bill cost the same as
  refusing one for a bill the hub never issued. Bills destroyed before this
  release no longer get a "spent" answer. (#119)
- Fixes the private-transport inbox going quiet after a relay reconnect: it
  now resubscribes with its original filter and checks itself with a
  periodic probe, shown in the node info as `inboxDelivering`. (#119)
- Requires a private-transport circle join's item proof to be signed by the
  pubkey that is joining, as NIP-CW now specifies. (#120)
- Fixes a private-transport envelope with one malformed item going
  unanswered: that item is now dropped and the rest are served, using
  `nmilat` v0.5.0-rc.22. (#120)
- Fixes `mint_cash`, `cash_transfer` and `cash_consolidate` returning an
  empty token after funding a bill. A bill whose token can't be produced is
  now refused before any funds move. (#120)
- Refuses a Cash Hub or Circle Hub name longer than its `cashhub1` /
  `circlehub1` connection can carry (255 bytes), instead of saving a hub whose
  connection was never shown. (#120)
- Fixes Circle Hubs created from lokihub's own UI leaving the
  `circle_wallet` block out of `get_info`: every Circle Hub now includes it. (#120)
- Fixes a Circle Wallet's receive cap: invoices it has issued and that can
  still be paid now count toward the cap, and concurrent `make_invoice`
  requests can no longer each pass the check. (#120)
- Refuses a `cash_secret` supplied by the caller when minting, over NWC and the
  admin API, instead of silently replacing it with a Hub-generated one. (#120)
- Verifies the caller before checking the amount in `cash_transfer`, so a
  refused request no longer reveals the slice's balance. (#120)
- Refuses bill methods on the standard NWC transport with `NOT_IMPLEMENTED`
  whatever the connection's scopes or expiry, as NIP-CASH requires. (#120)
- Refuses a private-envelope padding bucket below 2048 bytes in settings: one
  that small broke the Hub's private-transport announcement, so it stopped
  being published. (#120)
- Stops listing `get_budget` in a Cash wallet's `get_info`, which it refuses.
  (#120)
- Warns in the Cash Hub bills list that a destroyed bill's token still answers
  `cash_status` until its retention window ends. (#120)
- Resolves `cash_redeem` payouts whose outcome arrived while lokihub was down,
  by asking the node at startup and periodically. A failed payout releases its
  slice, and the node info reports how many are still pending. (#120)
- Deletes a drained Cash bill on every drain, as NIP-CASH requires, including
  an undelivered `cash_transfer`, a `cash_redeem` with no preimage and a payout
  that settled late. Before, these stayed live until the expiry sweep. (#120)
- Keeps a retention of 0 on Cash Hubs created before the destroyed-bill
  retention setting existed, so their destroyed bills answer nothing. Set a
  retention in the hub's settings to answer `cash_status` for them. (#120)
- Refuses a `cash_redeem` `amount` that differs from a fixed-amount invoice's
  own, and pays such an invoice for exactly its amount, as NIP-CASH requires.
  (#121)
- Makes refusing a private `cash_status` for a live bill cost the same as
  refusing one for a bill the hub never issued. (#121)
- Keeps a payment the node reports as sent, but which could not be recorded,
  as sent: a `cash_redeem` slice stays claimed, and a split, consolidate or
  removed-recipient sweep counts the funds as moved, instead of rolling back. (#124)
- Resolves an outgoing payment left pending for 48 hours by asking the node
  instead of failing it by age, and keeps a keysend pending when the node
  can't say whether it went out. (#124)
- Publishes a private-transport reply on the relay its request arrived on;
  before, it went to the first configured relay that accepted it. (#124)
- Makes an app's connection pubkey unique in the database, not only in the
  check before inserting it. (#124)
- Republishes the private-transport announcement when its envelope limits
  change in settings; clients kept the old limits until a restart. (#126)
- Forgets, after 90 days by default (`CASH_SPLIT_LINK_RETENTION_DAYS`), which
  bill an archived cash slice was split or consolidated into; it was kept
  forever. Amounts and outcomes stay. (#126)
- Returns to the page you opened before unlocking only when it is a page of
  this app; a link naming another site now lands on the home page. (#128)
- Follows an app or peer link's `return_to` only when it opens another app or
  site; a `javascript:` or `data:` link would have run in the hub's page with
  your session. (#131)
- Names the missing `signer:generate` macaroon permission, or the missing
  signer RPC, when the node cannot sign as itself; before, the private
  transport failed with a bare "permission denied". (#132)
- Marks a private-transport request `executed` or `error` once its handler
  ends; it stayed `executing` forever, unlike the same request sent over the
  standard transport. (#140)
- Holds a Circle Wallet's hold invoices to the same receive cap as ordinary
  invoices; before, `make_hold_invoice` could take in any amount. (#144)
- Refuses zero-amount invoices from a Circle Wallet; the payer could choose
  any amount and push the wallet past its cap. (#144)
- Credits a keysend to a Circle Wallet only when it fits under the wallet's
  cap; a larger one stays with the node, and one sent from this node is
  refused. Anyone could name the wallet's ID and push it past its cap. (#144)
- Keeps an expired or deleted Circle or Cash wallet until its outgoing payment
  ends; deleting it mid-payment lost the payment's record and the hub's fee
  credit. (#144)
- Keeps a Circle Hub, and any of its wallets with a live invoice or a payment
  in flight, when you delete it with "empty wallets only"; a zero balance alone
  let such a wallet be removed mid-payment. (#144)
- Shows a Circle Hub's stats in the desktop app; the page asked for them on a
  route the desktop app answered with the app list. (#144)
- Ignores a follow-set event whose id doesn't match its content; a relay could
  forge one to choose which of two same-second versions decides membership.
  (#145)
- Refetches a follow set cached more than two hours ago instead of admitting
  members from it; without the background refresh, a removed member stayed in.
  (#145)
- Answers a circle join with `INTERNAL` when the hub can't record its identity
  proof; it said the proof was already used, which reads as the client's
  mistake. (#145)
- Answers a private-transport item with a retryable error when the hub can't
  read the wallet's records; a circle join got a final refusal, and a bill
  holder was told no slice was theirs. (#145)
- Keeps a member removed from a follow set out after a restart, even when the
  relays still serve the older version that listed them. (#145)
- Freezes a circle wallet once its member leaves the allowlist or follow set:
  payments and new invoices are refused until they are added back, while the
  balance and history stay readable. (#145)
- Requires a Circle Wallet cap of at least 11 loki, on joins and in a hub's
  per-wallet limit. At 10 loki or less, the Lightning fee reserve left a wallet
  unable to pay out. (#148)
- Moves every member wallet's balance and the hub's own balance to your main
  balance as recorded transfers when you delete a Circle Hub with "Delete
  All". Before, those funds vanished with no record. (#148)
- Saves nothing when a Circle or Cash Hub settings change is rejected; before,
  a bad fee rate still saved the new name and budget from the same form.
  (#148)
- Shows Nostr profile pictures in the desktop app, which showed initials
  everywhere because it had no login token to put on the image request.
  (#148)
- Counts a payment made at the exact moment a budget period starts against
  that period; it counted against neither, so it escaped the budget. (#148)

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
