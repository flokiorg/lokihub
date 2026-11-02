package ratesoracle

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

const (
	testDTag    = "flokicoin-fiat-rate"
	testContent = `{"code":"USD","symbol":"$","rate":"0.00005","rate_float":0.00005}`
)

// signedEvent builds and signs a kind-30078 oracle event with the given secret
// key, d tag and content.
func signedEvent(t *testing.T, sk, dtag, content string, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      defaultKind,
		CreatedAt: createdAt,
		Tags:      nostr.Tags{nostr.Tag{"d", dtag}},
		Content:   content,
	}
	if err := ev.Sign(sk); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return ev
}

// testOracle returns an oracle configured for the given author with its fetch
// stubbed to return events.
func testOracle(t *testing.T, authorHex string, events ...*nostr.Event) *Oracle {
	t.Helper()
	o := New(Config{
		AuthorHex:       authorHex,
		Kind:            defaultKind,
		DTag:            testDTag,
		Relays:          []string{"wss://relay.example"},
		RefreshInterval: time.Hour,
		StalenessTTL:    time.Hour,
	})
	o.fetch = func(context.Context, []string, nostr.Filter) []*nostr.Event {
		return events
	}
	return o
}

func TestLatest_ValidEventParsesAndCaches(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	ev := signedEvent(t, sk, testDTag, testContent, nostr.Now())

	o := testOracle(t, pk, ev)
	o.refresh(context.Background())

	rate, err := o.Latest()
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if rate.Code != "USD" || rate.Symbol != "$" || rate.Rate != "0.00005" || rate.RateFloat != 0.00005 {
		t.Fatalf("unexpected rate: %+v", rate)
	}
}

func TestLatest_NewestEventWins(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	old := signedEvent(t, sk, testDTag, `{"code":"USD","symbol":"$","rate":"0.00001","rate_float":0.00001}`, nostr.Now()-100)
	newer := signedEvent(t, sk, testDTag, testContent, nostr.Now())

	o := testOracle(t, pk, old, newer)
	o.refresh(context.Background())

	rate, err := o.Latest()
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if rate.Rate != "0.00005" {
		t.Fatalf("expected the newer event's rate, got %q", rate.Rate)
	}
}

func TestLatest_WrongAuthorRejected(t *testing.T) {
	// Event is validly signed, but by a key other than the configured oracle.
	otherSK := nostr.GeneratePrivateKey()
	ev := signedEvent(t, otherSK, testDTag, testContent, nostr.Now())

	oracleSK := nostr.GeneratePrivateKey()
	oraclePK, _ := nostr.GetPublicKey(oracleSK)

	o := testOracle(t, oraclePK, ev)
	o.refresh(context.Background())

	if _, err := o.Latest(); err == nil {
		t.Fatal("expected unavailable for an event from the wrong author")
	}
}

func TestLatest_BadSignatureRejected(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	ev := signedEvent(t, sk, testDTag, testContent, nostr.Now())
	// Tamper the content after signing so the signature no longer matches.
	ev.Content = `{"code":"USD","symbol":"$","rate":"9.99999","rate_float":9.99999}`

	o := testOracle(t, pk, ev)
	o.refresh(context.Background())

	if _, err := o.Latest(); err == nil {
		t.Fatal("expected unavailable for an event with a bad signature")
	}
}

func TestLatest_WrongDTagRejected(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	ev := signedEvent(t, sk, "some-other-tag", testContent, nostr.Now())

	o := testOracle(t, pk, ev)
	o.refresh(context.Background())

	if _, err := o.Latest(); err == nil {
		t.Fatal("expected unavailable for an event with the wrong d tag")
	}
}

func TestLatest_StaleReportsUnavailable(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	ev := signedEvent(t, sk, testDTag, testContent, nostr.Now())

	o := testOracle(t, pk, ev)
	o.cfg.StalenessTTL = time.Minute
	o.refresh(context.Background())

	// A fresh cache is served.
	if _, err := o.Latest(); err != nil {
		t.Fatalf("expected a fresh rate to be served, got %v", err)
	}

	// Age the cached value past the TTL.
	o.mu.Lock()
	o.fetchedAt = time.Now().Add(-2 * time.Minute)
	o.mu.Unlock()

	if _, err := o.Latest(); err == nil {
		t.Fatal("expected unavailable once the cached rate is stale")
	}
}

func TestLatest_NoEventIsUnavailable(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)

	o := testOracle(t, pk) // no events
	o.refresh(context.Background())

	if _, err := o.Latest(); err == nil {
		t.Fatal("expected unavailable when no event is fetched")
	}
}

func TestDecodeNpub(t *testing.T) {
	// The owner-published oracle npub decodes to this hex (the confirmed
	// addressable 30078:<hex>:flokicoin-fiat-rate author).
	const (
		npub    = "npub1eywl4czqunqp69mal68as5mhy4jfzjwxwztrd63pgycelr3jhzgs0nlnt8"
		wantHex = "c91dfae040e4c01d177dfe8fd8537725649149c6709636ea2141319f8e32b891"
	)
	got, err := DecodeNpub(npub)
	if err != nil {
		t.Fatalf("DecodeNpub: %v", err)
	}
	if got != wantHex {
		t.Fatalf("DecodeNpub = %q, want %q", got, wantHex)
	}
}

func TestFromSeed_DisabledWithoutNpub(t *testing.T) {
	// An oracle with no author is disabled: Latest is always unavailable and
	// Start returns promptly.
	o := New(Config{DTag: testDTag})
	if _, err := o.Latest(); err == nil {
		t.Fatal("expected unavailable for a disabled oracle")
	}
	o.Start(context.Background()) // must not block
}
