//go:build integration

// Package nwcclient is a minimal, real NIP-47 (Nostr Wallet Connect) client
// used by the integration test suite to drive real, already-running
// lokihub parent/child connections exactly as an external NWC client would —
// over a real relay, with real NIP-44 encryption. It intentionally reuses
// the same building blocks the server itself uses (nip47/cipher,
// nip47/models) rather than reimplementing NIP-47 encoding from scratch.
package nwcclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
)

// DefaultCallTimeout bounds how long Call waits for a response event before
// giving up.
const DefaultCallTimeout = 30 * time.Second

// NWCError is returned by Call when the wallet responds with a NIP-47 error
// (e.g. QUOTA_EXCEEDED, RESTRICTED, RATE_LIMITED).
type NWCError struct {
	Code    string
	Message string
}

func (e *NWCError) Error() string {
	return fmt.Sprintf("nwc error %s: %s", e.Code, e.Message)
}

// Client is a connected NWC client identity: a client keypair paired with a
// specific wallet (hub or child) over one or more relays.
type Client struct {
	walletPubkey string
	clientPriv   string
	clientPub    string

	relaysMu sync.Mutex
	relays   []*nostr.Relay

	// Logger, if set, receives one line per Call: method, wallet, timing, and
	// on failure the error/params needed to debug it. Relay-level detail is
	// only logged when a relay actually fails. Callers typically wire this to
	// testing.T.Logf so `go test -v` stays readable while still showing what
	// went wrong against the real relay/hub.
	Logger func(format string, args ...any)
}

func (c *Client) logf(format string, args ...any) {
	if c.Logger != nil {
		c.Logger(format, args...)
	}
}

// Connect parses a nostr+walletconnect:// pairing URI and connects to every
// relay named in it.
func Connect(ctx context.Context, pairingURI string) (*Client, error) {
	walletPubkey, clientPriv, relayURLs, err := parsePairingURI(pairingURI)
	if err != nil {
		return nil, err
	}

	clientPub, err := nostr.GetPublicKey(clientPriv)
	if err != nil {
		return nil, fmt.Errorf("derive client pubkey: %w", err)
	}

	relays := make([]*nostr.Relay, 0, len(relayURLs))
	for _, relayURL := range relayURLs {
		relay, err := nostr.RelayConnect(ctx, relayURL)
		if err != nil {
			for _, r := range relays {
				r.Close()
			}
			return nil, fmt.Errorf("connect to relay %q: %w", relayURL, err)
		}
		relays = append(relays, relay)
	}

	return &Client{
		walletPubkey: walletPubkey,
		clientPriv:   clientPriv,
		clientPub:    clientPub,
		relays:       relays,
	}, nil
}

// Close disconnects from every relay.
func (c *Client) Close() {
	c.relaysMu.Lock()
	defer c.relaysMu.Unlock()
	for _, r := range c.relays {
		r.Close()
	}
}

// WalletPubkey returns the wallet pubkey this client is paired with.
func (c *Client) WalletPubkey() string {
	return c.walletPubkey
}

// ClientPubkey returns this client's own nostr pubkey (used e.g. as the
// requester_pubkey/pubkey param for create_circle_wallet/create_jit_wallet).
func (c *Client) ClientPubkey() string {
	return c.clientPub
}

