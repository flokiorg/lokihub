package nip47

import (
	"context"
	"sync"
	"testing"

	"github.com/ohstr/nmilat/nipCO"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// secondEngine is f's order engine as a second Hub process runs it: the same
// node and clock, the same database through its own pool (tests.SecondProcess).
// Its mint lock is its own, as a second process's would be.
func (f *intakeFixture) secondEngine(t *testing.T) *cashorder.Engine {
	t.Helper()
	other := tests.SecondProcess(t, f.svc)
	return &cashorder.Engine{DB: other.DB, Keys: other.Keys, Apps: other.AppsService,
		Transactions: transactions.NewTransactionsService(other.DB, other.EventPublisher),
		LN:           f.engine.LN, Relays: f.engine.Relays, Horizon: f.engine.Horizon, Now: f.engine.Now}
}

// raceAcross runs n calls of run at once, alternating between the engines.
func raceAcross(engines []*cashorder.Engine, n int, run func(e *cashorder.Engine)) {
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(e *cashorder.Engine) {
			defer wg.Done()
			<-ready
			run(e)
		}(engines[i%len(engines)])
	}
	close(ready)
	wg.Wait()
}

// NIP-CO §Minting: each payment is minted exactly once across two Hub processes
// sharing one postgres database, however concurrently each takes it in and
// mints it — a Lightning settlement (ln:<hash>) and an on-chain output
// (oc:<txid>:<vout>) alike. Postgres only.
func TestOrderIntake_TwoProcesses_PaymentMintedOnce(t *testing.T) {
	f := newIntakeFixture(t)
	engines := []*cashorder.Engine{f.engine, f.secondEngine(t)}
	const rounds, perRound = 5, 8

	var requestIDs []string
	for range rounds {
		res, _, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
		orderOf(t, res)
		o := f.orderRow(t, p.RequestID)
		settled := f.clock.Unix()
		raceAcross(engines, perRound, func(e *cashorder.Engine) {
			e.IntakeLightning(context.TODO(), &lnclient.Transaction{PaymentHash: *o.PaymentHash, Amount: o.PayMillis,
				AmountPaid: o.PayMillis, SettledAt: &settled})
		})
		requestIDs = append(requestIDs, p.RequestID)
	}

	res, _, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	placed := orderOf(t, res)
	f.chain.outputs = []lnclient.OnchainOutput{{TxID: tests.RandomHex32(), Vout: 1, Address: placed.Address,
		AmountLoki: 43, Confirmations: 1}}
	raceAcross(engines, perRound, func(e *cashorder.Engine) { _ = e.ScanChain(context.TODO()) })
	requestIDs = append(requestIDs, p.RequestID)

	// Every payment minted again by both processes, at once, mints nothing more.
	var paymentIDs []uint
	require.NoError(t, f.svc.DB.Model(&db.CashOrderPayment{}).Pluck("id", &paymentIDs).Error)
	for _, id := range paymentIDs {
		raceAcross(engines, 4, func(e *cashorder.Engine) { assert.NoError(t, e.MintPayment(context.TODO(), id)) })
	}

	for _, rid := range requestIDs {
		o := f.orderRow(t, rid)
		var payments int64
		require.NoError(t, f.svc.DB.Model(&db.CashOrderPayment{}).Where("order_id = ?", o.ID).Count(&payments).Error)
		assert.EqualValues(t, 1, payments, "order %s: one payment row", rid)
		assert.Equal(t, db.CashOrderMinted, o.State, "order %s", rid)
	}
	var payments, claims int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrderPayment{}).Count(&payments).Error)
	require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Count(&claims).Error)
	assert.EqualValues(t, len(requestIDs), payments, "one payment per order")
	assert.EqualValues(t, len(requestIDs), claims, "one bill per paid order")
}
