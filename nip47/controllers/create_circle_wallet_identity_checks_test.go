package controllers

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NIP-CW §Identity Proof (kind 23199): verification MUST establish every
// property, and a failure of any one MUST be rejected. The id, signer, d tag,
// freshness and replay checks are pinned elsewhere; these are the kind, the
// signature and the connection tag, each tested with a proof that passes every
// other check.

func identityProof(t *testing.T, signerPrivkey string, kind int, tags nostr.Tags) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags}
	require.NoError(t, ev.Sign(signerPrivkey))
	return ev
}

func identityProofFixture() (requester, requesterPubkey, connection, hub string) {
	requester = nostr.GeneratePrivateKey()
	requesterPubkey, _ = nostr.GetPublicKey(requester)
	connection, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	hub, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	return requester, requesterPubkey, connection, hub
}

func TestVerifyCircleWalletIdentityEvent_ValidProofAccepted(t *testing.T) {
	requester, requesterPubkey, connection, hub := identityProofFixture()
	ev := identityProof(t, requester, nostrKindCircleIdentityProof, nostr.Tags{{"d", hub}, {"connection", connection}})
	require.NoError(t, verifyCircleWalletIdentityEvent(ev, requesterPubkey, connection, hub))
}

func TestVerifyCircleWalletIdentityEvent_WrongKindRejected(t *testing.T) {
	requester, requesterPubkey, connection, hub := identityProofFixture()
	// A genuine signature, by the right key, over the wrong kind.
	ev := identityProof(t, requester, nostr.KindTextNote, nostr.Tags{{"d", hub}, {"connection", connection}})

	err := verifyCircleWalletIdentityEvent(ev, requesterPubkey, connection, hub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kind")
}

func TestVerifyCircleWalletIdentityEvent_ForgedSignatureRejected(t *testing.T) {
	_, requesterPubkey, connection, hub := identityProofFixture()
	// Signed by someone else, then relabelled as the requester with its id
	// recomputed: the id, signer, d and connection tags and freshness all check
	// out, and only the signature can tell it apart.
	ev := identityProof(t, nostr.GeneratePrivateKey(), nostrKindCircleIdentityProof, nostr.Tags{{"d", hub}, {"connection", connection}})
	ev.PubKey = requesterPubkey
	ev.ID = ev.GetID()
	require.True(t, ev.CheckID())

	err := verifyCircleWalletIdentityEvent(ev, requesterPubkey, connection, hub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}

// NIP-CW §Identity Proof check 6: exactly one connection tag, equal to the
// request's connection_pubkey. Without it, whoever sees a join could resubmit it
// with a connection key of their own and receive a wallet on the member's
// membership.
func TestVerifyCircleWalletIdentityEvent_ConnectionTag(t *testing.T) {
	requester, requesterPubkey, connection, hub := identityProofFixture()
	other, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	for name, tags := range map[string]nostr.Tags{
		"missing":        {{"d", hub}},
		"another key":    {{"d", hub}, {"connection", other}},
		"two tags":       {{"d", hub}, {"connection", connection}, {"connection", other}},
		"two equal tags": {{"d", hub}, {"connection", connection}, {"connection", connection}},
		"empty value":    {{"d", hub}, {"connection"}},
	} {
		ev := identityProof(t, requester, nostrKindCircleIdentityProof, tags)
		err := verifyCircleWalletIdentityEvent(ev, requesterPubkey, connection, hub)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "connection", name)
	}
}
