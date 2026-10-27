//go:build testhooks

package http

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/flokiorg/lokihub/integration/regtest"
	"github.com/flokiorg/lokihub/logger"
)

// TestHooksEnv turns the hooks on in a testhooks build.
const TestHooksEnv = "LOKIHUB_TEST_HOOKS"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type testHooks struct {
	httpSvc *HttpService
	once    sync.Once
	hooks   *regtest.Hooks
	err     error
}

// connect dials the chain node and the payer once; both are refused unless on regtest.
func (t *testHooks) connect(ctx context.Context) (*regtest.Hooks, error) {
	t.once.Do(func() {
		chain, err := regtest.DialChain(regtest.ChainConfig{
			Host:     envOr("LOKIHUB_TEST_CHAIN_RPC", "chain:25213"),
			User:     envOr("LOKIHUB_TEST_CHAIN_RPC_USER", "dev"),
			Pass:     envOr("LOKIHUB_TEST_CHAIN_RPC_PASS", "dev"),
			CertPath: envOr("LOKIHUB_TEST_CHAIN_RPC_CERT", "/chain/rpc.cert"),
		})
		if err != nil {
			t.err = err
			return
		}
		payer, err := regtest.DialNode(ctx, regtest.NodeConfig{
			Name:    "payer",
			Address: envOr("LOKIHUB_TEST_PAYER_GRPC", "payer:10006"),
			Dir:     envOr("LOKIHUB_TEST_PAYER_DIR", "/payer-data"),
		})
		if err != nil {
			chain.Close()
			t.err = err
			return
		}
		t.hooks = regtest.NewHooks(chain, payer)
	})
	return t.hooks, t.err
}

// requireRegtest refuses every hook unless this backend's own node is on regtest,
// so a hook can never act beside a mainnet node.
func (t *testHooks) requireRegtest(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ln := t.httpSvc.svc.GetLNClient()
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

func (t *testHooks) with(f func(c echo.Context, h *regtest.Hooks) error) echo.HandlerFunc {
	return func(c echo.Context) error {
		h, err := t.connect(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: "regtest stack: " + err.Error()})
		}
		return f(c, h)
	}
}

func hookError(c echo.Context, err error) error {
	return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
}

// registerTestHooks mounts the chain and payer hooks under /api/test, behind the
// full-access admin token, when LOKIHUB_TEST_HOOKS=1.
func (httpSvc *HttpService) registerTestHooks(full *echo.Group) {
	if os.Getenv(TestHooksEnv) != "1" {
		return
	}
	logger.Logger.Warn().Msg("Test hooks are on (testhooks build, LOKIHUB_TEST_HOOKS=1); they run only beside a regtest node")
	t := &testHooks{httpSvc: httpSvc}
	g := full.Group("/test", t.requireRegtest)

	g.POST("/chain/mine", t.with(func(c echo.Context, h *regtest.Hooks) error {
		var req struct {
			Blocks  int    `json:"blocks"`
			Address string `json:"address"`
		}
		if err := c.Bind(&req); err != nil {
			return hookError(c, err)
		}
		hashes, err := h.Mine(c.Request().Context(), req.Blocks, req.Address)
		if err != nil {
			return hookError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"blocks": hashes})
	}))
	g.POST("/chain/reorg", t.with(func(c echo.Context, h *regtest.Hooks) error {
		var req struct {
			Depth int `json:"depth"`
		}
		if err := c.Bind(&req); err != nil {
			return hookError(c, err)
		}
		if req.Depth == 0 {
			req.Depth = 1
		}
		hashes, err := h.Reorg(c.Request().Context(), req.Depth)
		if err != nil {
			return hookError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"blocks": hashes})
	}))
	g.GET("/chain/tip", t.with(func(c echo.Context, h *regtest.Hooks) error {
		tip, err := h.Tip()
		if err != nil {
			return hookError(c, err)
		}
		return c.JSON(http.StatusOK, tip)
	}))
	g.GET("/chain/tx/:txid", t.with(func(c echo.Context, h *regtest.Hooks) error {
		tx, err := h.Tx(c.Param("txid"))
		if err != nil {
			return c.JSON(http.StatusNotFound, ErrorResponse{Message: err.Error()})
		}
		return c.JSON(http.StatusOK, tx)
	}))
	g.POST("/chain/withhold", t.with(func(c echo.Context, h *regtest.Hooks) error {
		var req struct {
			Txid string `json:"txid"`
			On   bool   `json:"on"`
		}
		if err := c.Bind(&req); err != nil {
			return hookError(c, err)
		}
		if err := h.Withhold(req.Txid, req.On); err != nil {
			return hookError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	}))
	g.POST("/payer/pay-invoice", t.with(func(c echo.Context, h *regtest.Hooks) error {
		var req struct {
			Invoice string `json:"invoice"`
		}
		if err := c.Bind(&req); err != nil {
			return hookError(c, err)
		}
		ctx, cancel := context.WithTimeout(c.Request().Context(), 90*time.Second)
		defer cancel()
		p, err := h.PayInvoice(ctx, req.Invoice)
		if err != nil {
			return hookError(c, err)
		}
		return c.JSON(http.StatusOK, p)
	}))
	g.POST("/payer/pay-address", t.with(func(c echo.Context, h *regtest.Hooks) error {
		var req struct {
			Address      string `json:"address"`
			AmountMillis uint64 `json:"amount_millis"`
		}
		if err := c.Bind(&req); err != nil {
			return hookError(c, err)
		}
		txid, err := h.PayAddress(c.Request().Context(), req.Address, req.AmountMillis)
		if err != nil {
			return hookError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]string{"txid": txid})
	}))
	g.POST("/payer/invoice", t.with(func(c echo.Context, h *regtest.Hooks) error {
		var req struct {
			AmountMillis uint64 `json:"amount_millis"`
		}
		if err := c.Bind(&req); err != nil {
			return hookError(c, err)
		}
		inv, err := h.NewInvoice(c.Request().Context(), req.AmountMillis)
		if err != nil {
			return hookError(c, err)
		}
		return c.JSON(http.StatusOK, inv)
	}))
	g.GET("/payer/invoice/:hash", t.with(func(c echo.Context, h *regtest.Hooks) error {
		st, err := h.InvoiceState(c.Request().Context(), c.Param("hash"))
		if err != nil {
			return c.JSON(http.StatusNotFound, ErrorResponse{Message: err.Error()})
		}
		return c.JSON(http.StatusOK, st)
	}))
	g.GET("/payer/address", t.with(func(c echo.Context, h *regtest.Hooks) error {
		addr, err := h.Payer.NewAddress(c.Request().Context())
		if err != nil {
			return hookError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]string{"address": addr})
	}))
}
