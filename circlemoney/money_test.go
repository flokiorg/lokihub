package circlemoney_test

import (
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip47"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

type circleFixture struct {
	svc *tests.TestService
	hub *db.App
	cfg *db.CircleHubConfig
}

func newCircle(t *testing.T, mutate ...func(*db.CircleHubConfig)) *circleFixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	return addCircle(t, svc, mutate...)
}

func addCircle(t *testing.T, svc *tests.TestService, mutate ...func(*db.CircleHubConfig)) *circleFixture {
	t.Helper()
	hub := &db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(hub).Error)
	cfg, err := tests.AddCircleHubConfig(svc, hub.ID, mutate...)
	require.NoError(t, err)
	return &circleFixture{svc: svc, hub: hub, cfg: cfg}
}

// wallet is a member wallet with the given caps (mloki) and renewal, created
// at anchor and funded with balance.
func (c *circleFixture) wallet(t *testing.T, maxAmount, maxReceive int64, renewal string, anchor time.Time, balance uint64) *db.App {
	t.Helper()
	at := anchor.Unix()
	w := &db.App{Name: "w", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleWallet, ParentAppID: &c.hub.ID,
		ParentKind: db.ParentKindCircle, CircleMaxReceiveMloki: &maxReceive, CircleBudgetAnchorAt: &at}
	require.NoError(t, c.svc.DB.Create(w).Error)
	require.NoError(t, c.svc.DB.Create(&db.AppPermission{AppId: w.ID, Scope: constants.PAY_INVOICE_SCOPE,
		MaxAmountLoki: int(maxAmount / 1000), BudgetRenewal: renewal}).Error)
	if balance > 0 {
		tests.FundApp(c.svc, w.ID, balance, tests.RandomHex32())
	}
	return w
}

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	r, ok := circlemoney.AsRefusal(err)
	require.True(t, ok, "%v is not a refusal", err)
	return r.Code
}

// U-15.1: the four kinds, from the wallet's side.
func TestKind_Table(t *testing.T) {
	hubA, hubB := uint(1), uint(2)
	a1 := &db.App{ID: 10, Kind: db.AppKindCircleWallet, ParentAppID: &hubA}
	a2 := &db.App{ID: 11, Kind: db.AppKindCircleWallet, ParentAppID: &hubA}
	b1 := &db.App{ID: 12, Kind: db.AppKindCircleWallet, ParentAppID: &hubB}
	bill := &db.App{ID: 13, Kind: db.AppKindCashWallet, ParentAppID: &hubB}
	hub := &db.App{ID: 1, Kind: db.AppKindCircleHub}

	assert.Equal(t, circlemoney.KindSameCircle, circlemoney.Kind(a1, a2, true))
	assert.Equal(t, circlemoney.KindOtherCircle, circlemoney.Kind(a1, b1, true))
	assert.Equal(t, circlemoney.KindNode, circlemoney.Kind(a1, bill, true), "a cash bill")
	assert.Equal(t, circlemoney.KindNode, circlemoney.Kind(a1, hub, true), "a Hub's own connection")
	assert.Equal(t, circlemoney.KindNode, circlemoney.Kind(a1, nil, true), "the host's own wallet")
	assert.Equal(t, circlemoney.KindLightning, circlemoney.Kind(a1, a2, false), "off the node")
	assert.False(t, circlemoney.External(circlemoney.KindSameCircle))
	assert.False(t, circlemoney.External(circlemoney.KindOtherCircle))
	assert.True(t, circlemoney.External(circlemoney.KindNode))
	assert.True(t, circlemoney.External(circlemoney.KindLightning))
}

// U-15.8: the circle reserve rounds down, where the generic one rounds up.
func TestFeeReserve_FloorNotCeil(t *testing.T) {
	r := nip47.FeeReserve{Ppm: 10_000, MinMillis: 0}
	assert.Equal(t, uint64(1_234), nipcw.RoutingFeeReserve(123_456, r))
	assert.Equal(t, uint64(10_000), nipcw.RoutingFeeReserve(123_456, nip47.FeeReserve{Ppm: 10_000, MinMillis: 10_000}), "at least min_millis")
	assert.Equal(t, uint64(10_000), transactions.CalculateFeeReserveMloki(123_456), "the generic reserve stays for other apps")
	assert.Equal(t, uint64(1_234_568), transactions.CalculateFeeReserveMloki(123_456_789), "rounded up")
	assert.Equal(t, uint64(1_234_567), nipcw.RoutingFeeReserve(123_456_789, r), "rounded down")
}

