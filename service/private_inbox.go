package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"

	"github.com/flokiorg/lokihub/logger"
)

// Defaults for keeping the private-transport inbox reachable. Fields on
// privateTransport carry the values actually used, so tests can shorten them.
const (
	inboxProbeInterval = time.Minute
	inboxProbeTimeout  = 20 * time.Second
	// inboxProbeBackdate puts the probe's created_at in the past, as a real
	// envelope's is, so a subscription that has picked up a `since` misses it.
	inboxProbeBackdate = time.Hour
	inboxRetryMin      = time.Second
	inboxRetryMax      = time.Minute
	// inboxHealthySubscription is how long a subscription must last before its
	// end resets the retry backoff, so a relay that keeps closing it at once is
	// not asked again every second.
	inboxHealthySubscription = 30 * time.Second
)

// subscribeInbox keeps filter subscribed on every relay in urls until ctx ends,
// and returns the merged event stream, closed once every relay loop has exited.
//
// Not pool.SubscribeMany: when a connection drops it re-subscribes with `since`
// set to the reconnect time. A wrapped envelope's created_at is randomised up to
// two days into the past, so from then on the relay filtered out nearly every
// envelope, and nothing was logged. Here the filter is re-sent unchanged, after a
// relay CLOSE or a dropped connection alike.
func subscribeInbox(ctx context.Context, pool *nostr.SimplePool, urls []string, filter nostr.Filter) <-chan nostr.RelayEvent {
	out := make(chan nostr.RelayEvent)
	var wg sync.WaitGroup
	seen := map[string]bool{}
	for _, url := range urls {
		url = nostr.NormalizeURL(url)
		if seen[url] {
			continue
		}
		seen[url] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			keepInboxSubscribed(ctx, pool, url, filter, out)
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

func keepInboxSubscribed(ctx context.Context, pool *nostr.SimplePool, url string, filter nostr.Filter, out chan<- nostr.RelayEvent) {
	backoff := inboxRetryMin
	for ctx.Err() == nil {
		started := time.Now()
		err := subscribeInboxOnce(ctx, pool, url, filter, out)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= inboxHealthySubscription {
			backoff = inboxRetryMin
		}
		logger.Logger.Warn().Err(err).Str("relay", url).Dur("retry_in", backoff).
			Msg("Private transport inbox subscription ended, resubscribing")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, inboxRetryMax)
	}
}

// subscribeInboxOnce forwards one subscription's events until it ends, and
// reports why it ended.
func subscribeInboxOnce(ctx context.Context, pool *nostr.SimplePool, url string, filter nostr.Filter, out chan<- nostr.RelayEvent) error {
	relay, err := pool.EnsureRelay(url)
	if err != nil {
		return err
	}
	sub, err := relay.Subscribe(ctx, nostr.Filters{filter})
	if err != nil {
		return err
	}
	defer sub.Unsub()

	// Events closes when the relay sends CLOSED, the connection drops, or ctx ends.
	for event := range sub.Events {
		select {
		case out <- nostr.RelayEvent{Event: event, Relay: relay}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return context.Cause(sub.Context)
}

// inboxLiveness tells whether the inbox subscription still delivers, by sending
// a probe to the hub's own inbox and waiting for every relay that accepted it
// to hand it back. A subscription can stop delivering with no error at all, so
// this is the only signal that covers every way of going deaf.
type inboxLiveness struct {
	mu        sync.Mutex
	probeID   string
	delivered map[string]bool
	notify    chan struct{}
}

func newInboxLiveness() *inboxLiveness {
	return &inboxLiveness{notify: make(chan struct{}, 1)}
}

// observe consumes the probe: true means event was the probe and must not be
// treated as an envelope.
func (l *inboxLiveness) observe(event nostr.RelayEvent) bool {
	if event.Event == nil {
		return false
	}
	l.mu.Lock()
	isProbe := l.probeID != "" && event.ID == l.probeID
	if isProbe && event.Relay != nil {
		l.delivered[nostr.NormalizeURL(event.Relay.URL)] = true
	}
	l.mu.Unlock()
	if isProbe {
		select {
		case l.notify <- struct{}{}:
		default:
		}
	}
	return isProbe
}

var errProbeNotAccepted = errors.New("no relay accepted the inbox probe")

// probe publishes one probe and returns the relays that accepted it but did not
// deliver it back within timeout.
func (l *inboxLiveness) probe(ctx context.Context, pool *nostr.SimplePool, urls []string, inboxXOnly string, timeout time.Duration) ([]string, error) {
	content := make([]byte, 32)
	if _, err := rand.Read(content); err != nil {
		return nil, err
	}
	event := nostr.Event{
		Kind:      transport.KindPrivateRequest,
		CreatedAt: nostr.Timestamp(time.Now().Add(-inboxProbeBackdate).Unix()),
		Tags:      nostr.Tags{{"p", inboxXOnly}},
		Content:   hex.EncodeToString(content),
	}
	if err := event.Sign(nostr.GeneratePrivateKey()); err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.probeID = event.ID
	l.delivered = map[string]bool{}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.probeID = ""
		l.mu.Unlock()
	}()

	accepted := map[string]bool{}
	for result := range pool.PublishMany(ctx, urls, event) {
		if result.Error == nil {
			accepted[nostr.NormalizeURL(result.RelayURL)] = true
		}
	}
	if len(accepted) == 0 {
		return nil, errProbeNotAccepted
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		missing := l.missing(accepted)
		if len(missing) == 0 {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return missing, nil
		case <-l.notify:
		}
	}
}

func (l *inboxLiveness) missing(accepted map[string]bool) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var missing []string
	for url := range accepted {
		if !l.delivered[url] {
			missing = append(missing, url)
		}
	}
	return missing
}

// watchInboxLiveness probes the inbox every pt.probeInterval and calls deaf
// when a relay accepted a probe but never delivered it, then stops: the caller
// resubscribes, which starts a fresh watch.
func (svc *service) watchInboxLiveness(ctx context.Context, pool *nostr.SimplePool, pt *privateTransport, deaf func()) {
	ticker := time.NewTicker(pt.probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		missing, err := pt.liveness.probe(ctx, pool, svc.cfg.GetRelayUrls(), pt.inboxXOnly, pt.probeTimeout)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// Says nothing about the subscription: the relay refused the write.
			logger.Logger.Debug().Err(err).Msg("Private transport inbox probe could not be sent")
			continue
		}
		svc.setPrivateTransportInboxHealth(len(missing) == 0)
		if len(missing) > 0 {
			logger.Logger.Warn().Strs("relays", missing).
				Msg("Private transport inbox stopped delivering, resubscribing")
			deaf()
			return
		}
	}
}
