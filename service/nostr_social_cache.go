package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/logger"
	"github.com/nbd-wtf/go-nostr"
	"gorm.io/gorm"
)

const socialCacheTTL = time.Hour

// RelayQueryTimeout bounds any single kind:3 relay query. A var, not a
// const, so tests can lower it instead of waiting out the real 15s.
var RelayQueryTimeout = 15 * time.Second

type contactEntry struct {
	pubkeys   map[string]struct{}
	fetchedAt time.Time
}

// outboxEntry caches an owner's discovered NIP-65 write relays (see
// discoverWriteRelays), so repeated kind:3 fetches don't re-query kind:10002
// every time. NIP-65 relay lists change rarely, so this outlives contactEntry.
type outboxEntry struct {
	relayUrls []string
	fetchedAt time.Time
}

// nostrSocialCache implements controllers.NostrSocialCache.
// It fetches and caches kind:3 contact lists from nostr relays.
// C1: sync.RWMutex allows parallel cache reads (zero contention on hits).
// C1: singleflight.Group collapses concurrent stale-cache misses to one relay query per pubkey.
// C2: stale entries are evicted in the slow path (write lock already held) to bound map size.
type nostrSocialCache struct {
	cfg         config.Config
	mu          sync.RWMutex
	cache       map[string]*contactEntry // keyed by owner pubkey
	outboxCache map[string]*outboxEntry  // keyed by owner pubkey; guarded by mu alongside cache
	sfg         singleflight.Group       // deduplicates concurrent relay fetches per pubkey
	ready       atomic.Bool              // set once by StartNostrSocialCacheRefresher's initial warm-up pass; see IsAuthorized

	// sharedPool is set once by StartNostrSocialCacheRefresher to the
	// long-lived, already-warm pool used by the periodic refresher.
	// fetchAndCache reuses it when present instead of cold-dialing a new pool
	// per call; falls back to an ephemeral pool if unset (e.g. in tests).
	sharedPool atomic.Pointer[nostr.SimplePool]
}

func NewNostrSocialCache(cfg config.Config) *nostrSocialCache {
	return &nostrSocialCache{
		cfg:         cfg,
		cache:       make(map[string]*contactEntry),
		outboxCache: make(map[string]*outboxEntry),
	}
}

// IsAuthorized checks whether requesterPubkey is authorized under the circle
// identity's policy. For allowlist policy it queries the DB; for social-graph
// policies it fetches kind:3. identity may be shared by multiple circle_hub
// apps concurrently — authorization is scoped to the identity, not any one app.
func (s *nostrSocialCache) IsAuthorized(ctx context.Context, requesterPubkey string, identity *db.CircleIdentity, gormDB *gorm.DB) (bool, error) {
	switch identity.Policy {
	case db.CirclePolicyAllowlist:
		return s.isInAllowlist(gormDB, identity.ID, requesterPubkey)

	case db.CirclePolicyFollowing:
		// "following" = provider follows the requester (requester is in provider's
		// contact list). Provider-controlled: only the provider can add someone to
		// their own kind:3 list, so this is a real authorization signal.
		//
		// If this provider has no cached entry yet AND the hub's initial startup
		// warm-up sweep (StartNostrSocialCacheRefresher) hasn't finished, refuse
		// fast with ErrSocialCacheWarmingUp instead of falling through to
		// getContactList's cold-miss fetch. Without this, a request landing in
		// that narrow startup window would block on a live relay round-trip
		// (RelayQueryTimeout, up to ~15s) and, if relays weren't reachable yet
		// either, come back as a false "not authorized" for a legitimate
		// follower. Once warm-up completes (typically seconds), or once this
		// specific provider has any cached entry, this gate is a single no-op
		// atomic load and normal cold-miss-tolerant behavior applies exactly as
		// before.
		if _, cached := s.peek(identity.ProviderPubkey); !cached && !s.ready.Load() {
			return false, constants.ErrSocialCacheWarmingUp
		}

		contacts, err := s.getContactList(ctx, identity.ProviderPubkey)
		if err != nil {
			return false, err
		}
		_, ok := contacts[requesterPubkey]
		return ok, nil
	}

	return false, nil
}

