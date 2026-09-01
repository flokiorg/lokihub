package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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
// to walletPubkey, the target (newIdentityType, newIdentityValue, newIAPubkey),
// AND the exact amountMloki this proof authorizes — mirrors
// buildClaimProofEvent, but bound via new_identity_hash/amount_mloki instead
// of bolt11_hash. newIAPubkey is "" for non-connection_key targets. For a
// full transfer, pass the slice's exact current amount (the server resolves
// an omitted request amount_mloki to the slice's live full amount and
// requires the proof to match that exact number — see
// verifyTransferIdentityEvent's doc comment).
func buildTransferProofEvent(t *testing.T, signerPrivkey, walletPubkey, newIdentityType, newIdentityValue, newIAPubkey string, amountMloki uint64, extraTags nostr.Tags, createdAt time.Time) *nostr.Event {
	t.Helper()
	tags := nostr.Tags{
		{"d", walletPubkey},
		{"new_identity_hash", newIdentityHash(newIdentityType, newIdentityValue, newIAPubkey)},
		{"amount_mloki", strconv.FormatUint(amountMloki, 10)},
	}
	tags = append(tags, extraTags...)
	ev := &nostr.Event{
		Kind:      nostrKindClaimProof,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags:      tags,
	}
	require.NoError(t, ev.Sign(signerPrivkey))
	return ev
}

// handleCashTransferFor dispatches HandleCashTransferEvent against app and
// returns the decoded response.
func handleCashTransferFor(t *testing.T, svc *tests.TestService, controller *nip47Controller, app *db.App, params cashTransferParams) *models.Response {
	t.Helper()
	content := map[string]interface{}{
		"method": constants.NIP47MethodCashTransfer,
		"params": params,
	}
	reqBytes, _ := json.Marshal(content)
	nip47Request := &models.Request{}
	_ = json.Unmarshal(reqBytes, nip47Request)

	dbRequestEvent := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	var response *models.Response
	controller.HandleCashTransferEvent(context.TODO(), nip47Request, dbRequestEvent.ID, app, func(r *models.Response, _ nostr.Tags) {
		response = r
	}, nostr.Tags{})
	return response
}

func TestHandleCashTransferEvent_HappyPath_PubkeyToPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 1000, nil, time.Now())

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.Nil(t, response.Error)
	result := response.Result.(cashTransferResponse)
	assert.EqualValues(t, 1000, result.AmountMloki)
	assert.Equal(t, db.CashIdentityPubkey, result.IdentityType)
	assert.Equal(t, newPubkey, result.IdentityValue)

	// The OLD identity must no longer authorize anything on this slice.
	oldClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)

	// The NEW identity must now be the sole registered identity, still unclaimed.
	newClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, newPubkey)
	require.NoError(t, err)
	require.NotNil(t, newClaim)
	assert.EqualValues(t, 1000, newClaim.AmountMloki)
	assert.EqualValues(t, 1, newClaim.TransferCount)
}

func TestHandleCashTransferEvent_HappyPath_PubkeyToBearer_SingleSliceWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	// The CALLER generates their own secret and submits only its commitment —
	// the wallet never mints or returns a bearer secret over the shared
	// connection (Finding 1, 2026-07-28 audit: a server-generated secret
	// returned here would be decryptable by every other holder of this
	// shared cash_wallet connection).
	newSecretHex, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, newSecretHash, "", 1000, nil, time.Now())

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result := response.Result.(cashTransferResponse)
	assert.Equal(t, db.CashIdentityBearer, result.IdentityType)
	assert.Equal(t, newSecretHash, result.IdentityValue, "echoing the caller's own commitment back is safe — it's not a secret")

	oldClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)

	// End-to-end: the secret the caller chose actually redeems the slice.
	redeemResponse := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, cashRedeemParams{
		Invoice:      tests.MockZeroAmountInvoice,
		Amount:       ptrUint64(1000),
		BearerSecret: newSecretHex,
	})
	require.Nil(t, redeemResponse.Error)
}

func TestHandleCashTransferEvent_HappyPath_BearerToPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	secretHex, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		BearerSecret: secretHex,
		NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.Nil(t, response.Error)
	result := response.Result.(cashTransferResponse)
	assert.Equal(t, db.CashIdentityPubkey, result.IdentityType)
	assert.Equal(t, newPubkey, result.IdentityValue)

	// The old bearer secret must no longer redeem or transfer anything.
	oldClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityBearer, secretHash)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)
}

