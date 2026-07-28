package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
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

	// The CALLER generates their own secret and submits only its commitment —
	// the wallet never mints or returns a bearer secret over the shared
	// connection (Finding 1, 2026-07-28 audit: a server-generated secret
	// returned here would be decryptable by every other holder of this
	// shared jit_wallet connection).
	newSecretHex, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, newSecretHash, nil, time.Now())

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result := response.Result.(jitTransferResponse)
	assert.Equal(t, db.JITAllocIdentityBearer, result.IdentityType)
	assert.Equal(t, newSecretHash, result.IdentityValue, "echoing the caller's own commitment back is safe — it's not a secret")

	oldClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)

	// End-to-end: the secret the caller chose actually redeems the slice.
	redeemResponse := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:      tests.MockZeroAmountInvoice,
		Amount:       ptrUint64(1000),
		BearerSecret: newSecretHex,
	})
	require.Nil(t, redeemResponse.Error)
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

func TestHandleJITTransferEvent_HappyPath_BearerToBearer_CallerSuppliedNewSecret(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	secretHex, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	// Rotating to a new bearer secret still requires the CALLER to generate
	// and submit the new commitment — the wallet has no secret to mint and
	// hand back over this shared connection.
	newSecretHex, newSecretHash := bearerSecretAndHash(t)
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		BearerSecret: secretHex,
		NewIdentity:  jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result := response.Result.(jitTransferResponse)
	assert.Equal(t, db.JITAllocIdentityBearer, result.IdentityType)
	assert.Equal(t, newSecretHash, result.IdentityValue)

	// The old secret must be dead.
	oldClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityBearer, secretHash)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)

	// The new secret must actually redeem the slice.
	redeemResponse := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:      tests.MockZeroAmountInvoice,
		Amount:       ptrUint64(1000),
		BearerSecret: newSecretHex,
	})
	require.Nil(t, redeemResponse.Error)
}

func TestHandleJITTransferEvent_ToBearer_MissingIdentityValue_Rejected(t *testing.T) {
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

	// No identity_value supplied for the bearer target — MUST be rejected,
	// not silently minted server-side (that's the exact vulnerability this
	// design was changed to close).
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

func TestHandleJITTransferEvent_ToBearer_MalformedIdentityValue_Rejected(t *testing.T) {
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

	const malformed = "not-a-valid-hex-commitment"
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, malformed, nil, time.Now())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: malformed},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

func TestHandleJITTransferEvent_ToBearer_IAPubkeySupplied_Rejected(t *testing.T) {
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

	_, newSecretHash := bearerSecretAndHash(t)
	stray, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, newSecretHash, nil, time.Now())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity: jitTransferNewIdentityParam{
			IdentityType: db.JITAllocIdentityBearer, IdentityValue: newSecretHash, IAPubkey: stray,
		},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
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

