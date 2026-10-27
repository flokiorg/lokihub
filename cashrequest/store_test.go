package cashrequest_test

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/archive"
	"github.com/flokiorg/lokihub/tests"
)

func rid(n byte) string { return strings.Repeat(string("0123456789abcdef"[n%16]), 64) }

func newStore(t *testing.T) (*tests.TestService, cashrequest.Store, *time.Time) {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	now := time.Unix(1_800_000_000, 0)
	s := cashrequest.Store{DB: svc.DB, Now: func() time.Time { return now }}
	return svc, s, &now
}

// U-3.1: a request_id is marked once; a second mark is REQUEST_REUSED; a malformed
// one is refused before anything is stored.
func TestU_3_1_MarkUsedOnce(t *testing.T) {
	_, s, _ := newStore(t)
	used, err := s.IsUsed(rid(1))
	require.NoError(t, err)
	require.False(t, used)

	require.NoError(t, s.MarkUsed(rid(1), "mint_cash"))
	require.ErrorIs(t, s.MarkUsed(rid(1), "cash_transfer"), cashrequest.ErrRequestReused)
	used, err = s.IsUsed(rid(1))
	require.NoError(t, err)
	require.True(t, used)

	require.Error(t, s.MarkUsed("ABC", "mint_cash"))
}

// U-3.2: the mark is committed on its own, at authorization, so the call's commit
// transaction rolling back later leaves the id used (NIP-CASH §request_id).
func TestU_3_2_MarkSurvivesTheCallRollingBack(t *testing.T) {
	svc, s, now := newStore(t)
	require.NoError(t, s.MarkUsed(rid(2), "mint_cash"))
	err := svc.DB.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, s.CommitTx(tx, record(rid(2), now.Unix()+60), nil))
		return assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)
	used, err := s.IsUsed(rid(2))
	require.NoError(t, err)
	require.True(t, used)
	rec, err := s.Get(rid(2))
	require.NoError(t, err)
	require.Nil(t, rec, "the record rolled back with the call")
}

// U-3.3: concurrent marks of one id: exactly one wins.
func TestU_3_3_ConcurrentMarksOneWinner(t *testing.T) {
	_, s, _ := newStore(t)
	var wins, reused atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := s.MarkUsed(rid(3), "mint_cash"); {
			case err == nil:
				wins.Add(1)
			case err == cashrequest.ErrRequestReused:
				reused.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, wins.Load())
	require.EqualValues(t, 7, reused.Load())
}

func record(id string, deadline int64) *db.CashRequestRecord {
	return &db.CashRequestRecord{
		RequestID: id, Method: "mint_cash", HubAppID: 1,
		TargetWalletPubkey: strings.Repeat("aa", 32), TargetConnPubkey: strings.Repeat("bb", 32),
		AuthorizerKind: "client", Authorizer: strings.Repeat("bb", 32),
		Deadline: deadline, ResultMeta: "{}",
	}
}

