//go:build integration

package mcp

// Live regtest integration for the MCP endpoint (M1 gate). Runs only with
// `-tags integration` and MCP_HUB_URL set (e.g. https://127.0.0.1:5611) against
// a testhooks hub (LOKIHUB_TEST_HOOKS=1) on regtest. Never point at mainnet.
//
//   MCP_HUB_URL=https://127.0.0.1:5611 MCP_HUB_PASSWORD=regtest-dev \
//     GOWORK=off go test -tags integration -run TestLive ./mcp/

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type liveHub struct {
	base, admin string
	hc          *http.Client
}

var (
	liveOnce   sync.Once
	liveShared *liveHub
)

// newLiveHub returns one shared admin session: the hub rate-limits /api/unlock
// (1 req/s per peer), so tests must not each re-unlock.
func newLiveHub(t *testing.T) *liveHub {
	t.Helper()
	if os.Getenv("MCP_HUB_URL") == "" {
		t.Skip("MCP_HUB_URL not set")
	}
	liveOnce.Do(func() { liveShared = unlockLiveHub(t) })
	if liveShared == nil || liveShared.admin == "" {
		t.Fatal("no live admin session")
	}
	return liveShared
}

func unlockLiveHub(t *testing.T) *liveHub {
	t.Helper()
	base := os.Getenv("MCP_HUB_URL")
	if base == "" {
		t.Skip("MCP_HUB_URL not set")
	}
	pw := os.Getenv("MCP_HUB_PASSWORD")
	if pw == "" {
		pw = "regtest-dev"
	}
	h := &liveHub{base: base, hc: &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // regtest self-signed
	}}
	var out struct {
		Token string `json:"token"`
	}
	h.do(t, http.MethodPost, "/api/unlock", "", map[string]any{"unlockPassword": pw, "permission": "full"}, &out)
	if out.Token == "" {
		t.Fatal("unlock returned no admin token")
	}
	h.admin = out.Token
	return h
}

func (h *liveHub) do(t *testing.T, method, path, bearer string, body, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s decode: %v (%s)", method, path, err, raw)
		}
	}
	return resp.StatusCode
}

// bearerRT injects the MCP bearer on every request the SDK client makes.
type bearerRT struct {
	tok  string
	next http.RoundTripper
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return b.next.RoundTrip(r)
}

func (h *liveHub) dial(t *testing.T, tok string) (*mcpsdk.ClientSession, error) {
	t.Helper()
	tr := &mcpsdk.StreamableClientTransport{
		Endpoint:   h.base + "/mcp",
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: bearerRT{tok: tok, next: h.hc.Transport}},
		MaxRetries: -1,
	}
	c := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "live-test", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Connect(ctx, tr, nil)
}

