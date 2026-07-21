package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

const nip47PayInvoiceJson = `
{
	"method": "pay_invoice",
	"params": {
		"invoice": "lntbs1230n1pnkqautdqyw3jsnp4q09a0z84kg4a2m38zjllw43h953fx5zvqe8qxfgw694ymkq26u8zcpp5yvnh6hsnlnj4xnuh2trzlnunx732dv8ta2wjr75pdfxf6p2vlyassp5hyeg97a3ft5u769kjwsn7p0e85h79pzz8kladmnqhpcypz2uawjs9qyysgqcqpcxq8zals8sq9yeg2pa9eywkgj50cyzxd5elatujuc0c0wh6j9nat5mn34pgk8u9ufpgs99tw9ldlfk42cqlkr48au3lmuh09269prg4qkggh4a8cyqpfl0y6j",
		"metadata": {"a": 123}
	}
}
`
const nip47PayInvoiceZeroAmountJson = `
{
	"method": "pay_invoice",
	"params": {
		"invoice": "` + tests.MockZeroAmountInvoice + `",
		"amount": 1234
	}
}
`

const nip47PayJsonNoInvoice = `
{
	"method": "pay_invoice",
	"params": {
		"something": "else"
	}
}
`

const nip47PayJsonExpiredInvoice = `
{
	"method": "pay_invoice",
	"params": {
		"invoice": "lntb1230n1pjypux0pp5xgxzcks5jtx06k784f9dndjh664wc08ucrganpqn52d0ftrh9n8sdqyw3jscqzpgxqyz5vqsp5rkx7cq252p3frx8ytjpzc55rkgyx2mfkzzraa272dqvr2j6leurs9qyyssqhutxa24r5hqxstchz5fxlslawprqjnarjujp5sm3xj7ex73s32sn54fthv2aqlhp76qmvrlvxppx9skd3r5ut5xutgrup8zuc6ay73gqmra29m"
	}
}
`

func TestHandlePayInvoiceEvent(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47PayInvoiceJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse, nostr.Tags{})

	assert.Equal(t, "123preimage", publishedResponse.Result.(payResponse).Preimage)

	transactionType := constants.TRANSACTION_TYPE_OUTGOING
	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsSvc.LookupTransaction(ctx, "23277d5e13fce5534f9752c62fcf9337a2a6b0ebea9d21fa816a4c9d054cf93b", &transactionType, svc.LNClient, &app.ID)
	assert.NoError(t, err)

	type dummyMetadata struct {
		A int `json:"a"`
	}
	var decodedMetadata dummyMetadata
	err = json.Unmarshal(transaction.Metadata, &decodedMetadata)
	assert.NoError(t, err)
	assert.Equal(t, 123, decodedMetadata.A)
}

func TestHandlePayInvoiceEvent_ZeroAmount(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47PayInvoiceZeroAmountJson), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse, nostr.Tags{})

	assert.Equal(t, "123preimage", publishedResponse.Result.(payResponse).Preimage)

	transactionType := constants.TRANSACTION_TYPE_OUTGOING
	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsSvc.LookupTransaction(ctx, tests.MockZeroAmountPaymentHash, &transactionType, svc.LNClient, &app.ID)
	assert.NoError(t, err)
	// from the request amount
	assert.Equal(t, uint64(1234), transaction.AmountMloki)
}

func TestHandlePayInvoiceEvent_MalformedInvoice(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47PayJsonNoInvoice), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse, nostr.Tags{})

	assert.Nil(t, publishedResponse.Result)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
	assert.Equal(t, "Failed to decode bolt11 invoice: invalid flokicoin invoice prefix: ", publishedResponse.Error.Message)
}

func TestHandlePayInvoiceEvent_ExpiredInvoice(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47PayJsonExpiredInvoice), nip47Request)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, app, publishResponse, nostr.Tags{})

	assert.Nil(t, publishedResponse.Result)
	assert.Equal(t, constants.ERROR_INTERNAL, publishedResponse.Error.Code)
	assert.Equal(t, "this invoice has expired", publishedResponse.Error.Message)
}

