package controllers

import (
	"strconv"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Transferring and Splitting a Slice: the kind-23198 proof binds the
// caller to this wallet, this new_identity and this exact amount_millis, and
// is single-use (CASH-transferring-and-splitting-a-sli-09/-10/-13).

// A full transfer away and back leaves the original owner holding the slice
// again, so their first proof checks out on every binding: same wallet, same
// new_identity, same amount, still fresh. Only the single-use guard stops it.
func TestHandleCashTransferEvent_ProofReplayedAfterRoundTrip_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)

	alicePriv := nostr.GeneratePrivateKey()
	alice, _ := nostr.GetPublicKey(alicePriv)
	bobPriv := nostr.GeneratePrivateKey()
	bob, _ := nostr.GetPublicKey(bobPriv)
	const amount = 5000
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alice, AmountMloki: amount},
	}))

	transfer := func(fromPub string, proof *nostr.Event, to string) *models.Response {
		return handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
			IdentityType:  db.CashIdentityPubkey,
			IdentityValue: fromPub,
			IdentityEvent: mustMarshal(t, proof),
			NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: to},
		})
	}

	aliceToBob := buildTransferProofEvent(t, alicePriv, *wallet.WalletPubkey, db.CashIdentityPubkey, bob, "", amount, nil, time.Now())
	first := transfer(alice, aliceToBob, bob)
	require.Nil(t, first.Error, "%+v", first.Error)
	bobToAlice := buildTransferProofEvent(t, bobPriv, *wallet.WalletPubkey, db.CashIdentityPubkey, alice, "", amount, nil, time.Now())
	back := transfer(bob, bobToAlice, alice)
	require.Nil(t, back.Error, "%+v", back.Error)

	replay := transfer(alice, aliceToBob, bob)
	require.NotNil(t, replay.Error, "a proof must not authorize a second transfer")

	slice := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, alice)
	assert.Nil(t, slice.ClaimedAt, "the slice stays with Alice")
}

func transferProofFixture(t *testing.T) (signerPriv, signer, wallet, newHash string) {
	t.Helper()
	signerPriv = nostr.GeneratePrivateKey()
	signer, _ = nostr.GetPublicKey(signerPriv)
	wallet, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	return signerPriv, signer, wallet, newIdentityHash(db.CashIdentityPubkey, newPub, "")
}

func signedTransferProof(t *testing.T, privkey string, tags nostr.Tags) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{Kind: nostrKindClaimProof, CreatedAt: nostr.Now(), Tags: tags}
	require.NoError(t, ev.Sign(privkey))
	return ev
}

func TestVerifyTransferIdentityEvent_Bindings(t *testing.T) {
	priv, signer, wallet, newHash := transferProofFixture(t)
	const amount uint64 = 2000
	amountTag := nostr.Tag{"amount_millis", strconv.FormatUint(amount, 10)}

	valid := signedTransferProof(t, priv, nostr.Tags{{"d", wallet}, {"new_identity_hash", newHash}, amountTag})
	require.NoError(t, verifyTransferIdentityEvent(valid, db.CashIdentityPubkey, signer, wallet, newHash, amount, ""))

	otherWallet, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	wrongWallet := signedTransferProof(t, priv, nostr.Tags{{"d", otherWallet}, {"new_identity_hash", newHash}, amountTag})
	assert.ErrorContains(t, verifyTransferIdentityEvent(wrongWallet, db.CashIdentityPubkey, signer, wallet, newHash, amount, ""),
		"d-tag", "a proof for another wallet")

	noAmount := signedTransferProof(t, priv, nostr.Tags{{"d", wallet}, {"new_identity_hash", newHash}})
	assert.ErrorContains(t, verifyTransferIdentityEvent(noAmount, db.CashIdentityPubkey, signer, wallet, newHash, amount, ""),
		"amount_millis", "an omitted amount is never a wildcard")
}

func TestVerifyTransferIdentityEvent_ConnectionKeyBindings(t *testing.T) {
	priv, _, wallet, newHash := transferProofFixture(t)
	const amount uint64 = 2000
	connectionKey := tests.RandomHex32()
	attestationID := tests.RandomHex32()
	base := nostr.Tags{{"d", wallet}, {"new_identity_hash", newHash}, {"amount_millis", strconv.FormatUint(amount, 10)}}
	with := func(extra ...nostr.Tag) nostr.Tags { return append(append(nostr.Tags{}, base...), extra...) }

	valid := signedTransferProof(t, priv, with(nostr.Tag{"connection_key", connectionKey}, nostr.Tag{"e", attestationID}))
	require.NoError(t, verifyTransferIdentityEvent(valid, db.CashIdentityConnectionKey, connectionKey, wallet, newHash, amount, attestationID))

	otherKey := signedTransferProof(t, priv, with(nostr.Tag{"connection_key", tests.RandomHex32()}, nostr.Tag{"e", attestationID}))
	assert.ErrorContains(t, verifyTransferIdentityEvent(otherKey, db.CashIdentityConnectionKey, connectionKey, wallet, newHash, amount, attestationID),
		"connection_key")

	otherAttestation := signedTransferProof(t, priv, with(nostr.Tag{"connection_key", connectionKey}, nostr.Tag{"e", tests.RandomHex32()}))
	assert.ErrorContains(t, verifyTransferIdentityEvent(otherAttestation, db.CashIdentityConnectionKey, connectionKey, wallet, newHash, amount, attestationID),
		"e-tag")
}