// U-3.4: a call commits only up to its deadline; past it nothing is written.
func TestU_3_4_CommitOnlyBeforeTheDeadline(t *testing.T) {
	svc, s, now := newStore(t)
	err := svc.DB.Transaction(func(tx *gorm.DB) error {
		return s.CommitTx(tx, record(rid(4), now.Unix()-1), nil)
	})
	require.ErrorIs(t, err, cashrequest.ErrDeadlinePassed)
	got, err := s.Get(rid(4))
	require.NoError(t, err)
	require.Nil(t, got)

	_, err = cashrequest.RaiseHorizon(svc.DB, 300, *now)
	require.NoError(t, err)
	bills := []db.CashRequestRecordBill{
		{Role: db.CashRecordBillCarved, WalletAppID: 7, WalletPubkey: strings.Repeat("cc", 32), RelayHints: `["wss://r"]`, AmountMillis: 1000, Scheme: 1, Signature: strings.Repeat("dd", 65)},
		{Role: db.CashRecordBillRemainder, WalletAppID: 8, WalletPubkey: strings.Repeat("ee", 32), RelayHints: `["wss://r"]`, AmountMillis: 2000, Scheme: 1, Signature: strings.Repeat("ff", 65)},
	}
	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
		return s.CommitTx(tx, record(rid(5), now.Unix()), bills) // exactly at the deadline
	}))
	got, err = s.Get(rid(5))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, now.Unix(), got.CommittedAt)
	assert.Nil(t, got.DropAfter, "a record with live bills is kept")
	stored, err := s.Bills(got.ID)
	require.NoError(t, err)
	require.Len(t, stored, 2)
	assert.EqualValues(t, 0, stored[0].Ordinal)
	assert.Equal(t, db.CashRecordBillRemainder, stored[1].Role)

	// An in-place transfer creates no bill: droppable after deadline + H.
	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
		return s.CommitTx(tx, record(rid(6), now.Unix()+30), nil)
	}))
	got, err = s.Get(rid(6))
	require.NoError(t, err)
	require.NotNil(t, got.DropAfter)
	assert.Equal(t, now.Unix()+30+300+1, *got.DropAfter)
}

