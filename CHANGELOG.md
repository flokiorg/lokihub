# Changelog

## [0.5.0]

Adds the Cash Hub, multi-recipient cash bills served over NIP-CASH's private
transport, and moves Circles onto member-generated connection keys and NIP-51
follow sets. Also fixes the money-safety and privacy findings from several
audits of both.

### Added

- Asks again in the desktop app too when an LSP purchase is refused for
  terms worse than announced: the refusal carries the same body as the HTTP
  409 (message, announced and live terms), so the terms-changed dialog opens
  and the user can accept there.
- Lists a Cash Hub's on-chain payouts (state, address, amount, payout, fee,
  `sent_by`, signed, txid:vout, depth / `final_depth`, cancellation, re-minted
  bill link) with a detail view of every version and archive fact
  (`GET /api/cash-hubs/:id/payouts/:payout`, also in the desktop app), and
  marks ordered and re-minted bills, whose remove and delete actions are
  disabled (`ordered` / `reminted` on the cash-wallet listings).
- Adds the on-chain withdrawal operator screens: an "On-chain withdrawals"
  node switch under Settings → Advanced with the payout engine's status, and
  a per-Cash Hub card for its enable, `withdraw_fee_base`, `withdraw_fee_ppm`
  and optional minimum and maximum, with the script types paid and
  `final_depth` shown read-only. `GET /api/cash-withdraw/node` reads the
  switch, and the desktop app serves every on-chain withdrawal route.
- Shows an LSP's announced and live terms side by side on the JIT receive
  and channel order screens when the live ones are worse, and asks again
  before buying (`terms` on `GET /api/lsps2/info` and `/api/lsps1/info`; the
  purchase sends the worse values accepted as `acceptedTerms` /
  `accepted_terms`, and a purchase whose terms turned worse meanwhile is
  refused with 409 and both).
