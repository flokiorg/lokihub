package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// transferWithCall runs cash_transfer with an explicit private call.
func transferWithCall(t *testing.T, svc *tests.TestService, wallet *db.App, params cashTransferParams, call *PrivateCall) *models.Response {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"method": constants.NIP47MethodCashTransfer, "params": params})
	require.NoError(t, err)
	req := &models.Request{}
	require.NoError(t, json.Unmarshal(raw, req))
	var resp *models.Response
	NewTestNip47Controller(svc).HandleCashTransferEvent(context.TODO(), req, 0, wallet,
		func(r *models.Response, _ nostr.Tags) { resp = r }, nostr.Tags{}, call)
	require.NotNil(t, resp)
	return resp
}

type transferFixture struct {
	svc         *tests.TestService
	hub, wallet *db.App
	ownerPriv   string
	ownerPub    string
}

func newTransferFixture(t *testing.T, amount int64) *transferFixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, amount)
	priv := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(priv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pub, AmountMloki: amount},
	}))
	return &transferFixture{svc: svc, hub: hub, wallet: wallet, ownerPriv: priv, ownerPub: pub}
}

func (f *transferFixture) params(t *testing.T, to string, amount uint64, partial bool, requestID string) cashTransferParams {
	proof := buildTransferProofEvent(t, f.ownerPriv, *f.wallet.WalletPubkey, db.CashIdentityPubkey, to, "", amount, nil, time.Now())
	p := cashTransferParams{
		IdentityType: db.CashIdentityPubkey, IdentityValue: f.ownerPub, IdentityEvent: mustMarshal(t, proof),
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: to},
		RequestID:   requestID,
	}
	if partial {
		p.AmountMillis = &amount
	}
	return p
}

func isUsed(t *testing.T, svc *tests.TestService, id string) bool {
	t.Helper()
	used, err := cashrequest.New(svc.DB).IsUsed(id)
	require.NoError(t, err)
	return used
}

// The request_id is marked once the call is authorized, not before: a proof
// that fails leaves it unused; an authorized call refused later leaves it used,
// and a resend is REQUEST_REUSED.
func TestCashTransfer_RequestIDMarkedAtAuthorization(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// A proof signed by someone else fails authorization.
	id := testRequestID()
	p := f.params(t, to, 5_000, false, id)
	stranger := nostr.GeneratePrivateKey()
	p.IdentityEvent = mustMarshal(t, buildTransferProofEvent(t, stranger, *f.wallet.WalletPubkey, db.CashIdentityPubkey, to, "", 5_000, nil, time.Now()))
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.NotNil(t, resp.Error)
	assert.False(t, isUsed(t, f.svc, id), "an unauthorized call leaves its id unused")

	// Authorized, then refused: an amount over the slice.
	id = testRequestID()
	p = f.params(t, to, 6_000, true, id)
	resp = transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.True(t, isUsed(t, f.svc, id), "an authorized call's id is used, whatever happens to it")

	p = f.params(t, to, 5_000, false, id)
	resp = transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_REQUEST_REUSED, resp.Error.Code)
	assert.Nil(t, cashWalletClaimByIdentity(t, f.svc, f.wallet.ID, db.CashIdentityPubkey, f.ownerPub).ClaimedAt)
}

// The in-place transfer's record commits with it: authorizer and delivery key
// are the claim signer, there are no bills, and it may be dropped at deadline +
// H + 1.
func TestCashTransfer_InPlaceRecord(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	id := testRequestID()
	p := f.params(t, to, 5_000, false, id)
	call := testPrivateCall(p.IdentityEvent)
	resp := transferWithCall(t, f.svc, f.wallet, p, call)
	require.Nil(t, resp.Error)

	rec, err := cashrequest.New(f.svc.DB).Get(id)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, constants.NIP47MethodCashTransfer, rec.Method)
	assert.Equal(t, *f.wallet.WalletPubkey, rec.TargetWalletPubkey)
	assert.Equal(t, f.wallet.AppPubkey, rec.TargetConnPubkey)
	assert.Equal(t, "pubkey", rec.AuthorizerKind)
	assert.Equal(t, f.ownerPub, rec.Authorizer)
	require.NotNil(t, rec.DeliveryPubkey)
	assert.Equal(t, f.ownerPub, *rec.DeliveryPubkey)
	assert.Equal(t, call.Meta.Deadline, rec.Deadline)
	require.NotNil(t, rec.DropAfter, "an in-place transfer creates no bill")
	h, err := cashrequest.HorizonTx(f.svc.DB)
	require.NoError(t, err)
	assert.Equal(t, call.Meta.Deadline+h+1, *rec.DropAfter)
	bills, err := cashrequest.New(f.svc.DB).Bills(rec.ID)
	require.NoError(t, err)
	assert.Empty(t, bills)
}

// A partial split's record names both new bills, carved then remainder, and its
// result_meta carries what the caller keeps.
func TestCashTransfer_SplitRecord(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	id := testRequestID()
	p := f.params(t, to, 2_000, true, id)
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.Nil(t, resp.Error)
	result := resp.Result.(cashTransferResponse)

	rec, err := cashrequest.New(f.svc.DB).Get(id)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Nil(t, rec.DropAfter, "kept while its bills live")
	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(rec.ResultMeta), &meta))
	assert.EqualValues(t, 2_000, meta["amount_millis"])
	assert.EqualValues(t, 3_000, meta["remaining_amount_millis"])
	for _, secret := range []string{"new_wallet_token", "remainder_wallet_token"} {
		assert.NotContains(t, meta, secret, "no token is stored in the record")
	}
	bills, err := cashrequest.New(f.svc.DB).Bills(rec.ID)
	require.NoError(t, err)
	require.Len(t, bills, 2)
	assert.Equal(t, db.CashRecordBillCarved, bills[0].Role)
	assert.Equal(t, result.NewWalletPubkey, bills[0].WalletPubkey)
	assert.Equal(t, db.CashRecordBillRemainder, bills[1].Role)
	assert.Equal(t, result.RemainderWalletPubkey, bills[1].WalletPubkey)
}

// A cash source's record is authorized by its commitment, and its tokens travel
// in the clear (no delivery key).
func TestCashTransfer_CashSourceRecord(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1_000)
	secret, commitment := cashSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: commitment, AmountMloki: 1_000},
	}))
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	id := testRequestID()
	resp := transferWithCall(t, svc, wallet, cashTransferParams{
		CashSecret:  secret,
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: to},
		RequestID:   id,
	}, testPrivateCall(""))
	require.Nil(t, resp.Error)
	rec, err := cashrequest.New(svc.DB).Get(id)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "commitment", rec.AuthorizerKind)
	assert.Equal(t, commitment, rec.Authorizer)
	assert.Nil(t, rec.DeliveryPubkey)
}

// A call that reaches its commit after its deadline is refused BAD_REQUEST with
// nothing committed; its id stays used.
func TestCashTransfer_CommitAfterDeadline(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	id := testRequestID()
	p := f.params(t, to, 2_000, true, id)
	call := testPrivateCall(p.IdentityEvent)
	call.Meta.Deadline = time.Now().Unix() - 1
	resp := transferWithCall(t, f.svc, f.wallet, p, call)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "deadline")
	assert.True(t, isUsed(t, f.svc, id))
	assert.Nil(t, cashWalletClaimByIdentity(t, f.svc, f.wallet.ID, db.CashIdentityPubkey, f.ownerPub).ClaimedAt, "nothing committed")
	rec, err := cashrequest.New(f.svc.DB).Get(id)
	require.NoError(t, err)
	assert.Nil(t, rec)
}
