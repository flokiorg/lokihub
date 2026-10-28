# Real-NWC integration suite

This is a **black-box** test suite that drives real JIT Hub and Circle Hub
parent connections purely as an external NWC client would — over a real
Nostr relay, with real NIP-44 encryption, creating real child wallets and
moving real money between them. It is a separate thing from the unit tests
elsewhere in this repo (`apps/`, `nip47/controllers/`, etc.), which call Go
functions directly in-process against a mocked Lightning client.

It is gated behind the `integration` Go build tag, so it's completely
invisible to normal `go build`/`go test ./...` runs.

Every jit_hub/circle_hub/simple-wallet fixture a test needs — including a
synthetic Nostr identity for a circle "follow_set" policy — is provisioned on
demand through the instance's own admin API and torn down again in its own
`t.Cleanup` (see `ephemeral_test.go`). Nothing is hand-provisioned ahead of
time.

## What you need before running this

**The regtest dev stack** (`just dev up`): a regtest chain node, the Hub's flnd,
a second flnd (the payer) with a channel each way, the dev relay and lokihub
itself. `just dev up` runs `just dev bootstrap`, which sets lokihub up, mints an
admin JWT through `POST /api/unlock`, points the Hub's `GeneralRelay` at the dev
relay, seeds the relay (`testdata/social_graph.json`, see `cmd/seedrelay`) and
writes `config.local.yaml` (git-ignored) here. Hand editing is optional; see
`config.example.yaml` for its fields. Point `INTEGRATION_CONFIG` at another path
to use a different file.

**Never point this suite at a mainnet Hub.** `TestAAA_PreflightCleanup` checks
the Hub's `get_info` `network` first and ends the whole run unless it is
`regtest`. Never configure an LSP on the test Hub: it would buy JIT channels and
shrink invoice amounts.

What each node holds after a bootstrap: both on-chain wallets hold mature
coinbases, and each node has opened one 5 000 000 loki channel to the other with
half pushed across, so each side has outbound and inbound room. Fixtures are
funded by admin transfers from the Hub's balance (see `adminClient.transfer`), and
every invoice they pay is the Hub's own, so the existing tests settle as
self-payments; the payer and the chain serve the order, withdraw and external
payment cases. `just dev mine`, `just dev fund` and `just dev status` drive and
inspect the chain (`just dev help`).

**Test hooks.** The dev backend is built with the `testhooks` tag and runs with
`LOKIHUB_TEST_HOOKS=1`, so it serves chain and payer hooks under `/api/test`
behind the admin token. They refuse every call unless the Hub's own node is on
regtest, and release builds never contain them:

| Hook | Does |
|---|---|
| `POST /api/test/chain/mine {blocks, address?}` | mines blocks (to the payer unless `address`), leaving out withheld transactions |
| `GET /api/test/chain/tip` | tip height and hash |
| `GET /api/test/chain/tx/{txid}` | raw transaction and depth (0 in the mempool) |
| `POST /api/test/chain/withhold {txid, on}` | keeps a transaction, and anything spending it, out of mined blocks |
| `POST /api/test/payer/pay-invoice {invoice}` | the payer pays a BOLT11 invoice |
| `POST /api/test/payer/pay-address {address, amount_millis}` | the payer pays on-chain (whole loki) |
| `POST /api/test/payer/invoice {amount_millis}` | an invoice from the payer, for an external payment |
| `GET /api/test/payer/invoice/{hash}` | that invoice's state |
| `GET /api/test/payer/address` | a fresh payer address |
| `POST /api/test/chain/reorg {depth}` | reorganises the last `depth` blocks (default 1) away and mines `depth + 1` new ones to the payer, leaving out withheld transactions |
| `POST /api/test/circle/fund {wallet_pubkey, amount_millis}` | an invoice on that wallet's app (a Circle Wallet's Hub-settled one), paid by the payer |
| `POST /api/test/relay/min-pow {difficulty}` | the relay enforces and announces that NIP-13 minimum (0 clears); its supervisor restarts the relay process, and the hook answers once NIP-11 shows it |
| `POST /api/test/restart` | the backend stops and air runs it again, reloading the Hub from its store; `202` at once, back when `GET /api/info` answers |
| `POST /api/test/flspd/fee/{raise,restore}` | worsens flspd's terms by its dashboard (opening fee rate twice plus 1 % and floor plus 1,000 loki; smallest channel plus 1,000 loki), or restores them |
| `POST /api/test/flspd/announce/{pause,resume,unlist}` | sets flspd's NIP-LSP announcement state by its dashboard |