func TestHandlePayInvoiceEvent_JITWallet_FullBalanceAllowed(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Create a JIT wallet (child of a hub) with exactly the invoice amount as balance.
	jitWallet, _, err := svc.AppsService.CreateApp("jit-w", "", 0, "never", nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindJITWallet, nil, db.ParentKindJIT, nil)
	require.NoError(t, err)

	// MockInvoice decodes to 123000 mloki. Pre-fund with invoice + fee reserve
	// (max(1%, 10000) = 10000 mloki) so SendPaymentSync can succeed.
	svc.DB.Create(&db.Transaction{
		AppId:       &jitWallet.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 133000, // 123000 invoice + 10000 fee reserve
		PaymentHash: "jitfund",
	})

	appPermission := &db.AppPermission{AppId: jitWallet.ID, Scope: constants.PAY_INVOICE_SCOPE}
	svc.DB.Create(appPermission)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47PayInvoiceJson), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	transactionsService := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	_ = transactionsService

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, jitWallet, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	}, nostr.Tags{})

	// Payment succeeds (mock LN always succeeds) — no ERROR_RESTRICTED.
	assert.Nil(t, publishedResponse.Error, "full-balance JIT wallet payment must succeed")
}

func TestHandlePayInvoiceEvent_JITWallet_PartialAmountRejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Pre-fund with more than the invoice amount so it's a partial spend.
	jitWallet, _, err := svc.AppsService.CreateApp("jit-w2", "", 0, "never", nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindJITWallet, nil, db.ParentKindJIT, nil)
	require.NoError(t, err)

	svc.DB.Create(&db.Transaction{
		AppId:       &jitWallet.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 500_000, // much more than the 123000 mloki invoice
		PaymentHash: "jitfund2",
	})

	svc.DB.Create(&db.AppPermission{AppId: jitWallet.ID, Scope: constants.PAY_INVOICE_SCOPE})

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47PayInvoiceJson), nip47Request) // invoice is 123000 mloki
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, jitWallet, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	}, nostr.Tags{})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, publishedResponse.Error.Code, "partial JIT payment must be rejected with ERROR_RESTRICTED")
}

func TestHandlePayInvoiceEvent_NonJIT_NoFullBalanceRequirement(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A regular isolated wallet — partial spends should be allowed.
	isolatedApp, _, err := svc.AppsService.CreateApp("isolated", "", 0, "never", nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindIsolated, nil, "", nil)
	require.NoError(t, err)

	svc.DB.Create(&db.Transaction{
		AppId:       &isolatedApp.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 500_000,
		PaymentHash: "isofund",
	})

	svc.DB.Create(&db.AppPermission{AppId: isolatedApp.ID, Scope: constants.PAY_INVOICE_SCOPE})

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47PayInvoiceJson), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, isolatedApp, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	}, nostr.Tags{})

	// Standard isolated wallet: no full-balance guard, so no ERROR_RESTRICTED.
	if publishedResponse.Error != nil {
		assert.NotEqual(t, constants.ERROR_RESTRICTED, publishedResponse.Error.Code)
	}
}