// Call sends a NIP-47 request and blocks until a matching response arrives
// (or ctx/timeout expires). params is marshaled to JSON as the request
// params; on success, the response's Result is re-marshaled into result
// (pass a pointer, or nil to ignore it). On a NIP-47-level error response,
// Call returns a *NWCError.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, DefaultCallTimeout)
	defer cancel()

	nip47Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, c.walletPubkey, c.clientPriv)
	if err != nil {
		return fmt.Errorf("build cipher: %w", err)
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	reqPayload, err := json.Marshal(models.Request{Method: method, Params: paramsJSON})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	encryptedContent, err := nip47Cipher.Encrypt(string(reqPayload))
	if err != nil {
		return fmt.Errorf("encrypt request: %w", err)
	}

	requestEvent := nostr.Event{
		PubKey:    c.clientPub,
		CreatedAt: nostr.Now(),
		Kind:      models.REQUEST_KIND,
		Tags: nostr.Tags{
			{"p", c.walletPubkey},
			{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2},
		},
		Content: encryptedContent,
	}
	if err := requestEvent.Sign(c.clientPriv); err != nil {
		return fmt.Errorf("sign request event: %w", err)
	}

	start := time.Now()

	filter := nostr.Filter{
		Kinds:   []int{models.RESPONSE_KIND},
		Authors: []string{c.walletPubkey},
		Tags:    nostr.TagMap{"e": {requestEvent.ID}},
	}

	respEvent, err := c.subscribeAndPublish(ctx, filter, requestEvent)
	if err != nil {
		c.logf("nwc %s -> %s: FAILED after %s: %v (params=%s)", method, short(c.walletPubkey), time.Since(start).Round(time.Millisecond), err, paramsJSON)
		return err
	}

	payload, err := nip47Cipher.Decrypt(respEvent.Content)
	if err != nil {
		return fmt.Errorf("decrypt response: %w", err)
	}

	var resp models.Response
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	elapsed := time.Since(start).Round(time.Millisecond)

	if resp.Error != nil {
		c.logf("nwc %s -> %s: error after %s: %s: %s (params=%s)", method, short(c.walletPubkey), elapsed, resp.Error.Code, resp.Error.Message, paramsJSON)
		return &NWCError{Code: resp.Error.Code, Message: resp.Error.Message}
	}

	c.logf("nwc %s -> %s: ok in %s", method, short(c.walletPubkey), elapsed)

	if result != nil && resp.Result != nil {
		resultJSON, err := json.Marshal(resp.Result)
		if err != nil {
			return fmt.Errorf("re-marshal result: %w", err)
		}
		if err := json.Unmarshal(resultJSON, result); err != nil {
			return fmt.Errorf("unmarshal result into %T: %w", result, err)
		}
	}

	return nil
}

// reconnectRelay replaces c.relays[idx] with a freshly dialed connection to
// the same URL, closing the old one (best effort). A go-nostr *Relay's
// background connection context is cancelled permanently the first time it
// disconnects (see relay.go's NewRelay/ConnectWithTLS) - calling Connect()
// again on that same object cannot revive it, so this always constructs a
// brand new *Relay rather than reusing the old one.
func (c *Client) reconnectRelay(ctx context.Context, idx int) (*nostr.Relay, error) {
	c.relaysMu.Lock()
	defer c.relaysMu.Unlock()

	old := c.relays[idx]
	fresh, err := nostr.RelayConnect(ctx, old.URL)
	if err != nil {
		return nil, fmt.Errorf("reconnect to relay %s: %w", old.URL, err)
	}
	old.Close()
	c.relays[idx] = fresh
	c.logf("nwc: relay %s reconnected", fresh.URL)
	return fresh, nil
}

func (c *Client) relayAt(idx int) *nostr.Relay {
	c.relaysMu.Lock()
	defer c.relaysMu.Unlock()
	return c.relays[idx]
}

// subscribeOnRelay subscribes on the relay at idx, transparently
// reconnecting and retrying once if its cached connection has gone stale.
// Clients in this suite are long-lived and often cached/shared across many
// tests spanning tens of seconds (see cacheCircleChild in the integration
// package), so a relay connection idling or blipping mid-suite is routine.
// go-nostr's IsConnected() only reflects a disconnect the relay's own read
// loop has already noticed - a connection that died silently (no read
// attempted since) can still report connected right up until the next
// actual write fails, which is exactly what surfaced as "not connected to
// relay" / "failed to write: connection closed" against these cached
// clients before this retry existed.
func (c *Client) subscribeOnRelay(ctx context.Context, idx int, filter nostr.Filter) (*nostr.Subscription, error) {
	relay := c.relayAt(idx)
	if !relay.IsConnected() {
		var err error
		relay, err = c.reconnectRelay(ctx, idx)
		if err != nil {
			return nil, err
		}
	}

	sub, err := relay.Subscribe(ctx, nostr.Filters{filter})
	if err == nil {
		return sub, nil
	}

	reconnected, rerr := c.reconnectRelay(ctx, idx)
	if rerr != nil {
		return nil, fmt.Errorf("subscribe on relay %s: %w (reconnect also failed: %v)", relay.URL, err, rerr)
	}
	c.logf("nwc: subscribe on relay %s failed (%v), retrying after reconnect", relay.URL, err)
	return reconnected.Subscribe(ctx, nostr.Filters{filter})
}

