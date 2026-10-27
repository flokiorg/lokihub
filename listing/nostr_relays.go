package listing

import (
	"context"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
)

// relayTimeout bounds one relay's publish or fetch.
const relayTimeout = 10 * time.Second

// NostrRelays is Relays over go-nostr, one short-lived pool per call: listings
// publish rarely, and nothing else shares these connections.
type NostrRelays struct{}

// Publish sends ev to every url at once and waits for each OK.
func (NostrRelays) Publish(ctx context.Context, urls []string, ev *nip01.Event) map[string]error {
	out := make(map[string]error, len(urls))
	gev := toGoNostr(ev)
	pctx, cancelPool := context.WithCancel(ctx)
	defer cancelPool()
	pool := nostr.NewSimplePool(pctx)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, u := range urls {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, relayTimeout)
			defer cancel()
			relay, err := pool.EnsureRelay(u)
			if err == nil {
				err = relay.Publish(cctx, gev)
			}
			mu.Lock()
			out[u] = err
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	return out
}

// Fetch reads the events matching f from urls until each relay's EOSE.
func (NostrRelays) Fetch(ctx context.Context, urls []string, f nip01.SubscriptionFilter) ([]*nip01.Event, error) {
	cctx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()
	pool := nostr.NewSimplePool(cctx)
	filter := nostr.Filter{IDs: f.IDs, Authors: f.Authors, Kinds: f.Kinds, Limit: f.Limit}
	if len(f.Tags) > 0 {
		filter.Tags = nostr.TagMap(f.Tags)
	}
	if f.Since > 0 {
		ts := nostr.Timestamp(f.Since) //nolint:gosec // a unix time
		filter.Since = &ts
	}
	if f.Until > 0 {
		ts := nostr.Timestamp(f.Until) //nolint:gosec // a unix time
		filter.Until = &ts
	}
	seen := map[string]bool{}
	var out []*nip01.Event
	for ie := range pool.FetchMany(cctx, urls, filter) {
		if ie.Event == nil || seen[ie.Event.ID] {
			continue
		}
		seen[ie.Event.ID] = true
		ev := fromGoNostr(ie.Event)
		if ev.Verify() != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

func toGoNostr(ev *nip01.Event) nostr.Event {
	tags := make(nostr.Tags, 0, len(ev.Tags))
	for _, t := range ev.Tags {
		tags = append(tags, nostr.Tag(t))
	}
	return nostr.Event{
		ID:        ev.ID,
		PubKey:    ev.PubKey,
		CreatedAt: nostr.Timestamp(ev.CreatedAt), //nolint:gosec // a unix time
		Kind:      ev.Kind,
		Tags:      tags,
		Content:   ev.Content,
		Sig:       ev.Sig,
	}
}

func fromGoNostr(ev *nostr.Event) *nip01.Event {
	tags := make([][]string, 0, len(ev.Tags))
	for _, t := range ev.Tags {
		tags = append(tags, []string(t))
	}
	return &nip01.Event{
		ID:        ev.ID,
		PubKey:    ev.PubKey,
		CreatedAt: uint64(ev.CreatedAt), //nolint:gosec // a unix time
		Kind:      ev.Kind,
		Tags:      tags,
		Content:   ev.Content,
		Sig:       ev.Sig,
	}
}
