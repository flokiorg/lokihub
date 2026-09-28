# API ↔ NIP vocabulary

The REST API and the NIP wire deliberately do not use the same names for the same
things. This file exists so that is a recorded decision rather than folklore — the
kind that gets "fixed" by someone who assumes it was an oversight, breaking clients
in the process.

Written 2026-09-28, as Phase 5 of the NIP conformance review. Every row was checked
against both sides.

## The rule

**The NIP wire is the contract with other implementations. The REST API is the
contract with our own frontend.** They are versioned independently and have
different audiences, so they are allowed to diverge — but only deliberately, and
only in naming. Neither may diverge in *meaning*: the same field must denote the
same quantity on both sides, or one of them is a bug.

## Amounts: `mloki` vs `millis`

| surface | name | example |
|---|---|---|
| NIP wire (NIP-CASH, NIP-CW) | `..._millis` | `amount_millis`, `min_transfer_millis`, `redeem_fee_millis` |
| REST API + DB columns | `..._mloki` | `amount_mloki`, `min_transfer_mloki` |

Same unit, different word. The NIP rename to `millis` was deliberately scoped to the
wire, because `mloki` is chain-specific and a spec meant to work for a Bitcoin-backed
`satscash1...` wallet cannot name its unit after flokicoin. The REST and DB side kept
`mloki` because renaming it is a migration plus a frontend change, for no benefit to
any external implementer.

**There is no longer an exception.** `available_mloki` in `get_info`'s `circle_wallet`
block was the one field on the NIP wire still using `mloki` — the rename had missed it.
It was renamed to `available_millis` on 2026-09-28 (C-13 in the conformance triage),
deliberately **without** a compatibility alias: the field had no external consumers we
could find, the private transport it is most useful to is not enabled by default, and an
alias on a field that exists to be read once at discovery time would have outlived its
usefulness immediately. Any client reading the old name must be updated.

## Casing: `snake_case` vs `camelCase`

| surface | convention |
|---|---|
| NIP wire | `snake_case` throughout — NIP-47's own convention |
| REST API, cash endpoints | `snake_case`, deliberately mirroring the NIP |
| REST API, circle endpoints | `camelCase` — `circleFeesPpm`, `circleMinBudgetRenewal` |

The cash REST endpoints mirror the NIP's `snake_case` because their payloads are
close to pass-through; the circle config endpoints use the app-config convention the
rest of the REST API already uses. This is inconsistent *between* REST endpoints,
which is the real cost of the decision and is accepted rather than unnoticed.

## Deliberate asymmetries that are not naming

These are recorded here because each has previously looked like an omission:

- **A cash connection can be re-revealed; a circle wallet cannot.** There is a REST
  endpoint that re-derives a Cash Wallet's connection string on demand, and
  deliberately no equivalent for a Circle Wallet. A Cash Wallet's pairing secret is
  derived from its connection identifier, so it is re-derivable; a Circle Wallet's is
  not, which is a custody choice NIP-CW states explicitly. Adding the circle endpoint
  would require changing that custody model.
- **`empty_only` teardown mode is REST-only.** The API accepts a teardown mode that
  NIP-CW's own lifecycle section does not describe, because it is an operator
  affordance rather than a protocol behaviour a member can invoke.
- **`min_transfer_mloki` is not a create-request field.** It is inherited from the
  issuing Cash Hub's configured default, never supplied per call. The REST request
  type omits it on purpose, and so does the TypeScript type.

## If you are about to change one of these

Renaming a field on the NIP wire is a protocol change: it needs a compatibility
window during which both names are accepted and both are advertised in `get_info`.
NIP-CASH §Cash Status describes how that was done for `list_recipients` →
`cash_status`, including the part that is easy to miss — the alias has to be honoured
on *every* transport, not just the one you tested. Renaming a REST field is a
frontend change in the same commit, because nothing else pins the two together.