// U-15.9: fees_ppm and the reserve apply to Lightning only.
func TestCheckSend_FeesOnlyOnLightning(t *testing.T) {
	c := newCircle(t, func(cfg *db.CircleHubConfig) { cfg.FeesPpm = 10_000 })
	now := time.Now()
	w := c.wallet(t, 10_000_000, 10_000_000, constants.BUDGET_RENEWAL_NEVER, now, 1_000_000)
	other := c.wallet(t, 10_000_000, 10_000_000, constants.BUDGET_RENEWAL_NEVER, now, 0)

	q, err := circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, SameNode: false, Amount: 100_000, Now: now, Active: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, circlemoney.KindLightning, q.Kind)
	assert.Equal(t, uint64(1_000), q.FeeSkim)
	assert.Equal(t, uint64(10_000), q.FeeReserve)
	assert.Equal(t, uint64(111_000), q.FullCost)
	assert.True(t, q.CountsSend)

	for _, payee := range []*db.App{other, nil} {
		q, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, Payee: payee, SameNode: true, Amount: 100_000, Now: now, Active: true}, nil)
		require.NoError(t, err)
		assert.Zero(t, q.FeeSkim)
		assert.Zero(t, q.FeeReserve)
		assert.Equal(t, uint64(100_000), q.FullCost)
	}
	q, _ = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, Payee: other, SameNode: true, Amount: 100_000, Now: now, Active: true}, nil)
	assert.False(t, q.CountsSend, "a same-circle transfer counts on no cap")
	assert.Zero(t, q.OutboundPeriodStart)
}

// writeSend stamps a pending or settled send as the payment path does.
func writeSend(t *testing.T, c *circleFixture, w *db.App, q circlemoney.SendQuote, amount uint64, state string) {
	t.Helper()
	require.NoError(t, c.svc.DB.Create(&db.Transaction{
		AppId: &w.ID, Type: constants.TRANSACTION_TYPE_OUTGOING, State: state, AmountMloki: amount,
		FeeSkimMloki: q.FeeSkim, FeeReserveMloki: q.FeeReserve, PaymentHash: tests.RandomHex32(),
		PaymentKind: q.Kind, CountsSend: q.CountsSend, PeriodStart: q.PeriodStart, OutboundPeriodStart: q.OutboundPeriodStart,
	}).Error)
}

// U-15.2: the send cap counts external sends in the current period, pending at
// the most they may cost; the next period has the cap in full again.
func TestCheckSend_SendCapPerPeriod(t *testing.T) {
	c := newCircle(t)
	anchor := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	now := anchor.Add(time.Hour)
	w := c.wallet(t, 100_000, 100_000, constants.BUDGET_RENEWAL_MONTHLY, anchor, 10_000_000)

	q, err := circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, SameNode: true, Amount: 60_000, Now: now, Active: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, anchor.Unix(), q.PeriodStart)
	writeSend(t, c, w, q, 60_000, constants.TRANSACTION_STATE_PENDING)

	_, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, SameNode: true, Amount: 40_001, Now: now, Active: true}, nil)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, refusalCode(t, err))
	_, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, SameNode: true, Amount: 40_000, Now: now, Active: true}, nil)
	require.NoError(t, err, "exactly at the cap")

	// 2026-02-28 10:00Z starts the next monthly period (clamped).
	next := time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC)
	q, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, SameNode: true, Amount: 100_000, Now: next, Active: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, next.Unix(), q.PeriodStart)
}

// U-15.13: a wallet that is not Active pays out off its send cap, but its
// payouts count on the circle's outbound ceiling (no exemption).
func TestCheckSend_NotActiveCountsOnOutboundOnly(t *testing.T) {
	c := newCircle(t, func(cfg *db.CircleHubConfig) { cfg.OutboundCeilingMloki = 150_000 })
	now := time.Now()
	w := c.wallet(t, 50_000, 50_000, constants.BUDGET_RENEWAL_NEVER, now, 10_000_000)

	q, err := circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, SameNode: true, Amount: 100_000, Now: now, Active: false}, nil)
	require.NoError(t, err, "over the send cap, but not Active")
	assert.False(t, q.CountsSend)
	assert.NotZero(t, q.OutboundPeriodStart)
	writeSend(t, c, w, q, 100_000, constants.TRANSACTION_STATE_SETTLED)

	_, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, SameNode: true, Amount: 50_001, Now: now, Active: false}, nil)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, refusalCode(t, err), "the outbound ceiling")
	_, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, SameNode: true, Amount: 50_000, Now: now, Active: false}, nil)
	require.NoError(t, err)
}

