# Real-NWC integration suite

This is a **black-box** test suite that drives real Cash Hub and Circle Wallet
Hub connections purely as an external NWC client would — over a real
Nostr relay, with real NIP-44 encryption, creating real child wallets and
moving real money between them. It is a separate thing from the unit tests
elsewhere in this repo (`apps/`, `nip47/controllers/`, etc.), which call Go
functions directly in-process against a mocked Lightning client.

It is gated behind the `integration` Go build tag, so it's completely
invisible to normal `go build`/`go test ./...` runs.

Every cash_hub/circle_hub/simple-wallet fixture a test needs — including a
synthetic Nostr identity for a circle "follow_set" policy — is provisioned on
demand through the instance's own admin API and torn down again in its own
`t.Cleanup` (see `ephemeral_test.go`). Nothing is hand-provisioned ahead of
time.

A Hub is reached through its signed connection string (`cashhub1…`,
`circlehub1…`), whose signature the client checks before connecting; a bill
through its `cash_token`, whose mint provenance is checked first; a member's
circle wallet from its join's details and the member's own secret. Only the
simple-wallet fixture still uses a `nostr+walletconnect://` URI. Every call
that NIP-CASH makes recoverable carries a `request_id`, and `mint_cash`
carries an `expiration` tag as its deadline.

## What you need before running this