func TestLiveMCP(t *testing.T) {
	h := newLiveHub(t)

	// Mint a connection with Node + Wallet read only.
	var mint struct {
		AppID     uint   `json:"app_id"`
		AppPubkey string `json:"app_pubkey"`
		Token     string `json:"token"`
	}
	code := h.do(t, http.MethodPost, "/api/test/mcp/connection", h.admin, map[string]any{
		"name":   fmt.Sprintf("live-mcp-%d", time.Now().UnixNano()),
		"groups": map[string]string{"node": "read", "wallet": "read"},
	}, &mint)
	if code != http.StatusOK || mint.Token == "" {
		t.Fatalf("mint connection: HTTP %d %+v", code, mint)
	}

	// No token / bad token → 401 (M-BIND server half).
	if c := h.do(t, http.MethodPost, "/mcp", "", map[string]any{}, nil); c != http.StatusUnauthorized {
		t.Errorf("no-token /mcp = %d, want 401", c)
	}
	if c := h.do(t, http.MethodPost, "/mcp", "lkm_1_00", map[string]any{}, nil); c != http.StatusUnauthorized {
		t.Errorf("forged-token /mcp = %d, want 401", c)
	}
	// The owner's admin JWT must NOT be accepted at /mcp.
	if c := h.do(t, http.MethodPost, "/mcp", h.admin, map[string]any{}, nil); c != http.StatusUnauthorized {
		t.Errorf("owner JWT at /mcp = %d, want 401", c)
	}

	cs, err := h.dial(t, mint.Token)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx := context.Background()

	// Scope-filtered catalogue against the real hub.
	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range lt.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"get_info", "get_balances", "node_status"} {
		if !got[want] {
			t.Errorf("granted tool %q missing", want)
		}
	}
	for _, deny := range []string{"list_channels", "set_node_alias", "redeem_onchain_funds", "close_channel"} {
		if got[deny] {
			t.Errorf("ungranted tool %q advertised", deny)
		}
	}

	// Real reads against the hub core.
	for _, name := range []string{"get_info", "get_balances"} {
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name})
		if err != nil || res.IsError {
			t.Errorf("%s: err=%v res=%+v", name, err, res)
		} else if res.StructuredContent == nil {
			t.Errorf("%s returned no structuredContent", name)
		}
	}

	// Owner revokes (deletes the App) → the live session's next call fails, and
	// a fresh dial is refused.
	if c := h.do(t, http.MethodDelete, "/api/apps/"+mint.AppPubkey, h.admin, nil, nil); c >= 300 {
		t.Fatalf("revoke: HTTP %d", c)
	}
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_info"})
	if err == nil && (res == nil || !res.IsError) {
		t.Errorf("call after revoke succeeded: %+v", res)
	}
	_ = cs.Close()
	if c := h.do(t, http.MethodPost, "/mcp", mint.Token, map[string]any{}, nil); c != http.StatusUnauthorized {
		t.Errorf("revoked token /mcp = %d, want 401", c)
	}
}