`relay_url` is the Hub's own relay (the `RELAY` value in `.env`), and it must be
one of the Hub's `GeneralRelay` settings, which the backend's `follow_set`
circle-policy check (`service/nostr_social_cache.go`) queries. This suite never
talks to a public relay.

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
`CASH_WALLET_RATE_LIMIT_PER_HOUR`/`CASH_WALLET_CLAIM_RATE_LIMIT_PER_HOUR`/
`CIRCLE_WALLET_RATE_LIMIT_PER_HOUR` all set to `0` ("disabled", not "zero
allowed" — see `rate_limiter.go`'s `Allow()`), so an operator can hammer
`create_jit_wallet`/`claim_funds`/`create_circle_wallet` from the frontend
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
  on: `createEphemeralJITHub`/`createEphemeralCircleHub` (either policy, via
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
- `cash_hub_test.go` — `create_jit_wallet` happy path, invalid identity_type
  rejection, a shared multi-recipient wallet funded with the SUM of every
  recipient (one connection, `list_recipients` shows both), the total-across-
  recipients per-wallet amount cap, max-expiry cap, spend-only enforcement (no
  `make_invoice`/`pay_invoice`/`list_transactions`/`lookup_invoice` on a
  jit_wallet child), `connection_key` mode (missing `ia_pubkey`, invalid hex,
  untrusted-IA rejection, happy path via `createEphemeralTrustedIA`),
  empty-recipients rejection, pubkey mode having no cross-request dedupe (two
  independent `create_jit_wallet` calls for the same beneficiary yield two
  distinct wallets — only duplicate identities *within one request* are
  rejected, and that's unit-test-only, see below), omitted-expiry defaulting
  to the hub's max, and (opt-in) rate limiting.
- `cash_redeem_test.go` — the shared-wallet, proof-gated `claim_funds` model:
  several independent recipients each claiming their own slice from the same
  connection without affecting each other, a wrong/outsider identity finding
  no matching slice, a claim proof bound to one invoice being rejected when
  submitted against another (the core "shared connection is public" audit
  scenario), a proof for one wallet being replayed against a different wallet
  the same identity also has a slice on, `get_budget` staying restricted while
  `get_info` stays reachable, `list_transactions`/`lookup_invoice` staying
  ungranted, `connection_key`-mode claiming (live IA attestation verification
  via `createEphemeralTrustedIA`), a stale claim proof being rejected and the
  same invoice remaining claimable with a fresh one, a wallet's own expiry
  (not the claim proof's freshness window) being enforced end to end via a
  short-lived (2s) real wallet, an attestation reused for a different
  claimant than it was issued to, an attestation whose own expiration has
  passed, an attestation missing its (now-mandatory) expiration tag, an
  attestation signed by a real keypair that isn't this slice's recorded
  trusted IA, an attestation with a forged/garbage signature, and (opt-in)
  rate limiting.
- `cash_hub_payment_test.go` — real money movement sourced entirely from one
  jit_hub (no `circle_hub` needed): the hub mints an invoice for its own
  freshly-minted JIT child to pay and fully drain (including that a second
  claim attempt on the same now-fully-claimed slice is rejected), and paying
  an invoice that exceeds the child's declared slice is rejected.
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
  child mints an invoice, a jit_wallet child pays it as a real internal
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
  at all, unlike a jit wallet child.
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
  can't move money. Separately, for both jit_hub and circle_hub: once the
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

The JIT Hub allocation ("voucher") override path (`CreateJITHubAllocations`
pre-defining a fixed amount/expiry for an identity, picked up automatically by
`create_jit_wallet`) isn't covered — it's a small, deliberate gap rather than
an admin-API limitation (this suite already reaches for the admin API for
fixture setup elsewhere).

Real insufficient-hub-balance and transfer-failure/rollback paths, the
per-wallet recipient-count cap (`maxRecipientsPerWallet`), and in-request
identity dedupe are also left unit-test-only — all pure, deterministic
validation logic that doesn't need a live relay or LN node to exercise
meaningfully.