func (s *nostrSocialCache) isInAllowlist(gormDB *gorm.DB, identityID uint, pubkey string) (bool, error) {
	var count int64
	err := gormDB.Model(&db.CircleIdentityAllowedPubkey{}).
		Where("circle_identity_id = ? AND pubkey = ?", identityID, pubkey).
		Count(&count).Error
	return count > 0, err
}

// peek returns ownerPubkey's cached entry, if any, without ever fetching —
// the single read-only lookup shared by getContactList's fast path,
// PeekContactCount, PeekContactSyncedAt, and IsAuthorized's startup-readiness
// gate, so the RLock+map-read isn't duplicated four times over.
func (s *nostrSocialCache) peek(ownerPubkey string) (*contactEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.cache[ownerPubkey]
	return entry, ok
}

// getContactList returns the set of pubkeys in the given owner's latest kind:3 event.
// Any cached entry is served immediately, however stale — freshness for
// "following"-policy providers is instead maintained by a periodic background
// refresh (see StartNostrSocialCacheRefresher), so the request path never blocks
// on a relay round-trip except on a true first-time miss.
//
// Revocation latency: as long as StartNostrSocialCacheRefresher is running, every
// following-policy provider is refreshed at least once every socialCacheRefreshInterval
// (5 minutes) as long as the total following-policy provider count stays within
// maxRefresherBatchesPerTick*refresherBatchSize; beyond that it wraps across
// multiple ticks (see runSocialCacheRefresh), so the bound becomes a multiple of
// the interval proportional to provider count. If the refresher isn't running at
// all (e.g. no Lightning node configured yet), staleness is bounded only by
// evictStaleUnlocked's 2×socialCacheTTL cutoff, after which a cold-miss forces a
// fresh fetch on the next request.
func (s *nostrSocialCache) getContactList(ctx context.Context, ownerPubkey string) (map[string]struct{}, error) {
	// Fast path: read-only check under RLock — zero contention with other readers.
	if entry, ok := s.peek(ownerPubkey); ok {
		return entry.pubkeys, nil
	}

	// Slow path (true cold miss only): singleflight ensures only one goroutine
	// fetches per pubkey. All other concurrent callers for the same key wait
	// and share the result.
	v, err, _ := s.sfg.Do(ownerPubkey, func() (interface{}, error) {
		return s.fetchAndCache(ctx, ownerPubkey)
	})
	if err != nil {
		return nil, err
	}
	return v.(map[string]struct{}), nil
}

// maxOutboxRelays bounds how many of the owner's own NIP-65 write relays get
// merged into the kind:3 query on top of the configured General relays.
const maxOutboxRelays = 6

// outboxDiscoveryTimeout bounds the NIP-65 relay-list (kind:10002) lookup
// that precedes the kind:3 query, independent of RelayQueryTimeout. A var,
// not a const, so tests can lower it too.
var outboxDiscoveryTimeout = 5 * time.Second

// warmRelays connects to each of relayUrls concurrently on pool, bounding the
// worst-case wait to a single relay's dial time instead of the sum across
// all of them. Best-effort: a relay that fails to connect is just logged.
func warmRelays(pool *nostr.SimplePool, relayUrls []string) {
	var wg sync.WaitGroup
	wg.Add(len(relayUrls))
	for _, relayUrl := range relayUrls {
		go func(relayUrl string) {
			defer wg.Done()
			if _, err := pool.EnsureRelay(relayUrl); err != nil {
				logger.Logger.Error().Err(err).Str("relay_url", relayUrl).Msg("failed to connect to relay")
			}
		}(relayUrl)
	}
	wg.Wait()
}