// TestLiveMCPGuardAndPause: M3 owner levers on the real hub — a guarded redeem
// is queued (not executed) and visible in the owner's queue; deny makes it
// unapprovable; the persistent pause switch blocks a live session and unpause
// restores it.
func TestLiveMCPGuardAndPause(t *testing.T) {
	h := newLiveHub(t)
	var mint struct {
		AppPubkey string `json:"app_pubkey"`
		Token     string `json:"token"`
	}
	if c := h.do(t, http.MethodPost, "/api/test/mcp/connection", h.admin, map[string]any{
		"name":   fmt.Sprintf("live-guard-%d", time.Now().UnixNano()),
		"groups": map[string]string{"wallet": "write", "node": "read"},
	}, &mint); c != http.StatusOK {
		t.Fatalf("mint: HTTP %d", c)
	}
	defer h.do(t, http.MethodDelete, "/api/apps/"+mint.AppPubkey, h.admin, nil, nil)

	cs, err := h.dial(t, mint.Token)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	ctx := context.Background()

	key := fmt.Sprintf("live-redeem-%d", time.Now().UnixNano())
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "redeem_onchain_funds", Arguments: map[string]any{
		"idempotency_key": key, "to_address": "bcrt1qattackercontrolled000000000000000000000", "amount": 1000,
	}})
	if err != nil || res.IsError {
		t.Fatalf("guarded redeem call: err=%v res=%+v", err, res)
	}
	var pr struct {
		Status   string `json:"status"`
		ActionID string `json:"action_id"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &pr)
	if pr.Status != "pending_approval" || pr.ActionID == "" {
		t.Fatalf("redeem not queued: %+v", pr)
	}

	var queue []struct {
		ID, Status string
	}
	h.do(t, http.MethodGet, "/api/mcp/pending", h.admin, nil, &queue)
	found := false
	for _, q := range queue {
		if q.ID == pr.ActionID && q.Status == "open" {
			found = true
		}
	}
	if !found {
		t.Fatalf("queued action %s not in owner queue: %+v", pr.ActionID, queue)
	}
	if c := h.do(t, http.MethodPost, "/api/mcp/pending/"+pr.ActionID+"/deny", h.admin, nil, nil); c != http.StatusNoContent {
		t.Fatalf("deny: HTTP %d", c)
	}
	if c := h.do(t, http.MethodPost, "/api/mcp/pending/"+pr.ActionID+"/approve", h.admin, nil, nil); c != http.StatusConflict {
		t.Errorf("approve after deny = HTTP %d, want 409", c)
	}
	// The assistant's own token cannot reach the owner queue.
	if c := h.do(t, http.MethodGet, "/api/mcp/pending", mint.Token, nil, nil); c < 400 {
		t.Errorf("assistant token reached owner queue: HTTP %d", c)
	}

	// Pause (persistent) blocks the live session; unpause restores it.
	if c := h.do(t, http.MethodPut, "/api/mcp/controls", h.admin, map[string]any{"paused": true}, nil); c != http.StatusNoContent {
		t.Fatalf("pause: HTTP %d", c)
	}
	defer h.do(t, http.MethodPut, "/api/mcp/controls", h.admin, map[string]any{"paused": false}, nil)
	if res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_info"}); err == nil && !res.IsError {
		t.Error("call succeeded while paused")
	}
	h.do(t, http.MethodPut, "/api/mcp/controls", h.admin, map[string]any{"paused": false}, nil)
	if res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_info"}); err != nil || res.IsError {
		t.Errorf("call failed after unpause: err=%v res=%+v", err, res)
	}
}

// TestLiveOwnerConnectionAPI: the real (non-testhook) owner API the Connect-AI
// UI uses — create returns a working token; the list reports the right groups
// and a fresh lastUsedAt after use; revoke removes it from the list.
func TestLiveOwnerConnectionAPI(t *testing.T) {
	h := newLiveHub(t)
	name := fmt.Sprintf("owner-api-%d", time.Now().UnixNano())
	var created struct {
		AppID     uint   `json:"appId"`
		AppPubkey string `json:"appPubkey"`
		Token     string `json:"token"`
	}
	if c := h.do(t, http.MethodPost, "/api/mcp/connections", h.admin, map[string]any{
		"name": name, "groups": map[string]string{"channels": "write", "node": "read"},
	}, &created); c != http.StatusOK || created.Token == "" {
		t.Fatalf("create: HTTP %d %+v", c, created)
	}
	// Bad request: no groups.
	if c := h.do(t, http.MethodPost, "/api/mcp/connections", h.admin, map[string]any{"name": "x"}, nil); c != http.StatusBadRequest {
		t.Errorf("create with no groups = HTTP %d, want 400", c)
	}
	// The assistant's token cannot mint connections (owner-only).
	if c := h.do(t, http.MethodPost, "/api/mcp/connections", created.Token, map[string]any{
		"name": "escalate", "groups": map[string]string{"wallet": "write"},
	}, nil); c < 400 {
		t.Errorf("assistant token minted a connection: HTTP %d", c)
	}

	cs, err := h.dial(t, created.Token)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_info"}); err != nil || res.IsError {
		t.Fatalf("get_info: err=%v res=%+v", err, res)
	}
	_ = cs.Close()

	type view struct {
		AppID      uint              `json:"appId"`
		Groups     map[string]string `json:"groups"`
		LastUsedAt *time.Time        `json:"lastUsedAt"`
	}
	var list []view
	h.do(t, http.MethodGet, "/api/mcp/connections", h.admin, nil, &list)
	var mine *view
	for i := range list {
		if list[i].AppID == created.AppID {
			mine = &list[i]
		}
	}
	if mine == nil {
		t.Fatalf("created connection missing from list")
	}
	if mine.Groups["channels"] != "write" || mine.Groups["node"] != "read" || len(mine.Groups) != 2 {
		t.Errorf("groups = %v", mine.Groups)
	}
	if mine.LastUsedAt == nil {
		t.Error("lastUsedAt not set after use")
	}

	if c := h.do(t, http.MethodDelete, "/api/apps/"+created.AppPubkey, h.admin, nil, nil); c >= 300 {
		t.Fatalf("revoke: HTTP %d", c)
	}
	list = nil
	h.do(t, http.MethodGet, "/api/mcp/connections", h.admin, nil, &list)
	for _, v := range list {
		if v.AppID == created.AppID {
			t.Error("revoked connection still listed")
		}
	}
}

// TestLiveOAuth: a remote client connecting by URL alone — discovery from the
// 401 challenge, DCR, authorize w/ PKCE S256 → owner consent → code → token →
// /mcp. Plus M-OAUTH attacks: code replay, wrong verifier, foreign resource,
// unregistered redirect, and no open redirect for an unknown client.
func TestLiveOAuth(t *testing.T) {
	h := newLiveHub(t)
	noFollow := &http.Client{Transport: h.hc.Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// 1. Discovery: the unauthenticated 401 points at the PRM document.
	resp, err := noFollow.Post(h.base+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("401 lacks resource_metadata: %q", resp.Header.Get("WWW-Authenticate"))
	}
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	h.do(t, http.MethodGet, "/.well-known/oauth-protected-resource/mcp", "", nil, &prm)
	var as struct {
		Registration string   `json:"registration_endpoint"`
		PKCE         []string `json:"code_challenge_methods_supported"`
	}
	h.do(t, http.MethodGet, "/.well-known/oauth-authorization-server", "", nil, &as)
	if prm.Resource == "" || len(as.PKCE) != 1 || as.PKCE[0] != "S256" || as.Registration == "" {
		t.Fatalf("metadata: prm=%+v as=%+v", prm, as)
	}

	// 2. DCR.
	redirect := "http://127.0.0.1:53682/callback"
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if c := h.do(t, http.MethodPost, "/oauth/register", "", map[string]any{
		"client_name": "Live OAuth client", "redirect_uris": []string{redirect},
	}, &reg); c != http.StatusCreated || reg.ClientID == "" {
		t.Fatalf("register: HTTP %d %+v", c, reg)
	}
	time.Sleep(600 * time.Millisecond) // /oauth/* is rate-limited per peer

	verifier := "live-verifier-" + strings.Repeat("v", 40)
	authorize := func(clientID, redir, res string) *http.Response {
		time.Sleep(600 * time.Millisecond) // /oauth/* shares a 2 req/s per-peer limit
		q := url.Values{
			"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redir},
			"state": {"st-1"}, "code_challenge": {pkceChallenge(verifier)},
			"code_challenge_method": {"S256"}, "resource": {res},
		}
		r, err := noFollow.Get(h.base + "/oauth/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r
	}

	// Attacks on /authorize: never a redirect to an unverified destination.
	if r := authorize("lkc_unknown", redirect, prm.Resource); r.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown client: HTTP %d (want 400, no redirect)", r.StatusCode)
	}
	if r := authorize(reg.ClientID, "https://evil.example/cb", prm.Resource); r.StatusCode != http.StatusBadRequest {
		t.Errorf("unregistered redirect: HTTP %d (want 400)", r.StatusCode)
	}
	if r := authorize(reg.ClientID, redirect, "https://other-hub.example/mcp"); r.StatusCode != http.StatusBadRequest {
		t.Errorf("foreign resource: HTTP %d (want 400)", r.StatusCode)
	}

	// 3. Authorize → consent screen.
	r := authorize(reg.ClientID, redirect, prm.Resource)
	loc := r.Header.Get("Location")
	if r.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "/connect-assistant?consent=") {
		t.Fatalf("authorize: HTTP %d Location %q", r.StatusCode, loc)
	}
	consent := strings.TrimPrefix(loc, "/connect-assistant?consent=")
	var view struct {
		ClientName, RedirectHost string
	}
	h.do(t, http.MethodGet, "/api/mcp/oauth/consent/"+consent, h.admin, nil, &view)
	if view.ClientName != "Live OAuth client" || view.RedirectHost != "127.0.0.1:53682" {
		t.Errorf("consent view = %+v", view)
	}
	// The assistant side (no admin JWT) cannot approve its own request.
	if c := h.do(t, http.MethodPost, "/api/mcp/oauth/consent/"+consent+"/approve", "", map[string]any{
		"name": "self-approve", "groups": map[string]string{"wallet": "write"},
	}, nil); c < 400 {
		t.Fatalf("unauthenticated self-approval accepted: HTTP %d", c)
	}

	// 4. Owner approves (read-only node).
	var appr struct {
		Redirect string `json:"redirect"`
	}
	if c := h.do(t, http.MethodPost, "/api/mcp/oauth/consent/"+consent+"/approve", h.admin, map[string]any{
		"name": "Live OAuth client", "groups": map[string]string{"node": "read"},
	}, &appr); c != http.StatusOK {
		t.Fatalf("approve: HTTP %d", c)
	}
	ru, _ := url.Parse(appr.Redirect)
	code := ru.Query().Get("code")
	if !strings.HasPrefix(appr.Redirect, redirect) || code == "" || ru.Query().Get("state") != "st-1" {
		t.Fatalf("approve redirect = %q", appr.Redirect)
	}

	token := func(code, verifier, res string) (int, map[string]any) {
		time.Sleep(600 * time.Millisecond)
		form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {reg.ClientID},
			"redirect_uri": {redirect}, "code_verifier": {verifier}, "resource": {res}}
		resp, err := h.hc.PostForm(h.base+"/oauth/token", form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	// Wrong verifier burns the code (single use) — a fresh code is needed after.
	if c, m := token(code, "wrong-"+strings.Repeat("w", 50), prm.Resource); c != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: HTTP %d %v", c, m)
	}
	if c, _ := token(code, verifier, prm.Resource); c != http.StatusBadRequest {
		t.Errorf("code reuse after failed attempt: HTTP %d (want 400)", c)
	}

	// Fresh round: authorize → approve → exchange.
	time.Sleep(600 * time.Millisecond)
	r = authorize(reg.ClientID, redirect, prm.Resource)
	consent = strings.TrimPrefix(r.Header.Get("Location"), "/connect-assistant?consent=")
	h.do(t, http.MethodPost, "/api/mcp/oauth/consent/"+consent+"/approve", h.admin, map[string]any{
		"name": "Live OAuth client", "groups": map[string]string{"node": "read"},
	}, &appr)
	ru, _ = url.Parse(appr.Redirect)
	code = ru.Query().Get("code")
	c, tok := token(code, verifier, prm.Resource)
	access, _ := tok["access_token"].(string)
	if c != http.StatusOK || access == "" || tok["token_type"] != "Bearer" {
		t.Fatalf("token: HTTP %d %v", c, tok)
	}
	if c, _ := token(code, verifier, prm.Resource); c != http.StatusBadRequest {
		t.Errorf("code replay after success: HTTP %d (want 400)", c)
	}

	// 5. The OAuth access token works at /mcp with exactly the approved scope.
	cs, err := h.dial(t, access)
	if err != nil {
		t.Fatalf("dial with OAuth token: %v", err)
	}
	defer cs.Close()
	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range lt.Tools {
		names[tl.Name] = true
	}
	if !names["get_info"] || names["get_balances"] || names["send_payment"] {
		t.Errorf("OAuth connection catalogue not limited to node:read: %v", names)
	}
	if res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_info"}); err != nil || res.IsError {
		t.Errorf("get_info with OAuth token: err=%v res=%+v", err, res)
	}

	// Cleanup: revoke the OAuth-created connections.
	var list []struct {
		AppPubkey, Name string
	}
	h.do(t, http.MethodGet, "/api/mcp/connections", h.admin, nil, &list)
	for _, l := range list {
		if l.Name == "Live OAuth client" {
			h.do(t, http.MethodDelete, "/api/apps/"+l.AppPubkey, h.admin, nil, nil)
		}
	}
}
