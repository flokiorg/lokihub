//go:build testhooks

package http

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/testknobs"
)

// registerE2ETestHooks mounts the fault, cash-limit and archive hooks the e2e
// conformance round drives, on the regtest-only /api/test group of
// registerWithdrawTestHooks.
func (httpSvc *HttpService) registerE2ETestHooks(g *echo.Group) {
	// POST /api/test/faults {stall_commit, break_signer, drop_reply,
	// drop_before_commit, route_over_bound, hold_payments, hold_self_payments,
	// fail_chain_scan: bool, reset}: a field
	// left out keeps its value; reset clears every fault first. Answers the
	// faults now on.
	g.POST("/faults", func(c echo.Context) error {
		var req struct {
			testknobs.Faults
			Reset bool `json:"reset"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
		}
		on := testknobs.SetFaults(req.Faults, req.Reset)
		logger.Logger.Warn().Interface("faults", on).Msg("Test faults set")
		return c.JSON(http.StatusOK, on)
	})

	// POST /api/test/cash/limits {live_wallet_cap, spin_off_rate, per_secs,
	// reset}: overrides the live-bill cap per Cash Hub and the spin-off rate per
	// bill lineage (per_secs, default an hour); 0 lifts either; reset returns to
	// the configured limits. Answers the overrides in force.
	g.POST("/cash/limits", func(c echo.Context) error {
		var req struct {
			testknobs.Limits
			Reset bool `json:"reset"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
		}
		return c.JSON(http.StatusOK, testknobs.SetLimits(req.Limits, req.Reset))
	})

	// POST /api/test/cash/commit-crash {commit_crash_at, reset}: arms a crash of
	// the cash-ledger commit at that stage (cashwallet.CommitStages), as a crash
	// would — gorm rolls back every write, only the pre-commit request_id mark
	// survives (R-RACE-2, NIP-CASH §Atomicity, §request_id). "" or reset turns it
	// off. Answers the stage now armed and the nameable stages. [rec4-local, NEVER-PUSH]
	g.POST("/cash/commit-crash", func(c echo.Context) error {
		var req struct {
			CommitCrashAt *string `json:"commit_crash_at"`
			Reset         bool    `json:"reset"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
		}
		if req.Reset {
			empty := ""
			req.CommitCrashAt = &empty
		}
		testknobs.SetFaults(testknobs.Faults{CommitCrashAt: req.CommitCrashAt}, false)
		logger.Logger.Warn().Str("commit_crash_at", testknobs.CommitCrashAt()).Msg("Cash commit crash hook set")
		return c.JSON(http.StatusOK, map[string]any{
			"commit_crash_at": testknobs.CommitCrashAt(),
			"stages":          cashwallet.CommitStages,
		})
	})

	// POST /api/test/cash/drop-archive {wallet_pubkey}: forgets a destroyed
	// bill's archive (the bill row and its slices' rows). Its pairing key stays
	// derivable from the node's keys; request records naming it are kept.
	g.POST("/cash/drop-archive", func(c echo.Context) error {
		var req struct {
			WalletPubkey string `json:"wallet_pubkey"`
		}
		if err := c.Bind(&req); err != nil || req.WalletPubkey == "" {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "wallet_pubkey is required"})
		}
		gormDB := httpSvc.svc.GetDB()
		var live int64
		if err := gormDB.Model(&db.App{}).Where("wallet_pubkey = ?", req.WalletPubkey).Count(&live).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
		}
		if live > 0 {
			return c.JSON(http.StatusConflict, ErrorResponse{Message: "the bill is live; only a destroyed bill's archive can be dropped"})
		}
		var bills, slices int64
		err := gormDB.Transaction(func(tx *gorm.DB) error {
			var rows []db.CashBillArchive
			if err := tx.Where("wallet_pubkey = ?", req.WalletPubkey).Find(&rows).Error; err != nil {
				return err
			}
			for _, r := range rows {
				res := tx.Where("wallet_app_id = ?", r.WalletAppID).Delete(&db.CashBillSliceArchive{})
				if res.Error != nil {
					return res.Error
				}
				slices += res.RowsAffected
			}
			res := tx.Where("wallet_pubkey = ?", req.WalletPubkey).Delete(&db.CashBillArchive{})
			bills = res.RowsAffected
			return res.Error
		})
		if err != nil {
			return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
		}
		if bills == 0 {
			return c.JSON(http.StatusNotFound, ErrorResponse{Message: "no archive for this wallet pubkey"})
		}
		return c.JSON(http.StatusOK, map[string]int64{"bills": bills, "slices": slices})
	})
}