// The full cost against the balance, the reserve included.
func TestCheckSend_InsufficientBalance(t *testing.T) {
	c := newCircle(t)
	now := time.Now()
	w := c.wallet(t, 10_000_000, 10_000_000, constants.BUDGET_RENEWAL_NEVER, now, 109_999)
	_, err := circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, Amount: 100_000, Now: now, Active: true}, nil)
	assert.Equal(t, constants.ERROR_INSUFFICIENT_BALANCE, refusalCode(t, err))
}

// The pending and in-flight bounds, and the same-circle transfer rate.
func TestCheckSend_RateLimits(t *testing.T) {
	c := newCircle(t)
	now := time.Now()
	w := c.wallet(t, 1_000_000_000, 1_000_000_000, constants.BUDGET_RENEWAL_NEVER, now, 100_000_000)
	other := c.wallet(t, 1_000_000_000, 1_000_000_000, constants.BUDGET_RENEWAL_NEVER, now, 0)

	for range circlemoney.MaxInflightExternal {
		q, err := circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, Amount: 1_000, Now: now, Active: true}, nil)
		require.NoError(t, err)
		writeSend(t, c, w, q, 1_000, constants.TRANSACTION_STATE_PENDING)
	}
	_, err := circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, Amount: 1_000, Now: now, Active: true}, nil)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, refusalCode(t, err), "external payments in flight")

	limiter := circlemoney.NewLimiter()
	for range circlemoney.SameCircleTransfersPerMinute {
		_, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, Payee: other, SameNode: true, Amount: 1_000, Now: now, Active: true}, limiter)
		require.NoError(t, err, "in-circle transfers are not external")
	}
	_, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, Payee: other, SameNode: true, Amount: 1_000, Now: now, Active: true}, limiter)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, refusalCode(t, err), "same-circle transfer rate")
	_, err = circlemoney.CheckSend(c.svc.DB, circlemoney.SendInput{Wallet: w, Payee: other, SameNode: true, Amount: 1_000, Now: now.Add(time.Minute), Active: true}, limiter)
	require.NoError(t, err, "a minute later")
}

// U-15.3: receive room is max_receive less this period's external receipts and
// every open invoice, held payments included; kind 1-2 receipts are no receipt.
func TestReceiveRoom_ReceiptsAndOpenInvoices(t *testing.T) {
	c := newCircle(t)
	now := time.Now()
	w := c.wallet(t, 1_000_000, 1_000_000, constants.BUDGET_RENEWAL_NEVER, now.Add(-time.Hour), 0)
	start := now.Add(-time.Hour).Unix()

	future, past := now.Add(time.Hour), now.Add(-time.Minute)
	rows := []db.Transaction{
		{State: constants.TRANSACTION_STATE_SETTLED, AmountMloki: 100_000, PaymentKind: circlemoney.KindLightning, PeriodStart: start},
		{State: constants.TRANSACTION_STATE_SETTLED, AmountMloki: 50_000, PaymentKind: circlemoney.KindNode, PeriodStart: start},
		{State: constants.TRANSACTION_STATE_SETTLED, AmountMloki: 70_000, PaymentKind: circlemoney.KindSameCircle, PeriodStart: start},
		{State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 200_000, ExpiresAt: &future},
		{State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 300_000, ExpiresAt: &past},
		{State: constants.TRANSACTION_STATE_ACCEPTED, AmountMloki: 25_000, ExpiresAt: &past, Hold: true},
	}
	for i := range rows {
		rows[i].AppId, rows[i].Type, rows[i].PaymentHash = &w.ID, constants.TRANSACTION_TYPE_INCOMING, tests.RandomHex32()
		require.NoError(t, c.svc.DB.Create(&rows[i]).Error)
	}
	room, err := circlemoney.ReceiveRoom(c.svc.DB, w, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1_000_000-100_000-50_000-200_000-25_000), room)
}

