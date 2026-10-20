package service

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/logger"
)

const socialCacheTTL = time.Hour

// RelayQueryTimeout bounds any single follow-set relay query. A var, not a
// const, so tests can lower it instead of waiting out the real 15s.
var RelayQueryTimeout = 15 * time.Second

// FollowSet is one resolved NIP-51 follow set, read under NIP-CW §Membership.
type FollowSet struct {
	Provider    string
	D           string
	Title       string
	Description string
	// Public are the set's `p` members, in tag order.
	Public []string
	// Hidden are the `hidden_p` members this hub's inbox key decrypts, minus
	// any already listed in Public.
	Hidden []string
	// Event is the set as published, kept so a republish can carry over the
	// tags lokihub does not edit.
	Event     *nostr.Event
	FetchedAt time.Time

	members map[string]struct{}
}

// IsMember reports whether pubkey is a public or hidden member.
// NewFollowSet builds a set from its members, for callers that hold the
// lists rather than the event.
func NewFollowSet(provider, d string, public, hidden []string) *FollowSet {
	set := &FollowSet{Provider: provider, D: d, Public: public, Hidden: hidden, members: make(map[string]struct{})}
	for _, pubkey := range append(append([]string{}, public...), hidden...) {
		set.members[pubkey] = struct{}{}
	}
	return set
}

func (fs *FollowSet) IsMember(pubkey string) bool {
	_, ok := fs.members[pubkey]
	return ok
}

// MemberCount counts public and hidden members together. Both lists are
// deduplicated, and Hidden excludes anyone in Public.
func (fs *FollowSet) MemberCount() int {
	return len(fs.Public) + len(fs.Hidden)
}

// outboxEntry caches an owner's discovered NIP-65 write relays (see
// discoverWriteRelays), so repeated follow-set fetches don't re-query
// kind:10002 every time. NIP-65 relay lists change rarely, so this outlives
// a cached set.
type outboxEntry struct {
	relayUrls []string
	fetchedAt time.Time
}

// followSetKey is the set's NIP-01 address, which keys the cache.
func followSetKey(provider, d string) string {
	return "30000:" + provider + ":" + d
}

// nostrSocialCache implements controllers.NostrSocialCache.
// It fetches and caches follow sets (NIP-51 kind 30000) from nostr relays.
// C1: sync.RWMutex allows parallel cache reads (zero contention on hits).
// C1: singleflight.Group collapses concurrent stale-cache misses to one relay query per set.
// C2: stale entries are evicted in the slow path (write lock already held) to bound map size.
type nostrSocialCache struct {
	cfg         config.Config
	mu          sync.RWMutex
	cache       map[string]*FollowSet   // keyed by followSetKey
	outboxCache map[string]*outboxEntry // keyed by owner pubkey; guarded by mu alongside cache
	sfg         singleflight.Group      // deduplicates concurrent relay fetches per set
	ready       atomic.Bool             // set once by StartNostrSocialCacheRefresher's initial warm-up pass; see IsAuthorized

	// sharedPool is set once by StartNostrSocialCacheRefresher to the
	// long-lived, already-warm pool used by the periodic refresher.
	// fetchAndCache reuses it when present instead of cold-dialing a new pool
	// per call; falls back to an ephemeral pool if unset (e.g. in tests).
	sharedPool atomic.Pointer[nostr.SimplePool]

	// inboxKey returns the private key hidden_p entries are encrypted to: the
	// private transport's inbox key (NIP-CW §Hidden Members). Nil, or an
	// error, means no hidden entry can be read.
	inboxKey func() (string, error)

	// floor is the newest version of each set read so far, keyed like cache
	// and guarded by mu. Never evicted, and saved through persistDB, so a
	// relay serving an older version can't roll membership back.
	floor     map[string]*nostr.Event
	persistDB atomic.Pointer[gorm.DB]
	persistMu sync.Mutex
}

func NewNostrSocialCache(cfg config.Config) *nostrSocialCache {
	return &nostrSocialCache{
		cfg:         cfg,
		cache:       make(map[string]*FollowSet),
		outboxCache: make(map[string]*outboxEntry),
		floor:       make(map[string]*nostr.Event),
	}
}