// TestHandleJITTransferEvent_TransferIntoBearer_MultiSliceWallet_SpinsOffToNewWallet
// covers a multi-recipient wallet where every OTHER slice is still
// unclaimed — the mixing check used to reject this outright; now it spins
// the slice off into its own dedicated wallet instead (see
// TestHandleJITTransferEvent_TransferIntoBearer_ClaimedCotenant_SpinsOffToNewWallet
// for the full inner-encryption-exclusivity assertions this test doesn't
// repeat). The one thing worth checking here specifically: the untouched
// cotenant's own slice must be completely unaffected.
func TestHandleJITTransferEvent_TransferIntoBearer_MultiSliceWallet_SpinsOffToNewWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 200_000)
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-spinoff", Amount: 1000},
	}

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	otherPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: otherPubkey, AmountMloki: 1000},
	}))

	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, newSecretHash, nil, time.Now())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result, ok := response.Result.(jitTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	require.NotEmpty(t, result.NewWalletToken)

	// The un-transferred cotenant's own slice is completely unaffected.
	otherClaim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, otherPubkey)
	require.NoError(t, err)
	require.NotNil(t, otherClaim)
	assert.Equal(t, int64(1000), otherClaim.AmountMloki)

	oldClaim := jitWalletClaimByIdentity(t, svc, wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NotNil(t, oldClaim.ClaimedAt)
	require.NotNil(t, oldClaim.SpunOffToWalletAppID)
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

// TestHandleJITTransferEvent_TransferIntoBearer_ClaimedCotenant_SpinsOffToNewWallet
// is a regression test for an independent-audit finding (2026-07-28b): a
// bearer slice used to be safe only when no other party had ever held the
// wallet's shared NWC connection, because a bearer redeem transmits the raw
// secret in the request body — decryptable by every party that ever received
// the (single, shared) pairing secret, whether or not they've since claimed
// their own slice and moved on.
// TestHandleJITTransferEvent_TransferIntoBearer_MultiSliceWallet_SpinsOffToNewWallet
// covers the still-unclaimed-cotenant case (also spun off, same as this one
// — the mixing check no longer distinguishes claimed from unclaimed
// cotenants, since spin-off leaves the shared connection entirely either
// way); this test's own distinct value is specifically the CLAIMED-cotenant
// case, since a former mixing check bug (fixed separately — see
// TestHandleJITTransferEvent_RaceAgainstJITRedeem_NeverBothSucceed) also
// touched claimed-cotenant accounting. Rather than reject outright,
// jit_transfer moves the victim's slice into a brand-new dedicated wallet
// whose connection is delivered nested-encrypted to the victim's own pubkey
// — so the attacker, despite still holding the shared connection this
// response itself travels over, gets nothing usable out of it.
func TestHandleJITTransferEvent_TransferIntoBearer_ClaimedCotenant_SpinsOffToNewWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	// Funded well above MockInvoice's own fixed baked-in amount (123000
	// mloki) — the mock LN client's default MakeInvoice response doesn't
	// reflect whatever amount is actually requested (see
	// jitwallet/create_test.go's TestCreate_ConcurrentCreation_BothIndependentlySucceed
	// for the same accommodation), so the source wallet's balance has to
	// absorb that fixed amount regardless of the 1000-mloki slice being
	// spun off.
	wallet := newFundedJITWallet(t, svc, hub, 200_000)
	mockLN := svc.LNClient.(*tests.MockLn)
	// Matches MockInvoice's own embedded Payee — without this, the mock LN
	// client's self-payment interception (transactions_service.go's
	// interceptSelfPayment, keyed on paymentRequest.Payee == lnClient.GetPubkey())
	// never fires, and the internal transfer's incoming leg is left PENDING
	// forever instead of settling. Same constant every other test that
	// exercises a real internal transfer through this mock uses (e.g.
	// transactions/self_payments_test.go).
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-spinoff", Amount: 1000},
	}

	// Two original recipients of the SAME shared connection: the attacker and
	// the victim. Both hold the wallet's one pairing secret.
	attackerPrivkey := nostr.GeneratePrivateKey()
	attackerPubkey, _ := nostr.GetPublicKey(attackerPrivkey)
	victimPrivkey := nostr.GeneratePrivateKey()
	victimPubkey, _ := nostr.GetPublicKey(victimPrivkey)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: attackerPubkey, AmountMloki: 1000},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: victimPubkey, AmountMloki: 1000},
	}))

	// The attacker has already redeemed their own slice. It is now claimed,
	// but the attacker STILL holds the shared connection and can decrypt
	// every future request/response on it.
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, attackerPubkey)
	require.NoError(t, err)

	childrenBefore, err := svc.AppsService.ListJITHubWalletChildren(hub.ID)
	require.NoError(t, err)

	// The victim now transfers their still-unclaimed slice into a bearer
	// note to hand it off as cash.
	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, victimPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, newSecretHash, nil, time.Now())

	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: victimPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error, "a claimed co-tenant must no longer block spinning a slice off into its own wallet")
	result, ok := response.Result.(jitTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	assert.Equal(t, uint64(1000), result.AmountMloki)
	assert.Equal(t, db.JITAllocIdentityBearer, result.IdentityType)
	assert.Equal(t, newSecretHash, result.IdentityValue)
	require.NotEmpty(t, result.NewWalletToken, "spin-off must deliver the new wallet's connection")

	// The victim's OLD slice is now terminal, distinct from a real
	// redemption: claimed, and tagged with exactly which wallet it moved to.
	oldClaim := jitWalletClaimByIdentity(t, svc, wallet.ID, db.JITAllocIdentityPubkey, victimPubkey)
	require.NotNil(t, oldClaim.ClaimedAt, "the spun-off slice must be terminal so it can never be redeemed or transferred again")
	require.NotNil(t, oldClaim.SpunOffToWalletAppID)

	// Exactly one new jit_wallet child of the SAME hub appeared.
	childrenAfter, err := svc.AppsService.ListJITHubWalletChildren(hub.ID)
	require.NoError(t, err)
	require.Len(t, childrenAfter, len(childrenBefore)+1)
	var newWallet *db.App
	for i := range childrenAfter {
		if childrenAfter[i].ID == *oldClaim.SpunOffToWalletAppID {
			newWallet = &childrenAfter[i]
		}
	}
	require.NotNil(t, newWallet, "spun-off target must be a real child of the hub")
	assert.Equal(t, db.AppKindJITWallet, newWallet.Kind)
	require.NotEmpty(t, result.NewWalletPubkey)
	assert.Equal(t, *newWallet.WalletPubkey, result.NewWalletPubkey, "the plaintext pubkey the response hands the caller must be the real new wallet's own")

	// The new wallet is funded with exactly the victim's slice amount and
	// holds one unclaimed bearer slice for the caller-supplied commitment.
	assert.Equal(t, int64(1000), queries.GetIsolatedBalance(svc.DB, newWallet.ID))
	newClaim, err := svc.AppsService.GetJITWalletClaim(newWallet.ID, db.JITAllocIdentityBearer, newSecretHash)
	require.NoError(t, err)
	require.NotNil(t, newClaim)
	assert.Equal(t, int64(1000), newClaim.AmountMloki)

	// The inner token decrypts for the victim using ONLY what a real
	// black-box caller would actually have: their own privkey plus
	// result.NewWalletPubkey from the response itself (never looked up from
	// the DB — a real caller has no DB access). ECDH is symmetric, so
	// (NewWalletPubkey, victim privkey) derives the same conversation key as
	// (victim pubkey, new wallet's own privkey), which is what the wallet
	// actually encrypted with (mirrors create_circle_wallet_controller.go's
	// identical WalletPubkey-in-the-clear delivery pattern)...
	victimCipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, result.NewWalletPubkey, victimPrivkey)
	require.NoError(t, err)
	decrypted, err := victimCipher.Decrypt(result.NewWalletToken)
	require.NoError(t, err, "the intended recipient must be able to decrypt the inner token")
	decodedToken, err := lokicash.Decode(decrypted)
	require.NoError(t, err)
	assert.Equal(t, *newWallet.WalletPubkey, decodedToken.WalletPubkey)

	// ...but NOT for the attacker, despite the attacker still holding this
	// wallet's own shared (outer) connection — and despite the attacker
	// ALSO seeing the same plaintext result.NewWalletPubkey and result.NewWalletToken
	// the victim does (this whole response is decryptable by every holder of
	// the shared outer connection) — the whole point of the inner encryption
	// layer is that a bare pubkey plus ciphertext is useless without the
	// victim's own privkey, which the attacker never has.
	attackerCipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, result.NewWalletPubkey, attackerPrivkey)
	require.NoError(t, err)
	_, err = attackerCipher.Decrypt(result.NewWalletToken)
	assert.Error(t, err, "a claimed co-tenant must not be able to decrypt the spun-off wallet's connection")
}

