package service

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// ringFixture is a transport whose ring holds a current key and one retired key
// whose grace ends at until, with a sealer for each.
func ringFixture(t *testing.T, until int64) (pt *privateTransport, sealCurrent, sealRetired func(t *testing.T, env transport.Envelope) *nostr.Event) {
	t.Helper()
	pt, sealRetired = newUnwrapFixture(t)
	retired := inboxKey{index: 0, priv: pt.inboxPrivKey, xonly: pt.inboxXOnly, until: until}
	current, sealCurrent := newUnwrapFixture(t)
	pt.keyRing.Store(&inboxRing{current: inboxKey{index: 1, priv: current.inboxPrivKey, xonly: current.inboxXOnly}, retired: []inboxKey{retired}})
	return pt, sealCurrent, sealRetired
}

// After a rotation an envelope sealed to the retired inbox still opens while
// its grace lasts, and is dropped from the second its grace ends; the current
// inbox always opens.
func TestInboxRing_RetiredKeyOpensOnlyWithinItsGrace(t *testing.T) {
	now := time.Now()
	pt, sealCurrent, sealRetired := ringFixture(t, now.Unix()+300)

	_, _, err := pt.unwrap(sealCurrent(t, testEnvelope(t)), transport.DefaultLimits(), now)
	require.NoError(t, err, "the current inbox")
	_, _, err = pt.unwrap(sealRetired(t, testEnvelope(t)), transport.DefaultLimits(), now)
	require.NoError(t, err, "the retired inbox, inside its grace")
	assert.True(t, pt.acceptsPrivateEvent(sealRetired(t, testEnvelope(t))))
	assert.ElementsMatch(t, []string{pt.ring().current.xonly, pt.ring().retired[0].xonly}, pt.inboxPTags(now))

	atEnd := time.Unix(now.Unix()+300, 0)
	_, _, err = pt.unwrap(sealRetired(t, testEnvelope(t)), transport.DefaultLimits(), atEnd)
	require.Error(t, err, "dropped from the second its grace ends")
	_, ok := pt.keyFor(pt.ring().retired[0].xonly, atEnd)
	assert.False(t, ok)
	assert.Equal(t, []string{pt.ring().current.xonly}, pt.inboxPTags(atEnd))

	other := nostr.GeneratePrivateKey()
	otherPub, _ := nostr.GetPublicKey(other)
	_, ok = pt.keyFor(otherPub, now)
	assert.False(t, ok, "an inbox the hub never held")
}

// rotationHub is a running private transport on a scripted relay.
type rotationHub struct {
	svc   *service
	ts    *tests.TestService
	relay *scriptedRelay
	pt    *privateTransport
}

func newRotationHub(t *testing.T) *rotationHub {
	t.Helper()
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(ts.Remove)
	relay := newScriptedRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", relay.url, ""))
	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	svc := &service{cfg: ts.Cfg, db: ts.DB, keys: ts.Keys, lnClient: newAnnouncingLN(t, base)}

	ctx, cancel := context.WithCancel(context.Background())
	group, groupCtx := errgroup.WithContext(ctx)
	pool := nostr.NewSimplePool(groupCtx)
	require.NoError(t, svc.startPrivateTransport(groupCtx, pool, group))
	t.Cleanup(func() {
		cancel()
		_ = group.Wait()
	})
	relay.awaitREQ(t)
	h := &rotationHub{svc: svc, ts: ts, relay: relay, pt: svc.inboxRotation.pt}
	h.announcedInbox(t) // the start's announcement
	return h
}

// announcedInbox is the inbox of the next announcement the relay receives.
func (h *rotationHub) announcedInbox(t *testing.T) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-h.relay.published:
			if ev.Kind == transport.KindHubAnnouncement {
				if tag := ev.Tags.Find("inbox"); len(tag) >= 2 {
					return tag[1]
				}
			}
		case <-deadline:
			t.Fatal("no announcement reached the relay")
			return ""
		}
	}
}