// queryRelaysBounded runs pool.QuerySingle but, unlike calling it directly,
// actually returns once ctx expires. go-nostr's EnsureRelay dials each relay
// using the pool's own long-lived Context with a hardcoded 15s timeout — not
// the ctx passed here — so a relay that's merely slow to connect can stall
// QuerySingle regardless of a shorter ctx deadline. Racing against ctx.Done()
// fixes that; the abandoned goroutine keeps running in the background,
// self-bounded by go-nostr's own timeout, but nothing here waits on it.
func queryRelaysBounded(ctx context.Context, pool *nostr.SimplePool, relayUrls []string, filter nostr.Filter) *nostr.RelayEvent {
	result := make(chan *nostr.RelayEvent, 1)
	go func() {
		result <- pool.QuerySingle(ctx, relayUrls, filter)
	}()
	select {
	case event := <-result:
		return event
	case <-ctx.Done():
		return nil
	}
}

// discoverWriteRelays looks up ownerPubkey's NIP-65 relay list (kind:10002)
// on baseRelayUrls and returns up to maxOutboxRelays of the relays marked (or
// left unmarked, meaning both read+write) as write relays — the relays
// ownerPubkey actually publishes to. Returns nil (not an error) on any
// failure to discover — a best-effort widening of the search, never a
// precondition for the kind:3 query itself.
func discoverWriteRelays(ctx context.Context, pool *nostr.SimplePool, ownerPubkey string, baseRelayUrls []string) []string {
	if len(baseRelayUrls) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, outboxDiscoveryTimeout)
	defer cancel()

	event := queryRelaysBounded(ctx, pool, baseRelayUrls, nostr.Filter{
		Authors: []string{ownerPubkey},
		Kinds:   []int{nostr.KindRelayListMetadata},
		Limit:   1,
	})
	if event == nil {
		return nil
	}

	writeRelays := make([]string, 0, maxOutboxRelays)
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}
		// A missing marker (len(tag) == 2) means the relay is used for both
		// read and write, per NIP-65.
		if len(tag) >= 3 && tag[2] != "write" {
			continue
		}
		writeRelays = append(writeRelays, tag[1])
		if len(writeRelays) == maxOutboxRelays {
			break
		}
	}
	return writeRelays
}

// getOutboxRelays returns ownerPubkey's cached NIP-65 write relays,
// discovering them via discoverWriteRelays on a true cache miss. A
// successful discovery is cached for the life of the process, since NIP-65
// relay lists change rarely. An unsuccessful discovery is NOT cached, so an
// owner whose relay list only becomes reachable later is picked up on the
// next fetch instead of being stuck forever.
func (s *nostrSocialCache) getOutboxRelays(ctx context.Context, pool *nostr.SimplePool, ownerPubkey string, baseRelayUrls []string) []string {
	s.mu.RLock()
	entry, ok := s.outboxCache[ownerPubkey]
	s.mu.RUnlock()
	if ok {
		return entry.relayUrls
	}

	discovered := discoverWriteRelays(ctx, pool, ownerPubkey, baseRelayUrls)
	if len(discovered) == 0 {
		return nil
	}

	s.mu.Lock()
	s.outboxCache[ownerPubkey] = &outboxEntry{relayUrls: discovered, fetchedAt: time.Now()}
	s.mu.Unlock()
	return discovered
}

// mergeRelayUrls unions base with extra, preserving base's order and
// deduplicating exact string matches (relay URLs are compared as configured,
// not normalized — the same relay reachable under two different-looking URLs
// is queried twice, same as go-nostr's own pool.QuerySingle would do).
func mergeRelayUrls(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	merged := make([]string, 0, len(base)+len(extra))
	for _, url := range base {
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		merged = append(merged, url)
	}
	for _, url := range extra {
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		merged = append(merged, url)
	}
	return merged
}

