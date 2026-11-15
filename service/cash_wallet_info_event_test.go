package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/nip47"
	"github.com/flokiorg/lokihub/tests"
)

// infoPublishRecorder records which apps a kind-13194 info event was queued for.
type infoPublishRecorder struct {
	nip47.Nip47Service
	mu     sync.Mutex
	appIDs map[uint]int
}

func (r *infoPublishRecorder) EnqueueNip47InfoPublishRequest(appID uint, _, _, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appIDs[appID]++
}

func (r *infoPublishRecorder) queued(appID uint) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appIDs[appID]
}

// NIP-CASH §Lifecycle and Deletion → Info Events: a Cash Wallet MUST NOT publish
// a kind-13194 NIP-47 info event (CASH-lifecycle-and-deletion-17). The predicate
// is applied at three places, each of which alone would re-advertise every bill:
// creation, an update, and the startup republish. An ordinary app, which must
// be advertised, is the control at each.
func TestCashWallet_NeverQueuesAnInfoEvent(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	require.NoError(t, ts.Cfg.SetUpdate("Relay", "wss://relay.example", ""))

	hub := tests.CreateCashHub(t, ts, 100_000, 3600)
	bill, _, err := ts.AppsService.CreateApp("cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil)
	require.NoError(t, err)
	ordinary, _, err := ts.AppsService.CreateApp("ordinary", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, "", nil, "", nil)
	require.NoError(t, err)

	newService := func() (*service, *infoPublishRecorder) {
		recorder := &infoPublishRecorder{appIDs: map[uint]int{}}
		return &service{cfg: ts.Cfg, keys: ts.Keys, db: ts.DB, nip47Service: recorder, walletRegistry: newWalletRegistry()}, recorder
	}
	appEvent := func(name string, app *db.App) *events.Event {
		return &events.Event{Event: name, Properties: map[string]interface{}{"id": app.ID}}
	}

	t.Run("on creation", func(t *testing.T) {
		svc, recorder := newService()
		consumer := &createAppConsumer{svc: svc}
		consumer.ConsumeEvent(context.TODO(), appEvent("nwc_app_created", bill), nil)
		consumer.ConsumeEvent(context.TODO(), appEvent("nwc_app_created", ordinary), nil)
		assert.Zero(t, recorder.queued(bill.ID))
		assert.NotZero(t, recorder.queued(ordinary.ID))
	})

	t.Run("on update", func(t *testing.T) {
		svc, recorder := newService()
		consumer := &updateAppConsumer{svc: svc}
		consumer.ConsumeEvent(context.TODO(), appEvent("nwc_app_updated", bill), nil)
		consumer.ConsumeEvent(context.TODO(), appEvent("nwc_app_updated", ordinary), nil)
		assert.Zero(t, recorder.queued(bill.ID))
		assert.NotZero(t, recorder.queued(ordinary.ID))
	})

	t.Run("on startup", func(t *testing.T) {
		svc, recorder := newService()
		svc.publishAllAppInfoEvents()
		assert.Zero(t, recorder.queued(bill.ID))
		assert.NotZero(t, recorder.queued(ordinary.ID))
	})
}