- Takes order payments in and mints them (NIP-CO §Paying and Minting). A
  settled order invoice, or a payment of one by a wallet on this node, is
  recorded once by its payment hash and minted in one commit: the Hub is
  credited with what it brought in and each bill of the split (none above
  `max_millis`; nothing for a payment of at most the fee, which the Hub keeps)
  is created, signed, funded and recorded on the order. Outputs to order
  addresses are read from the node's wallet every 30 s, seen, confirmed at
  their tier and minted; an unconfirmed output gone for two scans is dropped,
  a minted one that leaves the chain is reorged (the bill stands, alerted); a
  payment to an address with no live order is kept by the Hub. A failed mint
  is retried with backoff for good. pay_by expires an unpaid Lightning order
  once its invoice is cancelled, and closes or expires an on-chain one;
  retention drops what NIP-CO allows, with the used mark of an unpaid order.
  Every state change and new bill publishes an order notice on the relay the
  order arrived on. `cash_lookup` of an order returns its bills, tokens
  rebuilt. Ordered slices never expire, cannot be removed or have their wallet
  deleted by the owner, carry their mark through transfers and merges, and are
  never consolidated with other slices. Open orders and unminted payments keep
  their Cash Hub (`orders` in the delete check). (#158)
- Sells a Cash Hub's cash through NIP-CO Order connections. The operator
  creates any number per Cash Hub (`POST /api/cash-hubs/:id/order-connections`
  returns the signed `cashorder1` string, shown again by the list), retires one
  for good (`POST …/order-connections/:conn/retire`) and sets the selling terms
  (`GET`/`PUT /api/cash-hubs/:id/order-terms`: Lightning and on-chain amounts,
  fees, windows, confirmation tiers, bill terms and open-order ceilings,
  refused rather than clamped out of bounds); the same routes exist in Wails.
  An Order connection answers `get_info` only on the standard transport, with
  its terms (`{}` once retired), and takes `cash_order` items on the private
  transport: checked in NIP-CO's order, the order is recorded with its invoice
  or a fresh address, its reservation and its request record, moving no funds;
  `cash_lookup` reads it back with the buyer key. A Cash Hub's per-wallet
  ceiling can no longer drop below an offered `max_millis`, and an Order
  connection is deleted only with its Hub. (#158)
- Adds the circle operator screens for NIP-CW: the circle settings editor
  covers the default lifetime, receive ceiling, holdings ceiling, routing-fee
  reserve, outbound ceiling and period, grace period, dust threshold and member
  requests with their delay; the wallet list shows each wallet's state, grace
  countdown, freeze and pending rotation, and its action is Close (the wallet
  moves to Closing and keeps its balance); deleting a circle is Close circle
  (teardown); a "New members" toggle closes or opens the circle
  (`POST /api/apps/:id/circle/accepting`, and its Wails route); a member
  request card submits a signed member request and shows the Hub's code. The
  Cash Hub delete dialog reads the delete check and shows each blocker and the
  earliest deletion time. (#158)
- Serves NIP-CW §Recovering a Lost Connection on a Circle Wallet Hub that
  enables it (new settings: rotation on/off, delay default 5 days; `get_info`
  reports `rotation_delay_secs` and each wallet's details carry its own, fixed
  at join). The host submits a member request the member signed with
  `POST /api/circle-hubs/:id/member-requests` (and its Wails route); it is
  checked in the spec's order (`NOT_IMPLEMENTED`, `UNAUTHORIZED`,
  `REQUEST_REUSED` with the id kept 24 h 1 min, `NOT_FOUND`, `UNAUTHORIZED`,
  `NOT_IMPLEMENTED`, then the action). A `rotate` is accepted under the
  node-wide connection-key lock shared with joins (`ROTATION_PENDING`,
  `RESTRICTED`), completes at its `completes_at` (lazily on a request by either
  key, and by a 10-second job), is cancelled by any request of the old key or
  when the new key got used, and swaps the wallet's key, ending a freeze. A
  `freeze` refuses every request of the current key `FROZEN` until an
  `unfreeze` or a completed rotation. `rotation_pending`, `rotation_cancelled`,
  `rotation_completed` and `wallet_frozen` notifications go to the wallet's
  key; Circle Wallets are granted `notifications`. A join lookup by the member
  naming a rotated key answers `pending` until `completes_at`, then `done` with
  the details re-encrypted for the new key, writing nothing. A join to a key a
  pending rotation names is `RESTRICTED`. (#158)
- Adds NIP-CW's Circle Wallet lifecycle (new `circlelife` package): Active,
  Suspended, Expired and Closing, with only the spec's edges; a wallet past its
  `expires_at` is Expired at once, whether or not the job has run; the grace
  period (new Hub setting, default 7 days) starts when a wallet first leaves
  Active and is never restarted; reaching Expired or Closing frees the member to
  join again; a wallet that is not Active has its open invoices cancelled,
  stops counting toward the holdings and cannot receive (`PAYMENT_FAILED` to a
  same-node payer). A job every minute records expiries, closes Suspended
  wallets past their grace, and deletes Expired or Closing wallets past their
  grace that hold at most their dust (new Hub setting, default 1000 mloki,
  fixed per wallet at join and reported in the join details) and have nothing
  in flight, keeping the dust on the Hub. (#158)
- Serves `delete_circle_wallet` on a Circle Wallet's own connection in every
  state: refused `RESTRICTED` while it holds more than dust or a payment is in
  flight; otherwise its open invoices are cancelled at the node first and the
  wallet is deleted in one step. Any other connection is refused `RESTRICTED`. (#158)
- Gates every Circle Wallet request by state (NIP-CW §What a Circle Wallet
  Connection Allows): an Active wallet serves `pay_invoice`, `pay_keysend`,
  `make_invoice`, `lookup_invoice`, `list_transactions`, `get_balance`,
  `get_budget`, `get_info` and `delete_circle_wallet`; any other state only
  pays out, reads and deletes; everything else is `RESTRICTED`. (#158)
- Adds `GET /api/cash-hubs/:id/delete-check` (and its Wails route): what keeps
  a Cash Hub from being deleted (live bills, retained request records) with
  each blocker's time and the earliest possible deletion. (#158)
- Adds NIP-CW's money model for Circle Wallets (new `circlemoney` package).
  Every payment is classified as same circle, another circle on the node, any
  other connection on the node, or Lightning; only Lightning carries the
  member's `fees_ppm` fee and the routing-fee reserve (the larger of
  `min_millis` and amount × `ppm` / 1,000,000, rounded down, never exceeded:
  it is the payment's fee limit). The send cap (`max_amount`) and the receive
  cap (`max_receive`) count external traffic only, per budget period anchored
  at the wallet's creation in UTC; the receive room counts open invoices and
  accepted held payments. A circle's holdings (its wallets' balances and open
  invoices) stay under its holdings ceiling (`INSUFFICIENT_CAPACITY`), and an
  optional outbound ceiling bounds what its wallets send per Hub period
  (`QUOTA_EXCEEDED`). Pending payments (32), external payments in flight (8),
  same-circle transfers (60 a minute), open invoices (100) and invoices (60 a
  minute) are bounded per wallet (`RATE_LIMITED`). New Circle Hub settings:
  holdings ceiling (default: 100 wallets at the receive ceiling), fee reserve
  ppm and minimum (default 10000 and 10000), outbound ceiling and period. (#158)
- Issues every Circle Wallet invoice as a hold invoice the Hub settles itself
  after re-checking the payment (amount equal to the invoice's, not expired),
  cancelling it otherwise; a same-node payment of one is settled at once, and
  accepted ones left from a restart are resolved at start. `get_budget` on a
  Circle Wallet reports both caps for the current period, clamped. (#158)
- Adds a Circle Wallet Hub's per-wallet receive ceiling and default wallet
  lifetime (NIP-CW §Data Model): a join whose `max_receive` is over the
  ceiling is refused `LIMIT_EXCEEDED` naming `max_receive`, the wallet keeps
  the `max_receive` it asked for, and a join naming no expiry gets the default
  lifetime instead of the ceiling. A lifetime ceiling of 0 (none) is accepted;
  a default above a non-zero ceiling, or 0 under one, is refused. `get_info`
  reports `per_wallet_max_receive_millis`. (#158)
- Lists offers on relays (NIP-CASH §Listing Offers): a circle, from its hub
  page or the new Listings page, is published as a kind-31190 listing signed
  by the node identity, with its own public `circlehub1` connection (the
  members' invite is untouched). Listings are republished every 7 days and on
  each change, paused and resumed, and retired in the spec's order: the
  `closed` version, kept until the last open version expires, then a NIP-09
  deletion. The node publishes a kind 10002 naming the listing relays (a new
  Listing relays setting). An optional operator is named in the `p` tag and
  checked, read-only, against their own kind 11191; lokihub never asks for
  their key. A Cash Hub connection is never listed. Order connections are
  listed once they exist. (#158)
- Adds "Revoke and reissue the Cash Hub connection": the old `cashhub1` stops
  working at once and a new one is shown once; bills, records and the hub
  group are untouched. (#158)
- Builds the LSP list from NIP-LSP announcements (kind 10735) on the Hub's
  relays: every LSP announced for its chain and network is shown unranked in
  Settings > Services with its services, fee hint, status and whether it is
  in the node's own graph; the user ticks the LSPs they trust, and trusting
  runs a live check of the LSP's LSPS answers against its announcement. Only
  trusted LSPs are contacted; a paused one sells nothing new and a closed one
  disappears. Every purchase (an LSPS1 order, an LSPS2 buy, automatic JIT)
  runs the live check first and is refused when an announced protocol is
  missing; automatic JIT also skips an LSP whose last check failed and refuses
  live terms worse than announced. An LSP whose newest announcement fails a
  check, expires or closes is not reconnected and gets no zero-conf channels,
  though it stays trusted. An LSP can be added by its node key. The regtest
  dev stack runs flspd as the test LSP (compose profile `lsp`). (#158)
- Keeps a record of every circle join and serves the NIP-CW join lookup:
  `cash_lookup` by `connection_pubkey` at a Circle Wallet Hub answers the
  joiner (its item proof required, `UNAUTHORIZED` otherwise) with the join's
  own `encrypted_details`, and anyone else by deadline with the 6-minute
  window. A record is dropped once its wallet is gone and that window has
  passed. (#158)
- Serves `cash_lookup` on the private transport (NIP-CASH §Looking Up a Call):
  a held record answers `done` with the call's reply rebuilt (the same fields,
  each token re-encoded from the record and encrypted to the same key, a
  destroyed bill's token as its tombstone) to the recorded authorizer only, any
  other signer, target or secret `UNAUTHORIZED`; with no record the answer is
  `pending`, `unknown` or `expired` by the call's deadline. A lookup writes
  nothing, is served for a destroyed bill a record names, is limited to 600 an
  hour per signer, and is listed in `private_methods`; on kind 23194 it is
  `NOT_IMPLEMENTED`. (#158)
- Adds `PRIVATE_INBOX_EVENTS_PER_MIN` (600): the envelopes decrypted per relay
  connection a minute; the rest are dropped undecrypted. (#158)
- Adds the `cash_redeem` operator limits: `CASH_REDEEM_MAX_INVOICE_EXPIRY_SECS`
  (86400) and `CASH_REDEEM_MAX_FINAL_CLTV` (2880 blocks) refuse an invoice over them
  `RESTRICTED`; `CASH_REDEEM_INFLIGHT_PER_HUB` (32) and
  `CASH_REDEEM_INFLIGHT_NODE` (256) bound the payouts in flight
  (`RATE_LIMITED` before the claim); `CASH_REDEEM_LATE_FAILURE_COOLDOWN_SECS`
  (600) refuses another redeem of a slice whose payout failed after its answer;
  `CASH_REDEEM_ROUTE_MARGIN_PPM` (2000) and
  `CASH_REDEEM_ROUTE_MARGIN_MIN_MILLIS` (1000) set the routing margin; and
  `CASH_REDEEM_REPLY_WAIT_SECS` (20) is how long a redeem waits for its payment. (#158)

- Adds `CASH_LINEAGE_SPIN_OFFS_PER_HOUR` (default 20), which bounds the bills
  one minted bill's lineage may spin off per hour (`RATE_LIMITED` over it), and
  `CASH_MAX_LIVE_BILLS_PER_HUB` (default 10 000), which caps the live bills a
  spin-off may leave under one Cash Hub (`QUOTA_EXCEEDED`). (#158)
- Makes zap transfers (NIP-CZ): a `cash_transfer` with `zap_target` spins the
  slice off into a bill signed under the zap scheme, whose `issued_at` and
  `expires_at` the reply returns exactly as signed; the hub keeps the zap fields
  with the bill and stores the target only as its hash. (#158)
- Adds `CASH_MAX_REQUEST_AGE_SECS`, the horizon H a request's deadline may lie
  ahead (default 300, 60 to 86400); it can be raised, never lowered. (#158)
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

- Breaking: a Circle Wallet's membership is decided by resolutions, no longer
  re-checked on every call (NIP-CW §Membership): each successful resolution of
  the circle's follow set (nmilat's `ResolveFollowSet`: the newest signed
  version, one dated more than 15 minutes ahead ignored, one older than the
  last resolution a failed refresh) and each committed allowlist edit suspends
  the Active wallets of members no longer named and returns Suspended ones
  named again within their grace to Active; an unresolved or failed refresh
  changes nothing. A resolution suspending 20 % or more of a circle's Active
  wallets raises a host alert (`nwc_circle_mass_suspension`, log WARN, no
  member pubkeys). Hidden members are read with the current inbox key and every
  retired one an Active or Suspended wallet was issued under. (#158)
- Breaking: a host delete of a Circle Wallet (the wallet list, the generic app
  delete) moves it to Closing instead of sweeping its balance to the Hub, and
  deleting a circle hub tears it down: every wallet Closing, the circle closed
  to joins, the hub deleted only after its last wallet (at once if it has
  none). The cash expiry sweep never touches a Circle Wallet. A Cash Hub is not
  deleted while a request record naming it is retained. (#158)
- Breaking: a Circle Wallet's join checks the circle's holdings against its
  holdings ceiling instead of committing each wallet's `max_amount` against the
  Hub's balance; the Hub-wide budget ceiling is gone, and a circle hub's app
  listing shows its holdings against that ceiling. A Circle Wallet's
  zero-amount invoice is refused `RESTRICTED` (was `BAD_REQUEST`), and its
  `make_hold_invoice` is refused `RESTRICTED`. A same-node keysend over the
  receiving Circle Wallet's room or its circle's ceiling is refused before it
  is sent. (#158)
- A Lightning keysend to a Circle Wallet that is not Active, or over its
  receive room or its circle's holdings ceiling, is failed at the final hop
  instead of staying with the node. This needs flnd run with
  `keysend-hold-time` (for example `30s`) wherever it accepts keysends: lokihub
  then settles every held keysend that passes and cancels the rest; a node
  that settles keysends itself is logged as misconfigured whenever it settles
  one the checks refuse. The admin invoice endpoint gives a Circle Wallet the
  same Hub-settled invoice as its `make_invoice`. (#158)
- Resolves a private item whose target several connections share to the
  Circle Wallet Hub's row, and checks a circle's bill proof against every live
  connection of the Hub (nmilat's `VerifyBillProofAny` rule) for joins and join
  lookups; a join lookup still resolves through the retained join records once
  the Hub is gone. (#158)
- Breaking: the LSP list no longer comes from `services.json`, the `LSP`
  setting and env, or `pubkey@host` input, and setup no longer picks LSPs:
  the LSP table is recreated, and LSPs are trusted again from their
  announcements. `/api/info`'s `lsps` lists the trusted LSPs; the
  `/api/lsps/:pubkey` update and delete routes are replaced by
  `PUT /api/lsps/:pubkey/trust` and `POST /api/lsps/:pubkey/check`. (#158)
- Breaking: serves `create_circle_wallet` on the private transport only (kind
  23194 answers `NOT_IMPLEMENTED` before any proof is read) and in NIP-CW's
  order and codes: params `BAD_REQUEST`; a Hub not accepting joins
  `RESTRICTED`; the join proof, now checked by nmilat with its `terms` tag,
  `UNAUTHORIZED`; a used proof `REQUEST_REUSED`; membership `NOT_READY` or
  `RESTRICTED`; an existing wallet `WALLET_EXISTS`; a term over the Hub's limits
  `LIMIT_EXCEEDED` naming the field; a full circle `INSUFFICIENT_CAPACITY`; a
  connection key in use `RESTRICTED`; and a join reaching its commit past its
  proof's freshness window `BAD_REQUEST`. The details carry `max_receive`. (#158)
- Answers `get_info` on every connection a Cash Hub serves (its own, its
  bills, Circle Wallet Hubs and their wallets) with `network` and `chain`
  whatever the connection's scopes; a Cash Hub's `pubkey` is its node identity
  as 64-hex x-only (the `cashhub1` string's TLV 4). A Circle Wallet Hub's
  `circle_wallet` block always carries `identity`, `fee_reserve`,
  `dust_threshold_millis` and `accepting`, and caps `available_millis` at the
  per-wallet ceiling. `create_circle_wallet` moves from `methods` to
  `private_methods`. (#158)
- Gives a destroyed bill's tombstone its reason: the terminal state of the slice
  whose ending destroyed it (`redeemed`, `transferred`, `consolidated`,
  `expired` or `reclaimed`), kept on the bill's archive row and none for a
  written-off bill, on `cash_status`, the value methods and a lookup's rebuilt
  result. (#158)
- Answers a destroyed bill on kind 23194 with silence: its tombstone is served
  on the private transport only. The relay gate no longer keeps destroyed bills'
  wallets registered for their retention window, so the start-up reload, the
  retention-change resync and the expiry prune of those wallets are gone. (#158)
- Serves private-transport envelopes through nmilat's E1 to E6 classifier: an
  envelope refused at E4 to E6 gets an envelope-level `BAD_REQUEST` or
  `QUOTA_EXCEEDED` reply and its nonce stays unserved; served nonces are kept in
  the database (`private_served_nonces`) until their `not_after`, so a replay is
  dropped across a restart, and a nonce that cannot be recorded drops the
  envelope. Replies are dated a random 0 to 48 hours in the past. (#158)
- Serves private-transport items in NIP-CASH's I2 to I10 order: a destroyed
  bill is answered with its tombstone for `cash_redeem`, `cash_transfer` and
  `cash_consolidate` as well as `cash_status`; a method this hub does not serve is
  `NOT_IMPLEMENTED` once the bill proof verifies (no longer omitted); an item
  proof that does not verify, or a proofless item on an identity-bound bill, is
  `UNAUTHORIZED`; and after more than eight items of one envelope fail
  authorization its further value items are `RATE_LIMITED`. (#158)
- Runs `cash_redeem` in NIP-CASH's order and codes: locate (`NOT_FOUND`, a
  claimed or expired slice `RESTRICTED`), authorize (a claim proof that does not
  verify, now checked by nmilat with its `m` tag, or a wrong cash secret is
  `UNAUTHORIZED`; an IA no longer trusted `RESTRICTED`), the invoice policy, the
  amount rules (an `amount` beside an invoice's own amount, even an equal one, is
  `BAD_REQUEST`; a zero payout `RESTRICTED`), then the claim, which consumes the
  claim proof in the same transaction. Caller-set fee, rate or same-node fields
  are `BAD_REQUEST`. (#158)
- Answers `cash_redeem` while its payment may still settle: after the reply wait
  it is `INTERNAL` with the slice kept claimed; a payment that fails before it can
  have settled is `PAYMENT_FAILED` and releases the claim and the proof together;
  a payout that fails later releases both and starts the slice's cool-down. (#158)
- Caps every external redeem's route at the quoted fee plus the routing margin,
  never the generic fee reserve, so a fee-free slice is bounded too. (#158)
- Breaking: replaces `CASH_WALLET_CLAIM_RATE_LIMIT_PER_HOUR` (20 per bill) with
  `CASH_CLAIM_CALLS_PER_HOUR` (120), shared by `cash_redeem`, `cash_transfer`
  and `cash_consolidate` and keyed by the verified claim signer or the slice's
  commitment, never by a bill, so one co-recipient can no longer spend the
  others' allowance. (#158)

- Runs `cash_consolidate` in NIP-CASH's order and codes: the item names its
  first source; it requires a `request_id`, marked once every source's claim
  proof verifies (now by nmilat, with the `s` sources hash); any proof failure
  of any kind is one `UNAUTHORIZED` "sources not authorized"; a source's state,
  another Hub, an untrusted IA and differing terms are `RESTRICTED`; over the
  ceiling or the source cap is `QUOTA_EXCEEDED`; and the record commits with
  the merge. (#158)
- Runs `cash_transfer` in NIP-CASH's order and codes: a claimed or expired
  slice is `RESTRICTED`; a claim proof that does not verify (now checked by
  nmilat, `m` tag and exact tags included), a wrong cash secret, or an item proof
  by another key is `UNAUTHORIZED`; an untrusted IA and dust are `RESTRICTED`. A
  full transfer to an identity already holding a slice on the bill spins off, a
  cash slice's partial split takes a fresh `remainder_commitment`. (#158)
- Requires a `request_id` on `cash_transfer`: a missing or malformed one is
  refused before the item's proofs (`BAD_REQUEST`), a used one with
  `REQUEST_REUSED`; it is marked used once the call is authorized, and the
  call's record commits with the transfer, under the envelope's `not_after`. (#158)
- Returns only the `cash_token` from the admin mint and the bill connection
  endpoint, the bill's one credential; neither carries a `pairing_uri` any more,
  and the Cash screens show the token alone. (#158)
- Marks an admin mint's `request_id` used and commits its record with the bill,
  as over NWC, when the request carries one with its `expiration`. (#158)
- Checks `mint_cash` in NIP-CASH's order: recipients first (over 100, or one
  amount or the total over the per-wallet ceiling, is `QUOTA_EXCEEDED`), then
  the Identity Authorities (`RESTRICTED`), commitments and refused fields, then
  the expiry (over the Hub's ceiling is `RESTRICTED`), then the balance. (#158)
- Drops the stranded-fund records and the `void` bill outcome: a failed cash
  call now leaves nothing behind to reconcile. (#158)
- Commits each `mint_cash`, `cash_transfer` and `cash_consolidate` as one
  database transaction: the new bills, their slices and signed tokens, the
  funding, the consumed claim proofs and the mint's record commit together or
  not at all. Value moves between bills as ledger rows, with no Lightning
  invoice. A slice claimed by a concurrent call is refused with `RESTRICTED`,
  and a claim proof already used with `UNAUTHORIZED`. (#158)
- Answers `cash_status` `mine` only from the item-proof signer and refuses a
  signer that holds no slice with `NOT_FOUND` for every scope; roster rows of
  `connection_key` slices carry their `ia_pubkey`. (#158)
- Signs every `circlehub1` string with the node identity and chain, and names
  the member's `connection_pubkey` in a join's encrypted details. (#158)
- Requires an `expiration` tag on `mint_cash` (its deadline, at most the
  announced `max_request_age` ahead) and refuses a used `request_id` with
  `REQUEST_REUSED`; a mint running concurrently on the same Hub is
  `RATE_LIMITED`. (#158)
- Takes a `request_id` on `mint_cash` and, for a cash recipient, the
  caller's commitment; the Hub no longer generates or returns a cash secret.
  `idempotency_key`, `min_transfer_millis` and a recipient `cash_secret` are
  refused, and the reply carries `cash_token` without `pairing_uri`. (#158)
- Signs every bill under `cash-mint:v1` and every `cashhub1` string with the
  node identity and chain, as the final NIP-CASH requires. (#158)
- Refuses to sign text starting with `cash-mint:`, `cash-zap:`, `cash-hub:`,
  `cash-order:` or `circle-hub:`, over NWC (`RESTRICTED`) and from the
  operator's sign-message screen. (#158)
- Answers `cash_status` on a cash-mode bill from the bill proof alone and
  refuses one that carries a `cash_secret`; a destroyed bill's answer is
  `{"status":"spent"}`. (#158)
- Refuses to start on a database created before 0.5.0's final schema, and no
  longer converts older databases. Reset the database before upgrading. (#158)
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
- Moves to `nmilat` v0.5.0-rc.45, the published client SDK with the NIP-CASH
  and NIP-CW wire changes in this release. (#153)

### Fixed

- Ignores an LSP announcement dated more than 15 minutes ahead (NIP-LSP step
  4): it is not seen, undoes such a newest-seen mark kept earlier, and ranks a
  future-dated row last when the catalogue is pruned. One far-future event
  locked out the node's later announcements, `closed` included, and
  future-dated spam crowded real LSPs out of the catalogue.
- Keeps an LSP's announcement usable through its expiration second, as NIP-40
  asks, and refuses LSPS messages to an LSP whose announcement expired since
  the catalogue's last refresh. The sweep dropped it a second early, and
  purchases went ahead until the next refresh.
- Buys JIT liquidity only at terms compared with the LSP's announcement: an
  automatic purchase fetches the fee menu at buy time (and again on a stale
  retry) and refuses it when worse for the buyer, a manual one compares the
  exact fee row bought and refuses a worse one, and both refuse a payment
  outside the row's size bounds or one the fee would take whole. The buy
  re-fetched the menu and bought its first row unseen, so an LSP could pass
  the live check and sell at a higher fee.
- Fails an LSP's live check when its announced LSPS1 or LSPS2 `get_info`
  errors or cannot be read. Such a check passed as terms equal to the
  announced ones, so a purchase went ahead with no live terms compared.
- Asks no explorer at all when none is configured: `/api/mempool` and its
  Wails route answer empty, and the unused-address check keeps the current
  address, instead of sending a request to an empty base URL.
- Refuses to start beside a node on another chain than `NETWORK` names, and
  refuses an unknown `NETWORK` value, listing the accepted ones. A
  `NETWORK=regtest` on a mainnet node used regtest swap keys and addresses.
- Publishes the dev stack's ports (relay, flnd RPC, Caddy) on the loopback
  only, unless `DEV_BIND` says otherwise; they were open on every interface of
  the host.
- Rate-limits `/api/unlock`, `/api/start`, `/api/backup` and `/logout` by the
  connection's own address. The limiter trusted `X-Forwarded-For` and
  `X-Real-IP`, so a caller could forge a fresh address on every unlock attempt.
- Updates echo to 4.16.2, wails to 2.13.0, golang.org/x/net to 0.62.0,
  golang.org/x/image to 0.45.0 and the frontend's zod to 4.6.7, for the
  published advisories against the older versions (echo's WebSocket upgrade
  access bypass among them).
- Lets the operator rotate the private-transport inbox key
  (`POST /api/private-transport/rotate-inbox`, and its Wails route): the Hub
  moves to the next key index, subscribes the new inbox, announces it, and
  keeps opening requests to the old one for `max_request_age`. The index was a
  constant, so a leaked inbox key could not be replaced.
- Seals a circle wallet invoice's preimage at rest until it settles, with a
  key derived from the node's mnemonic at unlock; an open invoice's preimage
  is no longer stored plain or logged, and rows written before are sealed at
  the next unlock.
- Never stores a cached `get_info` computed before an Order connection's
  retirement or new terms, or a circle's close, committed: such a result could
  be stored after the invalidation and served stale for up to 5 s. A listed
  circle connection's cached `get_info` is now also dropped when the circle
  opens to new members again, and when any of its listings closes.
- Bounds an operator's own on-chain spend or channel open by what the
  wallet's coin selection may spend (confirmed coins under no lease), less the
  floor and what payouts still need from those coins. The guard counted every
  leased coin, another user's included, as spendable, and subtracted the
  set-aside of payouts whose coins were already spent.
- Reads the node's wallet for on-chain order payments from a stored floor 100
  blocks below the tip (and the mempool) instead of its whole history every
  30 s, and stops checking a minted payment deeper than that, counting its
  confirmations from its height. A mint's confirmation check reads from the
  payment's own block.
- Issues a circle wallet's hold invoice outside the database transaction:
  the invoice's row is reserved under the Hub's lock (it counts against every
  cap at once) and completed once the node answers, so a slow node no longer
  stalls every write. An invoice whose wallet stopped receiving meanwhile is
  cancelled at the node before it is handed out.
- Cancels at the node, after the delete commits, every hold invoice a circle
  wallet delete fails, and releases at once a payment the node holds for an
  invoice already failed. An invoice issued between the delete's cancel and
  its commit stayed payable, its HTLC held to its CLTV.
- Lets the circle lifecycle job delete a closing wallet even when the node
  refuses to cancel its open invoices, and asks the node only about wallets
  with an open invoice; an invoice the node does not know is taken as
  cancelled. One persistent node error blocked the wallet's deletion and
  logged an error every minute.
- Bounds a Cash Hub's `cash_redeem` payouts in flight to any one paying node
  (`CASH_REDEEM_INFLIGHT_PER_PAYEE`, default 8; same-node payouts are not
  counted). One holder of many slices paying its own hold invoices could take
  all 32 of the Hub's slots and block every other redeem.
- Spends `mint_cash`'s hourly allowance only on a mint that commits, as the
  lineage spin-off allowance already was (also under the regtest rate
  override). A refusal made after a call's `request_id` was marked used now
  says the id stays used, so a retry needs another.
- Counts a Cash Hub's live bills again inside a spin-off's or merge's commit,
  under a per-Hub lock, so concurrent calls on different bills can no longer
  pass `CASH_MAX_LIVE_BILLS_PER_HUB` together; the refusal stays
  `QUOTA_EXCEEDED`.
- Records an order payment's bills past the 32768th in mint order. The
  record ordinal was 16 bits and wrapped, so `cash_lookup` of a very large
  over-payment returned its bills out of order.
- Bounds how long a cash commit holds the database write lock while the node
  signs its bills: all of a commit's signatures share one budget of 3 s plus
  25 ms a bill, and the node identity is read once per commit. A slow node
  could hold the lock for up to 3 s per bill.
- Records a private item's request event only while its target bill still
  exists, under a lock the bill's delete waits on. A bill deleted by a sibling
  item's split in the same envelope logged a `FOREIGN KEY constraint failed`
  error before the item was answered as a destroyed bill.
- Keeps the newest-seen mark of an LSP pruned from the catalogue, until every
  version it covers has expired, so a relay cannot re-feed an older
  announcement of it as new.
- Asks the owner to confirm a Cash Hub delete that ends the watch on paid
  on-chain order addresses, in the delete dialog and on every delete path
  (`DELETE /api/apps/:id` takes `{"confirmWatchEnd": true}`). The generic
  delete went ahead unconfirmed.
- Reports a Cash Hub's listed offers in its delete check (`listed_offers`,
  `listings_delete_due_at`) and in the delete dialog, as information: the
  delete closes them and their deletions follow.
- Keeps a cash bill live through its whole `expires_at` second, as NIP-CASH's
  inclusive bound asks: the expiry is stored in whole seconds at mint, and
  calls, the sweep and the operator views compare whole seconds on the hub
  clock. A call late in that second was refused `RESTRICTED`.
- Ends a cash bill on an owner delete or the expiry sweep in one transaction:
  its unclaimed slices are taken first-wins, a `cash_redeem` in flight defers
  the delete, and the balance moves to the Cash Hub with the archive. A redeem
  claiming a slice during the old reclaim payment could lose its payout to
  the Hub.
- Removes a recipient's slice, sweeps its amount back to the Cash Hub and
  archives it in one transaction. A crash between the sweep and the removal
  could leave the slice claimed, impossible to remove again and archived as
  `redeemed`.
- Shows `cashhub1`, `circlehub1`, `cashorder1` and `lokicash1` QR codes in
  uppercase, which fits the QR alphanumeric mode; copying still gives the
  lowercase string.
- Reports only NIP-47's four networks: testnet4 is `testnet`, simnet
  `regtest`, and a node on a network with no NIP-47 name signs no cash.
- Refuses cash serving when the node identity already signed cash on another
  network, recorded in `cash_identity_networks` the first time cash serving
  starts. The private transport stays down and the node signs no bill,
  connection string, listing or withdraw quote.
- Drops a private envelope that carries a case variant of `nonce`,
  `reply_to`, `not_after` or `items`, and bounds a served envelope's
  `not_after` by `max_request_age` itself. Such an envelope could pass the
  replay and freshness checks on one value and run with another.
- Answers `cash_lookup` with `unknown` only after any commit of that
  `request_id` still in flight has finished, under the same lock the commit
  checks its deadline in. A call whose commit landed just after its deadline
  could otherwise be reported as never applied.
- Lists every slice in a `cash_status` answer of scope `all`, including the
  claimed slice of an identity that was later given another slice in place;
  that identity's live row comes first, and `mine` still returns that one row.
- Gives a new Cash Hub a `min_transfer_millis` default of 1000 (one loki) when
  its creator names none, and starts the New Cash Hub form there; an explicit
  0 still means no floor.
- Refuses a Lightning invoice that carries more than one payment hash, in
  `cash_redeem` and every other payment path. Decoders disagree on which hash
  counts, so the hash a claim proof binds could differ from the one paid.
- Refuses a recipient or `new_identity` whose `identity_value` or `ia_pubkey`
  has uppercase hex, in `mint_cash` and `cash_transfer`. Such a slice could
  never be claimed, because claim proofs and attestations carry lowercase keys.
- Keeps serving member requests for the Circle Wallets a Hub issued with a
  rotation delay after its host turns rotation off; only a Hub with no such
  wallet refuses them all with `NOT_IMPLEMENTED`. A member promised recovery
  in their details lost it when the setting changed.
- Bounds a Circle Wallet Hub's dust threshold by `CIRCLE_MAX_DUST_MLOKI`
  (default 10,000 mloki, the default routing-fee reserve floor), on creation
  and on update. A host could otherwise set a threshold above any balance and
  keep every deleted wallet's funds as dust.
- Refuses a frozen Circle Wallet's payment under the payment's lock, so a
  freeze committed while a request was past its gate still stops it.
- Refuses every request by a frozen Circle Wallet's key with `FROZEN`, a
  private-only method's included, and lets any other request by the current
  key cancel a pending rotation before a method is refused; both came after
  the `NOT_IMPLEMENTED` refusal of private-only methods.
- Refuses `delete_circle_wallet`, and the lifecycle's deletion, while a
  same-node keysend to the wallet is still to be credited or a held keysend
  the node settled is not yet credited; the held keysend's reservation is now
  recorded in `circle_held_keysends`, under the Hub lock the deletion takes. A
  same-node credit cancelled meanwhile now fails its payer instead of being
  settled to a deleted wallet.
- Checks a joiner's membership again inside the join's commit, under the lock
  a membership resolution takes. A member an allowlist edit removed while
  their join was in flight was issued an Active wallet.
- Resumes the node's invoice stream after a reconnect from the last
  `add_index` and `settle_index` it delivered, so an order invoice settled
  while the stream was down is minted at once rather than at `pay_by`.
- Refuses `cash_lookup` of an order past its `pay_by` with a retryable
  `INTERNAL` while its window cannot be ended (the chain or its invoice cannot
  be read, or the invoice not cancelled), instead of answering `open`. A client
  reads an open order past `pay_by` as ended unpaid, and stopped reading an
  order the Hub then minted.
- Funds an on-chain payout's replacement (an RBF bump, or the invalidation a
  cancel needs) from the inputs its broadcast version already spends. The node
  no longer lists a coin an unmined transaction of its own spends, so a
  replacement could fail "not an unspent wallet coin" whenever such an input
  was not leased; the input is now read from its parent transaction in the
  wallet, only when the wallet marks that output its own, then leased and
  signed as before. (#158)
- Stops re-leasing the coins a confirmed, not yet final payout version spent.
  The wallet no longer holds them, so every lease was refused "unknown
  output": at each restart's reconcile, which re-leases every coin, and on
  every pass once their leases came due (a clock jump makes them due at
  once). A reorganisation that unconfirms the version has them leased again. (#158)
- Marks a CPFP child conflicted once another version of its batch confirms
  (its parent, a reorganised version, lost): it stayed broadcast forever, and
  the parent's change it spends, which no longer exists, was leased every pass
  ("unknown output"). The change of a conflicted, replaced or invalid version
  is no longer leased. (#158)
- Keeps an on-chain order open past `pay_by` while the node's wallet cannot be
  scanned, instead of expiring it and keeping a payment seen in time. The next
  good scan judges each output by the node's first-seen time, or by the chain
  tip read at `pay_by`; failed scans are logged as errors. (#158)
- Refuses a `cash_withdraw` whose quote expired or whose bill expired while it
  waited for its locks: the commit now reads the chain tip and the bill again
  under them. (#158)
- Stops the expiry sweep from reclaiming a bill whose slice a `cash_redeem`
  claimed but has not started paying yet. The redemption won the slice, and
  its payment would have failed with the funds already back at the Hub. (#158)
- Publishes a payout's CPFP child on its own when the node refuses it as a
  package. lokid has no package relay, so the child of a reorganised payout
  was refused every pass; now its parent is republished and the child sent
  after it, which relays whenever the parent meets the minimum relay fee by
  itself. A parent below that fee still needs package relay: both are retried
  each pass while the parent keeps being republished. (#158)
- Answers `UNAUTHORIZED`, not `NOT_FOUND`, to a `cash_redeem`,
  `cash_withdraw`, `cash_transfer` or `cash_consolidate` item whose item proof
  is signed by a key other than its claim proof's signer: NIP-CASH §Item Proofs
  treats such a proof as one that does not verify. A signer holding no slice
  whose claim proof is its own still gets `NOT_FOUND`. (#158)
- Refuses a tampered `cash_withdraw` quote (its content changed, its id and
  signature kept) at step 3 with `RESTRICTED`/`quote_invalid`, as NIP-CASH
  orders it: the claim proof's `q` now binds the quote's id as sent, and the
  quote's own check finds that the content no longer hashes to it. It was
  refused `UNAUTHORIZED` at step 2. (#158)
- Holds a `cash_withdraw` claim proof's freshness to the wall clock, as
  `cash_redeem` and the item proofs are, not to the test hooks' Hub clock
  offset: with the offset moved a fresh proof was refused `UNAUTHORIZED`.
  Production runs no offset, so nothing changes there. (#158)
- Takes a circle join at any `max_amount`. A join was refused
  `LIMIT_EXCEEDED` under 11,000 mloki (the fee reserve plus 1 loki), a floor
  NIP-CW step 6 does not allow and `get_info` never advertised. The cap is
  rounded down to whole loki, reported as applied, and stored exactly on the
  wallet, so a request under 1 loki is a send cap of 0, never "no cap"; a cap
  under the fee reserve makes a wallet that pays only on this node. (#158)
- Reads a payment's update stream until it succeeds or fails. A node run with
  `routerrpc.usestatusinitiated` sends `INITIATED` first, which was taken as a
  failure while the payment's HTLCs were still out. (#158)
- Refuses a private-transport item whose `cash_secret` does not verify with
  `UNAUTHORIZED` instead of `NOT_FOUND`, as NIP-CASH requires. (#158)
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
