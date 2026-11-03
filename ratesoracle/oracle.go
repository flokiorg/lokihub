// Package ratesoracle subscribes to the NIP-78 (kind 30078) fiat-rate oracle
// that publishes the Flokicoin fiat rate, verifies it, and caches the latest
// good value for hubinfo.GetFiatRate to serve.
//
// It replaces the former rates.json HTTP fetch: the oracle publishes the exact
// same JSON body as event.content, so it is a drop-in. The oracle pubkey and
// relays come from the embedded seed (seed.FiatRateOracle). The newest
// parameterized-replaceable event authored by that pubkey and tagged
// #d == d_tag is fetched, its signature and author are verified, its content is
// unmarshalled into hubinfo.FiatRate, and the result is cached and refreshed
// on an interval. Until a rate is fetched — or once the last-good one is older
// than the staleness TTL — Latest reports unavailable and the UI degrades to
// sats/FLC.
package ratesoracle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/hubinfo"
	"github.com/flokiorg/lokihub/seed"
)

// ErrUnavailable is returned by Latest when no rate has been fetched yet or the
// last-good one is stale.
var ErrUnavailable = errors.New("flokicoin rate unavailable")

const (
	defaultKind            = 30078
	defaultRefreshInterval = 5 * time.Minute
	defaultStalenessTTL    = time.Hour
	// fetchTimeout bounds one relay round-trip.
	fetchTimeout = 15 * time.Second
)

// Config is the resolved oracle configuration.
type Config struct {
	// AuthorHex is the oracle pubkey (32-byte x-only hex). Empty disables the
	// oracle: Start is a no-op and Latest always reports unavailable.
	AuthorHex       string
	Kind            int
	DTag            string
	Relays          []string
	RefreshInterval time.Duration
	StalenessTTL    time.Duration
}

// fetchFunc fetches candidate events for a filter. It is a field so tests can
// inject events without a live relay.
type fetchFunc func(ctx context.Context, relays []string, filter nostr.Filter) []*nostr.Event

// Oracle holds the cached rate and refreshes it from the oracle relays.
type Oracle struct {
	cfg   Config
	fetch fetchFunc

	mu        sync.RWMutex
	rate      *hubinfo.FiatRate
	fetchedAt time.Time
}

// New builds an Oracle from a resolved Config.
func New(cfg Config) *Oracle {
	if cfg.Kind == 0 {
		cfg.Kind = defaultKind
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = defaultRefreshInterval
	}
	if cfg.StalenessTTL <= 0 {
		cfg.StalenessTTL = defaultStalenessTTL
	}
	o := &Oracle{cfg: cfg}
	o.fetch = o.fetchFromRelays
	return o
}

// FromSeed resolves the oracle Config from the embedded seed, defaulting the
// relays to the seed's nostr_relay suggestions when the oracle block lists
// none, and returns a ready Oracle. A malformed npub yields a disabled oracle
// (logged) rather than an error, so hub startup is never blocked by it.
func FromSeed(s *seed.Seed) *Oracle {
	block := s.FiatRateOracle
	cfg := Config{
		Kind:            block.Kind,
		DTag:            block.DTag,
		Relays:          block.Relays,
		RefreshInterval: parseDuration(block.RefreshInterval, defaultRefreshInterval),
		StalenessTTL:    parseDuration(block.StalenessTTL, defaultStalenessTTL),
	}
	if len(cfg.Relays) == 0 {
		cfg.Relays = nostrRelayURLs(s)
	}
	if block.Npub != "" {
		hexpk, err := DecodeNpub(block.Npub)
		if err != nil {
			logger.Logger.Error().Err(err).Str("npub", block.Npub).
				Msg("fiat-rate oracle: invalid npub in seed; rates will be unavailable")
		} else {
			cfg.AuthorHex = hexpk
		}
	}
	return New(cfg)
}

// Latest returns the cached rate when present and not stale.
func (o *Oracle) Latest() (*hubinfo.FiatRate, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.rate == nil {
		return nil, ErrUnavailable
	}
	if o.cfg.StalenessTTL > 0 && time.Since(o.fetchedAt) > o.cfg.StalenessTTL {
		return nil, ErrUnavailable
	}
	return o.rate, nil
}

