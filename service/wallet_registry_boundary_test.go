package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Answering About a Destroyed Bill: every place that evaluates the
// retention boundary MUST agree (CASH-cash-status-20). The window is open
// through retained_until itself, as db.RetentionWindowOpen has it, so at that
// exact instant the bill must be both admitted to the registry and kept by the
// prune. One second later, both must let it go.
func TestSpentBillRegistry_BoundaryAgreesAtRetainedUntil(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	hub := db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCashHub}
	require.NoError(t, ts.DB.Create(&hub).Error)
	require.NoError(t, ts.DB.Create(&db.CashHubConfig{AppID: hub.ID, SpentRetentionSecs: 3600}).Error)
	retainedUntil := time.Now().UTC().Truncate(time.Second)
	pubkey := tests.RandomHex32()
	require.NoError(t, ts.DB.Create(&db.CashBillArchive{
		WalletAppID: 1, HubAppID: hub.ID, WalletPubkey: pubkey,
		MintedAt: retainedUntil.Add(-2 * time.Hour), EndedAt: retainedUntil.Add(-time.Hour),
		RetainedUntil: &retainedUntil, Outcome: db.CashBillOutcomeExpired, TotalMloki: 1000,
	}).Error)

	t.Cleanup(func() { registryNow = time.Now })
	at := func(now time.Time) (admitted, kept bool) {
		registryNow = func() time.Time { return now }
		svc := &service{db: ts.DB, walletRegistry: newWalletRegistry()}
		svc.walletRegistry.Add(pubkey)
		svc.PruneExpiredSpentBills()
		return assert.ObjectsAreEqual([]string{pubkey}, svc.retainedSpentBillPubkeys()), svc.walletRegistry.Has(pubkey)
	}

	admitted, kept := at(retainedUntil.Add(-time.Second))
	assert.True(t, admitted && kept, "inside the window")

	admitted, kept = at(retainedUntil)
	assert.True(t, admitted, "the registry gate must admit the bill at exactly retained_until")
	assert.True(t, kept, "the prune must keep the bill at exactly retained_until")

	admitted, kept = at(retainedUntil.Add(time.Second))
	assert.False(t, admitted, "past the window the registry gate lets it go")
	assert.False(t, kept, "past the window the prune lets it go")
}
