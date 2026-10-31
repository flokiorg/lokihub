//go:build testhooks

package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/integration/regtest"
	"github.com/flokiorg/lokihub/logger"
)

// registerStackTestHooks mounts the hooks that act on the rest of the regtest
// stack: funding a Circle Wallet from the payer, the relay's proof-of-work
// minimum, a Hub restart, and flspd's dashboard.
func (t *testHooks) registerStackTestHooks(g *echo.Group) {
	// POST /api/test/circle/fund {wallet_pubkey, amount_millis}: an invoice on
	// that wallet's app (a Circle Wallet's is the Hub-settled one its
	// make_invoice gives), paid by the payer.
	g.POST("/circle/fund", t.with(func(c echo.Context, h *regtest.Hooks) error {
		var req struct {
			WalletPubkey string `json:"wallet_pubkey"`
			AmountMillis uint64 `json:"amount_millis"`
		}
		if err := c.Bind(&req); err != nil || req.WalletPubkey == "" || req.AmountMillis == 0 {
			return hookError(c, fmt.Errorf("wallet_pubkey and a positive amount_millis are required"))
		}
		var app db.App
		if err := t.httpSvc.db.Where("wallet_pubkey = ?", req.WalletPubkey).Limit(1).Find(&app).Error; err != nil || app.ID == 0 {
			return c.JSON(http.StatusNotFound, ErrorResponse{Message: "no app with this wallet pubkey"})
		}
		ctx, cancel := context.WithTimeout(c.Request().Context(), 90*time.Second)
		defer cancel()
		appID := app.ID
		inv, err := t.httpSvc.api.CreateInvoice(ctx, &api.MakeInvoiceRequest{
			Amount: req.AmountMillis, Description: "regtest test hook: circle/fund", AppId: &appID})
		if err != nil {
			return hookError(c, fmt.Errorf("invoice: %w", err))
		}
		p, err := h.PayInvoice(ctx, inv.Invoice)
		if err != nil {
			return hookError(c, fmt.Errorf("pay: %w", err))
		}
		return c.JSON(http.StatusOK, map[string]any{"app_id": app.ID, "invoice": inv.Invoice, "payment": p})
	}))

	// POST /api/test/relay/min-pow {difficulty}: the relay's NIP-13 minimum,
	// enforced and announced in NIP-11; 0 clears it. The relay's supervisor
	// (docker-compose.dev.yml) restarts the relay process on the new config;
	// this answers once NIP-11 shows it.
	g.POST("/relay/min-pow", func(c echo.Context) error {
		var req struct {
			Difficulty int `json:"difficulty"`
		}
		if err := c.Bind(&req); err != nil || req.Difficulty < 0 || req.Difficulty > 64 {
			return hookError(c, fmt.Errorf("difficulty must be 0 to 64"))
		}
		dir := os.Getenv("LOKIHUB_TEST_RELAY_CTL_DIR")
		if dir == "" {
			return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: "LOKIHUB_TEST_RELAY_CTL_DIR unset: the relay has no supervisor"})
		}
		tmp := filepath.Join(dir, "min-pow.tmp")
		if err := os.WriteFile(tmp, []byte(strconv.Itoa(req.Difficulty)+"\n"), 0o644); err != nil { //nolint:gosec // read by the relay container
			return hookError(c, err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "min-pow")); err != nil {
			return hookError(c, err)
		}
		relay := envOr("RELAY", "ws://relay:5500")
		got, err := awaitRelayMinPow(c.Request().Context(), relay, req.Difficulty, 45*time.Second)
		if err != nil {
			return c.JSON(http.StatusGatewayTimeout, ErrorResponse{Message: err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"difficulty": got, "relay": relay})
	})

	// POST /api/test/restart: the Hub stops as on SIGTERM and air runs it again
	// (--build.rerun), unlocking from AUTO_UNLOCK_PASSWORD; everything it holds
	// in memory is reloaded from its store. Answers 202 at once; the Hub is
	// back when GET /api/info answers.
	g.POST("/restart", func(c echo.Context) error {
		logger.Logger.Warn().Msg("Test hook: restarting the Hub")
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}()
		return c.JSON(http.StatusAccepted, map[string]string{"status": "restarting"})
	})

	// flspd's dashboard, as its operator (I-83, I-84): POST
	// /api/test/flspd/fee/raise and /fee/restore change the LSP's terms
	// (a hot-swap through update_config); /announce/{pause,resume,unlist} set
	// its NIP-LSP announcement state (set_announce_state).
	f := &flspdDashboard{}
	g.POST("/flspd/fee/:action", func(c echo.Context) error {
		switch c.Param("action") {
		case "raise", "restore":
		default:
			return hookError(c, fmt.Errorf("action must be raise or restore"))
		}
		out, err := f.fee(c.Request().Context(), c.Param("action") == "raise")
		if err != nil {
			return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: err.Error()})
		}
		return c.JSON(http.StatusOK, out)
	})
	g.POST("/flspd/announce/:action", func(c echo.Context) error {
		switch c.Param("action") {
		case "pause", "resume", "unlist":
		default:
			return hookError(c, fmt.Errorf("action must be pause, resume or unlist"))
		}
		var out json.RawMessage
		if err := f.call(c.Request().Context(), "set_announce_state", map[string]string{"action": c.Param("action")}, &out); err != nil {
			return c.JSON(http.StatusServiceUnavailable, ErrorResponse{Message: err.Error()})
		}
		return c.JSON(http.StatusOK, out)
	})
}