// queryAndStore fetches ownerPubkey's kind:3 contact list using pool and stores
// the result in the cache. Shared by the cold-miss fallback (fetchAndCache, which
// opens an ephemeral pool) and the proactive background refresher (refreshOne,
// which reuses a long-lived pool across many providers).
func (s *nostrSocialCache) queryAndStore(ctx context.Context, pool *nostr.SimplePool, ownerPubkey string) (map[string]struct{}, error) {
	baseRelayUrls := s.cfg.GetGeneralRelayUrls()
	relayUrls := mergeRelayUrls(baseRelayUrls, s.getOutboxRelays(ctx, pool, ownerPubkey, baseRelayUrls))

	// Bound the relay round-trip: without this, an unresponsive relay can stall
	// this call (and, via singleflight, every concurrent caller waiting on the
	// same ownerPubkey) for as long as the caller's own context allows.
	fetchCtx, cancel := context.WithTimeout(ctx, RelayQueryTimeout)
	defer cancel()

	event := queryRelaysBounded(fetchCtx, pool, relayUrls, nostr.Filter{
		Authors: []string{ownerPubkey},
		Kinds:   []int{3},
		Limit:   1,
	})

	// ctx.Err() (not fetchCtx.Err()) distinguishes "the caller gave up" from
	// "RelayQueryTimeout expired" — only the latter means the relays actually
	// had a chance to answer. A caller-side cancellation must not be treated
	// as a confirmed miss: no warning, no cache write.
	if event == nil && ctx.Err() != nil {
		if existing, ok := s.peek(ownerPubkey); ok {
			return existing.pubkeys, nil
		}
		return nil, ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// C2: evict entries older than 2×TTL while already holding the write lock.
	// This bounds the map size at the cost of a sweep during an already-slow relay fetch.
	defer s.evictStaleUnlocked()

	if event == nil {
		logger.Logger.Warn().Str("pubkey", ownerPubkey).Strs("relays", relayUrls).
			Msg("No kind:3 event found for pubkey")

		// Preserve the last known-good list instead of overwriting it with an
		// empty result — a transient relay hiccup must not look like the owner
		// revoked everyone. A genuine revocation still lands on the next
		// successful refresh.
		if existing, ok := s.cache[ownerPubkey]; ok {
			return existing.pubkeys, nil
		}

		// True first-ever fetch with nothing to fall back on.
		pubkeys := make(map[string]struct{})
		s.cache[ownerPubkey] = &contactEntry{pubkeys: pubkeys, fetchedAt: time.Now()}
		return pubkeys, nil
	}

	pubkeys := make(map[string]struct{})
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			pubkeys[tag[1]] = struct{}{}
		}
	}
	s.cache[ownerPubkey] = &contactEntry{
		pubkeys:   pubkeys,
		fetchedAt: time.Now(),
	}

	return pubkeys, nil
}

// fetchAndCache performs the relay query and stores the result. Reuses
// s.sharedPool when set, falling back to a one-off ephemeral pool otherwise.
func (s *nostrSocialCache) fetchAndCache(ctx context.Context, ownerPubkey string) (map[string]struct{}, error) {
	pool := s.sharedPool.Load()
	if pool == nil {
		pool = nostr.NewSimplePool(ctx)
	}
	return s.queryAndStore(ctx, pool, ownerPubkey)
}

// refreshOne proactively refreshes ownerPubkey's cached contact list using a
// shared, already-connected pool. Routed through the same singleflight key as
// fetchAndCache so a proactive refresh and a concurrent cold-miss request for
// the same provider collapse into a single relay query instead of racing.
func (s *nostrSocialCache) refreshOne(ctx context.Context, pool *nostr.SimplePool, ownerPubkey string) error {
	_, err, _ := s.sfg.Do(ownerPubkey, func() (interface{}, error) {
		return s.queryAndStore(ctx, pool, ownerPubkey)
	})
	return err
}