// IsAuthorized checks whether requesterPubkey is authorized under the circle
// identity's policy. For allowlist policy it queries the DB; for follow_set
// it reads the identity's follow set. identity may be shared by multiple
// circle_hub apps concurrently — authorization is scoped to the identity, not
// any one app.
func (s *nostrSocialCache) IsAuthorized(ctx context.Context, requesterPubkey string, identity *db.CircleIdentity, gormDB *gorm.DB) (bool, error) {
	switch identity.Policy {
	case db.CirclePolicyAllowlist:
		return s.isInAllowlist(gormDB, identity.ID, requesterPubkey)

	case db.CirclePolicyFollowSet:
		// Provider-controlled: only the provider can sign the set.
		//
		// If this set has no cached entry yet AND the hub's initial startup
		// warm-up sweep (StartNostrSocialCacheRefresher) hasn't finished, refuse
		// fast with ErrSocialCacheWarmingUp instead of falling through to a
		// cold-miss fetch. Without this, a request landing in that narrow
		// startup window would block on a live relay round-trip
		// (RelayQueryTimeout, up to ~15s). Once warm-up completes, or once this
		// set has any cached entry, this gate is a single atomic load.
		if _, cached := s.peek(identity.ProviderPubkey, identity.FollowSetD); !cached && !s.ready.Load() {
			return false, constants.ErrSocialCacheWarmingUp
		}

		set, err := s.getFollowSet(ctx, identity.ProviderPubkey, identity.FollowSetD)
		if err != nil {
			return false, err
		}
		return set.IsMember(requesterPubkey), nil
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

// peek returns the set's cached entry, if any, without ever fetching.
func (s *nostrSocialCache) peek(provider, d string) (*FollowSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.cache[followSetKey(provider, d)]
	return entry, ok
}

// getFollowSet returns the cached set, fetching it on a miss or once the entry
// is past the eviction cutoff. A stale entry inside that cutoff is served
// as-is — freshness is maintained by the periodic background refresh (see
// StartNostrSocialCacheRefresher), so the request path rarely blocks on a
// relay round-trip.
//
// Revocation latency: as long as StartNostrSocialCacheRefresher is running,
// every follow_set identity is refreshed at least once every
// socialCacheRefreshInterval (5 minutes) while the identity count stays within
// maxRefresherBatchesPerTick*refresherBatchSize; beyond that it wraps across
// ticks (see runSocialCacheRefresh). If the refresher isn't running at all,
// staleness is bounded by the 2×socialCacheTTL cutoff (staleCutoff), past which
// the entry is refetched rather than served.
func (s *nostrSocialCache) getFollowSet(ctx context.Context, provider, d string) (*FollowSet, error) {
	if entry, ok := s.peek(provider, d); ok && !entry.FetchedAt.Before(staleCutoff()) {
		return entry, nil
	}

	v, err, _ := s.sfg.Do(followSetKey(provider, d), func() (interface{}, error) {
		return s.fetchAndCache(ctx, provider, d)
	})
	if err != nil {
		return nil, err
	}
	return v.(*FollowSet), nil
}

// maxOutboxRelays bounds how many of the owner's own NIP-65 write relays get
// merged into the follow-set query on top of the configured General relays.
const maxOutboxRelays = 6

// outboxDiscoveryTimeout bounds the NIP-65 relay-list (kind:10002) lookup
// that precedes the follow-set query, independent of RelayQueryTimeout. A
// var, not a const, so tests can lower it too.
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

// fetchAllBounded collects every event relayUrls return for filter until each
// has sent EOSE, or ctx expires. Bounded the same way queryRelaysBounded is.
func fetchAllBounded(ctx context.Context, pool *nostr.SimplePool, relayUrls []string, filter nostr.Filter) []nostr.Event {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := pool.FetchMany(ctx, relayUrls, filter)
	var collected []nostr.Event
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return collected
			}
			if ev.Event != nil {
				collected = append(collected, *ev.Event)
			}
		case <-ctx.Done():
			return collected
		}
	}
}

