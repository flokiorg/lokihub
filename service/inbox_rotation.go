package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/nbd-wtf/go-nostr"
)

// The private-transport inbox key ring (NIP-CASH §The Hub Announcement). The
// inbox is a key of its own, derived at an index from the node's keys; rotating
// it moves to the next index and announces the new key. An envelope a client
// built before it saw the new announcement is fresh for at most
// max_request_age, so the retired key keeps opening envelopes for that long
// after the rotation, then is dropped from the subscription. A retired key also
// stays in NIP-CW's hidden-member reading while a wallet issued under it lives
// (retiredInboxKeys), which is the wallets' own record, not this ring.

// inboxKey is one key of the ring; until is 0 for the current key.
type inboxKey struct {
	index uint32
	priv  string
	xonly string
	until int64
}

// inboxRing is the current key and the retired keys still in their grace.
type inboxRing struct {
	current inboxKey
	retired []inboxKey
}

// inboxKeyAt derives the inbox key at index.
func (svc *service) inboxKeyAt(index uint32, nodeXOnly string) (inboxKey, error) {
	priv, err := svc.keys.GetPrivateTransportKey(index)
	if err != nil {
		return inboxKey{}, fmt.Errorf("derive private transport key %d: %w", index, err)
	}
	xonly, err := nostr.GetPublicKey(priv)
	if err != nil {
		return inboxKey{}, fmt.Errorf("derive private transport pubkey %d: %w", index, err)
	}
	if xonly == nodeXOnly {
		// Would mean the hub cannot decrypt its own mail. Impossible with H+3
		// derivation, but the consequence is silence, so check rather than assume.
		return inboxKey{}, errors.New("private transport inbox key collides with the node identity")
	}
	return inboxKey{index: index, priv: priv, xonly: xonly}, nil
}

// buildInboxRing derives every key of st.
func (svc *service) buildInboxRing(st db.InboxRing, nodeXOnly string) (*inboxRing, error) {
	cur, err := svc.inboxKeyAt(st.Current, nodeXOnly)
	if err != nil {
		return nil, err
	}
	ring := &inboxRing{current: cur}
	for _, r := range st.Retired {
		k, err := svc.inboxKeyAt(r.Index, nodeXOnly)
		if err != nil {
			return nil, err
		}
		k.until = r.Until
		ring.retired = append(ring.retired, k)
	}
	return ring, nil
}

// ring is the transport's key ring: the one set by a rotation or at start, or
// the single key the transport was built with.
func (pt *privateTransport) ring() *inboxRing {
	if r := pt.keyRing.Load(); r != nil {
		return r
	}
	return &inboxRing{current: inboxKey{priv: pt.inboxPrivKey, xonly: pt.inboxXOnly}}
}

// inbox is the inbox key clients are told to use now.
func (pt *privateTransport) inbox() string { return pt.ring().current.xonly }

// keyFor is the private key of the inbox an event is addressed to: the current
// one, or a retired one still in its grace at now.
func (pt *privateTransport) keyFor(p string, now time.Time) (string, bool) {
	r := pt.keyRing.Load()
	if r == nil {
		// The wire gate runs this per event: no ring is built for it.
		if p == pt.inboxXOnly {
			return pt.inboxPrivKey, true
		}
		return "", false
	}
	if p == r.current.xonly {
		return r.current.priv, true
	}
	for _, k := range r.retired {
		if p == k.xonly && now.Unix() < k.until {
			return k.priv, true
		}
	}
	return "", false
}

// inboxPTags are the inboxes the subscription reads at now.
func (pt *privateTransport) inboxPTags(now time.Time) []string {
	r := pt.ring()
	out := []string{r.current.xonly}
	for _, k := range r.retired {
		if now.Unix() < k.until {
			out = append(out, k.xonly)
		}
	}
	return out
}

// signalRekey asks the subscription to resubscribe with the ring's inboxes.
func (pt *privateTransport) signalRekey() {
	select {
	case pt.rekey <- struct{}{}:
	default:
	}
}

// rekeyAfterGrace resubscribes once each retired key's grace has ended, so the
// relay stops sending what the hub no longer opens.
func (pt *privateTransport) rekeyAfterGrace() {
	for _, k := range pt.ring().retired {
		if wait := time.Until(time.Unix(k.until, 0)); wait > 0 {
			time.AfterFunc(wait+time.Second, pt.signalRekey)
		}
	}
}

// ErrPrivateTransportNotRunning: there is no inbox to rotate.
var ErrPrivateTransportNotRunning = errors.New("the private transport is not running")

// inboxRotation is what the running transport needs to rotate.
type inboxRotation struct {
	mu   sync.Mutex
	ctx  context.Context
	pool *nostr.SimplePool
	pt   *privateTransport
}

// RotatePrivateTransportInbox moves the inbox to the next key index: it is
// persisted first, then the subscription reads both inboxes, then the new one is
// announced. The retired key opens envelopes for max_request_age more. Returns
// the new inbox; an announcement failure is returned with it, the rotation
// standing.
func (svc *service) RotatePrivateTransportInbox() (string, error) {
	rot := &svc.inboxRotation
	rot.mu.Lock()
	defer rot.mu.Unlock()
	pt := rot.pt
	if pt == nil {
		return "", ErrPrivateTransportNotRunning
	}
	now := time.Now()
	st, err := db.LoadInboxRing(svc.db, now)
	if err != nil {
		return "", err
	}
	if st.Current == math.MaxUint32 {
		return "", errors.New("no inbox key index is left to rotate to")
	}
	grace := svc.cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds
	next := db.InboxRing{Current: st.Current + 1,
		Retired: append(st.Retired, db.RetiredInbox{Index: st.Current, Until: now.Unix() + grace})}
	ring, err := svc.buildInboxRing(next, pt.nodeXOnly)
	if err != nil {
		return "", err
	}
	if err := db.StoreInboxRing(svc.db, next); err != nil {
		return "", fmt.Errorf("store the inbox key ring: %w", err)
	}
	pt.keyRing.Store(ring)
	svc.setPrivateTransportIdentity(pt.nodeXOnly, ring.current.xonly)
	pt.signalRekey()
	pt.rekeyAfterGrace()
	logger.Logger.Info().Str("inbox", ring.current.xonly).Uint32("index", next.Current).
		Int64("retired_grace_secs", grace).Msg("Rotated the private transport inbox key")
	if err := svc.publishTransportAnnouncement(rot.ctx, rot.pool, pt); err != nil {
		return ring.current.xonly, fmt.Errorf("rotated, but the new announcement failed: %w", err)
	}
	return ring.current.xonly, nil
}