// jitWalletClaimByIdentity looks up a JITWalletClaim regardless of its
// claimed_at state — unlike AppsService.GetJITWalletClaim (which only ever
// returns unclaimed rows), tests that need to inspect a slice AFTER it's been
// claimed or spun off need the raw row.
func jitWalletClaimByIdentity(t *testing.T, svc *tests.TestService, walletAppID uint, identityType, identityValue string) *db.JITWalletClaim {
	t.Helper()
	var claim db.JITWalletClaim
	err := svc.DB.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ?",
		walletAppID, identityType, identityValue).First(&claim).Error
	require.NoError(t, err)
	return &claim
}

// TestHandleJITTransferEvent_RaceAgainstJITRedeem_NeverBothSucceed is a
// regression test for an independent dynamic (live black-box) audit finding
// (2026-07-28): ClaimJITWalletSlice's committing UPDATE used to be guarded
// only by "id = ? AND claimed_at IS NULL" — it never re-checked
// identity_type/identity_value. A concurrent jit_transfer can reassign a
// row's identity without ever setting claimed_at, so a jit_redeem racing it
// could still match on id alone and pay out the PRE-transfer identity even
// after jit_transfer had already reported success to the NEW owner — a
// "phantom transfer": the transfer API call succeeds, but the funds it
// promised are already gone.
//
// Fixed in apps/jit_hub_service.go (ClaimJITWalletSlice's update now
// re-checks identity_type/identity_value, so a slice reassigned out from
// under a racing redeem correctly fails that redeem with "not found" instead
// of paying out the superseded identity). "Not both succeed" is now a
// structural guarantee from the atomic, identity-checked update — not a
// probabilistic outcome that needs many iterations to hit — so a single race
// is enough to prove it; the audit's own many-iteration live-node run was
// about *reliably observing the pre-fix bug*, not something this regression
// test needs to repeat.
func TestHandleJITTransferEvent_RaceAgainstJITRedeem_NeverBothSucceed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)
	controller := NewTestNip47Controller(svc)

	secret1Hex, secret1Hash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityBearer, IdentityValue: secret1Hash, AmountMloki: 1000},
	}))
	_, secret2Hash := bearerSecretAndHash(t)

	var redeemResp, transferResp *models.Response
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		redeemResp = handleClaimFundsFor(t, svc, controller, wallet, claimFundsParams{
			Invoice:      tests.MockZeroAmountInvoice,
			Amount:       ptrUint64(1000),
			BearerSecret: secret1Hex,
		})
	}()
	go func() {
		defer wg.Done()
		transferResp = handleJITTransferFor(t, svc, controller, wallet, jitTransferParams{
			BearerSecret: secret1Hex,
			NewIdentity:  jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: secret2Hash},
		})
	}()
	wg.Wait()

	redeemWon := redeemResp.Error == nil
	transferWon := transferResp.Error == nil
	require.False(t, redeemWon && transferWon,
		"jit_redeem and jit_transfer both reported success for the same slice — phantom transfer")
	require.True(t, redeemWon || transferWon,
		"neither op succeeded against an unclaimed slice (redeem=%v transfer=%v)", redeemResp.Error, transferResp.Error)

	if transferWon {
		// A genuinely successful transfer must be durable: the new secret
		// must actually redeem, using a DIFFERENT mock invoice than the
		// racing redeem attempted — payment-hash idempotency is checked
		// instance-wide, matching a real Lightning node, so reusing the same
		// invoice here would spuriously fail as "already paid" even though
		// no payment for it ever went through.
		oldRedeemResp := handleClaimFundsFor(t, svc, controller, wallet, claimFundsParams{
			Invoice:      tests.MockInvoice,
			Amount:       ptrUint64(1000),
			BearerSecret: secret1Hex,
		})
		require.NotNil(t, oldRedeemResp.Error, "the pre-transfer secret must no longer redeem")
	}
}

