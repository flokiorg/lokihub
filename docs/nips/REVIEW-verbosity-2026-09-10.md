# NIP-CASH / NIP-CW: verbosity & style review (2026-09-10)

Ask: are `NIP-CASH.md` and `NIP-CW.md` pitched at the right technical depth, or over-explained,
relative to how NIPs are actually written elsewhere? This is a diff against real reference
material, not a subjective read.

## Method

Two reference corpora were pulled from GitHub for comparison:

- **[nostr-protocol/nips](https://github.com/nostr-protocol/nips)** — the canonical registry.
  Sampled **NIP-47** (Nostr Wallet Connect), since it's the one dependency both of our documents
  already declare, making it the closest available same-domain baseline.
- **[block/buzz](https://github.com/block/buzz/tree/main/docs/nips)** — a third-party project
  writing its own lettered house NIPs, the same pattern lokihub follows. Sampled **NIP-CW**
  ("Channel Window") and **NIP-MP** (~1100 lines), as the closest structural analogues to our own
  documents: both are non-core, implementer-facing extension specs with wire formats and
  processing algorithms, not simple core-protocol NIPs.

## Headline finding: a real NIP-CW naming collision

`block/buzz/docs/nips/NIP-CW.md` already exists, and defines something entirely different:
**"Channel Window"**, a relay-side cursor-pagination extension for threaded channel timelines.
Ours is **"Circle Wallet"**. Same letter code, incompatible meaning.

nostr-protocol/nips has no reservation registry for third-party lettered NIPs — there's no
central authority either project could have checked against — so this collision isn't a process
violation on our side. But it's a real practical risk: an implementer who has buzz's NIP-CW open
in one tab and ours in another has no way to disambiguate by name alone, and any shared tooling,
search index, or LLM-assisted implementer that treats "NIP-CW" as a global key will collide the
two. Worth a deliberate decision (rename ours — e.g. `NIP-CIW`/`NIP-CRW` — or knowingly accept the
risk since neither is submitted to the canonical registry), not something to leave implicit.

## Verbosity assessment

| Document | Lines | Structure |
|---|---|---|
| NIP-47 (nostr-protocol/nips) | ~450-500 | Rationale → Terms → Theory of Operation → Events → Commands → Extensions → Example Flows |
| buzz NIP-CW (Channel Window) | ~250 | Abstract → Motivation → Non-Goals → Terminology → Request → Processing Algorithm → Client Behavior → Degradation → Security/Privacy → Gotchas → Relation to Other NIPs |
| buzz NIP-MP | ~1100 | Abstract → Motivation → Non-Goals → Terminology → Kinds → Event Format → Semantics → Relay Processing → Client Behavior → Fixtures → Security Considerations → Relation to Other NIPs |
| **our NIP-CW.md** | **400** | comparable shape and length to buzz's own analogues |
| **our NIP-CASH.md** | **1443** | same shape, but ~30% longer than the longest reference sampled (buzz NIP-MP), ~3x NIP-47 |

**our NIP-CW.md is proportioned correctly** — it lands right in the range both reference NIPs of
similar scope occupy. No verbosity flag there; its only issue is the name collision above.

**our NIP-CASH.md is the outlier**, and the excess isn't new normative content — it's the same
design rationale re-derived at each section that touches it, rather than stated once and
cross-referenced. Concrete example, the "why give both pieces of a split fresh wallets instead of
rewriting the source" rationale appears in essentially the same form four separate times:

- `NIP-CASH.md:60-63` (§Terminology, defining "recipient / slice")
- `NIP-CASH.md:487-491` (§Transferring and Splitting a Slice, "Split off a piece")
- `NIP-CASH.md:694-701` (§Spinning a Slice Off, under its own "Why fresh wallets for both pieces of
  a partial split?" heading)
- `NIP-CASH.md:1324-1332` (§Security Considerations, "A slice is consumed whole...")

Similarly, the reason a redeem fee only applies to a real external Lightning payment is stated in
§The Redeem Fee (`:349-352`), restated in its own "### Why" subsection (`:368-376`), and restated
again as a worked algebra block (`:378-391`, the "fairness invariant" — `claimed`, `fee`, `net`,
`real`, `delta` solved out to confirm the wallet's debit always equals the redeemed slice). Neither
reference corpus goes this far: buzz's own invariants (e.g. NIP-MP's "the fold is deterministic:
same heads in, same collection out") stay to one sentence, asserted rather than algebraically
proven. This is the single largest concentration of length that isn't buying new normative
coverage.

The bearer-slice security reasoning is a third recurring case: §Bearer Slices'
"Security Considerations for Bearer Slices" (`:962-978`), the main §Security Considerations'
"Shared bearer connection, and why that's fine" (`:1268-1273`), and the "A bearer source in a
many-source operation leaks its secret..." bullet (`:1342-1355`) all re-explain the same shared-
connection/no-revocation premise from slightly different entry points.

None of these restatements are wrong — each is locally correct and each section reads fine in
isolation — but stacked across a 1443-line document they're the difference between our doc and the
next-longest reference sampled.

## Is it too technical? (the audience question)

No — on technical depth alone, both documents match their references well. All four documents
compared (NIP-47, buzz NIP-CW, buzz NIP-MP, and ours) share the same conventions: RFC-2119
MUST/SHOULD/MAY normative language, exact wire-format tables, numbered processing algorithms,
and a Security Considerations section that assumes an implementer audience (client, wallet, or
relay authors), not an end user. Nothing in NIP-CASH.md or NIP-CW.md drifts into unnecessary
implementation detail that isn't also present in the reference corpus, and nothing drifts the
other way into vague, under-specified hand-waving either. The audience calibration itself is
correct — a NIP is written for the people implementing it, and ours reads like one.

The actual defect is **redundancy, not depth**: the same justified decision is justified more than
once. Trimming that wouldn't change who the document is for or how technical it reads at any
single point — it would just stop making the same technical point three or four times.

## Calibration guideline for future NIP writing

State a design rationale once, at the section that owns the decision, and cross-reference it
(`§Section Name`) everywhere else the decision is relevant, rather than re-deriving the "why" from
scratch at each touchpoint. This is already the pattern both documents use successfully for some
cross-references (e.g. NIP-CW.md's Identity Proof section explicitly says "see NIP-CASH.md's own
Methods section for the general reasoning, which applies identically here" instead of repeating
it) — the fix is applying that same discipline consistently inside NIP-CASH.md itself, not
introducing a new convention.