// discoverWriteRelays looks up ownerPubkey's NIP-65 relay list (kind:10002)
// on baseRelayUrls and returns up to maxOutboxRelays of the relays marked (or
// left unmarked, meaning both read+write) as write relays — the relays
// ownerPubkey actually publishes to. Returns nil (not an error) on any
// failure to discover — a best-effort widening of the search, never a
// precondition for the follow-set query itself.
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

// selectFollowSetEvent applies NIP-CW §Membership's "which event" rule: only a
// kind-30000 event whose id and signature verify, authored by provider, whose
// `d` tag is d. Of those, the highest created_at wins, and on a tie the lowest
// id. The id is checked because the signature doesn't cover it, and the tie
// break reads it.
func selectFollowSetEvent(events []nostr.Event, provider, d string) *nostr.Event {
	var best *nostr.Event
	for i := range events {
		ev := &events[i]
		if ev.Kind != db.KindFollowSet || ev.PubKey != provider || ev.Tags.GetD() != d {
			continue
		}
		if !ev.CheckID() {
			continue
		}
		if ok, err := ev.CheckSignature(); !ok || err != nil {
			continue
		}
		if best == nil || supersedes(ev, best) {
			best = ev
		}
	}
	return best
}

// supersedes is NIP-01's order for addressable events: a newer created_at
// wins, and on a tie the lower id.
func supersedes(a, b *nostr.Event) bool {
	return a.CreatedAt > b.CreatedAt || (a.CreatedAt == b.CreatedAt && a.ID < b.ID)
}

// parseFollowSet reads members from a selected event (NIP-CW §Membership):
// public `p` tags plus the `hidden_p` entries inboxPrivKey decrypts. NIP-51
// private items in `content` are never read, and no tag other than `d`, `p`
// and `hidden_p` affects membership. Title and description are display-only.
func parseFollowSet(ev *nostr.Event, d, inboxPrivKey string) *FollowSet {
	set := &FollowSet{
		Provider:  ev.PubKey,
		D:         d,
		Event:     ev,
		FetchedAt: time.Now(),
		members:   make(map[string]struct{}),
	}

	var conversationKey [32]byte
	canDecrypt := false
	if inboxPrivKey != "" {
		key, err := nip44.GenerateConversationKey(ev.PubKey, inboxPrivKey)
		if err == nil {
			conversationKey, canDecrypt = key, true
		}
	}

	var hiddenTags []string
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "title":
			if set.Title == "" {
				set.Title = tag[1]
			}
		case "description":
			if set.Description == "" {
				set.Description = tag[1]
			}
		case "p":
			// A malformed value could never match a requester, so it is
			// dropped rather than counted.
			if !nostr.IsValid32ByteHex(tag[1]) {
				continue
			}
			if _, dup := set.members[tag[1]]; !dup {
				set.members[tag[1]] = struct{}{}
				set.Public = append(set.Public, tag[1])
			}
		case "hidden_p":
			hiddenTags = append(hiddenTags, tag[1])
		}
	}

	if !canDecrypt {
		return set
	}
	for _, payload := range hiddenTags {
		// An entry this hub can't open may be meant for another hub sharing
		// the identity, so it is skipped without error (§Hidden Members).
		plaintext, err := nip44.Decrypt(payload, conversationKey)
		if err != nil || !nostr.IsValid32ByteHex(plaintext) {
			continue
		}
		if _, dup := set.members[plaintext]; !dup {
			set.members[plaintext] = struct{}{}
			set.Hidden = append(set.Hidden, plaintext)
		}
	}
	return set
}

// hiddenMemberKey resolves the inbox key, or "" when none is available.
func (s *nostrSocialCache) hiddenMemberKey() string {
	if s.inboxKey == nil {
		return ""
	}
	key, err := s.inboxKey()
	if err != nil {
		logger.Logger.Warn().Err(err).Msg("No inbox key, so hidden circle members can't be read")
		return ""
	}
	return key
}

