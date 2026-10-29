package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipIC"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// Requests built by nmilat's own client, handed to this Hub's controllers.
//
// Every other controller test builds its proofs by hand, so they pin the Hub's
// checks but not the agreement between the Hub and the client that real users
// run. These tests do: each request below is exactly what nipcash's builders
// produce. An SDK that binds the wrong thing (a renamed tag, an empty d tag, a
// dropped attestation, a hash over different fields, the wrong amount) yields a
// request this Hub refuses, so these turn red. The live-hub equivalents are in
// integration/nmilat_sdk_test.go, which unit runs do not reach.

const interopPlatform = nipIC.WebIdentity("discord")

func sdkRequest(t *testing.T, method string, params interface{}) *models.Request {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{"method": method, "params": params})
	require.NoError(t, err)
	req := &models.Request{}
	require.NoError(t, json.Unmarshal(raw, req))
	return req
}

func serveSDK(t *testing.T, svc *tests.TestService, app *db.App, req *models.Request) *models.Response {
	t.Helper()
	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var response *models.Response
	publish := func(r *models.Response, _ nostr.Tags) { response = r }
	controller := NewTestNip47Controller(svc)
	var params struct {
		IdentityEvent string `json:"identity_event"`
	}
	_ = json.Unmarshal(req.Params, &params)
	switch req.Method {
	case constants.NIP47MethodCashRedeem:
		controller.HandleCashRedeemEvent(context.TODO(), req, ev.ID, app, publish, nostr.Tags{}, testPrivateCall(params.IdentityEvent))
	case constants.NIP47MethodCashTransfer:
		controller.HandleCashTransferEvent(context.TODO(), req, ev.ID, app, publish, nostr.Tags{}, testPrivateCall(params.IdentityEvent))
	default:
		t.Fatalf("no handler wired for %s", req.Method)
	}
	require.NotNil(t, response)
	return response
}

// trustedConnectionKey registers a trusted Identity Authority and returns an
// attestation it issued binding externalID's connection key to the claimant.
func trustedConnectionKey(t *testing.T, svc *tests.TestService, externalID, claimantPub string) (iaPub string, connKey nipIC.ConnectionKey, attestation *nipIC.Attestation) {
	t.Helper()
	iaPriv := nostr.GeneratePrivateKey()
	iaPub, _ = nostr.GetPublicKey(iaPriv)
	registerTrustedIA(t, svc, iaPub)
	connKey, err := nipIC.NewConnectionKey(interopPlatform, externalID)
	require.NoError(t, err)
	if claimantPub == "" {
		return iaPub, connKey, nil
	}
	ev, err := nipIC.NewAttestation(nipIC.AttestationParams{
		PrivateKey:     iaPriv,
		ConnectionKey:  connKey,
		UserPubkey:     claimantPub,
		Platform:       interopPlatform,
		Evidence:       nipIC.Evidence{Platform: interopPlatform, UserID: externalID, Username: "interop", VerifiedAt: time.Now().Unix()},
		AuthType:       "oauth",
		ExpirationDays: 1,
	})
	require.NoError(t, err)
	attestation, err = nipIC.ParseAttestation(ev)
	require.NoError(t, err)
	return iaPub, connKey, attestation
}

func TestSDKInterop_Redeem_Pubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	priv := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(priv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pub, AmountMloki: 1000},
	}))

	amount := uint64(1000)
	params, err := nipcash.CashRedeemParams{Invoice: tests.MockZeroAmountInvoice, Amount: &amount, Credential: nipcash.BySigning(priv)}.
		Request(*wallet.WalletPubkey)
	require.NoError(t, err)

	response := serveSDK(t, svc, wallet, sdkRequest(t, constants.NIP47MethodCashRedeem, params))
	require.Nil(t, response.Error, "an SDK-built pubkey redeem must be served: %+v", response.Error)
	assert.NotNil(t, cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, pub).ClaimedAt)
}

