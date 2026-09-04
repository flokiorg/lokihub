package controllers

import (
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// circleWalletIdentityFreshnessWindow bounds how old (or how far in the
// future) a circle wallet identity proof's own timestamp may be. Mirrors
// cashRedeemIdentityFreshnessWindow (cash_redeem_controller.go) — defense in
// depth on top of the per-hub d-tag binding and the single-use replay guard,
// not the primary protection.
const circleWalletIdentityFreshnessWindow = 5 * time.Minute

// nostrKindCircleIdentityProof is NIP-CW's own per-call identity proof kind —
// deliberately its own number, not NIP-CASH's nostrKindClaimProof (23198),
// even though both are structurally similar "prove you control this pubkey,
// bound to this specific hub/wallet" single-use events: they're two distinct
// NIPs, and sharing a borrowed number was exactly the kind of ambiguity this
// split is meant to remove (see nostrKindClaimProof's own doc comment for the
// full history — this proof used to informally reuse that same constant,
// which was itself squatting on NIP-IC's kind 35521). Ephemeral range
// (20000-29999): never independently published to a relay, only ever
// travels embedded inside an already end-to-end-encrypted NIP-47 request.
// 23199 sits directly adjacent to NIP-CASH's 23198 and NIP-47's own
// 23194-23197 block, since NIP-CW depends on NIP-47 too.
const nostrKindCircleIdentityProof = 23199

// verifyCircleWalletIdentityEvent checks a kind-23199 proof that the caller
// of create_circle_wallet actually controls requesterPubkey, rather than
// merely knowing it. The circle_hub connection is meant to be shared/public
// (every prospective member uses the same connection string), so without
// this, anyone holding it could claim to be any pubkey they know — enabling
// rate-limit DoS against the real holder, an allowlist-membership oracle, and
// commitment/balance griefing. Unlike cash_redeem's kind-23198 proof, there is
// no invoice to bind to (this call creates a wallet, it doesn't pay against
// one); the d-tag binds the proof to this specific hub instead, and the
// caller is responsible for also enforcing single-use via the event ID
// (see the CircleWalletIdentityProof replay guard in the controller).
func verifyCircleWalletIdentityEvent(ev *nostr.Event, requesterPubkey, hubAppPubkey string) error {
	if ev.Kind != nostrKindCircleIdentityProof {
		return fmt.Errorf("identity_event must be kind %d, got %d", nostrKindCircleIdentityProof, ev.Kind)
	}
	valid, err := ev.CheckSignature()
	if err != nil || !valid {
		return fmt.Errorf("identity_event has invalid signature")
	}
	// CheckSignature verifies the signature against a hash it recomputes from
	// the event's own fields — it does NOT check that the client-supplied
	// evt.ID field equals that hash (only CheckID does). Since the
	// replay-guard in the controller trusts identityEvent.ID as a unique key,
	// skipping this would let anyone holding one captured, validly-signed
	// proof resubmit it indefinitely by changing only the ID field: the
	// signature stays valid (it doesn't cover ID), so the guard would never
	// see a repeat.
	if !ev.CheckID() {
		return fmt.Errorf("identity_event id does not match its content")
	}
	// The event's own signer IS the proof of ownership — circle identities
	// are always raw pubkeys, there is no connection_key/IA-attestation mode
	// like Cash's cash_redeem has.
	if ev.PubKey != requesterPubkey {
		return fmt.Errorf("identity_event must be signed by the requester pubkey")
	}
	dTag := ev.Tags.Find("d")
	if len(dTag) < 2 || dTag[1] != hubAppPubkey {
		return fmt.Errorf("identity_event d-tag does not match this circle hub")
	}
	now := time.Now()
	evTime := ev.CreatedAt.Time()
	if evTime.Before(now.Add(-circleWalletIdentityFreshnessWindow)) || evTime.After(now.Add(time.Minute)) {
		return fmt.Errorf("identity_event is stale or has a future timestamp")
	}
	return nil
}