// TestHandleJITTransferEvent_SpinOff_FundingFailure_RollsBack verifies
// handleJITTransferSpinOff's claim-then-fund-then-rollback-on-failure
// ordering: if the new wallet's funding transfer fails after the source
// slice has already been exclusively claimed, that claim must be undone
// (UnclaimJITWalletSlice) rather than left stranded — a caller who hits this
// error can safely retry the exact same request.
func TestHandleJITTransferEvent_SpinOff_FundingFailure_RollsBack(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 200_000)

	// No self-payment pubkey override here, deliberately: this forces
	// SendPaymentSync down its normal (non-intercepted) path, where a queued
	// PayInvoiceError actually has somewhere to bite.
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.PayInvoiceResponses = []*lnclient.PayInvoiceResponse{nil}
	mockLN.PayInvoiceErrors = []error{errors.New("simulated payment failure")}

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	otherPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: otherPubkey, AmountMloki: 1000},
	}))

	childrenBefore, err := svc.AppsService.ListJITHubWalletChildren(hub.ID)
	require.NoError(t, err)

	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, newSecretHash, nil, time.Now())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: newSecretHash},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_INTERNAL, response.Error.Code)

	// The source slice must be rolled back to unclaimed, not stranded.
	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "a failed spin-off must roll back the source slice's claim so the caller can retry")
	assert.Nil(t, claim.SpunOffToWalletAppID)

	// The half-created wallet must have been deleted, not left stranded.
	childrenAfter, err := svc.AppsService.ListJITHubWalletChildren(hub.ID)
	require.NoError(t, err)
	assert.Len(t, childrenAfter, len(childrenBefore), "a failed spin-off must not leave a half-created wallet behind")

	// A retry (this time with the payment succeeding) must now work.
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-retry", Amount: 1000},
	}
	retryProof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, newSecretHash, nil, time.Now())
	retryResponse := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, retryProof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: newSecretHash},
	})
	require.Nil(t, retryResponse.Error, "a retry after a rolled-back spin-off must succeed")
}

// TestHandleJITTransferEvent_SpinOff_TransferCapEnforced verifies a spin-off
// respects the wallet's own MaxTransfers cap the same way an in-place
// transfer does — a spin-off is conceptually a transfer of the slice's
// value, so it must not bypass a cap the wallet's creator configured.
func TestHandleJITTransferEvent_SpinOff_TransferCapEnforced(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 200_000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	otherPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000, MaxTransfers: 1, TransferCount: 1},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: otherPubkey, AmountMloki: 1000},
	}))

	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.JITAllocIdentityBearer, newSecretHash, nil, time.Now())
	response := handleJITTransferFor(t, svc, NewTestNip47Controller(svc), wallet, jitTransferParams{
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   jitTransferNewIdentityParam{IdentityType: db.JITAllocIdentityBearer, IdentityValue: newSecretHash},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "a slice rejected for exceeding its transfer cap must remain untouched")
}