func TestHandleCashTransferEvent_HappyPath_BearerToBearer_CallerSuppliedNewSecret(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	secretHex, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	// Rotating to a new bearer secret still requires the CALLER to generate
	// and submit the new commitment — the wallet has no secret to mint and
	// hand back over this shared connection.
	newSecretHex, newSecretHash := bearerSecretAndHash(t)
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		BearerSecret: secretHex,
		NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result := response.Result.(cashTransferResponse)
	assert.Equal(t, db.CashIdentityBearer, result.IdentityType)
	assert.Equal(t, newSecretHash, result.IdentityValue)

	// The old secret must be dead.
	oldClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityBearer, secretHash)
	require.NoError(t, err)
	assert.Nil(t, oldClaim)

	// The new secret must actually redeem the slice.
	redeemResponse := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, cashRedeemParams{
		Invoice:      tests.MockZeroAmountInvoice,
		Amount:       ptrUint64(1000),
		BearerSecret: newSecretHex,
	})
	require.Nil(t, redeemResponse.Error)
}

func TestHandleCashTransferEvent_ToBearer_MissingIdentityValue_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	// No identity_value supplied for the bearer target — MUST be rejected,
	// not silently minted server-side (that's the exact vulnerability this
	// design was changed to close).
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, "", "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.NotNil(t, claim, "a rejected transfer must not have touched the slice")
}

func TestHandleCashTransferEvent_ToBearer_MalformedIdentityValue_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	const malformed = "not-a-valid-hex-commitment"
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, malformed, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: malformed},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

func TestHandleCashTransferEvent_ToBearer_IAPubkeySupplied_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	_, newSecretHash := bearerSecretAndHash(t)
	stray, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, newSecretHash, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity: cashTransferNewIdentityParam{
			IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash, IAPubkey: stray,
		},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

func TestHandleCashTransferEvent_ConnectionKeyMode_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 2000)

	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	registerTrustedIA(t, svc, iaPubkey)
	connectionKey := tests.RandomHex32()
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPubkey, AmountMloki: 2000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	attestation := buildIAAttestationEvent(t, iaPrivkey, connectionKey, claimantPubkey, oneHourFromNow())
	proof := buildTransferProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 2000,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:     db.CashIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, proof),
		AttestationEvent: mustMarshal(t, attestation),
		NewIdentity:      cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.Nil(t, response.Error)
}

func TestHandleCashTransferEvent_ConnectionKeyMode_RevokedIA_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 2000)

	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	// Deliberately NOT registered as trusted.
	connectionKey := tests.RandomHex32()
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPubkey, AmountMloki: 2000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	attestation := buildIAAttestationEvent(t, iaPrivkey, connectionKey, claimantPubkey, oneHourFromNow())
	proof := buildTransferProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 2000,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:     db.CashIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, proof),
		AttestationEvent: mustMarshal(t, attestation),
		NewIdentity:      cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

func TestHandleCashTransferEvent_NewIdentityConnectionKey_UntrustedIA_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	untrustedIA, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	newConnectionKey := tests.RandomHex32()
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityConnectionKey, newConnectionKey, untrustedIA, 1000, nil, time.Now())

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity: cashTransferNewIdentityParam{
			IdentityType: db.CashIdentityConnectionKey, IdentityValue: newConnectionKey, IAPubkey: untrustedIA,
		},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// Untouched — the transfer must not have gone through.
	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.NotNil(t, claim)
}

// TestHandleCashTransferEvent_ProofBoundToDifferentNewIdentity_Rejected is the
// core audit-finding coverage for this method: an intercepted proof, signed
// for one new_identity, must not be redirectable to a different one by
// resubmitting it with attacker-chosen new_identity fields.
func TestHandleCashTransferEvent_ProofBoundToDifferentNewIdentity_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	// Proof is bound to intendedNewPubkey...
	intendedNewPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, intendedNewPubkey, "", 1000, nil, time.Now())

	// ...but the attacker submits it targeting a DIFFERENT pubkey (their own).
	attackerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: attackerPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// Neither the intended nor the attacker's identity gained anything —
	// the original registered identity is untouched.
	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	assert.NotNil(t, claim)
	attackerClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, attackerPubkey)
	require.NoError(t, err)
	assert.Nil(t, attackerClaim)
}

func TestHandleCashTransferEvent_WrongSignature_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Signed by a DIFFERENT key than the one whose identity is being claimed.
	impostorPrivkey := nostr.GeneratePrivateKey()
	proof := buildTransferProofEvent(t, impostorPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 1000, nil, time.Now())

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

