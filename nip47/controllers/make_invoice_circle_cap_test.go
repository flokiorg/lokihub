package controllers

// A circle wallet may not hold more than its MaxAmountLoki cap (lokihub policy;
// NIP-CW's max_amount is a spend cap). Production builds one controller per request,
// so the cap check must hold across controllers, and invoices still payable count.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func circleCapChild(t *testing.T, svc *tests.TestService, name string) *db.App {
	t.Helper()
	parent, _, err := svc.AppsService.CreateApp(name+"-hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)
	// cap = 100 loki = 100_000 mloki, balance 0
	child, _, err := svc.AppsService.CreateApp(name, "", 100, "never", nil,
		[]string{constants.MAKE_INVOICE_SCOPE, constants.PAY_INVOICE_SCOPE},
		db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	return child
}

// Concurrent make_invoice through separate controllers (the production shape):
// every request reads balance 0 and passes; nothing serializes them.
func TestMakeInvoice_CircleCap_ConcurrentRequests_AtMostOneIssued(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	child := circleCapChild(t, svc, "cap-conc")

	const n = 8
	ctls := make([]*nip47Controller, n)
	reqs := make([]*models.Request, n)
	ids := make([]uint, n)
	for i := 0; i < n; i++ {
		r := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeInvoiceRequestJSON(100_000)), r))
		reqs[i] = r
		ev := &db.RequestEvent{AppId: &child.ID, NostrId: tests.RandomHex32()}
		svc.DB.Create(&ev)
		ids[i] = ev.ID
		ctls[i] = NewTestNip47Controller(svc) // one per request, like production
	}
	ready := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	var codes []string
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-ready
			ctls[i].HandleMakeInvoiceEvent(ctx, reqs[i], ids[i], child.ID, func(r *models.Response, _ nostr.Tags) {
				mu.Lock()
				defer mu.Unlock()
				if r.Error == nil {
					ok++
					codes = append(codes, "OK")
				} else {
					codes = append(codes, r.Error.Code)
				}
			})
		}(i)
	}
	close(ready)
	wg.Wait()
	t.Logf("outcomes: %v", codes)
	if ok > 1 {
		t.Fatalf("%d concurrent invoices of 100_000 mloki issued against a 100_000 mloki cap (in-process guard is per-controller, production builds one per request)", ok)
	}
}

// Sequential make_invoice: even with the guard live (one controller, no overlap),
// unpaid invoices are invisible to GetIsolatedBalance (pending INCOMING is not
// counted, db/queries/get_isolated_balance.go:14), and settling an incoming
// payment re-checks nothing (transactions markTransactionSettled). So real received
// funds can exceed the cap without any concurrency at all.
func TestMakeInvoice_CircleCap_UnpaidInvoiceCountsTowardCap(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	child := circleCapChild(t, svc, "cap-seq")

	mock, isMock := svc.LNClient.(*tests.MockLn)
	require.True(t, isMock)
	for i := 0; i < 2; i++ {
		tx := *tests.MockLNClientTransaction
		tx.PaymentHash = fmt.Sprintf("%064x", 0xca9+i)
		tx.Amount = 100_000
		mock.MakeInvoiceQueue = append(mock.MakeInvoiceQueue, &tx)
	}

	ctl := NewTestNip47Controller(svc) // guard live: same controller, no overlap
	for i := 0; i < 2; i++ {
		r := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeInvoiceRequestJSON(100_000)), r))
		ev := &db.RequestEvent{AppId: &child.ID, NostrId: tests.RandomHex32()}
		svc.DB.Create(&ev)
		var resp *models.Response
		ctl.HandleMakeInvoiceEvent(ctx, r, ev.ID, child.ID, func(rr *models.Response, _ nostr.Tags) { resp = rr })
		require.NotNil(t, resp)
		if i == 0 {
			require.Nil(t, resp.Error, "the first invoice fits the cap exactly")
			continue
		}
		// The first invoice is still payable, so a second one would exceed the cap.
		require.NotNil(t, resp.Error, "an unpaid invoice counts toward the cap")
		require.Equal(t, constants.ERROR_QUOTA_EXCEEDED, resp.Error.Code)
		return
	}

	// Both invoices were issued. Simulate both being paid (an incoming settle carries no
	// cap check in lokihub, so the state transition is all that matters here).
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("app_id = ? AND type = ?", child.ID, constants.TRANSACTION_TYPE_INCOMING).
		Update("state", constants.TRANSACTION_STATE_SETTLED).Error)
	bal := queries.GetIsolatedBalance(svc.DB, child.ID)
	if bal > 100_000 {
		t.Fatalf("circle wallet holds %d mloki against a 100_000 mloki cap after two sequential make_invoice calls", bal)
	}
}

// A zero-amount invoice lets the payer choose the amount, so a capped circle
// wallet could receive any sum through one. Both ways to receive refuse it.
func TestMakeInvoice_CircleCap_ZeroAmountRefused(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	child := circleCapChild(t, svc, "cap-zero")

	r := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeInvoiceRequestJSON(0)), r))
	ev := &db.RequestEvent{AppId: &child.ID, NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var resp *models.Response
	NewTestNip47Controller(svc).HandleMakeInvoiceEvent(ctx, r, ev.ID, child.ID, func(rr *models.Response, _ nostr.Tags) { resp = rr })
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error, "a zero-amount invoice would let the payer pick any amount")
	require.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)

	hold := handleHoldInvoice(t, svc, child.ID, 0)
	require.NotNil(t, hold.Error)
	require.Equal(t, constants.ERROR_BAD_REQUEST, hold.Error.Code)
}

// A wallet with no circle parent may still issue zero-amount invoices.
func TestMakeInvoice_NonCircleApp_ZeroAmountAllowed(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	r := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeInvoiceRequestJSON(0)), r))
	ev := &db.RequestEvent{AppId: &app.ID, NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var resp *models.Response
	NewTestNip47Controller(svc).HandleMakeInvoiceEvent(ctx, r, ev.ID, app.ID, func(rr *models.Response, _ nostr.Tags) { resp = rr })
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
}
