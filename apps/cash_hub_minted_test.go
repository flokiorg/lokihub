package apps_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// Value that has been split must be counted once, not twice.
//
// A full split turns one bill into another: the source slice goes terminal as
// 'split' and the carved bill carries the same value forward under its own
// slice. Both land in IssuedMloki, so issuance grows every time a recipient
// splits, without the hub minting anything. On this node's hub 2273 that
// reports 3,600,000 issued against 1,800,000 actually minted.
//
// The archive is what makes this answerable after the fact. A carved bill's
// app row is hard-deleted when it drains and its transactions cascade away
// with it, so neither the apps table nor the ledger can be asked which bills
// were minted — but CashBillArchive.SplitFromWalletAppID survives, and
// records exactly that.
func TestGetCashHubStats_MintedExcludesSplitChurn(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now().UTC()
	hub := newCashHub(t, svc, 1_000_000, 3600)
	day := now.AddDate(0, 0, -1)

	// One bill minted by the hub for 10,000, then fully split into a second
	// bill carrying the same 10,000 onward.
	source := db.CashBillArchive{
		WalletAppID: 800_001, HubAppID: hub.ID, WalletPubkey: randomHex32(),
		MintedAt: day, EndedAt: now, Outcome: db.CashBillOutcomeDrained,
		TotalMloki: 10_000, FundedMloki: 10_000,
	}
	carvedFrom := source.WalletAppID
	carved := db.CashBillArchive{
		WalletAppID: 800_002, HubAppID: hub.ID, WalletPubkey: randomHex32(),
		MintedAt: day, EndedAt: now, Outcome: db.CashBillOutcomeDrained,
		TotalMloki: 10_000, FundedMloki: 10_000,
		// The marker that says this bill was carved out of another rather
		// than minted by the hub.
		SplitFromWalletAppID: &carvedFrom,
	}
	require.NoError(t, svc.DB.Create(&source).Error)
	require.NoError(t, svc.DB.Create(&carved).Error)

	for _, a := range []db.CashBillSliceArchive{
		{WalletAppID: source.WalletAppID, AmountMloki: 10_000, Outcome: db.CashSliceStatusSplit, CreatedAt: day, ClaimedAt: &day, ArchivedAt: now},
		{WalletAppID: carved.WalletAppID, AmountMloki: 10_000, Outcome: db.CashSliceStatusRedeemed, CreatedAt: day, ClaimedAt: &day, SettledAt: &now, ArchivedAt: now},
	} {
		a.HubAppID = hub.ID
		a.ClaimID = 1
		a.IdentityType = db.CashIdentityPubkey
		a.IdentityValue = randomHex32()
		require.NoError(t, svc.DB.Create(&a).Error)
	}

	stats, err := svc.AppsService.GetCashHubStats(hub.ID, now)
	require.NoError(t, err)

	assert.EqualValues(t, 20_000, stats.IssuedMloki,
		"IssuedMloki is the slice-level total and legitimately counts both — it is what keeps the internal identity balanced")
	assert.EqualValues(t, 10_000, stats.SplitMloki, "the source slice went terminal as a split")

	assert.EqualValues(t, 10_000, stats.MintedMloki,
		"the hub minted 10,000 once; splitting it does not mint more")
}