// TestHandleCashTransferEvent_TransferIntoBearer_MultiSliceWallet_SpinsOffToNewWallet
// covers a multi-recipient wallet where every OTHER slice is still
// unclaimed — the mixing check used to reject this outright; now it spins
// the slice off into its own dedicated wallet instead (see
// TestHandleCashTransferEvent_TransferIntoBearer_ClaimedCotenant_SpinsOffToNewWallet
// for the full inner-encryption-exclusivity assertions this test doesn't
// repeat). The one thing worth checking here specifically: the untouched
// cotenant's own slice must be completely unaffected.
func TestHandleCashTransferEvent_TransferIntoBearer_MultiSliceWallet_SpinsOffToNewWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 200_000)
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-spinoff", Amount: 1000},
	}

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	otherPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: otherPubkey, AmountMloki: 1000},
	}))

	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, newSecretHash, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	require.NotEmpty(t, result.NewWalletToken)

	// The un-transferred cotenant's own slice is completely unaffected.
	otherClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, otherPubkey)
	require.NoError(t, err)
	require.NotNil(t, otherClaim)
	assert.Equal(t, int64(1000), otherClaim.AmountMloki)

	oldClaim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NotNil(t, oldClaim.ClaimedAt)
	require.NotNil(t, oldClaim.SpunOffToWalletAppID)
}

// TestHandleCashTransferEvent_FullTransfer_PubkeyTarget_MultiRecipientWallet_StaysInPlace
// verifies the design refinement made when generalizing the old bearer-only
// spin-off rule: a FULL transfer to a pubkey/connection_key target is ALWAYS
// reassigned in place, unconditional on the wallet's recipient history —
// unlike a bearer target, an identity-bound transfer always requires a real
// signed proof, never just presenting a shared secret, so reusing the
// connection is safe regardless of who else has ever held it. Only a bearer
// target on a historically-multi-recipient wallet forces a split (see
// TestHandleCashTransferEvent_TransferIntoBearer_MultiSliceWallet_SpinsOffToNewWallet
// immediately above).
func TestHandleCashTransferEvent_FullTransfer_PubkeyTarget_MultiRecipientWallet_StaysInPlace(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 2000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	otherPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: otherPubkey, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.Nil(t, response.Error)
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	assert.Empty(t, result.NewWalletToken, "a pubkey-target full transfer must stay in-place, never spin off a new wallet")
	assert.Nil(t, result.RemainingAmountMloki, "in-place reassignment never populates remaining_amount_mloki")

	// Reassigned in place: same wallet, new identity, same amount.
	newClaim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, newPubkey)
	assert.Nil(t, newClaim.ClaimedAt)
	assert.Equal(t, int64(1000), newClaim.AmountMloki)
}

// TestHandleCashTransferEvent_PartialSplit_Success is the core new
// capability: transferring less than a slice's full amount carves off
// exactly that much into a brand-new dedicated wallet, leaving the remainder
// behind under the SAME identity on the SAME connection.
func TestHandleCashTransferEvent_PartialSplit_Success(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// Funded generously (mirrors TestSplit_BearerTarget_CashTokenHints):
	// the mock invoice used for the internal-transfer funding payment below
	// is a fixed, pre-encoded bolt11 string (tests.MockInvoice) whose own
	// baked-in amount is what the mock LN client actually treats as "paid" —
	// the queue entry's Amount field is informational for the queued
	// MakeInvoice return value only, not a parameter of the payment itself —
	// so the source wallet's balance needs enough headroom for that fixed
	// amount, independent of the claim/split amounts this test cares about.
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	// A partial split now funds TWO internal transfers (carved + remainder), so
	// the mock needs two distinct-payment-hash invoices — MakeInvoice/pay
	// decodes each bolt11 for its hash, and two identical hashes would trip the
	// duplicate-payment guard.
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-carved", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, PaymentHash: tests.MockLNClientHoldTransaction.PaymentHash, Preimage: "preimage-remainder", Amount: 3000},
	}

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 5000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	amount := uint64(2000)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 2000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
		AmountMloki:   &amount,
	})

	require.Nil(t, response.Error)
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	assert.EqualValues(t, 2000, result.AmountMloki)
	require.NotNil(t, result.RemainingAmountMloki)
	assert.EqualValues(t, 3000, *result.RemainingAmountMloki)
	require.NotEmpty(t, result.NewWalletToken, "the carved piece is delivered as a new dedicated wallet")
	require.NotEmpty(t, result.RemainderWalletToken, "the remainder is now delivered as its OWN new dedicated wallet, not left on the source")

	// The SOURCE slice is consumed whole (terminal) — a partial split no longer
	// decrements it in place; its value re-emerged as the two new wallets above.
	sourceClaim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NotNil(t, sourceClaim.ClaimedAt)
	require.NotNil(t, sourceClaim.SpunOffToWalletAppID)
}

