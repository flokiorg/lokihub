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
)

// nip47MakeHoldInvoicePaymentHash is the payment_hash the request below asks for,
// named rather than repeated so the assertions cannot drift from the request.
const nip47MakeHoldInvoicePaymentHash = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

const nip47MakeHoldInvoiceJson = `
{
	"method": "make_hold_invoice",
	"params": {
		"amount": 1000,
		"description": "Hello, world",
		"payment_hash": "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
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

func TestHandleMakeHoldInvoiceEvent(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47MakeHoldInvoiceJson), nip47Request)
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
		HandleMakeHoldInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, *dbRequestEvent.AppId, publishResponse)

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
	assert.Equal(t, tests.MockLNClientHoldTransaction.Invoice, publishedResponse.Result.(*makeHoldInvoiceResponse).Invoice)
	// The hash the CALLER asked for, not the fixture's.
	//
	// This assertion used to read MockLNClientHoldTransaction.PaymentHash while the
	// request above asks for 1234...cdef, so it asserted that make_hold_invoice hands
	// back a DIFFERENT hash than the one requested. It only passed because the LN mock
	// discarded the paymentHash argument and always returned its own constant
	// (audit finding D-QA-3). A hold invoice exists for a hash its payer supplies and
	// keeps the preimage of, so substituting another one would make the invoice
	// unsettleable by the only party able to settle it.
	assert.Equal(t, nip47MakeHoldInvoicePaymentHash, publishedResponse.Result.(*makeHoldInvoiceResponse).PaymentHash)
	assert.Equal(t, expectedMetadata, publishedResponse.Result.(*makeHoldInvoiceResponse).Metadata)

	// And the node was asked for that same hash, which is the half the response alone
	// cannot show: a hub that echoed the request back while asking the node for
	// something else would satisfy the assertion above.
	holdCalls := svc.LNClient.(*tests.MockLn).MakeHoldInvoiceCalls()
	assert.Len(t, holdCalls, 1)
	assert.Equal(t, nip47MakeHoldInvoicePaymentHash, holdCalls[0].PaymentHash)
}

const nip47MakeHoldInvoiceMissingPaymentHashJson = `
{
"method": "make_hold_invoice",
"params": {
"amount": 1000,
"description": "Hello, world",
"expiry": 3600
}
}
`

func TestHandleMakeHoldInvoiceEvent_MissingPaymentHash(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(nip47MakeHoldInvoiceMissingPaymentHashJson), nip47Request)
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
		HandleMakeHoldInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, *dbRequestEvent.AppId, publishResponse)

	require.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
}
