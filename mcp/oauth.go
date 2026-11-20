package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth 2.1 for remote MCP (M5): Lokihub is both the Authorization Server and
// the Resource Server (permitted by the MCP authorization spec). The owner
// approves each client on Lokihub's own consent screen; the issued access token
// is the connection's bearer token, bound to one kind=mcp App (so revoking the
// App revokes the token) and to this hub (HMAC under the hub secret — a token
// from another hub never validates here, which is the RFC 8707 audience
// guarantee in practice; the resource parameter is additionally checked).

// OAuth errors (RFC 6749 §5.2 / §4.1.2.1 codes).
var (
	ErrOAuthInvalidClient  = errors.New("invalid_client")
	ErrOAuthInvalidRequest = errors.New("invalid_request")
	ErrOAuthInvalidGrant   = errors.New("invalid_grant")
	ErrOAuthInvalidTarget  = errors.New("invalid_target") // RFC 8707
)

const (
	authCodeTTL       = 60 * time.Second
	consentTTL        = 10 * time.Minute
	maxConsents       = 256
	maxConsentsPerCli = 5
	clientIdleTTL     = 24 * time.Hour
	unprovenClientTTL = 10 * time.Minute
	maxRedirectURIs   = 5
	maxClientNameLen  = 100
	maxOAuthClients   = 256 // bounded DCR store (M-DOS)
	codeChallengeS256 = "S256"
)

// OAuthClient is a dynamically registered client (RFC 7591).
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
	lastUsed     time.Time // last /authorize; idle clients are evictable
}

type consent struct {
	req      AuthorizeRequest
	resource string
	expires  time.Time
}

type authCode struct {
	clientID, redirectURI, challenge, resource string
	appID                                      uint
	expires                                    time.Time
}

// OAuthStore holds registered clients and pending authorization codes. It is
// in-memory: a restart drops registrations (clients re-register via DCR) and
// pending codes (60s anyway); issued tokens survive because they are App-bound.
type OAuthStore struct {
	mu       sync.Mutex
	clients  map[string]*OAuthClient
	codes    map[string]*authCode
	consents map[string]*consent
	now      func() time.Time
}

// NewOAuthStore builds an empty store.
func NewOAuthStore() *OAuthStore {
	return &OAuthStore{clients: map[string]*OAuthClient{}, codes: map[string]*authCode{}, consents: map[string]*consent{}, now: time.Now}
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// validRedirectURI enforces OAuth 2.1 redirect rules: https, or http only for a
// loopback host (native/desktop clients); no fragment.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	default:
		return false
	}
}