// TestHandleCashTransferEvent_PartialSplit_FailedCompensation_SourceClaimLeftInPlace
// is the regression for independent Security Auditor B's finding 2:
// SplitInTwo's own carved-wallet compensation was already correctly gated
// (only deletes the carved wallet once its reverse transfer is confirmed), but
// the CALLER — this controller's rollback() — was still restoring the source
// slice to its full original amount unconditionally, even when that reverse
// transfer failed and the carved amount is still sitting in the (deliberately
// retained) carved wallet. Restoring the claim in that case would let the
// caller believe they have the full original amount when the source's real
// balance is short by exactly the carved piece.
//
// Reaches the failure with a REAL "already paid" collision, reusing
// tests.MockInvoice's payment_hash across all three internal transfers
// (carved funding, remainder funding, and the carved reversal) — the same
// technique cashwallet/consolidate_rollback_test.go and
// TestConsolidate_StrandedSource_ClaimLeftInPlace use, not a fault-injection
// seam: the carved transfer settles first, so the remainder attempt and the
// reversal attempt both genuinely collide with that same now-settled hash.
func TestHandleCashTransferEvent_PartialSplit_FailedCompensation_SourceClaimLeftInPlace(t *testing.T) {
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
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: 2000}, // 1: fund carved from source — succeeds, settles MockInvoice's hash
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p2", Amount: 3000}, // 2: fund remainder from source — same hash: fails, starts compensation
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p3", Amount: 2000}, // 3: reverse the carved transfer — same hash: fails too
	}

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 5000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	amount := uint64(2000)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 2000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
		AmountMloki:   &amount,
	})
	require.NotNil(t, response.Error)

	sourceClaim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NotNil(t, sourceClaim)
	assert.NotNil(t, sourceClaim.ClaimedAt,
		"the carved reversal failed — its funds are stranded in the retained carved wallet, so the source claim must NOT be restored to the full original amount")

	// The carved wallet (funded, then NOT deleted since its reversal failed)
	// must still exist as a child of the hub, alongside the source itself.
	var children []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&children).Error)
	assert.Len(t, children, 2, "source + the retained carved wallet holding the stranded funds")

	// A durable reconciliation record must exist too.
	records, err := svc.AppsService.ListCashStrandedFunds(true)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "split", records[0].Operation)
	assert.Equal(t, wallet.ID, records[0].SourceWalletAppID)
	assert.EqualValues(t, 2000, records[0].AmountMloki)
	assert.Nil(t, records[0].ResolvedAt)
}

// TestHandleCashTransferEvent_PartialSplit_BelowMinTransferFloor_Rejected
// verifies the new MinTransferMloki floor applies to the carved-off amount.
func TestHandleCashTransferEvent_PartialSplit_BelowMinTransferFloor_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 5000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 5000, MinTransferMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	amount := uint64(500)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 500, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
		AmountMloki:   &amount,
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// Untouched.
	claim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, currentPubkey)
	assert.Nil(t, claim.ClaimedAt)
	assert.Equal(t, int64(5000), claim.AmountMloki)
}

// TestHandleCashTransferEvent_PartialSplit_RemainderBelowFloor_Rejected
// verifies the floor ALSO applies to what's left behind.
func TestHandleCashTransferEvent_PartialSplit_RemainderBelowFloor_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 5000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 5000, MinTransferMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Splitting off 4500 would leave a 500 remainder — below the 1000 floor.
	amount := uint64(4500)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 4500, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
		AmountMloki:   &amount,
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

// TestHandleCashTransferEvent_PartialSplit_ExceedsSliceBalance_Rejected
// verifies amount_mloki can't exceed the caller's own slice, regardless of
// what the rest of a shared wallet might hold.
func TestHandleCashTransferEvent_PartialSplit_ExceedsSliceBalance_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 5000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	amount := uint64(1001)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 1001, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
		AmountMloki:   &amount,
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

// TestMaybeAutoDeleteDrainedCashWallet_DeletesWhenFullyDrained and its
// sibling below exercise the new auto-delete-on-full-drain behavior
// (decision: a lokicash's wallet is removed automatically once its balance
// is fully drained via a split, not left for manual admin cleanup) directly
// against the new private helper, rather than through a full real-payment
// end-to-end dance — the helper's own two guards (no unclaimed slices left,
// real ledger balance exactly zero) are what's actually new here.
func TestMaybeAutoDeleteDrainedCashWallet_DeletesWhenFullyDrained(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// Deliberately unfunded (GetIsolatedBalance is 0 with no transactions at
	// all) — this test only needs to prove the "no unclaimed slices left AND
	// zero balance" combination triggers deletion; a real cash_transfer split
	// funding the DESTINATION wallet via an internal transfer out of THIS
	// source wallet is exactly what would leave it in this same state
	// end-to-end (covered by TestHandleCashTransferEvent_PartialSplit_Success
	// and the spin-off tests above for the funds-movement side).
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	pubkey := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))
	_, err = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 1000)
	require.NoError(t, err)

	controller := NewTestNip47Controller(svc)
	controller.maybeAutoDeleteDrainedCashWallet(wallet)

	var count int64
	svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&count)
	assert.Equal(t, int64(0), count, "a fully-drained wallet with no unclaimed slices must be auto-deleted")
}

