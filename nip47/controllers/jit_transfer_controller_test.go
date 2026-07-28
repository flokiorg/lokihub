package controllers

import (
	"context"
	"encoding/json"
	"sync"
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

// buildTransferProofEvent builds and signs a kind-35521 transfer proof bound
// to walletPubkey and the target (newIdentityType, newIdentityValue) —
// mirrors buildClaimProofEvent, but bound via new_identity_hash instead of
// bolt11_hash.
func buildTransferProofEvent(t *testing.T, signerPrivkey, walletPubkey, newIdentityType, newIdentityValue string, extraTags nostr.Tags, createdAt time.Time) *nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"d", walletPubkey}, {"new_identity_hash", newIdentityHash(newIdentityType, newIdentityValue)}}
	tags = append(tags, extraTags...)
	ev := &nostr.Event{
		Kind:      nostrKindClaimProof,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags:      tags,
	}
	require.NoError(t, ev.Sign(signerPrivkey))
	return ev
}

// handleJITTransferFor dispatches HandleJITTransferEvent against app and
// returns the decoded response.
func handleJITTransferFor(t *testing.T, svc *tests.TestService, controller *nip47Controller, app *db.App, params jitTransferParams) *models.Response {
	t.Helper()
	content := map[string]interface{}{
		"method": constants.NIP47MethodJITTransfer,
		"params": params,
	}
	reqBytes, _ := json.Marshal(content)
	nip47Request := &models.Request{}
	_ = json.Unmarshal(reqBytes, nip47Request)

	dbRequestEvent := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	var response *models.Response
	controller.HandleJITTransferEvent(context.TODO(), nip47Request, dbRequestEvent.ID, app, func(r *models.Response, _ nostr.Tags) {
		response = r
	}, nostr.Tags{})
	return response
}

func TestHandleJITTransferEvent_HappyPath_PubkeyToPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, newPubkey, nil, time.Now())

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: newPubkey},
	})

	require.Nil(t, response.Error)
	result := response.Result.(jitTransferResponse)
	assert.EqualValues(t, 1000, result.AmountMloki)
	assert.Equal(t, db.JITAllocIdentityPubkey, result.IdentityType)
	assert.Equal(t, newPubkey, result.IdentityValue)

	// The OLD identity must no longer authorize anything on this slice.
	oldClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)

	// The NEW identity must now be the sole registered identity, still unclaimed.
	newClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, newPubkey)
	require.NoError(t, err)
	require.NotNil(t, newClaim)
	assert.EqualValues(t, 1000, newClaim.AmountMloki)
	assert.EqualValues(t, 1, newClaim.TransferCount)
}

func TestHandleJITTransferEvent_HappyPath_PubkeyToBearer_SingleSliceWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, "", nil, time.Now())

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer},
	})

	require.Nil(t, response.Error)
	result := response.Result.(jitTransferResponse)
	assert.Equal(t, db.JITAllocIdentityBearer, result.IdentityType)
	assert.NotEmpty(t, result.BearerSecret)
	assert.Empty(t, result.IdentityValue, "the internal secret hash must never be surfaced")

	oldClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)
}

func TestHandleJITTransferEvent_HappyPath_BearerToPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	secretHex, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		BearerSecret: secretHex,
		NewIdentity:  jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: newPubkey},
	})

	require.Nil(t, response.Error)
	result := response.Result.(jitTransferResponse)
	assert.Equal(t, db.JITAllocIdentityPubkey, result.IdentityType)
	assert.Equal(t, newPubkey, result.IdentityValue)

	// The old bearer secret must no longer redeem or transfer anything.
	oldClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityBearer, secretHash)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)
}

func TestHandleJITTransferEvent_HappyPath_BearerToBearer_SecretRotation(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	secretHex, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		BearerSecret: secretHex,
		NewIdentity:  jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer},
	})

	require.Nil(t, response.Error)
	result := response.Result.(jitTransferResponse)
	assert.Equal(t, db.JITAllocIdentityBearer, result.IdentityType)
	assert.NotEmpty(t, result.BearerSecret)
	assert.NotEqual(t, secretHex, result.BearerSecret, "rotation must mint a genuinely new secret")

	// The old secret must be dead.
	oldClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityBearer, secretHash)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)
}

func TestHandleJITTransferEvent_ConnectionKeyMode_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 2000)

	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	registerTrustedIA(t, svc, iaPubkey)
	connectionKey := tests.RandomHex32()
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPubkey, AmountMloki: 2000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	attestation := buildIAAttestationEvent(t, iaPrivkey, connectionKey, claimantPubkey, oneHourFromNow())
	proof := buildTransferProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, newPubkey,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:     db.JITAllocIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, proof),
		AttestationEvent: mustMarshal(t, attestation),
		NewIdentity:      jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: newPubkey},
	})

	require.Nil(t, response.Error)
}

func TestHandleJITTransferEvent_ConnectionKeyMode_RevokedIA_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 2000)

	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	// Deliberately NOT registered as trusted.
	connectionKey := tests.RandomHex32()
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPubkey, AmountMloki: 2000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	attestation := buildIAAttestationEvent(t, iaPrivkey, connectionKey, claimantPubkey, oneHourFromNow())
	proof := buildTransferProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, newPubkey,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:     db.JITAllocIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, proof),
		AttestationEvent: mustMarshal(t, attestation),
		NewIdentity:      jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