// PeekContactCount returns the number of pubkeys in ownerPubkey's cached kind:3
// contact list WITHOUT ever fetching — a plain cache read. Used by list-style
// API responses (e.g. the apps list, enriched with a Circles following-count)
// that must never block on a live relay round-trip. ok is false on a true
// cache miss; callers should treat that as "not yet known" (e.g. render a
// loading skeleton) rather than falling back to a blocking fetch.
func (s *nostrSocialCache) PeekContactCount(ownerPubkey string) (count int, ok bool) {
	entry, ok := s.peek(ownerPubkey)
	if !ok {
		return 0, false
	}
	return len(entry.pubkeys), true
}

// PeekContactSyncedAt returns when ownerPubkey's cached kind:3 contact list
// was last fetched from relays, without ever fetching itself — same
// non-blocking, peek-only contract as PeekContactCount. ok is false on a
// cache miss; callers should treat that as "not yet known."
func (s *nostrSocialCache) PeekContactSyncedAt(ownerPubkey string) (syncedAt time.Time, ok bool) {
	entry, ok := s.peek(ownerPubkey)
	if !ok {
		return time.Time{}, false
	}
	return entry.fetchedAt, true
}

// ContactCount returns the number of pubkeys in ownerPubkey's cached (or
// freshly-fetched-on-miss) kind:3 contact list. Unlike PeekContactCount, this
// may cold-fetch once — reserved for single-target reads (e.g. the
// GetCircleIdentity detail endpoint) where one deliberate, bounded fetch is
// acceptable, unlike a list endpoint iterating many rows.
func (s *nostrSocialCache) ContactCount(ctx context.Context, ownerPubkey string) (int, error) {
	contacts, err := s.getContactList(ctx, ownerPubkey)
	if err != nil {
		return 0, err
	}
	return len(contacts), nil
}

// WarmFollowingCache performs an immediate, one-off fetch of providerPubkey's
// contact list and returns it. Used to warm a new following-policy Circle
// Hub right after creation, and as the manual "Sync" flow's single fetch
// path (see api.fetchCircleFollowingPubkeys) so it shares the same outbox
// discovery as every other caller instead of a separate implementation.
func (s *nostrSocialCache) WarmFollowingCache(ctx context.Context, providerPubkey string) (map[string]struct{}, error) {
	return s.fetchAndCache(ctx, providerPubkey)
}

// WarmGeneralRelays re-connects s.sharedPool to the current
// GetGeneralRelayUrls() — call after the user updates that setting so a
// newly-added relay isn't left to be dialed lazily by whichever fetch
// touches it first. No-op if the refresher hasn't started yet.
func (s *nostrSocialCache) WarmGeneralRelays() {
	pool := s.sharedPool.Load()
	if pool == nil {
		return
	}
	warmRelays(pool, s.cfg.GetGeneralRelayUrls())
}

// evictStaleUnlocked removes cache entries older than 2×TTL.
// Must be called with s.mu held for writing.
func (s *nostrSocialCache) evictStaleUnlocked() {
	cutoff := time.Now().Add(-2 * socialCacheTTL)
	for k, e := range s.cache {
		if e.fetchedAt.Before(cutoff) {
			delete(s.cache, k)
			// A pubkey no longer worth caching contacts for isn't worth
			// caching outbox relays for either — same lifecycle, one sweep.
			delete(s.outboxCache, k)
		}
	}
}

// socialCacheRefreshInterval bounds how stale a "following"-policy cache entry
// can get before the periodic refresher revisits it — meaningfully shorter than
// socialCacheTTL so entries are proactively kept warm well before they'd have
// gone stale under the TTL alone.
const socialCacheRefreshInterval = 5 * time.Minute

// refresherBatchSize bounds memory per refresh tick.
const refresherBatchSize = 200

// maxRefresherBatchesPerTick bounds total work per tick, mirroring
// maxBatchesPerTick in jit_cleanup_service.go: a large number of following-policy
// providers spreads across multiple ticks instead of monopolizing this goroutine.
const maxRefresherBatchesPerTick = 10

