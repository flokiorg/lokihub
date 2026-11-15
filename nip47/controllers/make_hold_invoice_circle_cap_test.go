package controllers

// The make_invoice scope also grants make_hold_invoice; a Circle Wallet is
// refused it.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func makeHoldInvoiceRequestJSON(amountMloki uint64) string {
	return fmt.Sprintf(`{"method":"make_hold_invoice","params":{"amount":%d,"description":"test","payment_hash":"%s"}}`,
		amountMloki, tests.RandomHex32())
}

func handleHoldInvoice(t *testing.T, svc *tests.TestService, appID uint, amountMloki uint64) *models.Response {
	t.Helper()
	r := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeHoldInvoiceRequestJSON(amountMloki)), r))
	ev := &db.RequestEvent{AppId: &appID, NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var resp *models.Response
	NewTestNip47Controller(svc).HandleMakeHoldInvoiceEvent(context.TODO(), r, ev.ID, appID,
		func(rr *models.Response, _ nostr.Tags) { resp = rr })
	require.NotNil(t, resp)
	return resp
}

// A Circle Wallet cannot issue a hold invoice: make_hold_invoice is outside what
// its connection allows (NIP-CW §What a Circle Wallet Connection Allows), and
// its only held payments are the ones its Hub settles.
func TestMakeHoldInvoice_CircleWallet_Restricted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	child := circleCapChild(t, svc, "hold")

	for _, amount := range []uint64{1_000, 100_000, 1_000_000} {
		resp := handleHoldInvoice(t, svc, child.ID, amount)
		require.NotNil(t, resp.Error)
		require.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
	}
	var rows int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("app_id = ?", child.ID).Count(&rows).Error)
	require.Zero(t, rows, "no invoice was issued")
}

// A wallet with no circle parent keeps issuing hold invoices of any size.
func TestMakeHoldInvoice_NonCircleApp_NoCap(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	resp := handleHoldInvoice(t, svc, app.ID, 1_000_000)
	require.Nil(t, resp.Error)
}