func TestSDKInterop_Redeem_ConnectionKey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	claimantPriv := nostr.GeneratePrivateKey()
	claimantPub, _ := nostr.GetPublicKey(claimantPriv)
	iaPub, connKey, attestation := trustedConnectionKey(t, svc, "100000000000000001", claimantPub)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: string(connKey), IAPubkey: iaPub, AmountMloki: 1000},
	}))

	amount := uint64(1000)
	cred, err := nipcash.BySigningConnectionKey(claimantPriv, attestation)
	require.NoError(t, err)
	params, err := nipcash.CashRedeemParams{
		Invoice: tests.MockZeroAmountInvoice, Amount: &amount,
		Credential: cred,
	}.Request(*wallet.WalletPubkey)
	require.NoError(t, err)

	response := serveSDK(t, svc, wallet, sdkRequest(t, constants.NIP47MethodCashRedeem, params))
	require.Nil(t, response.Error, "an SDK-built connection_key redeem must be served: %+v", response.Error)
	assert.NotNil(t, cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityConnectionKey, string(connKey)).ClaimedAt)
}

// A partial split to a connection_key target exercises every transfer binding at
// once: the new_identity_hash tag and its preimage (ia_pubkey included), the
// new identity's ia_pubkey on the wire, and the amount_millis of the split.
func TestSDKInterop_Transfer_SplitToConnectionKey(t *testing.T) {
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
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c" // MockInvoice's payee: settles as a self-payment
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-carved", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, PaymentHash: tests.MockLNClientHoldTransaction.PaymentHash, Preimage: "preimage-remainder", Amount: 3000},
	}

	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, AmountMloki: 5000},
	}))
	iaPub, connKey, _ := trustedConnectionKey(t, svc, "100000000000000002", "")

	split := uint64(2000)
	to, err := nipcash.ConnectionKey(interopPlatform, "100000000000000002", iaPub)
	require.NoError(t, err)
	params, err := nipcash.CashTransferParams{
		RequestID:     testRequestID(),
		Credential:    nipcash.BySigning(ownerPriv),
		To:            to,
		CurrentAmount: 5000,
		SplitAmount:   &split,
	}.Request(*wallet.WalletPubkey)
	require.NoError(t, err)

	response := serveSDK(t, svc, wallet, sdkRequest(t, constants.NIP47MethodCashTransfer, params))
	require.Nil(t, response.Error, "an SDK-built split to a connection_key must be served: %+v", response.Error)
	result := response.Result.(cashTransferResponse)

	var carved db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", result.NewWalletPubkey).First(&carved).Error)
	slice, err := svc.AppsService.GetCashWalletClaim(carved.ID, db.CashIdentityConnectionKey, string(connKey))
	require.NoError(t, err)
	require.NotNil(t, slice)
	assert.Equal(t, iaPub, slice.IAPubkey)
	assert.EqualValues(t, split, slice.AmountMloki)
}

// A cash-mode holder authorizes with its secret alone.
func TestSDKInterop_Transfer_CashCurrent(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	secretHex, commitment, err := tests.GenerateCashSecret()
	require.NoError(t, err)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: commitment, AmountMloki: 1000},
	}))
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	params, err := nipcash.CashTransferParams{
		RequestID:     testRequestID(),
		Credential:    nipcash.BySecret(secretHex),
		To:            nipcash.Pubkey(newPub),
		CurrentAmount: 1000,
	}.Request(*wallet.WalletPubkey)
	require.NoError(t, err)

	response := serveSDK(t, svc, wallet, sdkRequest(t, constants.NIP47MethodCashTransfer, params))
	require.Nil(t, response.Error, "an SDK-built cash-mode transfer must be served: %+v", response.Error)
	slice, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, newPub)
	require.NoError(t, err)
	require.NotNil(t, slice, "the slice now belongs to the new pubkey")
}
