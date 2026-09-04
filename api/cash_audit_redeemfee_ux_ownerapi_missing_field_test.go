package api

// UX-audit coverage (data/docs/audits/cash-hub-redeem-fee-2026-08-02/) for the
// HUB OWNER's own side of the new redeem-fee feature. NIP-CASH.md's
// §The Redeem Fee promises a slice's redeem_fee_ppm is "fixed at the moment
// it's created... and does not change afterward, even if the Hub's own
// default rate later changes" — and the Hub Settings UI copy the owner sees
// while editing an existing hub echoes exactly that ("Changes only apply to
// Lokicash issued from now on and won't affect ones already issued" —
// frontend/src/i18n/locales/en/apps.json's circleHub.cashHubSettingsDescription).
//
// That's a strong claim for the owner to just take on faith. This test
// checks whether the owner's own admin API — api.ListCashWalletClaims, the
// data source for CashHubAllocations.tsx's per-recipient table — actually
// lets them VERIFY it, the same way it already lets them verify the
// analogous min_transfer_mloki claim (CashWalletClaimResponse.
// MinTransferMloki, api/models.go:454, populated at api/api.go:3042 and
// :3323). FINDING (UX M1, FIXED): RedeemFeePpm was never added to
// CashWalletClaimResponse at all — an owner who changed their Hub's default
// fee had no way, via this API or the admin frontend it feeds, to see what
// rate any specific already-issued lokicash actually locked in.
// CashWalletClaimResponse.RedeemFeePpm now mirrors MinTransferMloki's own
// treatment exactly, at both call sites — this test now confirms the fix.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestListCashWalletClaims_RedeemFeePpm_PresentInOwnerFacingResponse(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 10)
	pk := tests.RandomHex32()

	// A concrete, checkable, nonzero rate AND a concrete, checkable
	// min_transfer_mloki floor on the SAME claim, so this test can show one
	// survives to the owner-facing wire response and the other doesn't — not
	// just that a coincidentally-zero field is missing.
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{
			IdentityType:     db.CashIdentityPubkey,
			IdentityValue:    pk,
			AmountMloki:      3000,
			MinTransferMloki: 500,
			RedeemFeePpm:     100_000, // 10% — locked into this one slice forever
		},
	}))

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 1)

	assert.Equal(t, int64(500), result[0].MinTransferMloki)
	assert.Equal(t, 100_000, result[0].RedeemFeePpm,
		"the owner-facing response now carries this specific slice's own locked-in redeem fee rate")

	raw, err := json.Marshal(result[0])
	require.NoError(t, err)
	rawStr := string(raw)
	t.Logf("actual ListCashWalletClaims (owner-facing) wire response for a claim with a real 10%% redeem fee: %s", rawStr)

	assert.Contains(t, rawStr, `"min_transfer_mloki":500`)
	assert.Contains(t, rawStr, `"redeem_fee_ppm":100000`,
		"an owner can now confirm, after changing the Hub's default, what rate a specific already-issued lokicash actually locked in")
}

// TestGetCashHubConfig_HubLevelDefaultRedeemFeePpm_IsVisibleToOwner documents
// the flip side: the HUB-LEVEL default (what NEW wallets will get) is fully
// visible to the owner via GetCashHubConfig — confirming the fix above
// closed the gap without disturbing this, already-working, surface.
func TestGetCashHubConfig_HubLevelDefaultRedeemFeePpm_IsVisibleToOwner(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub, _, err := svc.AppsService.CreateCashHub(
		"test-hub-fee", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		db.CashHubConfig{PerWalletMaxMloki: 10_000, MaxExpSecs: 3600, RedeemFeePpm: 50_000},
	)
	require.NoError(t, err)

	theAPI := newTestAPI(svc)
	cfg, err := theAPI.appsSvc.GetCashHubConfig(hub.ID)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, 50_000, cfg.RedeemFeePpm,
		"the Hub-level default IS visible to the owner (surfaced via GetApp/api.App.CashRedeemFeePpm) — "+
			"only the PER-SLICE rate already locked into a specific issued lokicash has no such surface, per the sibling test above")
}
