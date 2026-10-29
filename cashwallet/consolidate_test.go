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

// Consolidate's commit (every source claim, the merged bill and its funding rows
// in one transaction) is covered with the other commits in commit_test.go. The
// test here covers the input guard that fires before anything is written.

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
		_, err := Consolidate(context.TODO(), newTestDeps(svc), ConsolidateParams{
			HubApp:           hub,
			Sources:          sources,
			NewIdentityType:  db.CashIdentityPubkey,
			NewIdentityValue: newPk,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least two sources")
	}
}