1. **A real, running lokihub instance** with a real (or regtest) Lightning
   backend, and its own real balance (any amount - every ephemeral fixture
   self-funds via an internal transfer straight from that balance, see
   `adminClient.transfer`'s doc comment).

2. **An admin API token.** Mint one with `POST {base_url}/api/unlock`
   (`unlock_password`, `permission: "full"`, an explicit
   `token_expiry_days`) - see `http/http_service.go`'s `unlockHandler`. It's
   a bearer JWT, not a permanent key, so refresh it in `config.local.yaml`
   once it expires.

3. Copy `config.example.yaml` to `config.local.yaml` (gitignored) and fill in
   `admin_api.base_url`/`admin_api.token`/`relay_url`. `config.local.yaml` is
   resolved relative to this directory, or point `INTEGRATION_CONFIG` at an
   absolute path.

4. **`relay_url` must be this instance's own relay** (e.g.
   `docker-compose.dev.yml`'s `relay` service in dev), and that relay must
   actually be one of this instance's `GeneralRelay` settings (`PATCH
   /api/settings`, `generalRelay` field) - that's what the backend's
   "follow_set" circle-policy check (`service/nostr_social_cache.go`)
   queries follow sets from. This suite deliberately never talks to
   a public/third-party relay for anything - a real relay this suite
   doesn't control being slow, rate-limited, or unreachable should never be
   why it fails. Run `just dev seed-relay` (or `just dev reset`, which
   already calls it) once to publish `testdata/social_graph.json`'s fixed
   identities' follow list to it - see that file and
   `cmd/seedrelay/main.go`.

5. **`CIRCLE_MAX_DUST_MLOKI=100000000`** (or more) in the instance's
   environment. Ephemeral circles clean up after themselves through a
   100,000,000-mloki dust threshold (`ephemeralCircleDustMloki`), which the
   default bound (10,000) refuses.

## Running

```sh
# from the repo root
go test -tags integration -v ./integration/...

# or with an explicit config path
INTEGRATION_CONFIG=/path/to/config.local.yaml go test -tags integration -v ./integration/...
```

Every test skips cleanly if `admin_api` isn't configured, rather than
failing.

### Running everything, with zero skips

A plain run above always skips the 3 rate-limit tests:
`TestCashHubs/RateLimiting_EleventhCreateIsRateLimited`,
`TestClaimFunds/RateLimiting_TwentyFirstClaimIsRateLimited` and
`TestAudit_CashTransferRateLimit_PerWallet`. That's not a
suite limitation — it's because the dev stack's `.env` ships with
`CASH_WALLET_RATE_LIMIT_PER_HOUR`/`CASH_CLAIM_CALLS_PER_HOUR`/
`CIRCLE_WALLET_RATE_LIMIT_PER_HOUR` all set to `0` ("disabled", not "zero
allowed" — see `rate_limiter.go`'s `Allow()`), so an operator can hammer
`mint_cash`/`cash_redeem`/`create_circle_wallet` from the frontend
during manual testing without tripping anything. These tests need the
opposite: real, nonzero limits, so the 11th/21st call in their loop actually
gets rejected. Setting `INTEGRATION_RUN_RATE_LIMIT_TESTS=1` alone is **not**
enough against dev's default config — the test will run instead of skip, but
then fail, because the limit never trips.

The two `just test` subcommands below handle this for you — the second
temporarily flips those three `.env` values on, restarts the backend, runs
just the three rate-limit tests, then restores `.env` and restarts again
regardless of outcome (a shell `trap ... EXIT`, so a test failure or Ctrl-C
still leaves dev back in its default disabled-limits state):

```sh
just test integration             # normal full run (skips the 3 rate-limit tests)
just test integration-ratelimits  # just those 3, with real limits temporarily on
just test integration-all         # both of the above, back to back - zero skips overall
```

`just test integration` runs `just dev reset` first (restart relay + backend, no
volumes touched) so every run starts from the same server-side session/
subscription baseline, whatever a previous run's own load left behind -
run `just dev reset` on its own to force the same reset without running the
suite. `TestAAA_PreflightCleanup` (runs first, see its own doc comment) then
best-effort-sweeps any ephemeral app/circle identity a prior *interrupted*
run (Ctrl-C, CI timeout, panic) left behind, so a run's leaked-fixture count
always starts at zero - this is separate from `TestZZZ_NoLeakedEphemeralFixtures`/
`TestZZZ_NoLeakedEphemeralCircleIdentities` (run last), which stay strict and
never auto-clean, so a real broken `t.Cleanup` in the run you're currently
watching still fails loudly instead of being swept away.

There's no equivalent test for Circle Hub's 3/hour cap — every
`circle_hub_test.go` scenario except the happy path is rejected by
validation *before* the rate limiter is even consulted (see
`create_circle_wallet_controller.go`), so the whole suite only ever spends 1
of that budget per ephemeral circle hub it creates, regardless of policy.

## What's covered

- `ephemeral_test.go` — the shared fixture layer every other file builds
  on: `createEphemeralCashHub`/`createEphemeralCircleHub` (either policy, via
  the admin API, root-funded through `adminClient.transfer`),
  `createEphemeralSimpleWallet`, `createEphemeralTrustedIA`, and
  `publishFollowSet` (a real signed kind-30000 follow set listing
  whichever pubkeys a test wants authorized under a synthetic provider's
  `follow_set`-policy hub — see its own doc comment for why it must run
  *before* the hub is created; publishes to `config.local.yaml`'s
  `relay_url`, this suite's own dev relay, never a public one). Every
  builder registers `t.Cleanup` to reclaim any children it mints and then
  delete the hub/wallet itself.
- `social_graph_test.go`/`testdata/social_graph.json`/`cmd/seedrelay` — a
  fixed, real (not synthetic) set of dedicated Nostr identities (one
  "provider", five "members") this suite generated once for its own use.
  `just dev seed-relay` (or `dev reset`, which already calls it) publishes
  the provider's follow set once, so a test that wants an
  already-authorized "follow_set"-policy identity can use
  `loadFixedSocialGraph(t)`'s pubkeys directly instead of paying for a fresh
  keypair + a live `publishFollowSet` round trip - existing tests weren't
  migrated to this wholesale, it's available for new/rewritten ones. Any
  pubkey not in `graph.Members` (e.g. `newTestPrivkey(t)`'s) is a ready-made
  "not followed" identity for rejection-path tests.
- `aaa_preflight_cleanup_test.go` — `TestAAA_PreflightCleanup` runs first and
  best-effort-sweeps any ephemeral app/circle identity left over from a
  previous run that never got to finish, so every run starts from the same
  guaranteed-empty baseline. Deliberately non-strict (log-only, never fails)
  and deliberately separate from the two `zz_leak_check_test.go` checks below.
- `zz_leak_check_test.go` — `TestZZZ_NoLeakedEphemeralFixtures` and
  `TestZZZ_NoLeakedEphemeralCircleIdentities` run last and fail loudly if any
  ephemeral hub/wallet/circle identity is still around, catching a
  missing/failing `t.Cleanup` before it can silently reaccumulate into the
  kind of stale-subscription backlog that once tipped a real relay into
  rejecting connections with "too many concurrent subscription" (or, for
  circle identities, into a background refresher endlessly re-querying
  thousands of leaked identities against the general relays - see
  `createEphemeralCircleHub`'s own identity-cleanup comment in
  `ephemeral_test.go`).
- `cash_hub_test.go` — `mint_cash` happy paths for every identity mode
  (pubkey, connection_key through `createEphemeralTrustedIA`, and cash, where
  the caller sends only the commitment and the Hub never returns a secret), a
  shared multi-recipient bill funded with the sum of every recipient, the
  per-wallet ceiling, the expiry ceiling, invalid identity types, spend-only
  enforcement on a bill, and (opt-in) rate limiting. Every bill's token is
  checked to decode and to carry a verifying `cash-mint:v1` provenance.
- `cash_redeem_test.go` — `cash_redeem` over the private transport: several
  recipients redeeming their own slices of one bill independently, an outsider
  finding no slice, claim proofs bound to another invoice or another wallet,
  stale proofs, connection_key redemption with live attestation checks
  (wrong claimant, expired, missing tags, wrong IA, forged signature), a
  bill's own expiry, and (opt-in) rate limiting. Proof failures are
  `UNAUTHORIZED` and an untrusted IA `RESTRICTED`, as NIP-CASH §Errors says.
- `cash_hub_payment_test.go` — real money movement from one Cash Hub: the Hub
  mints an invoice for its own freshly minted bill to redeem in full (the
  drained bill is then destroyed), and an invoice for another amount than the
  slice is refused.
- `circle_hub_test.go` — run against both circle policies:
  `create_circle_wallet` happy path against every authorized member (the
  allowlist policy's last member also exercises omitted-expiry/
  omitted-budget_renewal default resolution on a real successful creation),
  rejection of several independently-generated unauthorized identities,
  per-wallet amount cap, the `MinBudgetRenewal` floor, a max_amount large
  enough to overflow the int64 commitment check, an invalid budget_renewal
  value, several malformed requester pubkeys, the one-active-wallet-per-
  identity cap rejecting a second request once the first has actually
  minted, and the hub's own `get_info` advertising `create_circle_wallet`.
  The `identity_event` proof itself gets deep coverage: missing, malformed
  JSON, wrong signer (impersonation), bound to the wrong hub, stale,
  future-timestamped, a tampered `id` field (the CheckID-vs-CheckSignature
  regression guard), replayed, and a spoofed-identity pair (targeting both
  an actually-authorized and a definitely-unauthorized pubkey) proving the
  error code alone can't be used as an allowlist-membership oracle. All of
  these deep-validation scenarios run against both policies (they're
  rejected before the controller ever reaches the follow-set-vs-allowlist
  authorization check, so they'd behave identically either way, but they
  run unconditionally rather than skipping under `follow_set` — see
  "Running everything, with zero skips" above).
- `follow_set_join_test.go` — a follow set the hub publishes itself
  (`createEphemeralManagedFollowSetHub`), with one public and one hidden
  member: the invite carries the set's title, the published event never
  shows the hidden pubkey, the hidden member joins over the private
  transport and the public one over the standard transport, a non-member is
  refused on both, and a republished set revokes the member it drops.
- `cross_test.go` — real money movement across hub types: a circle_wallet
  child mints an invoice, a bill redeems into it as a real internal
  self-payment, and both sides' balances/transaction history are checked;
  paying an invoice that exceeds a wallet's own balance; the hub's own
  isolated balance decreasing when a child is funded; `get_info` advertising
  the right hub-level methods.
- `circle_wallet_scope_test.go` — a circle wallet child's own spend/receive
  scope: receiving a real external payment via an ephemeral simple wallet
  (`make_invoice` + the simple wallet paying it) and the balance/history
  reflecting it; withdrawing back out to it (mints + the child paying via
  `pay_invoice`) and the balance/history reflecting that too; `pay_invoice`
  rejected with `INSUFFICIENT_BALANCE` when the amount exceeds the child's
  real balance (as opposed to its `max_amount` cap); `make_invoice` rejected
  with `QUOTA_EXCEEDED` when an incoming amount would push the wallet's
  holdings past its own `max_amount` ceiling (computed from `get_budget`'s
  `total_budget`, not hardcoded); and `get_budget`'s `used_budget` reflecting
  a real payment's cost — `get_budget` is reachable on a circle wallet child
  at all, unlike a bill.
- `circle_fee_skim_test.go` — a circle hub created with a deliberately
  nonzero `FeesPpm`: a self-paid invoice (paid and received by the same
  wallet) must never be skimmed, regardless of the configured rate, proving
  `transactions_service.go`'s self-payment exemption from the forwarding
  fee. The genuinely-skimmable case (a payment that leaves the instance over
  the real Lightning network) isn't reachable from this black-box harness
  (no second, external node to pay out to) and is instead covered at the
  unit level by `transactions/circle_fee_skim_test.go`'s
  `TestSendPaymentSync_CircleWallet_FeeSkim_HappyPath`.
- `expiration_test.go` — what actually happens once a real wallet's own
  expiry lapses, for both hub kinds: every money-moving method
  (`get_balance`/`make_invoice`/`pay_invoice`/`list_transactions` on a
  circle child) is rejected with `ERROR_EXPIRED`, while `get_info`/
  `get_budget` keep answering (they're on
  `permissions.GetAlwaysGrantedMethods()` and bypass the expiry check
  entirely) — an expired wallet's balance/budget stays visible, it just
  can't move money. Separately, for both cash_hub and circle_hub: once the
  *hub's own* permission expires, its own create-wallet call starts failing,
  but a child it minted beforehand keeps working — parent and child expiry
  are tracked completely independently (every `AppPermission` row is keyed
  and expiry-checked purely by its own app id, with no join back to a
  parent anywhere in that check).
- `renewal_test.go` — the `MinBudgetRenewal` floor exercised at its exact
  boundary in both directions, for every possible floor value: a request
  exactly at the floor is accepted, one notch tighter is rejected, and
  `budget_renewal: "never"` (the loosest possible rank) is always accepted
  no matter the floor - plus `get_budget`'s own `renews_at`/`renewal_period`
  shape for a `"never"`-renewal wallet (no `renews_at` reported at all).
- `budget_test.go` — `pay_invoice`'s own cumulative-spend quota check
  (`transactions_service.go`'s `validateCanPay`, one layer below
  `pay_invoice_controller.go`): two payments comfortably under `max_amount`
  succeed and accumulate in `used_budget`, then a third payment that real
  balance alone would cover is rejected with `QUOTA_EXCEEDED` — deliberately
  distinct from `INSUFFICIENT_BALANCE`, run against both circle policies.
  Also `get_budget`'s `renews_at` sanity-checked against the real clock for
  every non-`"never"` renewal period.

## What's explicitly out of scope (for now)

Real insufficient-hub-balance and transfer-failure/rollback paths, the
per-wallet recipient-count cap (`maxRecipientsPerWallet`), and in-request
identity dedupe are also left unit-test-only — all pure, deterministic
validation logic that doesn't need a live relay or LN node to exercise
meaningfully.