// awaitRelayMinPow polls the relay's NIP-11 document until it announces want
// (0: none) and returns what it announced.
func awaitRelayMinPow(ctx context.Context, relay string, want int, within time.Duration) (int, error) {
	u, err := url.Parse(relay)
	if err != nil {
		return 0, err
	}
	u.Scheme = strings.Replace(strings.Replace(u.Scheme, "wss", "https", 1), "ws", "http", 1)
	deadline := time.Now().Add(within)
	last := -1
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		req.Header.Set("Accept", "application/nostr+json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			var info struct {
				Limitation struct {
					MinPowDifficulty int `json:"min_pow_difficulty"`
				} `json:"limitation"`
			}
			if json.NewDecoder(resp.Body).Decode(&info) == nil {
				last = info.Limitation.MinPowDifficulty
			}
			_ = resp.Body.Close()
			if last == want {
				return last, nil
			}
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return last, fmt.Errorf("the relay announces min_pow_difficulty %d, not %d, after %s", last, want, within)
}

// flspdDashboard calls flspd's dashboard JSON-RPC as its admin, by NIP-98.
type flspdDashboard struct {
	mu      sync.Mutex
	saved   any // the opening fee before the first raise
	hasSave bool
}

func (f *flspdDashboard) call(ctx context.Context, method string, params, out any) error {
	base := os.Getenv("LOKIHUB_TEST_FLSPD_DASHBOARD")
	keyFile := os.Getenv("LOKIHUB_TEST_FLSPD_ADMIN_KEY_FILE")
	if base == "" || keyFile == "" {
		return fmt.Errorf("no flspd dashboard on this stack (LOKIHUB_TEST_FLSPD_DASHBOARD, LOKIHUB_TEST_FLSPD_ADMIN_KEY_FILE)")
	}
	raw, err := os.ReadFile(keyFile) //nolint:gosec // a configured path
	if err != nil {
		return fmt.Errorf("flspd admin key: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": strconv.FormatInt(time.Now().UnixNano(), 10), "method": method, "params": json.RawMessage(p)})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(base, "/") + "/"
	sum := sha256.Sum256(body)
	ev := nostr.Event{Kind: 27235, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		// flspd compares u with the request URL less any trailing slash.
		{"u", strings.TrimSuffix(endpoint, "/")}, {"method", http.MethodPost}, {"m", http.MethodPost}, {"payload", hex.EncodeToString(sum[:])},
	}}
	if err := ev.Sign(key); err != nil {
		return fmt.Errorf("sign the dashboard login: %w", err)
	}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(evJSON))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("flspd dashboard %s: %s %s", method, resp.Status, strings.TrimSpace(string(data)))
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    any    `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &rpc); err != nil {
		return fmt.Errorf("flspd dashboard %s: %w", method, err)
	}
	if rpc.Error != nil {
		return fmt.Errorf("flspd dashboard %s: %d %s %v", method, rpc.Error.Code, rpc.Error.Message, rpc.Error.Data)
	}
	if out != nil {
		return json.Unmarshal(rpc.Result, out)
	}
	return nil
}

// fee raises the LSP's terms or restores the ones in force before the first
// raise. The raise doubles the opening fee rate plus 1 % and lifts its floor by
// 1,000 loki (the LSPS2 menu, which flspd syncs from its config only at start)
// and lifts the smallest channel by 1,000 loki (LSPS1's min_channel_balance,
// which flspd reads live).
func (f *flspdDashboard) fee(ctx context.Context, raise bool) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var got struct {
		Config map[string]any `json:"config"`
	}
	if err := f.call(ctx, "get_config", map[string]any{}, &got); err != nil {
		return nil, err
	}
	fee, _ := got.Config["opening_fee"].(map[string]any)
	size, _ := got.Config["channel_size"].(map[string]any)
	if fee == nil || size == nil {
		return nil, fmt.Errorf("flspd's config has no opening_fee or channel_size block")
	}
	switch {
	case raise:
		if !f.hasSave {
			f.saved, f.hasSave = cloneJSON(map[string]any{"opening_fee": fee, "channel_size": size}), true
		}
		rate, _ := fee["rate_percent"].(float64)
		floor, _ := fee["min_loki"].(float64)
		fee["rate_percent"] = rate*2 + 1
		fee["min_loki"] = floor + 1000
		minChan, _ := size["min_loki"].(float64)
		size["min_loki"] = minChan + 1000
	case f.hasSave:
		saved, _ := f.saved.(map[string]any)
		got.Config["opening_fee"], got.Config["channel_size"] = saved["opening_fee"], saved["channel_size"]
		f.hasSave = false
	default:
		return map[string]any{"opening_fee": fee, "channel_size": size, "changed": false}, nil
	}
	if err := f.call(ctx, "update_config", map[string]any{"config": got.Config}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"opening_fee": got.Config["opening_fee"], "channel_size": got.Config["channel_size"], "changed": true}, nil
}

func cloneJSON(v any) any {
	raw, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}