// queryAndStore fetches the set at (provider, d) using pool and stores the
// result in the cache. Shared by the cold-miss fallback (fetchAndCache, which
// may open an ephemeral pool) and the background refresher (refreshOne).
func (s *nostrSocialCache) queryAndStore(ctx context.Context, pool *nostr.SimplePool, provider, d string) (*FollowSet, error) {
	baseRelayUrls := s.cfg.GetGeneralRelayUrls()
	relayUrls := mergeRelayUrls(baseRelayUrls, s.getOutboxRelays(ctx, pool, provider, baseRelayUrls))

	// Bound the relay round-trip: without this, an unresponsive relay can stall
	// this call (and, via singleflight, every concurrent caller waiting on the
	// same set) for as long as the caller's own context allows.
	fetchCtx, cancel := context.WithTimeout(ctx, RelayQueryTimeout)
	defer cancel()

	events := fetchAllBounded(fetchCtx, pool, relayUrls, nostr.Filter{
		Authors: []string{provider},
		Kinds:   []int{db.KindFollowSet},
		Tags:    nostr.TagMap{"d": []string{d}},
	})
	selected := selectFollowSetEvent(events, provider, d)

	if selected == nil {
		// A caller-side cancellation is not a confirmed miss: no warning.
		if ctx.Err() == nil {
			logger.Logger.Warn().Str("address", followSetKey(provider, d)).Strs("relays", relayUrls).
				Msg("No follow set found")
		}
		// Preserve the last known-good set instead of dropping it — a
		// transient relay hiccup must not look like the owner revoked
		// everyone. A genuine change still lands on the next successful fetch.
		// Stale entries are evicted first, so relays that stay unreachable
		// can't keep an old set, and the members it lists, alive forever.
		s.mu.Lock()
		s.evictStaleUnlocked()
		existing, ok := s.cache[followSetKey(provider, d)]
		s.mu.Unlock()
		if ok {
			return existing, nil
		}
		// NIP-CW §Membership: a set not found yet is unresolved, never empty.
		// Nothing is cached, so the next request retries.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, constants.ErrCircleMembershipUnresolved
	}

	set := parseFollowSet(selected, d, s.hiddenMemberKey())
	return s.store(set), nil
}

// store caches set unless a newer version of it is already cached or was
// read before, and returns whichever is kept. A version older than the floor
// is replaced by the floor's, so neither eviction nor a restart lets relays
// roll membership back.
func (s *nostrSocialCache) store(set *FollowSet) *FollowSet {
	key := followSetKey(set.Provider, set.D)
	s.mu.RLock()
	floor := s.floor[key]
	s.mu.RUnlock()
	if floor != nil && supersedes(floor, set.Event) {
		set = parseFollowSet(floor, set.D, s.hiddenMemberKey())
	}

	kept := s.storeUnlessOlder(key, set)
	s.persistFloor(set.Provider, set.D)
	return kept
}

func (s *nostrSocialCache) storeUnlessOlder(key string, set *FollowSet) *FollowSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	// C2: evict entries older than 2×TTL while already holding the write lock.
	defer s.evictStaleUnlocked()

	kept := set
	if existing, ok := s.cache[key]; ok && supersedes(existing.Event, set.Event) {
		// A relay still serving an older version must not roll back a newer
		// one, such as the version this hub just published. The entry is
		// copied, not updated in place, since readers hold it without a lock.
		refreshed := *existing
		refreshed.FetchedAt = time.Now()
		kept = &refreshed
	}
	s.cache[key] = kept
	if floor := s.floor[key]; floor == nil || supersedes(kept.Event, floor) {
		s.floor[key] = kept.Event
	}
	return kept
}

// persistFloor saves the newest version read of the set at (provider, d) on
// every identity using it, so loadFloor restores it after a restart. A no-op
// until StartNostrSocialCacheRefresher supplies the database, and for rows
// that already hold it.
func (s *nostrSocialCache) persistFloor(provider, d string) {
	gormDB := s.persistDB.Load()
	if gormDB == nil {
		return
	}
	// Serialized, and the floor read under it, so a slower write of an older
	// version can't land after a newer one.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	floor := s.floor[followSetKey(provider, d)]
	s.mu.RUnlock()
	if floor == nil {
		return
	}
	raw, err := json.Marshal(floor)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to encode a follow set for saving")
		return
	}
	err = gormDB.Model(&db.CircleIdentity{}).
		Where("policy = ? AND provider_pubkey = ? AND follow_set_d = ?", db.CirclePolicyFollowSet, provider, d).
		Where("last_follow_set_event IS NULL OR last_follow_set_event <> ?", string(raw)).
		Update("last_follow_set_event", string(raw)).Error
	if err != nil {
		logger.Logger.Error().Err(err).Str("address", followSetKey(provider, d)).Msg("Failed to save the newest follow set version")
	}
}

