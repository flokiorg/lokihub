package cashwallet

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// Consolidate's fund movement (create merged wallet, N internal transfers from
// each source, saga-style compensating rollback on a mid-loop failure) is
// exercised end to end in the integration suite, where a real node issues the
// distinct signed invoices each internal transfer decodes — the in-process mock
// returns a single fixed invoice, so it can't stand in for many distinct
// transfers. The unit test here covers the input guard that fires before any
// funding, and the structural contract.

func TestConsolidate_RejectsFewerThanTwoSources(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// A single source (or none) is never a consolidation — the guard fires
	// before any wallet is created or any funds move.
	for _, sources := range [][]ConsolidateSource{
		nil,
		{{WalletApp: &db.App{}, AmountMloki: 1000}},
	} {
		_, _, err := Consolidate(context.TODO(), newTestDeps(svc), ConsolidateParams{
			HubApp:           hub,
			Sources:          sources,
			NewIdentityType:  db.CashIdentityPubkey,
			NewIdentityValue: newPk,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least two sources")
	}
}
