package cashwallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/tests"
)

// Every bill a Hub mints must carry that Hub's own fingerprint, on every path that
// creates one.
//
// It exists because nothing else in a token identifies the ISSUING HUB: mint
// provenance identifies the minting NODE, and one node routinely runs several Hubs.
// A holder grouping bills by minter therefore merges bills from sibling Hubs, and
// this Hub refuses the lot — which is what "consolidate just fails" looked like
// before this field.
//
// Per path rather than once, because there are three ways a bill comes into
// existence (mint, split, consolidate) and a bill missing its fingerprint is
// silently ungroupable — no error, just a holder who cannot tidy up.

func TestMint_StampsTheHubFingerprint(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")
	require.NotEmpty(t, hub.AppPubkey, "test premise: a Hub app must have a pairing pubkey to fingerprint")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub, Recipients: onePubkeyRecipient(1000), ExpirySecs: 1800,
	})
	require.NoError(t, err)

	tok, err := lokicash.Decode(result.CashToken)
	require.NoError(t, err)
	assert.Equal(t, lokicash.HubGroupFor(hub.AppPubkey), tok.HubGroup,
		"a minted bill must carry its issuing Hub's fingerprint")
}

// TestTwoHubsOnOneNode_GetDifferentFingerprints is the case the field exists for.
//
// Both bills share a minter — one node, one Lightning identity, so identical mint
// provenance — and MUST still be distinguishable, or a holder groups them together
// and the consolidation is refused.
// The remaining properties are asserted on encodeCashToken directly, not through
// three more Create/Split calls.
//
// Not a shortcut: the in-process mock LN client issues ONE fixed invoice, so a single
// test can fund exactly one wallet creation (consolidate_test.go's header says the
// same). encodeCashToken is also where the fingerprint is actually computed, so this
// is the level of the behaviour. That the three CALL SITES pass the right Hub is
// covered end to end in cashctl's integration suite, against a real node that issues
// distinct invoices.

func TestEncodeCashToken_StampsTheHubFingerprint(t *testing.T) {
	identityRequired := true
	relays := []string{"wss://relay.test"}
	hubA, hubB := "hub-pubkey-aaaa", "hub-pubkey-bbbb"

	tokenA := encodeCashToken(walletHex(1), secretHex(1), relays, &identityRequired, fakeMintSig(), 1000, hubA)
	tokenA2 := encodeCashToken(walletHex(2), secretHex(2), relays, &identityRequired, fakeMintSig(), 2000, hubA)
	tokenB := encodeCashToken(walletHex(3), secretHex(3), relays, &identityRequired, fakeMintSig(), 1000, hubB)

	a, err := lokicash.Decode(tokenA)
	require.NoError(t, err)
	a2, err := lokicash.Decode(tokenA2)
	require.NoError(t, err)
	b, err := lokicash.Decode(tokenB)
	require.NoError(t, err)

	require.Len(t, a.HubGroup, lokicash.HubGroupLen)
	assert.Equal(t, lokicash.HubGroupFor(hubA), a.HubGroup)

	// Stable across bills of one Hub — grouping is an equality test, so a per-bill
	// value would group nothing.
	assert.Equal(t, a.HubGroup, a2.HubGroup, "one Hub must stamp one fingerprint")

	// Distinct across Hubs of the SAME node. This is the case the field exists for:
	// both bills would carry identical mint provenance, because provenance identifies
	// the node, and a holder grouping by minter merges them and is refused.
	assert.NotEqual(t, a.HubGroup, b.HubGroup,
		"two Hubs on one node must be distinguishable, or a holder groups their bills together")
}

// TestEncodeCashToken_NoHubPubkeyMeansNoFingerprint: absent, never a hash of "".
//
// A bill with no fingerprint is ungrouped — inconvenient, and correct. A fingerprint
// that every Hub shared would group bills ACROSS Hubs and guarantee the refusal this
// field exists to prevent, which is strictly worse than not grouping at all.
func TestEncodeCashToken_NoHubPubkeyMeansNoFingerprint(t *testing.T) {
	identityRequired := true
	token := encodeCashToken(walletHex(9), secretHex(9), []string{"wss://relay.test"}, &identityRequired, fakeMintSig(), 1000, "")

	tok, err := lokicash.Decode(token)
	require.NoError(t, err)
	assert.Nil(t, tok.HubGroup, "an unknown Hub must leave the bill ungrouped, not share a fingerprint with every other Hub")
}

// walletHex/secretHex produce distinct, valid 32-byte hex keys per test bill. The
// values are arbitrary; only their validity and distinctness matter here.
func walletHex(n byte) string { return hex.EncodeToString(bytes.Repeat([]byte{0xa0 + n}, 32)) }
func secretHex(n byte) string { return hex.EncodeToString(bytes.Repeat([]byte{0xc0 + n}, 32)) }

// fakeMintSig is a well-formed (not valid) 65-byte recoverable signature. These tests
// assert on the hub-group TLV, not on provenance — but provenance is mandatory, so
// encodeCashToken refuses to produce a token without something of the right shape.
func fakeMintSig() []byte { return bytes.Repeat([]byte{0x01}, 65) }
