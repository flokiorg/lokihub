package controllers

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NIP-CW §Identity Proof (kind 23199): verification MUST establish every
// property, and a failure of any one MUST be rejected. The id, signer, d tag,
// freshness and replay checks are pinned elsewhere; these are the kind and the
// signature, each tested with a proof that passes every other check.

func validCircleIdentityProof(t *testing.T, requesterPrivkey, hubWalletPubkey string) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      nostrKindCircleIdentityProof,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", hubWalletPubkey}},
	}
	require.NoError(t, ev.Sign(requesterPrivkey))
	return ev
}

func TestVerifyCircleWalletIdentityEvent_ValidProofAccepted(t *testing.T) {
	requester := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requester)
	hub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	require.NoError(t, verifyCircleWalletIdentityEvent(validCircleIdentityProof(t, requester, hub), requesterPubkey, hub))
}

func TestVerifyCircleWalletIdentityEvent_WrongKindRejected(t *testing.T) {
	requester := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requester)
	hub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	ev := &nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", hub}}}
	require.NoError(t, ev.Sign(requester)) // a genuine signature, by the right key, over the wrong kind

	err := verifyCircleWalletIdentityEvent(ev, requesterPubkey, hub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kind")
}

func TestVerifyCircleWalletIdentityEvent_ForgedSignatureRejected(t *testing.T) {
	requester := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requester)
	hub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// Signed by someone else, then relabelled as the requester with its id
	// recomputed: the id, signer, d tag and freshness all check out, and only
	// the signature can tell it apart.
	ev := validCircleIdentityProof(t, nostr.GeneratePrivateKey(), hub)
	ev.PubKey = requesterPubkey
	ev.ID = ev.GetID()
	require.True(t, ev.CheckID())

	err := verifyCircleWalletIdentityEvent(ev, requesterPubkey, hub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}