// refresherConcurrency bounds how many providers are refreshed in parallel within
// a batch. Sequential refresh (one relay round-trip at a time) can make a full
// sweep of maxRefresherBatchesPerTick*refresherBatchSize providers take longer
// than socialCacheRefreshInterval itself; refreshing concurrently keeps a tick's
// total wall-clock time close to a single relay round-trip regardless of how many
// providers are in the batch.
const refresherConcurrency = 20

// StartNostrSocialCacheRefresher runs a background goroutine that periodically
// refreshes the cached contact list for every following-policy circle_hub
// using a shared, already-connected pool. This keeps IsAuthorized's request path
// serving from cache in the steady state instead of blocking on a live relay query.
// It runs an immediate pass before entering the ticker loop so following-policy
// caches are warm right after startup, rather than staying cold — and list
// endpoints like ListApps stuck peek-only — for up to socialCacheRefreshInterval.
// cache.ready flips to true once that immediate pass returns, which is what
// unblocks IsAuthorized's startup-readiness gate for following-policy identities.
func StartNostrSocialCacheRefresher(ctx context.Context, gormDB *gorm.DB, cache *nostrSocialCache, pool *nostr.SimplePool) {
	// Let cold-miss fetches (fetchAndCache) reuse this same already-warm pool
	// too — see sharedPool's doc comment.
	cache.sharedPool.Store(pool)
	go func() {
		ticker := time.NewTicker(socialCacheRefreshInterval)
		defer ticker.Stop()
		offset := runSocialCacheRefresh(ctx, gormDB, cache, pool, 0)
		cache.ready.Store(true)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				offset = runSocialCacheRefresh(ctx, gormDB, cache, pool, offset)
			}
		}
	}()
}

// runSocialCacheRefresh refreshes following-policy circle_identities starting at
// startOffset, in bounded batches processed concurrently (see refresherConcurrency).
// A single identity's relay failure is logged and skipped — it never aborts the
// rest of the sweep. Returns the offset to resume from on the next tick — once the
// end of the table is reached (or on a query error), it wraps back to 0 — so an
// identity count larger than one tick's capacity is still fully covered over
// successive ticks instead of the sweep being permanently limited to the first
// maxRefresherBatchesPerTick*refresherBatchSize rows. Since multiple circle_hub
// apps may share one identity, this also naturally dedupes refresh work — one relay
// query per unique identity, not per provider.
func runSocialCacheRefresh(ctx context.Context, gormDB *gorm.DB, cache *nostrSocialCache, pool *nostr.SimplePool, startOffset int) int {
	offset := startOffset
	for batchNum := 0; batchNum < maxRefresherBatchesPerTick; batchNum++ {
		identities, err := queries.GetFollowingCircleIdentities(gormDB, refresherBatchSize, offset)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Social cache refresh: failed to query following-policy circle identities")
			return 0
		}
		if len(identities) == 0 {
			return 0
		}

		sem := make(chan struct{}, refresherConcurrency)
		var wg sync.WaitGroup
		for _, identity := range identities {
			if identity.ProviderPubkey == "" {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(identity db.CircleIdentity) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := cache.refreshOne(ctx, pool, identity.ProviderPubkey); err != nil {
					logger.Logger.Warn().Err(err).Uint("identity_id", identity.ID).
						Msg("Social cache refresh: failed to refresh identity's contact list")
				}
			}(identity)
		}
		wg.Wait()

		offset += len(identities)
		if len(identities) < refresherBatchSize {
			return 0
		}
		if batchNum == maxRefresherBatchesPerTick-1 {
			logger.Logger.Warn().
				Int("batches_processed", maxRefresherBatchesPerTick).
				Int("next_offset", offset).
				Msg("Social cache refresh: per-tick batch cap reached, resuming from this offset on the next tick")
		}
	}
	return offset
}
