// Package lspservice is the Hub's client to a separately-deployed lspd daemon.
//
// It is the ONLY coupling between the Hub and lspd (plan §1.1, §3.1): a network
// boundary over lspd's internal NIP-98 JSON-RPC dashboard. This package must
// NOT import any github.com/flokiorg/lokihub/lspd/... package — the request and
// response JSON shapes are replicated here from lspd's dashboard handlers, never
// imported, so lspd can be rebuilt, re-versioned and run remotely without
// recompiling the Hub.
//
// The browser authenticates to the Hub over JWT; the Hub authenticates to lspd
// over NIP-98 signed by an operator-supplied nsec or NIP-46 bunker (plan §3.2),
// resolved through nmilat's nip46 signer dispatch (signer.go). Each dashboard
// call signs a fresh NIP-98 event (nip98.go).
package lspservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// dashboardPath is lspd's JSON-RPC endpoint. lspd serves JSON-RPC on any
	// path that is not /info or /healthz; we POST here and sign the matching
	// "u" tag so the NIP-98 URL check passes.
	dashboardPath = "/dashboard"

	httpTimeout  = 15 * time.Second
	statusTTL    = 15 * time.Second
	probeTimeout = 6 * time.Second
)

// ErrNotConfigured means no lspd endpoint is configured; the feature is off.
var ErrNotConfigured = errors.New("lspservice: not configured")

// ErrForbidden means lspd rejected the request with HTTP 403 — the signer's
// pubkey is not lspd's DASHBOARD_ADMIN_PUBKEY (a "key mismatch").
var ErrForbidden = errors.New("lspservice: forbidden (key mismatch)")

// RPCError is a JSON-RPC error returned by an lspd dashboard method. It is a
// reached-the-handler outcome (HTTP 200), not a transport or auth failure.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("lspd rpc error %d: %s", e.Code, e.Message)
}

// Config is the attach configuration, mapped from the Hub's env (plan §3.2).
type Config struct {
	Endpoint string // lspd dashboard base URL, e.g. http://lspd:8080 (no trailing slash)
	Nsec     string // raw nsec1…/64-hex admin key — exactly one of Nsec/Bunker
	Bunker   string // NIP-46 bunker:// string — exactly one of Nsec/Bunker
}

// Client talks to one lspd dashboard. Safe for concurrent use.
type Client struct {
	cfg        Config
	httpClient *http.Client

	signerGuard // signerMu + cached signer (signer.go)

	statusMu   sync.Mutex
	cached     Status
	cachedAt   time.Time
	refreshing bool
}

