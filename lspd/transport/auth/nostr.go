package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

const (
	KindHttpAuth = 27235

	// authValidityWindowSeconds must match the +/-60s "Verify Timestamp"
	// check below. replayCacheTTLSeconds is kept longer than double that
	// window so a seen event can never expire from the cache while its
	// timestamp would still otherwise be accepted.
	authValidityWindowSeconds = 60
	replayCacheTTLSeconds     = 3 * authValidityWindowSeconds
)

// seenEvents is a short-lived replay cache: NIP-98 auth events are only
// valid for a narrow timestamp window, so remembering event IDs a bit
// longer than that window is enough to reject replays without unbounded
// growth. Keyed by the event's recomputed canonical ID (event.GetID()),
// never the client-supplied ID field — go-nostr's CheckSignature does not
// itself validate that field against the signed content, so trusting it
// here would let an attacker dodge the replay cache by mutating it.
var (
	seenEventsMu sync.Mutex
	seenEvents   = make(map[string]int64)
)

func checkAndRecordReplay(eventID string, nowUnix int64) error {
	seenEventsMu.Lock()
	defer seenEventsMu.Unlock()

	for id, seenAt := range seenEvents {
		if nowUnix-seenAt > replayCacheTTLSeconds {
			delete(seenEvents, id)
		}
	}

	if _, exists := seenEvents[eventID]; exists {
		return fmt.Errorf("event already used (replay)")
	}
	seenEvents[eventID] = nowUnix
	return nil
}

// ReplayStore remembers NIP-98 auth event ids so a captured event cannot be
// replayed within its freshness window. The in-memory default (memReplayStore)
// is per-process, so it cannot stop a replay against a freshly restarted
// process (empty map) or a second replica (its own map); a durable, shared
// implementation -- e.g. a DB, mirroring the NIP-CASH served-nonce store --
// closes that gap, which matters once the Hub fronts lspd or it scales out
// (FLSPD-0016 / R-LSP-NIP98REPLAY).
//
// It must be fail-closed: a backend that cannot answer has to reject (return
// an error), never admit.
type ReplayStore interface {
	// CheckAndRecord records eventID as used at nowUnix and returns an error
	// if it was already used (a replay) or could not be recorded.
	CheckAndRecord(ctx context.Context, eventID string, nowUnix int64) error
}

// memReplayStore is the per-process in-memory fallback used when
// Options.ReplayStore is nil (tests, or a single-process deployment). It
// wraps the package-global seenEvents cache.
type memReplayStore struct{}

func (memReplayStore) CheckAndRecord(_ context.Context, eventID string, nowUnix int64) error {
	return checkAndRecordReplay(eventID, nowUnix)
}

// defaultReplayStore backs replay protection when Options.ReplayStore is nil.
// Production wires a durable, shared store (see postgres.Nip98ReplayStore).
var defaultReplayStore ReplayStore = memReplayStore{}

// DefaultMaxBodyBytes caps the body read for the payload hash when Options
// sets no cap; dashboard JSON-RPC requests are a few KiB.
const DefaultMaxBodyBytes int64 = 1 << 20

var (
	// ErrForbidden means the event is valid but its signer is not Options.PubKey.
	ErrForbidden = errors.New("signer is not allowed")
	// ErrBodyTooLarge means the body is longer than the cap.
	ErrBodyTooLarge = errors.New("request body too large")
)

// Options says whose events are accepted and how much body is read.
type Options struct {
	// PubKey, when set, is the only accepted signer; it is checked before
	// the body is read, so other keys cannot make the server buffer a body.
	PubKey string
	// MaxBodyBytes caps the body read to check the payload hash (0 means
	// DefaultMaxBodyBytes).
	MaxBodyBytes int64
	// AllowQueryToken also accepts the event in the "auth" query parameter.
	// Only browser WebSocket upgrades need it, since they cannot set headers;
	// elsewhere it would put tokens in proxy logs.
	AllowQueryToken bool
	// ReplayStore records used event ids for replay protection. When nil, the
	// per-process in-memory default is used; production should set a durable,
	// shared store so a captured event cannot be replayed across a restart or
	// a second replica (FLSPD-0016).
	ReplayStore ReplayStore
	// ExpectedHost, when set, is the authoritative public host used to build
	// the URL the NIP-98 'u' tag is matched against; any forwarded Host header
	// is ignored. ExpectedScheme pins the scheme ("http"/"https") the same
	// way. Pinning makes the 'u'-tag binding independent of attacker-settable
	// headers (A4-FL-3).
	ExpectedHost   string
	ExpectedScheme string
	// TrustedProxies are the networks whose X-Forwarded-*/X-Original-* headers
	// are honored. A request whose immediate peer (RemoteAddr) is not in this
	// set has those headers ignored, so a client talking to lspd directly
	// cannot forge the host/scheme/prefix the 'u' tag is checked against
	// (A4-FL-3). Empty: no proxy is trusted.
	TrustedProxies []*net.IPNet
}