func TestMaybeAutoDeleteDrainedCashWallet_KeepsAliveWithOtherUnclaimedSlice(t *testing.T) {
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
	claimedPubkey := tests.RandomHex32()
	unclaimedPubkey := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimedPubkey, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: unclaimedPubkey, AmountMloki: 1000},
	}))
	_, err = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, claimedPubkey, 1000)
	require.NoError(t, err)

	controller := NewTestNip47Controller(svc)
	controller.maybeAutoDeleteDrainedCashWallet(wallet)

	var count int64
	svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&count)
	assert.Equal(t, int64(1), count, "a wallet with another still-unclaimed slice must never be auto-deleted")
}

// TestMaybeAutoDeleteDrainedCashWallet_KeepsAliveWithNonzeroBalance verifies
// the defensive balance check: even with zero unclaimed slices, a nonzero
// real ledger balance must NOT be force-deleted (structurally shouldn't
// happen given the invariant this checks defensively, but if it ever did,
// deleting real funds out from under an operator would be far worse than
// leaving a stale wallet for the expiry sweep).
func TestMaybeAutoDeleteDrainedCashWallet_KeepsAliveWithNonzeroBalance(t *testing.T) {
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
	pubkey := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))
	_, err = svc.AppsService.SplitCashSliceAmount(wallet.ID, db.CashIdentityPubkey, pubkey, 1000)
	require.NoError(t, err)
	// Simulate a stray nonzero balance (shouldn't normally happen — this is
	// exactly the defensive case the balance re-check guards against).
	tests.FundApp(svc, wallet.ID, 500, tests.RandomHex32())
	require.NotZero(t, queries.GetIsolatedBalance(svc.DB, wallet.ID))

	controller := NewTestNip47Controller(svc)
	controller.maybeAutoDeleteDrainedCashWallet(wallet)

	var count int64
	svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&count)
	assert.Equal(t, int64(1), count, "a wallet with an unexpected nonzero balance must never be force-deleted")
}

func TestHandleCashTransferEvent_AlreadyClaimedSlice_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))
	// Claim it directly (bypassing redeem) to simulate an already-redeemed slice.
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, response.Error.Code)
}

func TestHandleCashTransferEvent_BearerCurrent_WrongSecret_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	_, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	wrongSecret, _ := bearerSecretAndHash(t)
	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		BearerSecret: wrongSecret,
		NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, response.Error.Code)
}

func TestHandleCashTransferEvent_BearerCurrent_MixedParams_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	secretHex, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityBearer, IdentityValue: secretHash, AmountMloki: 1000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		BearerSecret:  secretHex,
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: tests.RandomHex32(),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

func TestHandleCashTransferEvent_NonCashWalletApp_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), hub, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: tests.RandomHex32(),
		IdentityEvent: "{}",
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32()},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

// TestHandleCashTransferEvent_ConcurrentTransfers_OnlyOneSucceeds is the core
// fund-safety property: two concurrent transfer attempts against the same
// slice must never both succeed.
func TestHandleCashTransferEvent_ConcurrentTransfers_OnlyOneSucceeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	targetA, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	targetB, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proofA := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, targetA, "", 1000, nil, time.Now())
	proofB := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, targetB, "", 1000, nil, time.Now())

	controller := NewTestNip47Controller(svc)
	var wg sync.WaitGroup
	responses := make([]*models.Response, 2)
	paramsList := []cashTransferParams{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, IdentityEvent: mustMarshal(t, proofA),
			NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: targetA}},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, IdentityEvent: mustMarshal(t, proofB),
			NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: targetB}},
	}
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = handleCashTransferFor(t, svc, controller, wallet, paramsList[i])
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

