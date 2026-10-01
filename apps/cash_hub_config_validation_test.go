package apps_test

// Two validation gaps in the Cash Hub config, found by asking the carried-open
// question "what should min_transfer_mloki default to?" and discovering that the
// answer is "nothing — the real bug is elsewhere".
//
// 1. CreateCashHub validated NEITHER min_transfer_mloki NOR redeem_fee_base_mloki,
//    while UpdateCashHubConfig validated both, with comments explaining why. So the
//    create path accepted configs the update path refuses. A negative floor is the
//    dangerous half: every enforcement site tests `> 0`, so it reads as "no floor" and
//    silently disables the guard the operator believed they had set.
//
// 2. Neither path noticed that a floor BELOW the flat redeem fee is self-contradictory.
//    min_transfer_mloki exists so a split cannot produce a worthless piece, and a piece
//    at or below redeem_fee_base_mloki cannot be redeemed at all — the fee consumes it
//    and cash_redeem now refuses a zero payout outright. A floor beneath the base
//    claims to prevent exactly what it permits.
//
// What this deliberately does NOT do is promote a floor of 0 to the fee base. 0 means
// "no floor" in that field's own documentation and at every enforcement site, so
// reinterpreting it would repeat the exact mistake this round kept finding: a
// meaningful zero silently overridden (spent_retention_secs, identity_required, and
// the negative-reads-as-unset bug directly above). An operator may choose no floor;
// the consequence is dust that can be consolidated but not redeemed, and making that
// the default is their policy call, not a validation rule.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestCashHubConfig_FloorAndFeeBaseValidation(t *testing.T) {
	cases := []struct {
		name          string
		minTransfer   int64
		redeemFeeBase int64
		wantRejected  bool
		why           string
	}{
		{
			name: "both_zero_is_fine", minTransfer: 0, redeemFeeBase: 0, wantRejected: false,
			why: "the default config, and 0 means 'none' for both",
		},
		{
			name: "no_floor_with_a_fee_base_is_ALLOWED", minTransfer: 0, redeemFeeBase: 500, wantRejected: false,
			why: "0 means 'no floor' and must keep meaning that; promoting it to the base " +
				"would silently reinterpret a meaningful zero. The operator accepts dust",
		},
		{
			name: "floor_equal_to_the_base_is_fine", minTransfer: 500, redeemFeeBase: 500, wantRejected: false,
			why: "the boundary is inclusive — a piece exactly at the base is refused by " +
				"cash_redeem, but the floor is not claiming otherwise",
		},
		{
			name: "floor_above_the_base_is_fine", minTransfer: 1000, redeemFeeBase: 500, wantRejected: false,
			why: "the coherent configuration: every split piece can still be redeemed",
		},
		{
			name: "floor_BELOW_the_base_is_refused", minTransfer: 100, redeemFeeBase: 500, wantRejected: true,
			why: "self-contradictory: the floor permits 100-mloki pieces that the 500 fee " +
				"makes unredeemable, which is what the floor exists to prevent",
		},
		{
			name: "negative_floor_is_refused", minTransfer: -1, redeemFeeBase: 0, wantRejected: true,
			why: "every enforcement site tests `> 0`, so a negative value silently disables " +
				"the floor — this is the one the create path accepted",
		},
		{
			name: "negative_fee_base_is_refused", minTransfer: 0, redeemFeeBase: -1, wantRejected: true,
			why: "CalculateRedeemFeeMloki ignores a non-positive base, so this too reads as " +
				"'unset' rather than erroring; the update path already refused it",
		},
	}

	for _, c := range cases {
		t.Run("create/"+c.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			_, _, err = svc.AppsService.CreateCashHub(
				"validation-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
				[]string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE}, nil,
				db.CashHubConfig{
					PerWalletMaxMloki:  100_000,
					MaxExpSecs:         3600,
					MinTransferMloki:   c.minTransfer,
					RedeemFeeBaseMloki: c.redeemFeeBase,
				},
			)
			if c.wantRejected {
				require.ErrorIs(t, err, constants.ErrInvalidParams, "must be refused — %s", c.why)
				return
			}
			require.NoError(t, err, "must be accepted — %s", c.why)
		})
	}

	// The update path must agree with the create path, which is the asymmetry that
	// started this. Checked against the pair IN FORCE afterwards, so raising the base
	// alone on a hub with a low floor is caught too.
	t.Run("update/raising_the_base_alone_past_an_existing_floor_is_refused", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()

		hub, _, err := svc.AppsService.CreateCashHub(
			"validation-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
			[]string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE}, nil,
			db.CashHubConfig{PerWalletMaxMloki: 100_000, MaxExpSecs: 3600, MinTransferMloki: 100},
		)
		require.NoError(t, err)

		base := int64(500)
		err = svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, nil, nil, &base, nil)
		require.ErrorIs(t, err, constants.ErrInvalidParams,
			"the floor stays at 100 while the base becomes 500 — incoherent, and invisible "+
				"to a check that only looked at the values this call carried")

		// And the config must be unchanged, since the whole update is refused.
		cfg, err := svc.AppsService.GetCashHubConfig(hub.ID)
		require.NoError(t, err)
		require.Equal(t, int64(0), cfg.RedeemFeeBaseMloki, "a refused update must not partially apply")
	})

	t.Run("update/lowering_the_floor_alone_below_an_existing_base_is_refused", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()

		hub, _, err := svc.AppsService.CreateCashHub(
			"validation-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
			[]string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE}, nil,
			db.CashHubConfig{PerWalletMaxMloki: 100_000, MaxExpSecs: 3600,
				MinTransferMloki: 1000, RedeemFeeBaseMloki: 500},
		)
		require.NoError(t, err)

		floor := int64(100)
		err = svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, &floor, nil, nil, nil)
		require.ErrorIs(t, err, constants.ErrInvalidParams, "the mirror case of the one above")
	})

	t.Run("update/clearing_the_floor_to_zero_stays_allowed", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()

		hub, _, err := svc.AppsService.CreateCashHub(
			"validation-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
			[]string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE}, nil,
			db.CashHubConfig{PerWalletMaxMloki: 100_000, MaxExpSecs: 3600,
				MinTransferMloki: 1000, RedeemFeeBaseMloki: 500},
		)
		require.NoError(t, err)

		none := int64(0)
		require.NoError(t, svc.AppsService.UpdateCashHubConfig(hub.ID, nil, nil, &none, nil, nil, nil),
			"0 means 'no floor' and an operator must be able to go back to it; refusing "+
				"this would make the coherence rule a one-way door")
	})
}
