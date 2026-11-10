// The rates oracle subscribes to the NIP-78 (kind 30078) fiat-rate oracle
// that publishes the Flokicoin fiat rate, verifies it, and caches the latest
// good value for GetFiatRate to serve.
//
// It replaces the former rates.json HTTP fetch: the oracle publishes the exact
// same JSON body as event.content, so it is a drop-in. The oracle pubkey, kind
// and d-tag come from the embedded seed (seed.FiatRateOracle), but the relays
// it subscribes over do NOT — those are strictly the relays the user selected
// under Settings > Services (passed in as RelaysFn). With no selected relays
// the oracle is effectively disabled. The newest
// parameterized-replaceable event authored by that pubkey and tagged
// #d == d_tag is fetched, its signature and author are verified, its content is
// unmarshalled into FiatRate, and the result is cached and refreshed
// on an interval. Until a rate is fetched — or once the last-good one is older
// than the staleness TTL — Latest reports unavailable and the UI degrades to
// sats/FLC.

package hubinfo

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/internal/seed"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nostr/pubkeyhex"
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
	AuthorHex string
	Kind      int
	DTag      string
	// Relays is a static relay set, used only when RelaysFn is nil (tests).
	Relays []string
	// RelaysFn, when set, is the authoritative source of the relays the oracle
	// subscribes over and takes precedence over Relays. It is read on every
	// refresh so a runtime change to the user's relay selection is picked up.
	// The fiat-rate oracle is strictly opt-in: it subscribes ONLY over the
	// relays the user selected under Settings > Services (GetGeneralRelayUrls),
	// never over anything hardcoded or seeded. When it returns no relays the
	// oracle is effectively disabled — refresh is a no-op and Latest reports
	// unavailable, so the UI degrades to sats/FLC. The oracle npub/kind/d-tag
	// still come from the seed: they identify the oracle, not a connection.
	RelaysFn        func() []string
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
	rate      *FiatRate
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

// FromSeed resolves the oracle Config from the embedded seed and returns a
// ready Oracle. The oracle's identity (npub/kind/d-tag) comes from the seed,
// but the relays it subscribes over do NOT: they come from relaysFn, the
// user's selected relays (GetGeneralRelayUrls). The seed's own
// fiat_rate_oracle.relays list is deliberately ignored — a relay is contacted
// only because the user selected it, never because the seed named it. When
// relaysFn yields no relays the oracle is effectively disabled and the UI
// degrades to sats/FLC. A malformed npub yields a disabled oracle (logged)
// rather than an error, so hub startup is never blocked by it.
func FromSeed(s *seed.Seed, relaysFn func() []string) *Oracle {
	block := s.FiatRateOracle
	cfg := Config{
		Kind:            block.Kind,
		DTag:            block.DTag,
		RelaysFn:        relaysFn,
		RefreshInterval: parseDuration(block.RefreshInterval, defaultRefreshInterval),
		StalenessTTL:    parseDuration(block.StalenessTTL, defaultStalenessTTL),
	}
	if block.Npub != "" {
		hexpk, err := pubkeyhex.FromNpub(block.Npub)
		if err != nil {
			logger.Logger.Error().Err(err).Str("npub", block.Npub).
				Msg("fiat-rate oracle: invalid npub in seed; rates will be unavailable")
		} else {
			cfg.AuthorHex = hexpk
		}
	}
	return New(cfg)
}

// relays resolves the relay set for this cycle: the user-selected relays from
// RelaysFn when set (read fresh so a runtime change is honoured), else the
// static Relays. Empty means the oracle has nothing to subscribe over.
func (o *Oracle) relays() []string {
	if o.cfg.RelaysFn != nil {
		return o.cfg.RelaysFn()
	}
	return o.cfg.Relays
}

// Latest returns the cached rate when present and not stale.
func (o *Oracle) Latest() (*FiatRate, error) {
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
		Strs("relays", o.relays()).
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
	relays := o.relays()
	if len(relays) == 0 {
		// Strictly opt-in: with no user-selected relays there is nothing to
		// subscribe over. Skip quietly — Latest keeps reporting unavailable and
		// the UI degrades to sats/FLC. A relay selected later is picked up on
		// the next refresh tick.
		logger.Logger.Debug().Msg("fiat-rate oracle: no user-selected relays; rate unavailable")
		return
	}

	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	filter := nostr.Filter{
		Kinds:   []int{o.cfg.Kind},
		Authors: []string{o.cfg.AuthorHex},
		Tags:    nostr.TagMap{"d": []string{o.cfg.DTag}},
		Limit:   1,
	}

	best := newestValid(o.fetch(fctx, relays, filter), o.cfg)
	if best == nil {
		logger.Logger.Warn().Msg("fiat-rate oracle: no valid event this cycle; keeping last-good")
		return
	}

	var rate FiatRate
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