// TestHandleCashTransferEvent_TransferIntoBearer_ClaimedCotenant_SpinsOffToNewWallet
// is a regression test for an independent-audit finding (2026-07-28b): a
// bearer slice used to be safe only when no other party had ever held the
// wallet's shared NWC connection, because a bearer redeem transmits the raw
// secret in the request body — decryptable by every party that ever received
// the (single, shared) pairing secret, whether or not they've since claimed
// their own slice and moved on.
// TestHandleCashTransferEvent_TransferIntoBearer_MultiSliceWallet_SpinsOffToNewWallet
// covers the still-unclaimed-cotenant case (also spun off, same as this one
// — the mixing check no longer distinguishes claimed from unclaimed
// cotenants, since spin-off leaves the shared connection entirely either
// way); this test's own distinct value is specifically the CLAIMED-cotenant
// case, since a former mixing check bug (fixed separately — see
// TestHandleCashTransferEvent_RaceAgainstCashRedeem_NeverBothSucceed) also
// touched claimed-cotenant accounting. Rather than reject outright,
// cash_transfer moves the victim's slice into a brand-new dedicated wallet
// whose connection is delivered nested-encrypted to the victim's own pubkey
// — so the attacker, despite still holding the shared connection this
// response itself travels over, gets nothing usable out of it.
func TestHandleCashTransferEvent_TransferIntoBearer_ClaimedCotenant_SpinsOffToNewWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// Funded well above MockInvoice's own fixed baked-in amount (123000
	// mloki) — the mock LN client's default MakeInvoice response doesn't
	// reflect whatever amount is actually requested (see
	// cashwallet/create_test.go's TestCreate_ConcurrentCreation_BothIndependentlySucceed
	// for the same accommodation), so the source wallet's balance has to
	// absorb that fixed amount regardless of the 1000-mloki slice being
	// spun off.
	wallet := newFundedCashWallet(t, svc, hub, 200_000)
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

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: attackerPubkey, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: victimPubkey, AmountMloki: 1000},
	}))

	// The attacker has already redeemed their own slice. It is now claimed,
	// but the attacker STILL holds the shared connection and can decrypt
	// every future request/response on it.
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, attackerPubkey)
	require.NoError(t, err)

	childrenBefore, err := svc.AppsService.ListCashHubWalletChildren(hub.ID)
	require.NoError(t, err)

	// The victim now transfers their still-unclaimed slice into a bearer
	// note to hand it off as cash.
	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, victimPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, newSecretHash, "", 1000, nil, time.Now())

	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: victimPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error, "a claimed co-tenant must no longer block spinning a slice off into its own wallet")
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	assert.Equal(t, uint64(1000), result.AmountMloki)
	assert.Equal(t, db.CashIdentityBearer, result.IdentityType)
	assert.Equal(t, newSecretHash, result.IdentityValue)
	require.NotEmpty(t, result.NewWalletToken, "spin-off must deliver the new wallet's connection")

	// The victim's OLD slice is now terminal, distinct from a real
	// redemption: claimed, and tagged with exactly which wallet it moved to.
	oldClaim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, victimPubkey)
	require.NotNil(t, oldClaim.ClaimedAt, "the spun-off slice must be terminal so it can never be redeemed or transferred again")
	require.NotNil(t, oldClaim.SpunOffToWalletAppID)

	// Exactly one new cash_wallet child of the SAME hub appeared.
	childrenAfter, err := svc.AppsService.ListCashHubWalletChildren(hub.ID)
	require.NoError(t, err)
	require.Len(t, childrenAfter, len(childrenBefore)+1)
	var newWallet *db.App
	for i := range childrenAfter {
		if childrenAfter[i].ID == *oldClaim.SpunOffToWalletAppID {
			newWallet = &childrenAfter[i]
		}
	}
	require.NotNil(t, newWallet, "spun-off target must be a real child of the hub")
	assert.Equal(t, db.AppKindCashWallet, newWallet.Kind)
	require.NotEmpty(t, result.NewWalletPubkey)
	assert.Equal(t, *newWallet.WalletPubkey, result.NewWalletPubkey, "the plaintext pubkey the response hands the caller must be the real new wallet's own")

	// The new wallet is funded with exactly the victim's slice amount and
	// holds one unclaimed bearer slice for the caller-supplied commitment.
	assert.Equal(t, int64(1000), queries.GetIsolatedBalance(svc.DB, newWallet.ID))
	newClaim, err := svc.AppsService.GetCashWalletClaim(newWallet.ID, db.CashIdentityBearer, newSecretHash)
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

// cashWalletClaimByIdentity looks up a CashWalletClaim regardless of its
// claimed_at state — unlike AppsService.GetCashWalletClaim (which only ever
// returns unclaimed rows), tests that need to inspect a slice AFTER it's been
// claimed or spun off need the raw row.
func cashWalletClaimByIdentity(t *testing.T, svc *tests.TestService, walletAppID uint, identityType, identityValue string) *db.CashWalletClaim {
	t.Helper()
	var claim db.CashWalletClaim
	err := svc.DB.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ?",
		walletAppID, identityType, identityValue).First(&claim).Error
	require.NoError(t, err)
	return &claim
}