// Start fetches once, then refreshes on the configured interval until ctx is
// done. It returns immediately if the oracle is disabled (no author).
func (o *Oracle) Start(ctx context.Context) {
	if o.cfg.AuthorHex == "" {
		logger.Logger.Warn().Msg("fiat-rate oracle not configured (no npub); rates unavailable")
		return
	}
	logger.Logger.Info().
		Str("author", o.cfg.AuthorHex).
		Int("kind", o.cfg.Kind).
		Str("d", o.cfg.DTag).
		Strs("relays", o.cfg.Relays).
		Msg("starting fiat-rate oracle subscriber")

	o.refresh(ctx)

	ticker := time.NewTicker(o.cfg.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.refresh(ctx)
		}
	}
}

// refresh fetches the newest valid oracle event and, if found, updates the
// cache. On any failure it keeps the last-good value (which Latest ages out
// once it passes the staleness TTL).
func (o *Oracle) refresh(ctx context.Context) {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	filter := nostr.Filter{
		Kinds:   []int{o.cfg.Kind},
		Authors: []string{o.cfg.AuthorHex},
		Tags:    nostr.TagMap{"d": []string{o.cfg.DTag}},
		Limit:   1,
	}

	best := newestValid(o.fetch(fctx, o.cfg.Relays, filter), o.cfg)
	if best == nil {
		logger.Logger.Warn().Msg("fiat-rate oracle: no valid event this cycle; keeping last-good")
		return
	}

	var rate hubinfo.FiatRate
	if err := json.Unmarshal([]byte(best.Content), &rate); err != nil {
		logger.Logger.Error().Err(err).Msg("fiat-rate oracle: event content is not a valid rate")
		return
	}

	o.mu.Lock()
	o.rate = &rate
	o.fetchedAt = time.Now()
	o.mu.Unlock()

	logger.Logger.Debug().
		Str("code", rate.Code).
		Str("rate", rate.Rate).
		Msg("fiat-rate oracle: cached new rate")
}

// newestValid returns the newest event that is authored by the oracle, of the
// expected kind, tagged #d == DTag, and carrying a valid signature.
func newestValid(events []*nostr.Event, cfg Config) *nostr.Event {
	var best *nostr.Event
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ev.PubKey != cfg.AuthorHex || ev.Kind != cfg.Kind {
			continue
		}
		if dTag(ev) != cfg.DTag {
			continue
		}
		if ok, err := ev.CheckSignature(); err != nil || !ok {
			continue
		}
		if best == nil || ev.CreatedAt > best.CreatedAt {
			best = ev
		}
	}
	return best
}

// dTag returns the value of the event's first "d" tag, or "".
func dTag(ev *nostr.Event) string {
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == "d" {
			return t[1]
		}
	}
	return ""
}

// fetchFromRelays is the production fetcher: a short-lived pool reading matching
// events until each relay's EOSE.
func (o *Oracle) fetchFromRelays(ctx context.Context, relays []string, filter nostr.Filter) []*nostr.Event {
	if len(relays) == 0 {
		return nil
	}
	pool := nostr.NewSimplePool(ctx)
	var out []*nostr.Event
	seen := map[string]bool{}
	for ie := range pool.FetchMany(ctx, relays, filter) {
		if ie.Event == nil || seen[ie.ID] {
			continue
		}
		seen[ie.ID] = true
		out = append(out, ie.Event)
	}
	return out
}

// DecodeNpub decodes a bech32 npub to its 32-byte x-only hex pubkey.
func DecodeNpub(npub string) (string, error) {
	prefix, value, err := nip19.Decode(npub)
	if err != nil {
		return "", err
	}
	if prefix != "npub" {
		return "", fmt.Errorf("expected an npub, got %q", prefix)
	}
	hexpk, ok := value.(string)
	if !ok {
		return "", errors.New("npub did not decode to a hex pubkey")
	}
	return hexpk, nil
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// nostrRelayURLs pulls the relay URLs out of the seed's nostr_relay
// suggestions, used as the oracle's relay fallback.
func nostrRelayURLs(s *seed.Seed) []string {
	entries := s.Services["nostr_relay"]
	urls := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.URL != "" {
			urls = append(urls, e.URL)
		}
	}
	return urls
}