// subscribeAndPublish subscribes on every relay before publishing the
// request (to avoid racing a fast reply), then returns the first matching
// response event received on any relay.
func (c *Client) subscribeAndPublish(ctx context.Context, filter nostr.Filter, requestEvent nostr.Event) (*nostr.Event, error) {
	type result struct {
		event *nostr.Event
		err   error
	}
	numRelays := len(c.relays)
	resultCh := make(chan result, numRelays)

	for i := 0; i < numRelays; i++ {
		sub, err := c.subscribeOnRelay(ctx, i, filter)
		if err != nil {
			return nil, err
		}
		defer sub.Unsub()

		go func(sub *nostr.Subscription) {
			select {
			case evt := <-sub.Events:
				resultCh <- result{event: evt}
			case <-ctx.Done():
				resultCh <- result{err: ctx.Err()}
			}
		}(sub)
	}

	published := 0
	for i := 0; i < numRelays; i++ {
		relay := c.relayAt(i)
		if err := relay.Publish(ctx, requestEvent); err != nil {
			c.logf("nwc: publish to relay %s failed: %v", relay.URL, err)
			continue
		}
		published++
	}
	if published == 0 {
		return nil, fmt.Errorf("failed to publish request event %s to any relay", requestEvent.ID)
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, fmt.Errorf("waiting for response to %s: %w", requestEvent.ID, res.err)
		}
		return res.event, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for response (published to %d/%d relays): %w", published, len(c.relays), ctx.Err())
	}
}

// short truncates a hex id/pubkey to a readable prefix for log output; full
// values are only needed when cross-referencing a hub's own logs, which
// individual test runs rarely require.
func short(s string) string {
	const n = 10
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// DecryptPairingURI decrypts a create_jit_wallet/create_circle_wallet
// response's encrypted_pairing_uri (NIP-44 encrypted by the wallet to the
// beneficiary/requester pubkey) into a connectable nostr+walletconnect://
// URI, using the beneficiary/requester's own private key. This mirrors what
// a real beneficiary client does after receiving the response.
func DecryptPairingURI(recipientPrivkey, walletPubkey, encryptedURI string) (string, error) {
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPubkey, recipientPrivkey)
	if err != nil {
		return "", fmt.Errorf("build cipher: %w", err)
	}
	uri, err := c.Decrypt(encryptedURI)
	if err != nil {
		return "", fmt.Errorf("decrypt pairing uri: %w", err)
	}
	return uri, nil
}

// parsePairingURI parses a nostr+walletconnect://<walletPubkey>?relay=...&secret=...
// URI, returning the wallet pubkey, client secret key, and relay URLs.
func parsePairingURI(pairingURI string) (walletPubkey, secret string, relays []string, err error) {
	const scheme = "nostr+walletconnect://"
	if !strings.HasPrefix(pairingURI, scheme) {
		return "", "", nil, fmt.Errorf("pairing URI must start with %q", scheme)
	}

	// url.Parse handles this scheme fine since it's still authority+query shaped.
	parsed, err := url.Parse(pairingURI)
	if err != nil {
		return "", "", nil, fmt.Errorf("parse pairing uri: %w", err)
	}

	walletPubkey = parsed.Host
	if walletPubkey == "" {
		return "", "", nil, fmt.Errorf("pairing uri missing wallet pubkey")
	}

	query := parsed.Query()
	relays = query["relay"]
	if len(relays) == 0 {
		return "", "", nil, fmt.Errorf("pairing uri missing relay param")
	}

	secret = query.Get("secret")
	if secret == "" {
		return "", "", nil, fmt.Errorf("pairing uri missing secret param")
	}

	return walletPubkey, secret, relays, nil
}
