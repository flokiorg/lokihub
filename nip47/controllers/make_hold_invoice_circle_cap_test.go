package controllers

// The make_invoice scope also grants make_hold_invoice, so a hold invoice is a
// second way for a circle wallet to receive. It must respect the same cap.

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

func TestMakeHoldInvoice_CircleCap_AboveCapRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	child := circleCapChild(t, svc, "hold-cap")

	resp := handleHoldInvoice(t, svc, child.ID, 1_000_000) // 10x the 100_000 mloki cap
	require.NotNil(t, resp.Error, "a hold invoice above the cap must be refused")
	require.Equal(t, constants.ERROR_QUOTA_EXCEEDED, resp.Error.Code)
}

func TestMakeHoldInvoice_CircleCap_WithinCapIssued(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	child := circleCapChild(t, svc, "hold-ok")

	resp := handleHoldInvoice(t, svc, child.ID, 100_000)
	require.Nil(t, resp.Error, "a hold invoice that fits the cap exactly is issued")
}

// An unpaid hold invoice is still payable, so it counts toward the cap like an
// ordinary invoice does.
func TestMakeHoldInvoice_CircleCap_UnpaidInvoiceCountsTowardCap(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	child := circleCapChild(t, svc, "hold-seq")

	require.Nil(t, handleHoldInvoice(t, svc, child.ID, 60_000).Error)
	resp := handleHoldInvoice(t, svc, child.ID, 60_000)
	require.NotNil(t, resp.Error)
	require.Equal(t, constants.ERROR_QUOTA_EXCEEDED, resp.Error.Code)
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