// ParseTrustedProxies turns CIDRs or bare IPs into the networks for
// Options.TrustedProxies. A bare IP becomes a /32 (IPv4) or /128 (IPv6).
// Blank entries are skipped.
func ParseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			nets = append(nets, n)
			continue
		}
		ip := net.ParseIP(e)
		if ip == nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: not a CIDR or IP", e)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return nets, nil
}

// remoteIsTrustedProxy reports whether r's immediate peer (RemoteAddr) is in
// the trusted-proxy set; only then are forwarded headers honored.
func remoteIsTrustedProxy(r *http.Request, trusted []*net.IPNet) bool {
	if len(trusted) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr // may already be a bare IP
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// NostrAuth represents the authenticated session
type NostrAuth struct {
	PubKey string
}

// VerifyNostrAuth verifies a NIP-98 authentication header.
// Expected header: "Authorization: Nostr <base64_event>"
//
// Checks run cheapest first: kind, signature, timestamp, u/m tags, the
// allowed signer, then the (capped) body against the payload tag. The event
// enters the replay cache only once every check passed, so a captured token
// cannot be burnt by sending it with a wrong URL or body.
func VerifyNostrAuth(r *http.Request, opts Options) (*NostrAuth, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" && opts.AllowQueryToken {
		authHeader = queryToken(r)
		// The query form may omit the "Nostr " prefix.
		if authHeader != "" && !strings.HasPrefix(strings.ToLower(authHeader), "nostr ") {
			authHeader = "Nostr " + authHeader
		}
	}
	if authHeader == "" {
		return nil, fmt.Errorf("missing Authorization header")
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || strings.ToLower(parts[0]) != "nostr" {
		return nil, fmt.Errorf("invalid Authorization header format")
	}

	eventJson, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 event: %v", err)
	}

	var event nostr.Event
	if err := json.Unmarshal(eventJson, &event); err != nil {
		return nil, fmt.Errorf("failed to unmarshal nostr event: %v", err)
	}

	// 1. Verify Kind
	if event.Kind != KindHttpAuth {
		return nil, fmt.Errorf("invalid event kind: %d", event.Kind)
	}

	// 2. Verify Signature
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		return nil, fmt.Errorf("invalid event signature")
	}

	// 3. Verify Timestamp (+/- 60s)
	now := time.Now().Unix()
	if int64(event.CreatedAt) < now-authValidityWindowSeconds || int64(event.CreatedAt) > now+authValidityWindowSeconds {
		return nil, fmt.Errorf("event timestamp expired or too far in future")
	}

	// 4. Verify URL and Method
	var urlOk, methodOk bool
	fullUrl := getFullUrl(r, opts)

	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "u":
			// Full match or match with scheme indifference (http vs https)
			if tag[1] == fullUrl {
				urlOk = true
			} else {
				// Also handle the case where the only difference is the protocol scheme (e.g. http vs https)
				// This is common behind proxies that terminate TLS but don't forward it correctly.
				s1 := strings.TrimPrefix(tag[1], "https://")
				s1 = strings.TrimPrefix(s1, "http://")
				s2 := strings.TrimPrefix(fullUrl, "https://")
				s2 = strings.TrimPrefix(s2, "http://")
				if s1 == s2 {
					urlOk = true
				}
			}
		case "m":
			if strings.EqualFold(tag[1], r.Method) {
				methodOk = true
			}
		}
	}

	if !urlOk {
		return nil, fmt.Errorf("event 'u' tag mismatch: expected %s, got %v", fullUrl, event.Tags.GetFirst([]string{"u"}))
	}
	if !methodOk {
		return nil, fmt.Errorf("event 'm' tag mismatch: expected %s", r.Method)
	}

	// 5. The signer must be the allowed key before any body is read.
	if opts.PubKey != "" && event.PubKey != opts.PubKey {
		return nil, fmt.Errorf("%w: %s", ErrForbidden, event.PubKey)
	}

	// 6. Verify Payload Hash for methods that carry a body. Without this, a
	// validly signed auth event for one request body could be replayed
	// (within the timestamp window, and before the same event ID was seen)
	// against a different body on the same URL/method.
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		maxBody := opts.MaxBodyBytes
		if maxBody <= 0 {
			maxBody = DefaultMaxBodyBytes
		}
		if r.ContentLength > maxBody {
			return nil, ErrBodyTooLarge
		}
		bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return nil, ErrBodyTooLarge
			}
			return nil, fmt.Errorf("failed to read request body: %v", err)
		}
		if int64(len(bodyBytes)) > maxBody {
			return nil, ErrBodyTooLarge
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		if len(bodyBytes) > 0 {
			expectedHash := ComputePayloadHash(bodyBytes)
			payloadTag := event.Tags.Find("payload")
			if len(payloadTag) < 2 || payloadTag[1] != expectedHash {
				return nil, fmt.Errorf("event 'payload' tag mismatch or missing")
			}
		}
	}

	// 7. Verify this exact event hasn't been used before (replay
	// protection). GetID() recomputes the ID from the signed content, so it
	// can't be spoofed by the client. The store is consulted only once every
	// other check passed, so a token sent with a wrong URL or body is not
	// recorded (and so cannot be burnt for the real request).
	store := opts.ReplayStore
	if store == nil {
		store = defaultReplayStore
	}
	if err := store.CheckAndRecord(r.Context(), event.GetID(), now); err != nil {
		return nil, err
	}

	return &NostrAuth{
		PubKey: event.PubKey,
	}, nil
}

