package mcp

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk-abcdefgh" // >=43 chars

func authReq(c *OAuthClient) AuthorizeRequest {
	return AuthorizeRequest{
		ClientID: c.ID, RedirectURI: c.RedirectURIs[0], State: "st",
		Challenge: pkceChallenge(testVerifier), ChallengeMethod: "S256",
		Resource: "https://hub.example/mcp",
	}
}

func TestOAuthRegisterValidation(t *testing.T) {
	s := NewOAuthStore()
	for _, bad := range [][]string{
		nil,
		{"http://evil.example/cb"},    // plain http off-loopback
		{"https://x.example/cb#frag"}, // fragment
		{"javascript:alert(1)"},       // scheme
		{"https://a/1", "https://a/2", "https://a/3", "https://a/4", "https://a/5", "https://a/6"}, // too many
	} {
		if _, err := s.Register("c", bad); err == nil {
			t.Errorf("Register accepted bad redirect_uris %v", bad)
		}
	}
	if _, err := s.Register("", []string{"https://ok.example/cb"}); err == nil {
		t.Error("empty client_name accepted")
	}
	for _, good := range []string{"https://ok.example/cb", "http://localhost:33418/callback", "http://127.0.0.1:8080/cb"} {
		if _, err := s.Register("c", []string{good}); err != nil {
			t.Errorf("Register rejected valid %q: %v", good, err)
		}
	}
}

func TestOAuthCodeFlow(t *testing.T) {
	s := NewOAuthStore()
	c, _ := s.Register("assistant", []string{"https://client.example/cb"})
	res := "https://hub.example/mcp"

	code, err := s.IssueCode(authReq(c), res, 42)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Exchange(code, c.ID, c.RedirectURIs[0], testVerifier, res)
	if err != nil || id != 42 {
		t.Fatalf("exchange = (%d,%v), want (42,nil)", id, err)
	}
	// Single use: replay fails.
	if _, err := s.Exchange(code, c.ID, c.RedirectURIs[0], testVerifier, res); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Errorf("code replay err = %v, want invalid_grant", err)
	}
}

func TestOAuthExchangeRejects(t *testing.T) {
	s := NewOAuthStore()
	c, _ := s.Register("assistant", []string{"https://client.example/cb"})
	other, _ := s.Register("other", []string{"https://other.example/cb"})
	res := "https://hub.example/mcp"
	cases := []struct {
		name                             string
		clientID, redirect, verifier, rs string
		want                             error
	}{
		{"wrong verifier (PKCE)", c.ID, c.RedirectURIs[0], strings.Repeat("x", 50), res, ErrOAuthInvalidGrant},
		{"short verifier", c.ID, c.RedirectURIs[0], "short", res, ErrOAuthInvalidGrant},
		{"other client (confused deputy)", other.ID, c.RedirectURIs[0], testVerifier, res, ErrOAuthInvalidGrant},
		{"redirect mismatch", c.ID, "https://client.example/cb2", testVerifier, res, ErrOAuthInvalidGrant},
		{"other hub resource", c.ID, c.RedirectURIs[0], testVerifier, "https://other-hub.example/mcp", ErrOAuthInvalidTarget},
	}
	for _, tc := range cases {
		code, err := s.IssueCode(authReq(c), res, 7)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Exchange(code, tc.clientID, tc.redirect, tc.verifier, tc.rs); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	// Expired code.
	code, _ := s.IssueCode(authReq(c), res, 7)
	s.now = func() time.Time { return time.Now().Add(2 * authCodeTTL) }
	if _, err := s.Exchange(code, c.ID, c.RedirectURIs[0], testVerifier, res); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Errorf("expired code err = %v", err)
	}
}