// U-15.5: holdings are the Active wallets' balances plus their open invoices.
func TestHoldings_BalancesAndOpenInvoices(t *testing.T) {
	c := newCircle(t, func(cfg *db.CircleHubConfig) { cfg.HoldingsCeilingMloki = 1_000_000 })
	now := time.Now()
	w1 := c.wallet(t, 1_000_000, 1_000_000, constants.BUDGET_RENEWAL_NEVER, now, 300_000)
	c.wallet(t, 1_000_000, 1_000_000, constants.BUDGET_RENEWAL_NEVER, now, 200_000)
	other := addCircle(t, c.svc)
	other.wallet(t, 1_000_000, 1_000_000, constants.BUDGET_RENEWAL_NEVER, now, 999_000)
	future := now.Add(time.Hour)
	require.NoError(t, c.svc.DB.Create(&db.Transaction{AppId: &w1.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
		State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 100_000, ExpiresAt: &future, PaymentHash: tests.RandomHex32()}).Error)

	holdings, err := circlemoney.Holdings(c.svc.DB, c.hub.ID, now)
	require.NoError(t, err)
	assert.Equal(t, int64(600_000), holdings)
	available, err := circlemoney.Available(c.svc.DB, c.hub.ID, c.cfg, now)
	require.NoError(t, err)
	assert.Equal(t, int64(400_000), available)

	assert.NoError(t, circlemoney.CheckHoldings(c.svc.DB, c.hub.ID, c.cfg, 400_000, now), "up to the ceiling")
	assert.Equal(t, "INSUFFICIENT_CAPACITY", refusalCode(t, circlemoney.CheckHoldings(c.svc.DB, c.hub.ID, c.cfg, 400_001, now)))
}

// A keysend credit: the receive room when external, the holdings ceiling unless
// it moves money between two Active wallets of the circle.
func TestCheckCredit(t *testing.T) {
	c := newCircle(t, func(cfg *db.CircleHubConfig) { cfg.HoldingsCeilingMloki = 500_000 })
	now := time.Now()
	w := c.wallet(t, 1_000_000, 100_000, constants.BUDGET_RENEWAL_NEVER, now, 0)
	peer := c.wallet(t, 1_000_000, 1_000_000, constants.BUDGET_RENEWAL_NEVER, now, 450_000)

	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, refusalCode(t,
		circlemoney.CheckCredit(c.svc.DB, circlemoney.CreditInput{Wallet: w, Amount: 100_001, Now: now})), "over the receive room")
	assert.Equal(t, "INSUFFICIENT_CAPACITY", refusalCode(t,
		circlemoney.CheckCredit(c.svc.DB, circlemoney.CreditInput{Wallet: w, Amount: 60_000, Now: now})), "over the holdings ceiling")
	assert.NoError(t, circlemoney.CheckCredit(c.svc.DB, circlemoney.CreditInput{Wallet: w, Amount: 50_000, Now: now}))
	assert.NoError(t, circlemoney.CheckCredit(c.svc.DB, circlemoney.CreditInput{Wallet: w, Payer: peer, SameNode: true, PayerActive: true, Amount: 400_000, Now: now}),
		"between two Active wallets of the circle: no cap, no ceiling")
	assert.Equal(t, "INSUFFICIENT_CAPACITY", refusalCode(t,
		circlemoney.CheckCredit(c.svc.DB, circlemoney.CreditInput{Wallet: w, Payer: peer, SameNode: true, PayerActive: false, Amount: 60_000, Now: now})),
		"from a wallet that is not Active it raises the holdings")
}

// U-15.10: periods are counted in UTC whatever the server's time zone.
func TestPeriod_ServerTimeZoneIrrelevant(t *testing.T) {
	auckland, err := time.LoadLocation("Pacific/Auckland")
	if err != nil {
		t.Skip("no zoneinfo")
	}
	anchor := time.Date(2024, 1, 31, 10, 0, 0, 0, time.UTC)
	now := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
	utcStart, utcNext, err := circlemoney.Period(anchor, constants.BUDGET_RENEWAL_MONTHLY, now)
	require.NoError(t, err)
	nzStart, nzNext, err := circlemoney.Period(anchor.In(auckland), constants.BUDGET_RENEWAL_MONTHLY, now.In(auckland))
	require.NoError(t, err)
	assert.Equal(t, utcStart.Unix(), nzStart.Unix())
	assert.Equal(t, utcNext.Unix(), nzNext.Unix())
	assert.Equal(t, time.Date(2024, 2, 29, 10, 0, 0, 0, time.UTC), utcStart)
	assert.Equal(t, time.Date(2024, 3, 31, 10, 0, 0, 0, time.UTC), utcNext)
}