// loadFloor restores the newest version of each set saved by persistFloor.
// The saved event must pass the same rule as one read from a relay.
func (s *nostrSocialCache) loadFloor(gormDB *gorm.DB) {
	var identities []db.CircleIdentity
	if err := gormDB.Where("policy = ? AND last_follow_set_event IS NOT NULL", db.CirclePolicyFollowSet).
		Find(&identities).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to load saved follow set versions")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, identity := range identities {
		var ev nostr.Event
		if err := json.Unmarshal([]byte(*identity.LastFollowSetEvent), &ev); err != nil {
			continue
		}
		selected := selectFollowSetEvent([]nostr.Event{ev}, identity.ProviderPubkey, identity.FollowSetD)
		if selected == nil {
			continue
		}
		key := followSetKey(identity.ProviderPubkey, identity.FollowSetD)
		if floor := s.floor[key]; floor == nil || supersedes(selected, floor) {
			s.floor[key] = selected
		}
	}
}

// fetchAndCache performs the relay query and stores the result. Reuses
// s.sharedPool when set, falling back to a one-off ephemeral pool otherwise.
func (s *nostrSocialCache) fetchAndCache(ctx context.Context, provider, d string) (*FollowSet, error) {
	pool := s.sharedPool.Load()
	if pool == nil {
		pool = nostr.NewSimplePool(ctx)
	}
	return s.queryAndStore(ctx, pool, provider, d)
}

// refreshOne refreshes the cached set using a shared, already-connected pool.
// Routed through the same singleflight key as getFollowSet so a background
// refresh and a concurrent cold-miss request collapse into one relay query.
func (s *nostrSocialCache) refreshOne(ctx context.Context, pool *nostr.SimplePool, provider, d string) error {
	_, err, _ := s.sfg.Do(followSetKey(provider, d), func() (interface{}, error) {
		return s.queryAndStore(ctx, pool, provider, d)
	})
	return err
}

// PeekFollowSet returns the cached set without ever fetching. ok is false on
// a cache miss, which callers treat as "not yet known", never as empty. Used
// by list-style responses that must never block on a relay.
func (s *nostrSocialCache) PeekFollowSet(provider, d string) (*FollowSet, bool) {
	return s.peek(provider, d)
}

// FollowSet returns the cached set, or fetches it once on a miss. Reserved for
// single-target reads, unlike PeekFollowSet.
func (s *nostrSocialCache) FollowSet(ctx context.Context, provider, d string) (*FollowSet, error) {
	return s.getFollowSet(ctx, provider, d)
}

// RefreshFollowSet fetches the set now, whatever is cached. Used right after a
// circle is created and by the manual Sync action.
func (s *nostrSocialCache) RefreshFollowSet(ctx context.Context, provider, d string) (*FollowSet, error) {
	v, err, _ := s.sfg.Do(followSetKey(provider, d), func() (interface{}, error) {
		return s.fetchAndCache(ctx, provider, d)
	})
	if err != nil {
		return nil, err
	}
	return v.(*FollowSet), nil
}