// New returns a client for cfg, or nil when no endpoint is configured (the
// feature is off: no nav item, zero calls to lspd — plan §3.4). The endpoint
// is normalized without a trailing slash so the signed "u" tag is stable.
func New(cfg Config) *Client {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	cfg.Nsec = strings.TrimSpace(cfg.Nsec)
	cfg.Bunker = strings.TrimSpace(cfg.Bunker)
	if cfg.Endpoint == "" {
		return nil
	}
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: httpTimeout,
			// The Hub is a deliberate proxy in front of lspd; it must not chase
			// redirects to some other host (defensive — lspd never issues any).
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Endpoint is the configured lspd dashboard base URL.
func (c *Client) Endpoint() string { return c.cfg.Endpoint }

// Close releases the signer (closes a bunker connection, if any).
func (c *Client) Close() {
	c.signerMu.Lock()
	defer c.signerMu.Unlock()
	if c.signerClose != nil {
		c.signerClose()
		c.signerClose = nil
		c.signer = nil
	}
}

// call signs and POSTs a JSON-RPC request. On HTTP 403 it returns ErrForbidden;
// on a JSON-RPC error it returns an *RPCError; otherwise it unmarshals the
// result into out (when out is non-nil).
func (c *Client) call(ctx context.Context, method string, params json.RawMessage, out any) error {
	signer, err := c.getSigner(ctx)
	if err != nil {
		return err
	}

	reqEnvelope := struct {
		JsonRpc string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
		Id      int             `json:"id"`
	}{JsonRpc: "2.0", Method: method, Params: params, Id: 1}

	body, err := json.Marshal(reqEnvelope)
	if err != nil {
		return fmt.Errorf("lspservice: marshal request: %w", err)
	}

	url := c.cfg.Endpoint + dashboardPath
	authHeader, err := c.signNIP98(ctx, signer, url, http.MethodPost, body)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("lspservice: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", authHeader)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("lspservice: %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		return ErrForbidden
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("lspservice: %s: unexpected status %d: %s", method, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("lspservice: %s: decode response: %w", method, err)
	}
	if rpcResp.Error != nil {
		return rpcResp.Error
	}
	if out != nil {
		if err := json.Unmarshal(rpcResp.Result, out); err != nil {
			return fmt.Errorf("lspservice: %s: decode result: %w", method, err)
		}
	}
	return nil
}

// callRaw is call for the pass-through proxy handlers: it returns lspd's exact
// JSON-RPC result verbatim so the Hub does not have to model every dashboard
// result shape (the Hub is a thin pass-through — plan §3.3).
func (c *Client) callRaw(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.call(ctx, method, params, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// --- public GET endpoints (unauthenticated on lspd) ---

// LspdInfo is lspd's public GET /info body (plan §3.4 health probe). Replicated
// from lspd/transport/dashboard/server.go InfoResponse; not imported.
type LspdInfo struct {
	Version          string           `json:"version"`
	Commit           string           `json:"commit"`
	DashboardEnabled bool             `json:"dashboard_enabled"`
	Nodes            []LspdNodeStatus `json:"nodes"`
}

// LspdNodeStatus is one configured node's identity + reachability in /info.
type LspdNodeStatus struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

// Info probes lspd's public /info (no auth, no body).
func (c *Client) Info(ctx context.Context) (*LspdInfo, error) {
	var info LspdInfo
	if err := c.getJSON(ctx, "/info", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Healthz probes lspd's public /healthz.
func (c *Client) Healthz(ctx context.Context) error {
	return c.getJSON(ctx, "/healthz", nil)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	url := c.cfg.Endpoint + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("lspservice: build request: %w", err)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("lspservice: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lspservice: GET %s: unexpected status %d", path, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("lspservice: GET %s: decode: %w", path, err)
		}
	}
	return nil
}

// --- dashboard JSON-RPC proxy methods (NIP-98 authed) ---
//
// Each returns lspd's result JSON verbatim. Param shapes are replicated from
// lspd/dashboard (handlers.go, announce_state.go, node_select.go); not imported.
// Methods that select a node accept an optional node pubkey (hex); empty omits
// the selector, which lspd allows for a single-node fleet.

func (c *Client) GetSummary(ctx context.Context) (json.RawMessage, error) {
	return c.callRaw(ctx, "get_summary", nil)
}

func (c *Client) ListOrders(ctx context.Context) (json.RawMessage, error) {
	return c.callRaw(ctx, "list_orders", nil)
}

func (c *Client) GetOrderDetail(ctx context.Context, orderID string) (json.RawMessage, error) {
	params, err := json.Marshal(map[string]string{"order_id": orderID})
	if err != nil {
		return nil, err
	}
	return c.callRaw(ctx, "get_order_detail", params)
}

func (c *Client) ListPeers(ctx context.Context) (json.RawMessage, error) {
	return c.callRaw(ctx, "list_peers", nil)
}

func (c *Client) ListJitChannels(ctx context.Context) (json.RawMessage, error) {
	return c.callRaw(ctx, "list_jit_channels", nil)
}

func (c *Client) ListNodes(ctx context.Context) (json.RawMessage, error) {
	return c.callRaw(ctx, "list_nodes", nil)
}

func (c *Client) GetAnalytics(ctx context.Context) (json.RawMessage, error) {
	return c.callRaw(ctx, "get_analytics", nil)
}

func (c *Client) GetConfig(ctx context.Context, node string) (json.RawMessage, error) {
	return c.callRaw(ctx, "get_config", nodeParams(node))
}

// UpdateConfig forwards the caller-supplied params body verbatim ({config,node}).
func (c *Client) UpdateConfig(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	return c.callRaw(ctx, "update_config", params)
}

func (c *Client) GetAnnounceState(ctx context.Context, node string) (json.RawMessage, error) {
	return c.callRaw(ctx, "get_announce_state", nodeParams(node))
}

func (c *Client) SetAnnounceState(ctx context.Context, action, node string) (json.RawMessage, error) {
	m := map[string]string{"action": action}
	if node != "" {
		m["node"] = node
	}
	params, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return c.callRaw(ctx, "set_announce_state", params)
}

func (c *Client) RecheckAnnouncement(ctx context.Context, node string) (json.RawMessage, error) {
	return c.callRaw(ctx, "recheck_announcement", nodeParams(node))
}

// nodeParams builds the {"node": <hex>} selector, or nil when node is empty.
func nodeParams(node string) json.RawMessage {
	if node == "" {
		return nil
	}
	raw, _ := json.Marshal(map[string]string{"node": node})
	return raw
}