func TestHandleJITTransferEvent_NewIdentityConnectionKey_UntrustedIA_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	untrustedIA, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	newConnectionKey := tests.RandomHex32()
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityConnectionKey, newConnectionKey, nil, time.Now())

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity: jitTransferNewIdentityParam{
			IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: newConnectionKey, IAPubkey: untrustedIA,
		},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// Untouched — the transfer must not have gone through.
	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.NotNil(t, claim)
}

// TestHandleJITTransferEvent_ProofBoundToDifferentNewIdentity_Rejected is the
// core audit-finding coverage for this method: an intercepted proof, signed
// for one new_identity, must not be redirectable to a different one by
// resubmitting it with attacker-chosen new_identity fields.
func TestHandleJITTransferEvent_ProofBoundToDifferentNewIdentity_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	// Proof is bound to intendedNewPubkey...
	intendedNewPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, intendedNewPubkey, nil, time.Now())

	// ...but the attacker submits it targeting a DIFFERENT pubkey (their own).
	attackerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: attackerPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// Neither the intended nor the attacker's identity gained anything —
	// the original registered identity is untouched.
	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.NotNil(t, claim)
	attackerClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, attackerPubkey)
	require.NoError(t, err)
	assert.Nil(t, attackerClaim)
}

func TestHandleJITTransferEvent_WrongSignature_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	currentPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Signed by a DIFFERENT key than the one whose identity is being claimed.
	impostorPrivkey := nostr.GeneratePrivateKey()
	proof := buildTransferProofEvent(t, impostorPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, newPubkey, nil, time.Now())

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

func TestHandleJITTransferEvent_TransferIntoBearer_MultiSliceWallet_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 2000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	otherPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: otherPubkey, AmountMloki: 1000},
	}))

	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, "", nil, time.Now())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.NotNil(t, claim, "a rejected transfer must not have touched the slice")
}

func TestHandleJITTransferEvent_MaxTransfersCapEnforced(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	firstPrivkey := nostr.GeneratePrivateKey()
	firstPubkey, _ := nostr.GetPublicKey(firstPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: firstPubkey, AmountMloki: 1000, MaxTransfers: 1},
	}))

	// First transfer: within the cap, must succeed.
	secondPrivkey := nostr.GeneratePrivateKey()
	secondPubkey, _ := nostr.GetPublicKey(secondPrivkey)
	proof1 := buildTransferProofEvent(t, firstPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, secondPubkey, nil, time.Now())
	response1 := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: firstPubkey,
		IdentityEvent: mustMarshal(t, proof1),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: secondPubkey},
	})
	require.Nil(t, response1.Error)

	// Second transfer of the SAME slice (now registered to secondPubkey):
	// the cap of 1 has been reached — must be rejected, "redeem instead".
	thirdPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof2 := buildTransferProofEvent(t, secondPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, thirdPubkey, nil, time.Now())
	response2 := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: secondPubkey,
		IdentityEvent: mustMarshal(t, proof2),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: thirdPubkey},
	})
	require.NotNil(t, response2.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response2.Error.Code)

	// The slice must remain registered to secondPubkey — the rejected
	// second transfer must not have partially applied.
	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, secondPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim)
	assert.EqualValues(t, 1, claim.TransferCount)
}

func TestHandleJITTransferEvent_AlreadyClaimedSlice_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))
	// Claim it directly (bypassing redeem) to simulate an already-redeemed slice.
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, newPubkey, nil, time.Now())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, response.Error.Code)
}

func TestHandleJITTransferEvent_BearerCurrent_WrongSecret_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	_, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	wrongSecret, _ := bearerSecretAndHash(t)
	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		BearerSecret: wrongSecret,
		NewIdentity:  jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, response.Error.Code)
}

func TestHandleJITTransferEvent_BearerCurrent_MixedParams_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	secretHex, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		BearerSecret:  secretHex,
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: tests.RandomHex32(),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

func TestHandleJITTransferEvent_NonJITWalletApp_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), hub, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: tests.RandomHex32(),
		IdentityEvent: "{}",
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: tests.RandomHex32()},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

// TestHandleJITTransferEvent_ConcurrentTransfers_OnlyOneSucceeds is the core
// fund-safety property: two concurrent transfer attempts against the same
// slice must never both succeed.
func TestHandleJITTransferEvent_ConcurrentTransfers_OnlyOneSucceeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	targetA, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	targetB, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proofA := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, targetA, nil, time.Now())
	proofB := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityPubkey, targetB, nil, time.Now())

	controller := NewTestNip47Controller(svc)
	var wg sync.WaitGroup
	responses := make([]*models.Response, 2)
	paramsList := []jitTransferParams{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, IdentityEvent: mustMarshal(t, proofA),
			NewIdentity: jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: targetA}},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, IdentityEvent: mustMarshal(t, proofB),
			NewIdentity: jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: targetB}},
	}
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = handleJITTransferFor(t, svc, controller, wallet, paramsList[i])
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, r := range responses {
		if r.Error == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes, "exactly one of two concurrent transfers of the same slice must succeed")
}