// TestHandleCashTransferEvent_RaceAgainstCashRedeem_NeverBothSucceed is a
// regression test for an independent dynamic (live black-box) audit finding
// (2026-07-28): ClaimCashSlice's committing UPDATE used to be guarded
// only by "id = ? AND claimed_at IS NULL" — it never re-checked
// identity_type/identity_value. A concurrent cash_transfer can reassign a
// row's identity without ever setting claimed_at, so a cash_redeem racing it
// could still match on id alone and pay out the PRE-transfer identity even
// after cash_transfer had already reported success to the NEW owner — a
// "phantom transfer": the transfer API call succeeds, but the funds it
// promised are already gone.
//
// Fixed in apps/cash_hub_service.go (ClaimCashSlice's update now
// re-checks identity_type/identity_value, so a slice reassigned out from
// under a racing redeem correctly fails that redeem with "not found" instead
// of paying out the superseded identity). "Not both succeed" is now a
// structural guarantee from the atomic, identity-checked update — not a
// probabilistic outcome that needs many iterations to hit — so a single race
// is enough to prove it; the audit's own many-iteration live-node run was
// about *reliably observing the pre-fix bug*, not something this regression
// test needs to repeat.
func TestHandleCashTransferEvent_RaceAgainstCashRedeem_NeverBothSucceed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	controller := NewTestNip47Controller(svc)

	secret1Hex, secret1Hash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityBearer, IdentityValue: secret1Hash, AmountMloki: 1000},
	}))
	_, secret2Hash := bearerSecretAndHash(t)

	var redeemResp, transferResp *models.Response
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		redeemResp = handleClaimFundsFor(t, svc, controller, wallet, cashRedeemParams{
			Invoice:      tests.MockZeroAmountInvoice,
			Amount:       ptrUint64(1000),
			BearerSecret: secret1Hex,
		})
	}()
	go func() {
		defer wg.Done()
		transferResp = handleCashTransferFor(t, svc, controller, wallet, cashTransferParams{
			BearerSecret: secret1Hex,
			NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: secret2Hash},
		})
	}()
	wg.Wait()

	redeemWon := redeemResp.Error == nil
	transferWon := transferResp.Error == nil
	require.False(t, redeemWon && transferWon,
		"cash_redeem and cash_transfer both reported success for the same slice — phantom transfer")
	require.True(t, redeemWon || transferWon,
		"neither op succeeded against an unclaimed slice (redeem=%v transfer=%v)", redeemResp.Error, transferResp.Error)

	// A losing cash_transfer here lost purely to timing (a concurrent redeem
	// claimed the slice first), not to anything wrong with the transfer
	// request itself — it must report NOT_FOUND, the same code cash_redeem's
	// own identical race-loss case reports, not BAD_REQUEST (a bug found live
	// against this fix: ReassignCashSliceIdentity's race-loss error used to be
	// indistinguishable from a genuinely-invalid-request error, both of which
	// the controller mapped to BAD_REQUEST at the time).
	if !transferWon {
		require.NotNil(t, transferResp.Error)
		assert.Equal(t, constants.ERROR_NOT_FOUND, transferResp.Error.Code,
			"a cash_transfer that lost a race against a concurrent cash_redeem must report NOT_FOUND, not BAD_REQUEST")
	}

	if transferWon {
		// A genuinely successful transfer must be durable: the new secret
		// must actually redeem, using a DIFFERENT mock invoice than the
		// racing redeem attempted — payment-hash idempotency is checked
		// instance-wide, matching a real Lightning node, so reusing the same
		// invoice here would spuriously fail as "already paid" even though
		// no payment for it ever went through.
		oldRedeemResp := handleClaimFundsFor(t, svc, controller, wallet, cashRedeemParams{
			Invoice:      tests.MockInvoice,
			Amount:       ptrUint64(1000),
			BearerSecret: secret1Hex,
		})
		require.NotNil(t, oldRedeemResp.Error, "the pre-transfer secret must no longer redeem")
	}
}

