package transactions

// NIP-CW §Own Funds and Caps: concurrent payments from one Circle Wallet never pass its balance or send cap.

import (
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

func raceKeysends(t *testing.T, ts *transactionsService, svc *tests.TestService, walletID uint, n int) (ok int, codes []string) {
	t.Helper()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ts.SendKeysend(10_000, "some-destination", nil, "", svc.LNClient, &walletID, nil)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
				return
			}
			if r, isRefusal := circlemoney.AsRefusal(err); isRefusal {
				codes = append(codes, r.Code)
			} else {
				codes = append(codes, err.Error())
			}
		}()
	}
	wg.Wait()
	return ok, codes
}

func TestCircleSpendRace_BalanceNeverOverspent(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCircleHub(t, svc, 0)
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	// One keysend of 10_000 costs at most 20_000 (reserve 10_000): funds one only.
	tests.FundApp(svc, wallet.ID, 25_000, tests.RandomHex32())
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)

	ok, codes := raceKeysends(t, ts, svc, wallet.ID, 6)
	assert.Equal(t, 1, ok, "refusals: %v", codes)
	for _, c := range codes {
		assert.Equal(t, constants.ERROR_INSUFFICIENT_BALANCE, c)
	}
	assert.GreaterOrEqual(t, queries.GetIsolatedBalance(svc.DB, wallet.ID), int64(0))
}

func TestCircleSpendRace_SendCapNeverPassed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCircleHub(t, svc, 0)
	// Send cap 40 loki = 40_000 mloki: exactly two payments at 20_000 full cost.
	wallet := newCircleWallet(t, svc, hub, 40, constants.BUDGET_RENEWAL_NEVER)
	tests.FundApp(svc, wallet.ID, 1_000_000, tests.RandomHex32())
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)

	ok, codes := raceKeysends(t, ts, svc, wallet.ID, 6)
	// Two fit while both are in flight; a third fits only once they settled at 10_000.
	assert.True(t, ok >= 2 && ok <= 3, "ok %d, refusals: %v", ok, codes)
	for _, c := range codes {
		assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, c)
	}
	start, _, err := circlemoney.Period(circlemoney.Anchor(wallet), constants.BUDGET_RENEWAL_NEVER, time.Now())
	require.NoError(t, err)
	used := circlemoney.SendUsage(svc.DB, wallet.ID, start.Unix())
	assert.Positive(t, used)
	assert.LessOrEqual(t, used, int64(40_000))
}

// NIP-CW §Recovering a Lost Connection: a freeze that lands after a request
// passed the gate still refuses its payment, checked under the payment's lock.
func TestCircleSend_FrozenUnderTheLockRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCircleHub(t, svc, 0)
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	tests.FundApp(svc, wallet.ID, 100_000, tests.RandomHex32())
	ts := NewTransactionsService(svc.DB, svc.EventPublisher)

	require.NoError(t, svc.DB.Model(wallet).Update("circle_frozen", true).Error)
	_, err = ts.SendKeysend(10_000, "02"+tests.RandomHex32(), nil, "", svc.LNClient, &wallet.ID, nil)
	r, isRefusal := circlemoney.AsRefusal(err)
	require.True(t, isRefusal, "%v", err)
	assert.Equal(t, nipcw.ErrFrozen, r.Code)
	assert.Equal(t, int64(100_000), queries.GetIsolatedBalance(svc.DB, wallet.ID), "nothing left the wallet")

	require.NoError(t, svc.DB.Model(wallet).Update("circle_frozen", false).Error)
	_, err = ts.SendKeysend(10_000, "02"+tests.RandomHex32(), nil, "", svc.LNClient, &wallet.ID, nil)
	require.NoError(t, err, "unfrozen, it pays")
}