// queryToken returns the "auth" query parameter, also when a proxy left
// r.URL.RawQuery empty on an upgrade but kept it in RequestURI.
func queryToken(r *http.Request) string {
	if v := r.URL.Query().Get("auth"); v != "" {
		return v
	}
	_, rawQuery, found := strings.Cut(r.RequestURI, "?")
	if !found {
		return ""
	}
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	return vals.Get("auth")
}

// getFullUrl derives the public request URL that the NIP-98 'u' tag is
// matched against. Forwarded headers (X-Forwarded-*, X-Original-*) are
// attacker-settable unless a trusted proxy put them there, so they are
// honored only when the request's immediate peer is in opts.TrustedProxies;
// a pinned opts.ExpectedHost/ExpectedScheme always wins. A client talking to
// lspd directly therefore cannot forge the host/scheme/prefix the 'u' tag is
// checked against (A4-FL-3).
func getFullUrl(r *http.Request, opts Options) string {
	trusted := remoteIsTrustedProxy(r, opts.TrustedProxies)

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if trusted {
		// Respect common proxy/load balancer forwarded headers.
		if xForwardedProto := r.Header.Get("X-Forwarded-Proto"); xForwardedProto != "" {
			scheme = xForwardedProto
		} else if xForwardedSsl := r.Header.Get("X-Forwarded-Ssl"); xForwardedSsl == "on" {
			scheme = "https"
		} else if r.Header.Get("Front-End-Https") == "on" || r.Header.Get("X-Forwarded-Protocol") == "https" {
			scheme = "https"
		}
		// Prefer our custom X-Original-Proto if set by the Cloudflare Worker.
		if xOrigProto := r.Header.Get("X-Original-Proto"); xOrigProto != "" {
			scheme = xOrigProto
		}
	}
	if opts.ExpectedScheme != "" {
		scheme = opts.ExpectedScheme
	}

	host := r.Host
	if trusted {
		if xForwardedHost := r.Header.Get("X-Forwarded-Host"); xForwardedHost != "" {
			host = xForwardedHost
		}
		// Prefer our custom X-Original-Host if set by the Cloudflare Worker.
		if xOrigHost := r.Header.Get("X-Original-Host"); xOrigHost != "" {
			host = xOrigHost
		}
	}
	if opts.ExpectedHost != "" {
		host = opts.ExpectedHost
	}

	path := r.URL.Path
	if trusted {
		// Traefik appends X-Forwarded-Prefix when using stripprefix (e.g. /dashboard).
		if xForwardedPrefix := r.Header.Get("X-Forwarded-Prefix"); xForwardedPrefix != "" {
			path = xForwardedPrefix + path
		}
	}

	// Normalize WS/WSS to HTTP/HTTPS for signature comparison
	// Some clients sign WSS events with HTTPS because they construct the URL generically
	if scheme == "ws" {
		scheme = "http"
	} else if scheme == "wss" {
		scheme = "https"
	}

	result := fmt.Sprintf("%s://%s%s", scheme, host, path)
	return strings.TrimSuffix(result, "/")
}

// ComputePayloadHash computes the SHA256 hex hash of a request body
func ComputePayloadHash(body []byte) string {
	hash := sha256.Sum256(body)
	return fmt.Sprintf("%x", hash)
}