// Register performs dynamic client registration.
func (s *OAuthStore) Register(name string, redirectURIs []string) (*OAuthClient, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxClientNameLen {
		return nil, fmt.Errorf("%w: client_name required (max %d chars)", ErrOAuthInvalidRequest, maxClientNameLen)
	}
	if len(redirectURIs) == 0 || len(redirectURIs) > maxRedirectURIs {
		return nil, fmt.Errorf("%w: 1-%d redirect_uris required", ErrOAuthInvalidRequest, maxRedirectURIs)
	}
	for _, r := range redirectURIs {
		if !validRedirectURI(r) {
			return nil, fmt.Errorf("%w: redirect_uri %q must be https (or http on localhost)", ErrOAuthInvalidRequest, r)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.clients) >= maxOAuthClients {
		// Evict registrations that never (or no longer) start authorizations,
		// so the registry cannot be locked by squatting (audit MED-3).
		now := s.now()
		for id, c := range s.clients {
			// A client that never completed a token exchange (which needs the
			// owner's approval) is evictable after a short grace period; one
			// that did is kept until idle for clientIdleTTL (re-audit N2).
			switch {
			case c.lastUsed.IsZero() && now.Sub(c.CreatedAt) > unprovenClientTTL:
				delete(s.clients, id)
			case !c.lastUsed.IsZero() && now.Sub(c.lastUsed) > clientIdleTTL:
				delete(s.clients, id)
			}
		}
	}
	if len(s.clients) >= maxOAuthClients {
		return nil, fmt.Errorf("%w: too many registered clients", ErrOAuthInvalidRequest)
	}
	c := &OAuthClient{ID: "lkc_" + randToken(18), Name: name, RedirectURIs: append([]string(nil), redirectURIs...), CreatedAt: s.now()}
	s.clients[c.ID] = c
	return c, nil
}

// Client looks up a registered client.
func (s *OAuthStore) Client(id string) (*OAuthClient, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	return c, ok
}

// AuthorizeRequest is the validated set of /authorize parameters.
type AuthorizeRequest struct {
	ClientID, RedirectURI, State, Challenge, ChallengeMethod, Resource string
}

// ValidateAuthorize checks an authorization request before the owner sees the
// consent screen: known client, exact redirect match, PKCE S256 present, and the
// resource names this hub's MCP endpoint.
func (s *OAuthStore) ValidateAuthorize(r AuthorizeRequest, expectedResource string) (*OAuthClient, error) {
	c, ok := s.Client(r.ClientID)
	if !ok {
		return nil, ErrOAuthInvalidClient
	}
	if !containsExact(c.RedirectURIs, r.RedirectURI) {
		return nil, fmt.Errorf("%w: redirect_uri not registered for this client", ErrOAuthInvalidRequest)
	}
	if r.ChallengeMethod != codeChallengeS256 || len(r.Challenge) < 43 {
		return nil, fmt.Errorf("%w: PKCE with code_challenge_method=S256 is required", ErrOAuthInvalidRequest)
	}
	if r.Resource != "" && r.Resource != expectedResource {
		return nil, ErrOAuthInvalidTarget
	}
	return c, nil
}

// IssueCode mints a single-use authorization code after owner approval, bound to
// the client, redirect URI, PKCE challenge, resource and the approved App.
func (s *OAuthStore) IssueCode(r AuthorizeRequest, expectedResource string, appID uint) (string, error) {
	if _, err := s.ValidateAuthorize(r, expectedResource); err != nil {
		return "", err
	}
	code := "lka_" + randToken(24)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, c := range s.codes { // prune lapsed codes so the map stays bounded
		if now.After(c.expires) {
			delete(s.codes, k)
		}
	}
	s.codes[code] = &authCode{
		clientID: r.ClientID, redirectURI: r.RedirectURI, challenge: r.Challenge,
		resource: expectedResource, appID: appID, expires: s.now().Add(authCodeTTL),
	}
	return code, nil
}

// Exchange redeems a code (token endpoint, grant_type=authorization_code). The
// code is consumed on first use — a replayed code fails even if the first
// exchange did — and must match the client, redirect URI, PKCE verifier and
// resource it was issued for.
func (s *OAuthStore) Exchange(code, clientID, redirectURI, verifier, resource string) (uint, error) {
	s.mu.Lock()
	ac, ok := s.codes[code]
	delete(s.codes, code) // single use, whatever the outcome
	s.mu.Unlock()
	if !ok || s.now().After(ac.expires) {
		return 0, ErrOAuthInvalidGrant
	}
	if ac.clientID != clientID || ac.redirectURI != redirectURI {
		return 0, ErrOAuthInvalidGrant
	}
	if resource != "" && resource != ac.resource {
		return 0, ErrOAuthInvalidTarget
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if len(verifier) < 43 || subtle.ConstantTimeCompare([]byte(want), []byte(ac.challenge)) != 1 {
		return 0, ErrOAuthInvalidGrant
	}
	s.mu.Lock()
	if cl := s.clients[clientID]; cl != nil {
		cl.lastUsed = s.now() // proven: an owner approved this client
	}
	s.mu.Unlock()
	return ac.appID, nil
}

func containsExact(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// RequestOrigin is the externally visible scheme://host of a request (honouring
// X-Forwarded-Proto from the reverse proxy in front of the hub).
func RequestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

// ResourceURL is the canonical MCP resource identifier for a request's origin.
func ResourceURL(r *http.Request) string { return RequestOrigin(r) + "/mcp" }

// ProtectedResourceMetadata is the RFC 9728 document for /mcp.
func ProtectedResourceMetadata(r *http.Request) map[string]any {
	return map[string]any{
		"resource":                 ResourceURL(r),
		"authorization_servers":    []string{RequestOrigin(r)},
		"scopes_supported":         AllScopes(),
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Lokihub",
	}
}

// AuthorizationServerMetadata is the RFC 8414 document.
func AuthorizationServerMetadata(r *http.Request) map[string]any {
	o := RequestOrigin(r)
	return map[string]any{
		"issuer":                                o,
		"authorization_endpoint":                o + "/oauth/authorize",
		"token_endpoint":                        o + "/oauth/token",
		"registration_endpoint":                 o + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{codeChallengeS256},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      AllScopes(),
	}
}

// pkceChallenge computes the S256 challenge for a verifier (tests, clients).
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ConsentView is what the owner's consent screen shows: who is asking and where
// the result will be sent. It never carries secrets.
type ConsentView struct {
	ClientName   string `json:"clientName"`
	RedirectHost string `json:"redirectHost"`
}

// BeginConsent validates an authorization request (with the resource computed
// from the request that actually reached /oauth/authorize) and parks it under a
// random consent id. The consent screen refers to the id only, so the browser
// never gets to re-supply redirect_uri / resource / PKCE values.
func (s *OAuthStore) BeginConsent(r AuthorizeRequest, expectedResource string) (string, error) {
	if _, err := s.ValidateAuthorize(r, expectedResource); err != nil {
		return "", err
	}
	id := "lkn_" + randToken(18)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, c := range s.consents { // prune expired before bounding
		if now.After(c.expires) {
			delete(s.consents, k)
		}
	}
	if len(s.consents) >= maxConsents {
		// Evict the oldest pending consent rather than refusing: a flood can
		// then only push out other unanswered requests (which the user simply
		// restarts), never lock out a legitimate authorize (re-audit N2).
		var oldestK string
		var oldest time.Time
		for k, c := range s.consents {
			if oldestK == "" || c.expires.Before(oldest) {
				oldestK, oldest = k, c.expires
			}
		}
		delete(s.consents, oldestK)
	}
	perClient := 0
	for _, c := range s.consents {
		if c.req.ClientID == r.ClientID {
			perClient++
		}
	}
	if perClient >= maxConsentsPerCli {
		return "", fmt.Errorf("%w: too many pending authorizations for this client", ErrOAuthInvalidRequest)
	}

	s.consents[id] = &consent{req: r, resource: expectedResource, expires: now.Add(consentTTL)}
	return id, nil
}

// Consent returns the view for a pending consent.
func (s *OAuthStore) Consent(id string) (ConsentView, error) {
	s.mu.Lock()
	c, ok := s.consents[id]
	s.mu.Unlock()
	if !ok || s.now().After(c.expires) {
		return ConsentView{}, ErrOAuthInvalidRequest
	}
	cl, ok := s.Client(c.req.ClientID)
	if !ok {
		return ConsentView{}, ErrOAuthInvalidClient
	}
	host := c.req.RedirectURI
	if u, err := url.Parse(c.req.RedirectURI); err == nil {
		host = u.Host
	}
	return ConsentView{ClientName: cl.Name, RedirectHost: host}, nil
}

func (s *OAuthStore) takeConsent(id string) (*consent, error) {
	s.mu.Lock()
	c, ok := s.consents[id]
	delete(s.consents, id) // one decision per consent
	s.mu.Unlock()
	if !ok || s.now().After(c.expires) {
		return nil, ErrOAuthInvalidRequest
	}
	return c, nil
}

// ApproveConsent issues a code for the approved App and returns the client
// redirect (code + state + iss, RFC 9207).
func (s *OAuthStore) ApproveConsent(id string, appID uint, issuer string) (string, error) {
	c, err := s.takeConsent(id)
	if err != nil {
		return "", err
	}
	code, err := s.IssueCode(c.req, c.resource, appID)
	if err != nil {
		return "", err
	}
	return redirectWith(c.req.RedirectURI, url.Values{"code": {code}, "state": {c.req.State}, "iss": {issuer}}), nil
}

// DenyConsent returns the client redirect carrying error=access_denied.
func (s *OAuthStore) DenyConsent(id, issuer string) (string, error) {
	c, err := s.takeConsent(id)
	if err != nil {
		return "", err
	}
	return redirectWith(c.req.RedirectURI, url.Values{"error": {"access_denied"}, "state": {c.req.State}, "iss": {issuer}}), nil
}

func redirectWith(base string, v url.Values) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for k, vals := range v {
		if len(vals) > 0 && vals[0] != "" {
			q.Set(k, vals[0])
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