func TestOAuthAuthorizeValidation(t *testing.T) {
	s := NewOAuthStore()
	c, _ := s.Register("assistant", []string{"https://client.example/cb"})
	res := "https://hub.example/mcp"
	r := authReq(c)
	r.ChallengeMethod = "plain"
	if _, err := s.ValidateAuthorize(r, res); err == nil {
		t.Error("plain PKCE accepted")
	}
	r = authReq(c)
	r.Challenge = ""
	if _, err := s.ValidateAuthorize(r, res); err == nil {
		t.Error("missing PKCE accepted")
	}
	r = authReq(c)
	r.ClientID = "lkc_unknown"
	if _, err := s.ValidateAuthorize(r, res); !errors.Is(err, ErrOAuthInvalidClient) {
		t.Error("unknown client accepted")
	}
	r = authReq(c)
	r.Resource = "https://evil.example/mcp"
	if _, err := s.ValidateAuthorize(r, res); !errors.Is(err, ErrOAuthInvalidTarget) {
		t.Error("foreign resource accepted")
	}
}

// TestUnauthorizedChallenge: the 401 carries the RFC 9728 resource_metadata
// pointer at the request's own origin (proxy-aware).
func TestUnauthorizedChallenge(t *testing.T) {
	req := httptest.NewRequest("POST", "http://hub.example/mcp", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	unauthorized(rec, req)
	got := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(got, `resource_metadata="https://hub.example/.well-known/oauth-protected-resource/mcp"`) {
		t.Errorf("WWW-Authenticate = %q", got)
	}
}

func TestOAuthConsentFlow(t *testing.T) {
	s := NewOAuthStore()
	c, _ := s.Register("Assistant X", []string{"https://client.example/cb?keep=1"})
	res := "https://hub.example/mcp"
	id, err := s.BeginConsent(authReq(c), res)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Consent(id)
	if err != nil || v.ClientName != "Assistant X" || v.RedirectHost != "client.example" {
		t.Fatalf("consent view = %+v, %v", v, err)
	}
	redirect, err := s.ApproveConsent(id, 9, "https://hub.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(redirect, "https://client.example/cb?") || !strings.Contains(redirect, "keep=1") ||
		!strings.Contains(redirect, "state=st") || !strings.Contains(redirect, "code=lka_") {
		t.Errorf("redirect = %q", redirect)
	}
	// One decision per consent.
	if _, err := s.ApproveConsent(id, 9, "x"); err == nil {
		t.Error("consent approved twice")
	}
	// The issued code redeems for the approved App.
	code := redirect[strings.Index(redirect, "code=")+5:]
	if i := strings.IndexByte(code, '&'); i >= 0 {
		code = code[:i]
	}
	if app, err := s.Exchange(code, c.ID, c.RedirectURIs[0], testVerifier, res); err != nil || app != 9 {
		t.Errorf("exchange after consent = (%d,%v)", app, err)
	}

	id2, _ := s.BeginConsent(authReq(c), res)
	deny, err := s.DenyConsent(id2, "https://hub.example")
	if err != nil || !strings.Contains(deny, "error=access_denied") {
		t.Errorf("deny redirect = %q, %v", deny, err)
	}
	if _, err := s.Consent("lkn_unknown"); err == nil {
		t.Error("unknown consent id accepted")
	}
}

// TestOAuthStoreBounds (audit MED-3, pentest A F1/F2): the per-client consent
// cap replaces the oldest instead of locking the client out; a registration
// flood never locks a newcomer out; one IP cannot hold more than
// maxUnprovenPerIP unproven registrations; owner-approved (proven) clients are
// never evicted by a flood; hostile names and redirect URIs are refused.
func TestOAuthStoreBounds(t *testing.T) {
	s := NewOAuthStore()
	c, _ := s.Register("a", []string{"https://client.example/cb"})
	res := "https://hub.example/mcp"
	first, _ := s.BeginConsent(authReq(c), res)
	for i := 0; i < maxConsentsPerCli; i++ {
		if _, err := s.BeginConsent(authReq(c), res); err != nil {
			t.Fatalf("consent %d refused: %v", i, err)
		}
	}
	if _, err := s.Consent(first); err == nil {
		t.Error("oldest consent for the client was not replaced")
	}
	n := 0
	for _, cs := range s.consents {
		if cs.req.ClientID == c.ID {
			n++
		}
	}
	if n > maxConsentsPerCli {
		t.Errorf("client holds %d consents, cap %d", n, maxConsentsPerCli)
	}

	// One flooding IP keeps at most maxUnprovenPerIP registrations.
	s2 := NewOAuthStore()
	for i := 0; i < 100; i++ {
		if _, err := s2.RegisterFrom("squat", []string{"https://x.example/cb"}, "6.6.6.6"); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	if len(s2.clients) > maxUnprovenPerIP {
		t.Errorf("one IP holds %d unproven registrations (cap %d)", len(s2.clients), maxUnprovenPerIP)
	}

	// Many IPs fill the registry: a newcomer still gets in, and a proven
	// (owner-approved) client survives the flood.
	s3 := NewOAuthStore()
	proven, _ := s3.Register("trusted", []string{"https://t.example/cb"})
	proven.lastUsed = time.Now()
	for i := 0; i < maxOAuthClients*2; i++ {
		_, _ = s3.RegisterFrom("flood", []string{"https://x.example/cb"}, fmt.Sprintf("10.0.%d.%d", i/250, i%250))
	}
	if _, err := s3.RegisterFrom("legit", []string{"https://y.example/cb"}, "1.2.3.4"); err != nil {
		t.Errorf("newcomer locked out by flood: %v", err)
	}
	if _, ok := s3.Client(proven.ID); !ok {
		t.Error("proven client evicted by a registration flood")
	}
	if len(s3.clients) > maxOAuthClients {
		t.Errorf("registry grew to %d", len(s3.clients))
	}

	// Hostile names and redirect URIs.
	for _, name := range []string{"Claude\u202egnp.exe", "Desk assistant\u200b", "a\x00b", "x\nSYSTEM"} {
		if _, err := s.Register(name, []string{"https://ok.example/cb"}); err == nil {
			t.Errorf("hostile client_name accepted: %q", name)
		}
	}
	if _, err := s.Register(strings.Repeat("é", 60), []string{"https://ok.example/cb"}); err != nil {
		t.Errorf("60-char accented name refused (limit must count characters): %v", err)
	}
	for _, u := range []string{"https://user:pw@evil.example/cb", "https://evil.example/cb#", "https://\u0430nthropic.com/cb", "http://127.0.0.1:99999/cb", "https://trusted.example@evil.example/cb"} {
		if _, err := s.Register("ok", []string{u}); err == nil {
			t.Errorf("hostile redirect_uri accepted: %q", u)
		}
	}
}

// TestConsentFloodSparesOwner (pentest R2-3 MED-1): flooding sources only
// displace their own pending consents; the owner's survives.
func TestConsentFloodSparesOwner(t *testing.T) {
	s := NewOAuthStore()
	res := "https://hub.example/mcp"
	owner, _ := s.RegisterFrom("Desk", []string{"https://legit.example/cb"}, "1.1.1.1")
	ownerConsent, err := s.BeginConsentFrom(authReq(owner), res, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	for ip := 0; ip < 40; ip++ { // 40 flooding addresses
		addr := fmt.Sprintf("10.9.0.%d", ip)
		for c := 0; c < 3; c++ {
			cl, _ := s.RegisterFrom("x", []string{"https://x.example/cb"}, addr)
			for i := 0; i < 10; i++ {
				_, _ = s.BeginConsentFrom(authReq(cl), res, addr)
			}
		}
	}
	if _, err := s.Consent(ownerConsent); err != nil {
		t.Fatal("flood pushed out the owner's pending consent")
	}
	per := map[string]int{}
	for _, c := range s.consents {
		per[c.fromIP]++
	}
	for ip, n := range per {
		if n > maxConsentsPerIP {
			t.Errorf("source %s holds %d consents (cap %d)", ip, n, maxConsentsPerIP)
		}
	}
	for _, u := range []string{"https://claude.ai;evil.com/cb", "https://claude.ai,evil.com/cb"} {
		if _, err := s.Register("ok", []string{u}); err == nil {
			t.Errorf("odd host syntax accepted: %s", u)
		}
	}
}