// storePublished caches a set this hub just signed, so membership reflects it
// without waiting for relays to serve it back.
func (s *nostrSocialCache) storePublished(ev *nostr.Event, d string) *FollowSet {
	return s.store(parseFollowSet(ev, d, s.hiddenMemberKey()))
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

// staleCutoff is the fetch time before which a cached set is too old to
// decide membership on: evicted, and refetched rather than served.
func staleCutoff() time.Time {
	return time.Now().Add(-2 * socialCacheTTL)
}

// evictStaleUnlocked removes cache entries fetched before staleCutoff.
// Must be called with s.mu held for writing.
func (s *nostrSocialCache) evictStaleUnlocked() {
	cutoff := staleCutoff()
	for k, e := range s.cache {
		if e.FetchedAt.Before(cutoff) {
			delete(s.cache, k)
			// The provider's outbox relays may still serve another of its
			// sets, so they are only dropped once none of them is cached.
			if !s.providerCachedUnlocked(e.Provider) {
				delete(s.outboxCache, e.Provider)
			}
		}
	}
}

func (s *nostrSocialCache) providerCachedUnlocked(provider string) bool {
	for _, e := range s.cache {
		if e.Provider == provider {
			return true
		}
	}
	return false
}

// socialCacheRefreshInterval bounds how stale a cached follow set can get
// before the periodic refresher revisits it — meaningfully shorter than
// socialCacheTTL so entries are kept warm well before they'd go stale.
const socialCacheRefreshInterval = 5 * time.Minute

// refresherBatchSize bounds memory per refresh tick.
const refresherBatchSize = 200

// maxRefresherBatchesPerTick bounds total work per tick, mirroring
// maxBatchesPerTick in cash_cleanup_service.go: a large number of follow_set
// identities spreads across multiple ticks instead of monopolizing this goroutine.
const maxRefresherBatchesPerTick = 10

// refresherConcurrency bounds how many sets are refreshed in parallel within a
// batch, so a tick's wall-clock time stays close to a single relay round-trip.
const refresherConcurrency = 20

// StartNostrSocialCacheRefresher runs a background goroutine that periodically
// refreshes every follow_set identity's set using a shared, already-connected
// pool. This keeps IsAuthorized's request path serving from cache in the
// steady state. It runs an immediate pass before entering the ticker loop so
// caches are warm right after startup; cache.ready flips to true once that
// pass returns, which unblocks IsAuthorized's startup-readiness gate.
func StartNostrSocialCacheRefresher(ctx context.Context, gormDB *gorm.DB, cache *nostrSocialCache, pool *nostr.SimplePool) {
	// Let cold-miss fetches (fetchAndCache) reuse this same already-warm pool
	// too — see sharedPool's doc comment.
	cache.sharedPool.Store(pool)
	// Before the first fetch, so no relay can serve a version older than the
	// last one this hub read before it restarted.
	cache.loadFloor(gormDB)
	cache.persistDB.Store(gormDB)
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

// runSocialCacheRefresh refreshes follow_set circle_identities starting at
// startOffset, in bounded batches processed concurrently (see
// refresherConcurrency). A single identity's relay failure is logged and
// skipped — it never aborts the rest of the sweep. Returns the offset to
// resume from on the next tick, wrapping to 0 at the end of the table (or on
// a query error). Identities sharing one set address are refreshed once.
func runSocialCacheRefresh(ctx context.Context, gormDB *gorm.DB, cache *nostrSocialCache, pool *nostr.SimplePool, startOffset int) int {
	offset := startOffset
	for batchNum := 0; batchNum < maxRefresherBatchesPerTick; batchNum++ {
		identities, err := queries.GetFollowSetCircleIdentities(gormDB, refresherBatchSize, offset)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Social cache refresh: failed to query follow_set circle identities")
			return 0
		}
		if len(identities) == 0 {
			return 0
		}

		sem := make(chan struct{}, refresherConcurrency)
		var wg sync.WaitGroup
		seen := make(map[string]struct{}, len(identities))
		for _, identity := range identities {
			if identity.ProviderPubkey == "" || identity.FollowSetD == "" {
				continue
			}
			key := followSetKey(identity.ProviderPubkey, identity.FollowSetD)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			wg.Add(1)
			sem <- struct{}{}
			go func(identity db.CircleIdentity) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := cache.refreshOne(ctx, pool, identity.ProviderPubkey, identity.FollowSetD); err != nil {
					logger.Logger.Warn().Err(err).Uint("identity_id", identity.ID).
						Msg("Social cache refresh: failed to refresh identity's follow set")
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
