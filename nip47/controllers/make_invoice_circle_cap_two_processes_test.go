package controllers

// NIP-CW §Own Funds and Caps (CW-35, NIP-CASH.md:288-291): a circle's per-wallet
// receive cap and its hub-wide holdings ceiling must each admit a payment once,
// even when two Hub processes race make_invoice across their own connection pools
// on one database. The guarantee is the hub advisory lock (db.LockApp) plus the
// holdings/receive reads taken inside the locked transaction, so a second process
// with its own pool cannot see a stale pre-reservation balance. Postgres only
// (tests.SecondProcess skips off-postgres; db.LockApp is a no-op on sqlite).

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
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

type circleInvoiceReq struct {
	wallet uint
	amount uint64
}

// raceCircleInvoicesAcross issues every make_invoice at once, each through its
// own controller bound to one of the processes' pools (round-robin), and returns
// how many were issued without error.
func raceCircleInvoicesAcross(t *testing.T, procs []*tests.TestService, reqs []circleInvoiceReq) (int, []string) {
	t.Helper()
	type prepared struct {
		ctl    *nip47Controller
		req    *models.Request
		id     uint
		wallet uint
	}
	ps := make([]prepared, len(reqs))
	for i, r := range reqs {
		proc := procs[i%len(procs)]
		req := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeInvoiceRequestJSON(r.amount)), req))
		w := r.wallet
		ev := &db.RequestEvent{AppId: &w, NostrId: tests.RandomHex32()}
		require.NoError(t, proc.DB.Create(ev).Error)
		ps[i] = prepared{NewTestNip47Controller(proc), req, ev.ID, r.wallet}
	}
	ready := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	var codes []string
	for _, p := range ps {
		wg.Add(1)
		go func(p prepared) {
			defer wg.Done()
			<-ready
			p.ctl.HandleMakeInvoiceEvent(context.TODO(), p.req, p.id, p.wallet, func(r *models.Response, _ nostr.Tags) {
				mu.Lock()
				defer mu.Unlock()
				if r.Error == nil {
					ok++
					codes = append(codes, "OK")
				} else {
					codes = append(codes, r.Error.Code)
				}
			})
		}(p)
	}
	close(ready)
	wg.Wait()
	return ok, codes
}

// Two processes racing make_invoice of the full receive cap on one wallet issue
// at most one: the per-wallet cap holds across pools.
func TestMakeInvoice_CircleCap_TwoProcesses_ReceiveCap_AtMostOneIssued(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	other := tests.SecondProcess(t, svc) // skips off-postgres
	child := circleCapChild(t, svc, "cap-2p")

	const n = 8
	reqs := make([]circleInvoiceReq, n)
	for i := range reqs {
		reqs[i] = circleInvoiceReq{wallet: child.ID, amount: 100_000}
	}
	ok, codes := raceCircleInvoicesAcross(t, []*tests.TestService{svc, other}, reqs)
	require.LessOrEqual(t, ok, 1, "two processes issued %d invoices of 100_000 against a 100_000 receive cap: %v", ok, codes)
}

// Two processes racing make_invoice across sibling wallets of one hub, each for
// the hub's whole remaining holdings room, issue at most one: the hub-wide
// holdings ceiling holds across pools. The wallets' own receive caps are ample,
// so only the hub ceiling (CheckHoldings under the hub lock) can bind.
func TestMakeInvoice_CircleCap_TwoProcesses_HoldingsCeiling_AtMostOneIssued(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	other := tests.SecondProcess(t, svc)

	parent, _, err := svc.AppsService.CreateApp("ceil-2p-hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCircleHub, nil, "", nil)
	require.NoError(t, err)
	_, err = tests.AddCircleHubConfig(svc, parent.ID, func(c *db.CircleHubConfig) { c.HoldingsCeilingMloki = 100_000 })
	require.NoError(t, err)

	const n = 8
	reqs := make([]circleInvoiceReq, n)
	for i := range reqs {
		child, _, err := svc.AppsService.CreateApp(fmt.Sprintf("ceil-2p-w%d", i), "", 100, "never", nil,
			[]string{constants.MAKE_INVOICE_SCOPE, constants.PAY_INVOICE_SCOPE},
			db.AppKindCircleWallet, &parent.ID, db.ParentKindCircle, nil)
		require.NoError(t, err)
		require.NoError(t, tests.SetCircleMaxReceive(svc, child.ID, 100_000_000))
		reqs[i] = circleInvoiceReq{wallet: child.ID, amount: 100_000}
	}
	ok, codes := raceCircleInvoicesAcross(t, []*tests.TestService{svc, other}, reqs)
	require.LessOrEqual(t, ok, 1, "two processes issued %d invoices of 100_000 against a 100_000 hub holdings ceiling: %v", ok, codes)
}