// U-3.6: no column of the record tables can hold a secret, a token or a zap target.
func TestU_3_6_RecordsHoldNoSecret(t *testing.T) {
	for _, m := range []any{db.CashRequestRecord{}, db.CashRequestRecordBill{}, db.CashUsedRequestID{}, db.CashConsumedProof{}} {
		typ := reflect.TypeOf(m)
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			if name == "zaptargethash" {
				continue
			}
			for _, banned := range []string{"secret", "token", "zaptarget", "preimage"} {
				assert.NotContains(t, name, banned, "%s.%s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

// U-3.7: the horizon is stored and never decreases.
func TestU_3_7_HorizonNeverDecreases(t *testing.T) {
	svc, _, now := newStore(t)
	h, err := cashrequest.RaiseHorizon(svc.DB, 300, *now)
	require.NoError(t, err)
	assert.EqualValues(t, 300, h)
	h, err = cashrequest.RaiseHorizon(svc.DB, 600, *now)
	require.NoError(t, err)
	assert.EqualValues(t, 600, h)
	h, err = cashrequest.RaiseHorizon(svc.DB, 120, *now)
	require.NoError(t, err)
	assert.EqualValues(t, 600, h, "a lower H is ignored")
	stored, err := cashrequest.HorizonTx(svc.DB)
	require.NoError(t, err)
	assert.EqualValues(t, 600, stored)

	// The configuration reports the stored H, whatever the env says.
	assert.EqualValues(t, 600, svc.Cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds)
}

// Raising H after a record's drop time was set keeps the record until
// deadline + the new H: a lookup in between decides with the new H, and must not
// answer unknown for a call that committed.
func TestRaisingTheHorizonDelaysRecordDrops(t *testing.T) {
	svc, s, now := newStore(t)
	_, err := cashrequest.RaiseHorizon(svc.DB, 300, *now)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
		return s.CommitTx(tx, record(rid(1), now.Unix()+30), nil)
	}))
	got, err := s.Get(rid(1))
	require.NoError(t, err)
	require.Equal(t, now.Unix()+30+300+1, *got.DropAfter)

	_, err = cashrequest.RaiseHorizon(svc.DB, 900, *now)
	require.NoError(t, err)
	got, err = s.Get(rid(1))
	require.NoError(t, err)
	assert.Equal(t, now.Unix()+30+900+1, *got.DropAfter)

	// Past the old drop time, before the new one: still there.
	*now = now.Add(30*time.Second + 301*time.Second)
	_, err = s.PruneRecords(100)
	require.NoError(t, err)
	got, err = s.Get(rid(1))
	require.NoError(t, err)
	assert.NotNil(t, got)
}

// U-3.8: consumed claim proofs are single-use, and pruned an hour after their
// created_at. A redeem that did not happen releases its proof with its claim, in
// one transaction (apps.ReleaseRedeemClaim); nothing releases one on its own.
func TestU_3_8_ConsumedProofs(t *testing.T) {
	svc, s, now := newStore(t)
	ev := strings.Repeat("ab", 32)
	used, err := s.IsProofConsumed(ev)
	require.NoError(t, err)
	assert.False(t, used)
	require.NoError(t, s.ConsumeProofTx(svc.DB, ev, now.Unix(), "cash_transfer"))
	require.ErrorIs(t, s.ConsumeProofTx(svc.DB, ev, now.Unix(), "cash_redeem"), cashrequest.ErrProofConsumed)
	used, err = s.IsProofConsumed(ev)
	require.NoError(t, err)
	assert.True(t, used)

	later := cashrequest.Store{DB: svc.DB, Now: func() time.Time { return now.Add(cashrequest.ProofRetentionSecs * time.Second) }}
	n, err := later.PruneProofs()
	require.NoError(t, err)
	assert.Zero(t, n, "kept until an hour past created_at")
	later.Now = func() time.Time { return now.Add((cashrequest.ProofRetentionSecs + 1) * time.Second) }
	n, err = later.PruneProofs()
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

// U-3.9: once every bill a record created is archived, the record may be dropped at
// max(deadline + H + 1, latest retained_until + 1), and not before; the used mark
// stays forever, and records another component owns are left alone.
func TestU_3_9_RecordRetentionFollowsItsBills(t *testing.T) {
	svc, s, now := newStore(t)
	_, err := cashrequest.RaiseHorizon(svc.DB, 300, *now)
	require.NoError(t, err)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	require.NoError(t, svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", hub.ID).Update("spent_retention_secs", 86400).Error)
	bill := func(name string) *db.App {
		app, _, err := svc.AppsService.CreateApp(name, "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
			[]string{constants.CASH_REDEEM_SCOPE}, db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil)
		require.NoError(t, err)
		return app
	}
	a, b := bill("a"), bill("b")
	require.NoError(t, s.MarkUsed(rid(9), "cash_transfer"))
	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
		return s.CommitTx(tx, record(rid(9), now.Unix()+60), []db.CashRequestRecordBill{
			{Role: db.CashRecordBillCarved, WalletAppID: a.ID, WalletPubkey: *a.WalletPubkey, RelayHints: "[]", Signature: "x"},
			{Role: db.CashRecordBillRemainder, WalletAppID: b.ID, WalletPubkey: *b.WalletPubkey, RelayHints: "[]", Signature: "x"},
		})
	}))

	archiveBill := func(app *db.App, at time.Time) {
		require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
			return archive.ArchiveAndDeleteCashBillTx(tx, app, db.CashBillOutcomeDrained, 0, at)
		}))
	}
	archiveBill(a, *now)
	rec, err := s.Get(rid(9))
	require.NoError(t, err)
	assert.Nil(t, rec.DropAfter, "a live bill keeps the record")

	endedB := now.Add(10 * time.Minute)
	archiveBill(b, endedB)
	rec, err = s.Get(rid(9))
	require.NoError(t, err)
	require.NotNil(t, rec.DropAfter)
	assert.Equal(t, endedB.Add(86400*time.Second).Unix()+1, *rec.DropAfter, "the latest tombstone window wins over deadline + H")

	at := func(unix int64) cashrequest.Store {
		return cashrequest.Store{DB: svc.DB, Now: func() time.Time { return time.Unix(unix, 0) }}
	}
	n, err := at(*rec.DropAfter - 1).PruneRecords(500)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = at(*rec.DropAfter).PruneRecords(500)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	used, err := s.IsUsed(rid(9))
	require.NoError(t, err)
	assert.True(t, used, "the used mark is never pruned")

	// A cash_order record is NIP-CO's to retire.
	order := record(rid(10), now.Unix()+60)
	order.Method = "cash_order"
	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error { return s.CommitTx(tx, order, nil) }))
	n, err = at(now.Unix() + 100000).PruneRecords(500)
	require.NoError(t, err)
	assert.Zero(t, n)
}
