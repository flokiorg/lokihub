package nip47

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditB_RetentionBoundary_FourSitesDisagreeAtRetainedUntil
//
// NIP-CASH §Answering About a Destroyed Bill: "The window is inclusive of
// retained_until itself... An implementation that evaluates this boundary in more than
// one place MUST make those places agree: a Hub that admits a request at retained_until
// and then declines to answer it produces silence, which is precisely the indeterminate
// outcome this whole mechanism exists to remove."
//
// LIMITATION, stated because it bounds what a pass here means: sites 2 and 3 below
// TRANSCRIBE the production queries rather than calling them — they live in unexported
// service-package functions this test cannot reach. So this test proves the three
// comparisons AGREE as written, not that the transcription still matches production. If
// service/wallet_registry.go's operators are changed without changing these, this test
// will keep passing against a copy. The durable fix is one helper producing the
// comparison for all three sites; until then, treat these two queries as a mirror that
// must be updated in the same commit as the originals.
//
// db.RetentionWindowOpen calls itself "the ONE definition". It is not: two raw SQL
// comparisons in service/wallet_registry.go evaluate the same boundary and disagree with
// it at exactly retained_until. This test pins all three against one archive row whose
// deadline IS "now".
//
// The SQL here is copied verbatim from those two call sites, so if either is rewritten
// this test stops describing them — deliberate, since the point is the duplication.
func TestAuditB_RetentionBoundary_FourSitesDisagreeAtRetainedUntil(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// Retention must be enabled for the join in SpentBillRetainedUntil to match.
	require.NoError(t, svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", hub.ID).
		Update("spent_retention_secs", 15*24*3600).Error)

	deadline := time.Now().Truncate(time.Second)
	require.NoError(t, svc.DB.Create(&db.CashBillArchive{
		WalletAppID: 4242, HubAppID: hub.ID,
		WalletPubkey:  "aa" + tests.RandomHex32()[2:],
		EndedAt:       deadline.Add(-15 * 24 * time.Hour),
		RetainedUntil: &deadline,
	}).Error)

	now := deadline // a request landing at exactly retained_until

	// Site 1 — the declared single definition, and the spec's rule.
	site1 := db.RetentionWindowOpen(deadline, now)

	// Site 2 — service/wallet_registry.go:233, retainedSpentBillPubkeys (startup
	// rebuild and the retention-changed resync): strict >.
	var reloaded []string
	require.NoError(t, svc.DB.Table("cash_bill_archives").
		Joins("JOIN cash_hub_configs ON cash_hub_configs.app_id = cash_bill_archives.hub_app_id").
		Where("cash_hub_configs.spent_retention_secs > 0").
		Where("cash_bill_archives.retained_until >= ?", now).
		Pluck("cash_bill_archives.wallet_pubkey", &reloaded).Error)
	site2 := len(reloaded) == 1

	// Site 3 — service/wallet_registry.go:263, PruneExpiredSpentBills: <= prunes AT the
	// deadline, so the relay gate closes while site 1 still answers.
	var pruned []string
	require.NoError(t, svc.DB.Table("cash_bill_archives").
		Where("retained_until IS NOT NULL AND retained_until < ?", now).
		Pluck("wallet_pubkey", &pruned).Error)
	site3Keeps := len(pruned) == 0

	t.Logf("at exactly retained_until: RetentionWindowOpen=%v  registryReloadRegisters=%v  sweepKeepsRegistered=%v",
		site1, site2, site3Keeps)

	require.True(t, site1, "spec MUST: the window is inclusive of retained_until")
	if site1 != site2 || site1 != site3Keeps {
		t.Fatalf("AUDITB BUG PRESENT: the retention boundary is evaluated in three places and they "+
			"disagree at retained_until (open=%v, reload-registers=%v, sweep-keeps=%v); the relay gate "+
			"closes at an instant the replier is still contractually answering, producing silence",
			site1, site2, site3Keeps)
	}
}
