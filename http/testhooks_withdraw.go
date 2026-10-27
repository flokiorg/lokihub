//go:build testhooks

package http

import (
	"context"
	"net/http"
	"os"
	"slices"

	"github.com/labstack/echo/v4"

	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/logger"
)

// registerWithdrawTestHooks mounts the payout engine's holds and the
// withdraw-scoped and listing clocks under /api/test, behind the full-access admin token,
// in a testhooks build with LOKIHUB_TEST_HOOKS=1 beside a regtest node only
// (plan WP-20 item 0h, G1).
func (httpSvc *HttpService) registerWithdrawTestHooks(full *echo.Group) {
	if os.Getenv("LOKIHUB_TEST_HOOKS") != "1" {
		return
	}
	logger.Logger.Warn().Msg("Withdraw test hooks are on (testhooks build, LOKIHUB_TEST_HOOKS=1)")
	g := full.Group("/test", httpSvc.withdrawHooksRegtestOnly)
	engine := func() *cashwithdraw.Engine {
		e, ok := httpSvc.svc.(interface{ GetWithdrawEngine() *cashwithdraw.Engine })
		if !ok {
			return nil
		}
		return e.GetWithdrawEngine()
	}
	g.POST("/withdraw/hooks", func(c echo.Context) error {
		var req struct {
			HoldSign               *bool   `json:"hold_sign"`
			HoldBroadcast          *bool   `json:"hold_broadcast"`
			HoldBump               *bool   `json:"hold_bump"`
			CrashAfterSign         *bool   `json:"crash_after_sign"`
			UnconfirmedBoundMillis *int64  `json:"unconfirmed_bound_millis"`
			StarveCoins            *bool   `json:"starve_coins"`
			CrashAt                *string `json:"crash_at"`
			Reset                  bool    `json:"reset"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
		}
		e := engine()
		if e == nil || e.Hooks == nil {
			return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: "the payout engine has no test hooks"})
		}
		h := e.Hooks
		crashAt := func() string { s, _ := h.CrashAt.Load().(string); return s }
		wasCrashing := h.CrashAfterSign.Load() || crashAt() != ""
		if req.Reset {
			h.HoldSign.Store(false)
			h.HoldBroadcast.Store(false)
			h.HoldBump.Store(false)
			h.CrashAfterSign.Store(false)
			h.UnconfirmedBoundMillis.Store(0)
			h.StarveCoins.Store(false)
			h.CrashAt.Store("")
		}
		if req.CrashAt != nil {
			if *req.CrashAt != "" && !slices.Contains(cashwithdraw.EngineStages, *req.CrashAt) {
				return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "crash_at must be one of the engine stages"})
			}
			h.CrashAt.Store(*req.CrashAt)
		}
		set := func(b *bool, to interface{ Store(bool) }) {
			if b != nil {
				to.Store(*b)
			}
		}
		set(req.HoldSign, &h.HoldSign)
		set(req.HoldBroadcast, &h.HoldBroadcast)
		set(req.HoldBump, &h.HoldBump)
		set(req.CrashAfterSign, &h.CrashAfterSign)
		set(req.StarveCoins, &h.StarveCoins)
		if req.UnconfirmedBoundMillis != nil {
			h.UnconfirmedBoundMillis.Store(*req.UnconfirmedBoundMillis)
		}
		if wasCrashing && !h.CrashAfterSign.Load() && crashAt() == "" {
			// Turning the crash off is the restart: the engine reconciles as
			// at startup, publishing what it signed.
			go func() {
				if err := e.Reconcile(context.Background()); err != nil {
					logger.Logger.Warn().Err(err).Msg("Payout engine reconcile after the crash hook failed")
				}
			}()
		}
		e.Signal()
		return c.JSON(http.StatusOK, map[string]any{
			"hold_sign": h.HoldSign.Load(), "hold_broadcast": h.HoldBroadcast.Load(), "hold_bump": h.HoldBump.Load(),
			"crash_after_sign": h.CrashAfterSign.Load(), "unconfirmed_bound_millis": h.UnconfirmedBoundMillis.Load(),
			"starve_coins": h.StarveCoins.Load(), "crash_at": crashAt(), "stages": cashwithdraw.EngineStages,
		})
	})
	g.POST("/clock", func(c echo.Context) error {
		var req struct {
			OffsetSecs int64  `json:"offset_secs"`
			Scope      string `json:"scope"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
		}
		switch req.Scope {
		case "listing":
			l, ok := httpSvc.svc.(interface{ SetListingClockOffset(int64) })
			if !ok {
				return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: "no listing clock"})
			}
			l.SetListingClockOffset(req.OffsetSecs)
			return c.JSON(http.StatusOK, map[string]any{"offset_secs": req.OffsetSecs, "scope": req.Scope})
		case "withdraw":
		default:
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: `scope must be "withdraw" or "listing"`})
		}
		e := engine()
		if e == nil {
			return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: "no payout engine"})
		}
		e.S.CancelClockOffset.Store(req.OffsetSecs)
		e.Signal()
		return c.JSON(http.StatusOK, map[string]any{"offset_secs": req.OffsetSecs, "scope": req.Scope})
	})
}

// withdrawHooksRegtestOnly refuses every withdraw hook unless this backend's
// node is on regtest.
func (httpSvc *HttpService) withdrawHooksRegtestOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ln := httpSvc.svc.GetLNClient()
		if ln == nil {
			return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: "the node is not started"})
		}
		info, err := ln.GetInfo(c.Request().Context())
		if err != nil || info == nil || info.Network != "regtest" {
			return c.JSON(http.StatusForbidden, ErrorResponse{Message: "test hooks run only on a regtest node"})
		}
		return next(c)
	}
}