// awaitFilter is the next REQ whose p tags are want.
func (h *rotationHub) awaitFilter(t *testing.T, want ...string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f := <-h.relay.reqs:
			if sameSet(f.Tags["p"], want) {
				return
			}
		case <-deadline:
			t.Fatalf("no subscription for inboxes %v", want)
		}
	}
}

// A rotation persists the next index, subscribes the new and the retired inbox,
// announces the new one and reports it; a restart within the grace rebuilds the
// same ring; the index a circle wallet notes is the new one.
func TestRotatePrivateTransportInbox_AnnouncesTheNewKeyAndKeepsTheOldForItsGrace(t *testing.T) {
	h := newRotationHub(t)
	old := h.pt.inbox()
	before := time.Now()

	inbox, err := h.svc.RotatePrivateTransportInbox()
	require.NoError(t, err)
	assert.NotEqual(t, old, inbox)
	assert.Equal(t, inbox, h.announcedInbox(t), "the new inbox is announced")
	assert.Equal(t, inbox, h.svc.GetPrivateTransportStatus().InboxPubkey)
	h.awaitFilter(t, inbox, old)

	ring, err := db.LoadInboxRing(h.ts.DB, time.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 1, ring.Current)
	require.Len(t, ring.Retired, 1)
	assert.EqualValues(t, 0, ring.Retired[0].Index)
	grace := h.ts.Cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds
	assert.InDelta(t, before.Unix()+grace, ring.Retired[0].Until, 2, "kept for max_request_age")
	index, err := db.CurrentInboxIndex(h.ts.DB)
	require.NoError(t, err)
	assert.EqualValues(t, 1, index)

	restarted, err := h.svc.newPrivateTransport()
	require.NoError(t, err)
	assert.Equal(t, inbox, restarted.inbox())
	assert.ElementsMatch(t, []string{inbox, old}, restarted.inboxPTags(time.Now()))
	_, ok := restarted.keyFor(old, time.Now())
	assert.True(t, ok)

	// Past the grace the retired key is gone from a restart too.
	after, err := db.LoadInboxRing(h.ts.DB, time.Unix(ring.Retired[0].Until, 0))
	require.NoError(t, err)
	assert.Empty(t, after.Retired)
}

// A hub whose transport is not running has no inbox to rotate, and a fresh
// node is at index 0.
func TestRotatePrivateTransportInbox_RefusedWithoutARunningTransport(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	svc := &service{cfg: ts.Cfg, db: ts.DB, keys: ts.Keys}
	_, err = svc.RotatePrivateTransportInbox()
	assert.ErrorIs(t, err, ErrPrivateTransportNotRunning)
	index, err := db.CurrentInboxIndex(ts.DB)
	require.NoError(t, err)
	assert.Zero(t, index)
}

// After a rotation the hidden-member reading keeps the key a live wallet was
// issued under and never lists the current one.
func TestRetiredInboxKeys_FollowTheCurrentIndex(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	hub := &db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, ts.DB.Create(hub).Error)
	zero, one := uint32(0), uint32(1)
	for i, idx := range []*uint32{&zero, &one} {
		require.NoError(t, ts.DB.Create(&db.App{Name: "w", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleWallet,
			ParentAppID: &hub.ID, CircleInboxKeyIndex: idx}).Error, i)
	}
	key0, err := ts.Keys.GetPrivateTransportKey(0)
	require.NoError(t, err)
	key1, err := ts.Keys.GetPrivateTransportKey(1)
	require.NoError(t, err)

	assert.Equal(t, []string{key1}, retiredInboxKeys(ts.DB, ts.Keys), "at index 0, a wallet of index 1 is the other")
	require.NoError(t, db.StoreInboxRing(ts.DB, db.InboxRing{Current: 1}))
	assert.Equal(t, []string{key0}, retiredInboxKeys(ts.DB, ts.Keys), "after the rotation, the index-0 wallet's key")
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		if seen[x] == 0 {
			return false
		}
		seen[x]--
	}
	return true
}