// TestHandlePayInvoiceEvent_JITWallet_InternalTransferBypassRejected verifies that
// a user-supplied "internal_transfer":true in pay_invoice metadata cannot bypass the
// JIT full-drain enforcement. This is an H1 security test.
func TestHandlePayInvoiceEvent_JITWallet_InternalTransferBypassRejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Fund with more than the invoice amount — this would be a partial spend.
	jitWallet, _, err := svc.AppsService.CreateApp("jit-bypass", "", 0, "never", nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindJITWallet, nil, db.ParentKindJIT, nil)
	require.NoError(t, err)
	svc.DB.Create(&db.Transaction{
		AppId:       &jitWallet.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 500_000,
		PaymentHash: "jitbypass-fund",
	})
	svc.DB.Create(&db.AppPermission{AppId: jitWallet.ID, Scope: constants.PAY_INVOICE_SCOPE})

	// Include "internal_transfer":true in user-controlled pay_invoice metadata.
	const bypassAttemptJson = `{
		"method": "pay_invoice",
		"params": {
			"invoice": "lntbs1230n1pnkqautdqyw3jsnp4q09a0z84kg4a2m38zjllw43h953fx5zvqe8qxfgw694ymkq26u8zcpp5yvnh6hsnlnj4xnuh2trzlnunx732dv8ta2wjr75pdfxf6p2vlyassp5hyeg97a3ft5u769kjwsn7p0e85h79pzz8kladmnqhpcypz2uawjs9qyysgqcqpcxq8zals8sq9yeg2pa9eywkgj50cyzxd5elatujuc0c0wh6j9nat5mn34pgk8u9ufpgs99tw9ldlfk42cqlkr48au3lmuh09269prg4qkggh4a8cyqpfl0y6j",
			"metadata": {"internal_transfer": true}
		}
	}`
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(bypassAttemptJson), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, jitWallet, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	}, nostr.Tags{})

	// Must still be rejected as a partial drain despite the bypass attempt.
	assert.NotNil(t, publishedResponse.Error, "JIT partial spend must be rejected even with user-injected internal_transfer flag")
	assert.Equal(t, constants.ERROR_RESTRICTED, publishedResponse.Error.Code,
		"user-injected internal_transfer must not bypass JIT full-drain enforcement")
}

// TestHandlePayInvoiceEvent_JitClaimSliceMetadataSpoofRejected verifies that a
// user-supplied "jit_claim_slice":true in pay_invoice metadata cannot shave the
// fee-reserve headroom off the isolated-balance/budget checks in validateCanPay.
// That flag is meant to be set only by claim_funds_controller.go itself (for a
// shared JIT wallet's own proof-gated payout, which independently pins the
// exact amount); pay_invoice/multi_pay_invoice previously stripped only
// "internal_transfer" from caller metadata, leaving this one caller-settable.
func TestHandlePayInvoiceEvent_JitClaimSliceMetadataSpoofRejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Fund with EXACTLY the invoice amount (123000 mloki) — no room for the
	// mandatory fee reserve (max(1%, 10000) = 10000 mloki), so a normal
	// pay_invoice call must fail with insufficient balance.
	isolatedApp, _, err := svc.AppsService.CreateApp("isolated-spoof", "", 0, "never", nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindIsolated, nil, "", nil)
	require.NoError(t, err)
	svc.DB.Create(&db.Transaction{
		AppId:       &isolatedApp.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 123000,
		PaymentHash: "spoof-fund",
	})
	svc.DB.Create(&db.AppPermission{AppId: isolatedApp.ID, Scope: constants.PAY_INVOICE_SCOPE})

	const bypassAttemptJson = `{
		"method": "pay_invoice",
		"params": {
			"invoice": "lntbs1230n1pnkqautdqyw3jsnp4q09a0z84kg4a2m38zjllw43h953fx5zvqe8qxfgw694ymkq26u8zcpp5yvnh6hsnlnj4xnuh2trzlnunx732dv8ta2wjr75pdfxf6p2vlyassp5hyeg97a3ft5u769kjwsn7p0e85h79pzz8kladmnqhpcypz2uawjs9qyysgqcqpcxq8zals8sq9yeg2pa9eywkgj50cyzxd5elatujuc0c0wh6j9nat5mn34pgk8u9ufpgs99tw9ldlfk42cqlkr48au3lmuh09269prg4qkggh4a8cyqpfl0y6j",
			"metadata": {"jit_claim_slice": true}
		}
	}`
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(bypassAttemptJson), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, isolatedApp, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	}, nostr.Tags{})

	require.NotNil(t, publishedResponse.Error, "payment must still require fee-reserve headroom despite spoofed jit_claim_slice")
	assert.Equal(t, constants.ERROR_INSUFFICIENT_BALANCE, publishedResponse.Error.Code,
		"user-injected jit_claim_slice must not bypass the fee-reserve balance check")
}