// TestHandleCashTransferEvent_RaceLossAgainstCashRedeem_AlwaysReportsNotFound
// repeats the same cash_redeem-vs-cash_transfer race as
// TestHandleCashTransferEvent_RaceAgainstCashRedeem_NeverBothSucceed across
// several fresh trials specifically to reach the "transfer loses" outcome
// reliably (a single race's winner isn't deterministic), and asserts every
// losing transfer reports NOT_FOUND — never the BAD_REQUEST a real run of
// this exact race produced before cash_transfer_controller.go's error mapping
// was fixed to distinguish ReassignCashSliceIdentity's race-loss sentinel
// (constants.ErrNotFound) from a genuinely-invalid-request error
// (constants.ErrInvalidParams, still BAD_REQUEST). Found live via the
// integration suite's TestAudit_CashTransferVsRedeem_NeverBothSucceed
// (integration/audit_dynamic_test.go) during the 2026-07-29 follow-up audit.
func TestHandleCashTransferEvent_RaceLossAgainstCashRedeem_AlwaysReportsNotFound(t *testing.T) {
	const trials = 15
	sawTransferLoss := false

	for trial := 0; trial < trials; trial++ {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)

		hub := tests.CreateCashHub(t, svc, 100_000, 3600)
		wallet := newFundedCashWallet(t, svc, hub, 1000)
		controller := NewTestNip47Controller(svc)

		secret1Hex, secret1Hash := bearerSecretAndHash(t)
		require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
			{IdentityType: db.CashIdentityBearer, IdentityValue: secret1Hash, AmountMloki: 1000},
		}))
		_, secret2Hash := bearerSecretAndHash(t)

		var redeemResp, transferResp *models.Response
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			redeemResp = handleClaimFundsFor(t, svc, controller, wallet, cashRedeemParams{
				Invoice:      tests.MockZeroAmountInvoice,
				Amount:       ptrUint64(1000),
				BearerSecret: secret1Hex,
			})
		}()
		go func() {
			defer wg.Done()
			transferResp = handleCashTransferFor(t, svc, controller, wallet, cashTransferParams{
				BearerSecret: secret1Hex,
				NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: secret2Hash},
			})
		}()
		wg.Wait()
		svc.Remove()

		redeemWon := redeemResp.Error == nil
		transferWon := transferResp.Error == nil
		require.False(t, redeemWon && transferWon, "trial %d: both succeeded — phantom transfer", trial)
		require.True(t, redeemWon || transferWon, "trial %d: neither succeeded", trial)

		if !transferWon {
			sawTransferLoss = true
			require.NotNil(t, transferResp.Error)
			assert.Equal(t, constants.ERROR_NOT_FOUND, transferResp.Error.Code,
				"trial %d: a cash_transfer that lost a race against a concurrent cash_redeem must report "+
					"NOT_FOUND, not %s", trial, transferResp.Error.Code)
		}
	}

	require.True(t, sawTransferLoss,
		"the transfer never lost the race in %d trials — increase trials or check the race is still live", trials)
}

// TestHandleCashTransferEvent_SpinOff_FundingFailure_RollsBack verifies
// handleCashTransferSplit's claim-then-fund-then-rollback-on-failure
// ordering: if the new wallet's funding transfer fails after the source
// slice has already been exclusively claimed, that claim must be undone
// (UnclaimCashSlice) rather than left stranded — a caller who hits this
// error can safely retry the exact same request.
func TestHandleCashTransferEvent_SpinOff_FundingFailure_RollsBack(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 200_000)

	// No self-payment pubkey override here, deliberately: this forces
	// SendPaymentSync down its normal (non-intercepted) path, where a queued
	// PayInvoiceError actually has somewhere to bite.
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.PayInvoiceResponses = []*lnclient.PayInvoiceResponse{nil}
	mockLN.PayInvoiceErrors = []error{errors.New("simulated payment failure")}

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	otherPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: otherPubkey, AmountMloki: 1000},
	}))

	childrenBefore, err := svc.AppsService.ListCashHubWalletChildren(hub.ID)
	require.NoError(t, err)

	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, newSecretHash, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash},
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_INTERNAL, response.Error.Code)

	// The source slice must be rolled back to unclaimed, not stranded.
	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "a failed spin-off must roll back the source slice's claim so the caller can retry")
	assert.Nil(t, claim.SpunOffToWalletAppID)

	// The half-created wallet must have been deleted, not left stranded.
	childrenAfter, err := svc.AppsService.ListCashHubWalletChildren(hub.ID)
	require.NoError(t, err)
	assert.Len(t, childrenAfter, len(childrenBefore), "a failed spin-off must not leave a half-created wallet behind")

	// A retry (this time with the payment succeeding) must now work.
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-retry", Amount: 1000},
	}
	retryProof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, newSecretHash, "", 1000, nil, time.Now())
	retryResponse := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, retryProof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash},
	})
	require.Nil(t, retryResponse.Error, "a retry after a rolled-back spin-off must succeed")
}
