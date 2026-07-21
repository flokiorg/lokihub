package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

const nip47MakeInvoiceJson = `
{
	"method": "make_invoice",
	"params": {
		"amount": 1000,
		"description": "Hello, world",
		"expiry": 3600,
		"metadata": {
		  "a": 1,
			"b": "2",
			"c": {
			  "d": 3,
				"e": [{
					"f": "g"
				},{
					"h": "i"
				}]
			}
		}
	}
}
`

func TestHandleMakeInvoiceEvent(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47MakeInvoiceJson), nip47Request)
	assert.NoError(t, err)

	app, _, err := tests.CreateApp(svc)
	assert.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{
		AppId: &app.ID,
	}
	err = svc.DB.Create(&dbRequestEvent).Error
	assert.NoError(t, err)

	var publishedResponse *models.Response

	publishResponse := func(response *models.Response, tags nostr.Tags) {
		publishedResponse = response
	}

	NewTestNip47Controller(svc).
		HandleMakeInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, *dbRequestEvent.AppId, publishResponse)

	expectedMetadata := map[string]interface{}{
		"a": float64(1),
		"b": "2",
		"c": map[string]interface{}{
			"d": float64(3),
			"e": []interface{}{
				map[string]interface{}{"f": "g"},
				map[string]interface{}{"h": "i"},
			},
		},
	}

	assert.Nil(t, publishedResponse.Error)
	assert.Equal(t, tests.MockLNClientTransaction.Invoice, publishedResponse.Result.(*makeInvoiceResponse).Invoice)
	assert.Equal(t, expectedMetadata, publishedResponse.Result.(*makeInvoiceResponse).Metadata)
}

func makeInvoiceRequestJSON(amountMloki uint64) string {
	return fmt.Sprintf(`{"method":"make_invoice","params":{"amount":%d,"description":"test"}}`, amountMloki)
}

func TestHandleMakeInvoiceEvent_CircleChild_CapExceeded(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	// Circle child: cap = 100 loki = 100_000 mloki.
	child, _, err := svc.AppsService.CreateApp("child", "", 100, "never", nil,
		[]string{constants.MAKE_INVOICE_SCOPE, constants.PAY_INVOICE_SCOPE},
		db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	// Pre-fund the child with 80_000 mloki already received.
	svc.DB.Create(&db.Transaction{
		AppId:       &child.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 80_000,
		PaymentHash: "childinc",
	})

	dbRequestEvent := &db.RequestEvent{AppId: &child.ID}
	svc.DB.Create(&dbRequestEvent)

	// Request 30_000 mloki: 80k + 30k = 110k > 100k cap → must be rejected.
	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeInvoiceRequestJSON(30_000)), nip47Request)
	require.NoError(t, err)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMakeInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, child.ID, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, publishedResponse.Error.Code)
}

func TestHandleMakeInvoiceEvent_CircleChild_CapNotExceeded(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	parent, _, err := svc.AppsService.CreateApp("parent2", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)

	// Circle child: cap = 100 loki = 100_000 mloki, currently 0 balance.
	child, _, err := svc.AppsService.CreateApp("child2", "", 100, "never", nil,
		[]string{constants.MAKE_INVOICE_SCOPE, constants.PAY_INVOICE_SCOPE},
		db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{AppId: &child.ID}
	svc.DB.Create(&dbRequestEvent)

	// Request 50_000 mloki: 0 + 50k = 50k ≤ 100k cap → must succeed.
	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeInvoiceRequestJSON(50_000)), nip47Request)
	require.NoError(t, err)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMakeInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, child.ID, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.Nil(t, publishedResponse.Error, "invoice within circle cap must succeed")
}

func TestHandleMakeInvoiceEvent_NonCircleChild_NoCap(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A regular standard app — no cap should apply.
	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{AppId: &app.ID}
	svc.DB.Create(&dbRequestEvent)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeInvoiceRequestJSON(999_999_999)), nip47Request)
	require.NoError(t, err)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMakeInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, app.ID, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	// No quota error — any amount is OK for non-circle apps.
	if publishedResponse.Error != nil {
		assert.NotEqual(t, constants.ERROR_QUOTA_EXCEEDED, publishedResponse.Error.Code)
	}
}
