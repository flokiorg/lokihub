package service

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newUnwrapFixture builds a transport holding a real inbox key, plus a client-side
// sealer that produces envelopes addressed to it.
func newUnwrapFixture(t *testing.T) (*privateTransport, func(t *testing.T, env transport.Envelope) *nostr.Event) {
	t.Helper()

	inboxPriv := nostr.GeneratePrivateKey()
	inboxPub, err := nostr.GetPublicKey(inboxPriv)
	require.NoError(t, err)

	pt := &privateTransport{
		inboxPrivKey: inboxPriv,
		inboxXOnly:   inboxPub,
		nodeXOnly:    strings.Repeat("bb", 32),
		nonces:       newPrivateNonceSet(),
	}

	// A client: fresh ephemeral key per envelope, exactly as the real one does,
	// which is what makes the hub's ECDH uncacheable.
	seal := func(t *testing.T, env transport.Envelope) *nostr.Event {
		t.Helper()
		ephemeralPriv := nostr.GeneratePrivateKey()
		ephemeralPub, err := nostr.GetPublicKey(ephemeralPriv)
		require.NoError(t, err)

		plaintext, err := env.Encode(transport.DefaultLimits())
		require.NoError(t, err)

		ck, err := nip44.GenerateConversationKey(inboxPub, ephemeralPriv)
		require.NoError(t, err)
		ciphertext, err := nip44.Encrypt(string(plaintext), ck)
		require.NoError(t, err)

		return &nostr.Event{
			Kind:    transport.KindPrivateRequest,
			PubKey:  ephemeralPub,
			Tags:    nostr.Tags{nostr.Tag{"p", inboxPub}},
			Content: ciphertext,
		}
	}
	return pt, seal
}

func testEnvelope(t *testing.T) transport.Envelope {
	t.Helper()
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)

	return transport.Envelope{
		Version:  transport.EnvelopeVersion,
		NotAfter: time.Now().Add(60 * time.Second).Unix(),
		Nonce:    nonce,
		ReplyTo:  replyTo,
		Items: []transport.Item{{
			ID:     "1",
			Target: strings.Repeat("cc", 32),
			Method: "cash_status",
			Params: json.RawMessage(`{}`),
			Proof:  json.RawMessage(`{"kind":23192}`),
			// A placeholder like the slice proof above: these tests exercise the
			// ENVELOPE layer — sealing, unwrapping, replay, freshness — which never
			// verifies either proof. It only requires both to be present, since an
			// item missing one could not be served anyway.
			BillProof: json.RawMessage(`{"kind":23193}`),
		}},
	}
}

func TestUnwrap_RoundTrip(t *testing.T) {
	pt, seal := newUnwrapFixture(t)
	env := testEnvelope(t)

	got, conversationKey, err := pt.unwrap(seal(t, env), transport.DefaultLimits(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, env.Nonce, got.Nonce)
	assert.Equal(t, env.ReplyTo, got.ReplyTo)
	require.Len(t, got.Items, 1)
	assert.Equal(t, "cash_status", got.Items[0].Method)

	// The conversation key must come back usable: the response is encrypted under
	// a key derived from it, which is what avoids a second ECDH on the way out.
	replyKey, err := transport.DeriveReplyKey(conversationKey, got.ReplyTo)
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, replyKey)
}

// TestUnwrap_RejectsReplay is the property the nonce set exists for.
func TestUnwrap_RejectsReplay(t *testing.T) {
	pt, seal := newUnwrapFixture(t)
	event := seal(t, testEnvelope(t))

	_, _, err := pt.unwrap(event, transport.DefaultLimits(), time.Now())
	require.NoError(t, err, "the first delivery must be accepted")

	// Byte-identical redelivery, which is exactly what an attacker who copied the
	// event off the relay can produce.
	_, _, err = pt.unwrap(event, transport.DefaultLimits(), time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already seen")
}

// TestUnwrap_RejectsJunkAndForeignCiphertext covers what an attacker can actually
// send. None of it is distinguishable before the decrypt attempt, which is the
// intrinsic cost of an anonymous inbox.
func TestUnwrap_RejectsJunkAndForeignCiphertext(t *testing.T) {
	pt, seal := newUnwrapFixture(t)

	t.Run("random content", func(t *testing.T) {
		event := seal(t, testEnvelope(t))
		event.Content = strings.Repeat("deadbeef", 40)
		_, _, err := pt.unwrap(event, transport.DefaultLimits(), time.Now())
		require.Error(t, err)
	})

	t.Run("encrypted to someone else", func(t *testing.T) {
		// Well-formed NIP-44, just not for us. The MAC is what refuses it.
		strangerPriv := nostr.GeneratePrivateKey()
		strangerPub, err := nostr.GetPublicKey(strangerPriv)
		require.NoError(t, err)
		senderPriv := nostr.GeneratePrivateKey()
		senderPub, err := nostr.GetPublicKey(senderPriv)
		require.NoError(t, err)

		ck, err := nip44.GenerateConversationKey(strangerPub, senderPriv)
		require.NoError(t, err)
		ciphertext, err := nip44.Encrypt(`{"v":1}`, ck)
		require.NoError(t, err)

		_, _, err = pt.unwrap(&nostr.Event{
			Kind: transport.KindPrivateRequest, PubKey: senderPub,
			Tags: nostr.Tags{nostr.Tag{"p", pt.inboxXOnly}}, Content: ciphertext,
		}, transport.DefaultLimits(), time.Now())
		require.Error(t, err)
	})

	t.Run("tampered ciphertext", func(t *testing.T) {
		event := seal(t, testEnvelope(t))
		body := []byte(event.Content)
		body[len(body)/2] ^= 0x01
		event.Content = string(body)
		_, _, err := pt.unwrap(event, transport.DefaultLimits(), time.Now())
		require.Error(t, err)
	})
}

// TestUnwrap_RejectsStaleBeforeConsumingANonceSlot pins the ordering. The nonce set
// is bounded and fails closed, so a stale envelope must be refused by the freshness
// check rather than being allowed to occupy a slot — otherwise replaying old
// envelopes would be a way to fill the set and get live traffic refused.
func TestUnwrap_RejectsStaleBeforeConsumingANonceSlot(t *testing.T) {
	pt, seal := newUnwrapFixture(t)

	env := testEnvelope(t)
	env.NotAfter = time.Now().Add(-time.Minute).Unix() // already expired
	event := seal(t, env)

	_, _, err := pt.unwrap(event, transport.DefaultLimits(), time.Now())
	require.Error(t, err)
	assert.Zero(t, pt.nonces.Len(), "a stale envelope must not occupy a nonce slot")
}

func TestUnwrap_RejectsOverlongValidityWindow(t *testing.T) {
	pt, seal := newUnwrapFixture(t)

	env := testEnvelope(t)
	env.NotAfter = time.Now().Add(time.Hour).Unix()

	_, _, err := pt.unwrap(seal(t, env), transport.DefaultLimits(), time.Now())
	require.Error(t, err, "a client must not mint itself an hour-long envelope")
	assert.Zero(t, pt.nonces.Len())
}

func TestNonceSet_SeenOrRecord(t *testing.T) {
	s := newPrivateNonceSet()
	notAfter := time.Now().Add(time.Minute).Unix()

	assert.False(t, s.seenOrRecord("aa"+strings.Repeat("1", 62), notAfter))
	assert.True(t, s.seenOrRecord("aa"+strings.Repeat("1", 62), notAfter))
	assert.Equal(t, int64(1), s.Len())

	// A different nonce is independent.
	assert.False(t, s.seenOrRecord("bb"+strings.Repeat("2", 62), notAfter))
	assert.Equal(t, int64(2), s.Len())
}

// TestNonceSet_IsAtomicUnderConcurrency is why this is one call rather than a
// check followed by an insert: two deliveries of the same envelope can arrive at
// once, and exactly one must win.
func TestNonceSet_IsAtomicUnderConcurrency(t *testing.T) {
	s := newPrivateNonceSet()
	nonce := "cc" + strings.Repeat("3", 62)
	notAfter := time.Now().Add(time.Minute).Unix()

	const racers = 64
	var wg sync.WaitGroup
	accepted := make(chan bool, racers)

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			accepted <- !s.seenOrRecord(nonce, notAfter)
		}()
	}
	wg.Wait()
	close(accepted)

	var wins int
	for ok := range accepted {
		if ok {
			wins++
		}
	}
	assert.Equal(t, 1, wins, "exactly one concurrent delivery may be accepted, got %d", wins)
	assert.Equal(t, int64(1), s.Len())
}

// TestNonceSet_SweepReclaimsExpired keeps the set tracking the live window instead
// of drifting toward its cap, where it would refuse real envelopes.
func TestNonceSet_SweepReclaimsExpired(t *testing.T) {
	s := newPrivateNonceSet()
	// Both entries are burnt for at least transport.ProofFreshnessPast from NOW,
	// whatever their envelope's own not_after said — a nonce has to outlive every
	// proof bound to it, or a lapsed entry becomes an unseen first sighting and the
	// proof can be lifted into someone else's envelope. So the sweep is driven from
	// that window, not from the envelope's.
	past := time.Now().Add(-time.Minute).Unix()
	longFuture := time.Now().Add(transport.ProofFreshnessPast + 10*time.Minute).Unix()

	s.seenOrRecord("aa"+strings.Repeat("1", 62), past)
	s.seenOrRecord("bb"+strings.Repeat("2", 62), longFuture)
	require.Equal(t, int64(2), s.Len())

	require.Equal(t, int64(0), s.sweepExpired(time.Now()),
		"nothing is reclaimable yet: even the entry whose envelope window is an hour past "+
			"is still burnt for the proof's own lifetime")

	// Past the proof window, the short entry becomes reclaimable and the long one does not.
	removed := s.sweepExpired(time.Now().Add(transport.ProofFreshnessPast + time.Second))
	assert.Equal(t, int64(1), removed)
	assert.Equal(t, int64(1), s.Len(), "the longer-lived nonce must survive the sweep")
}

// TestNonceSet_FailsClosedAtCapacity is the decision that matters most here.
// Evicting an entry to admit a new one would silently reopen the replay window for
// whatever was evicted, so a full set declines instead — and says so, since
// refusing legitimate traffic must not be silent.
func TestNonceSet_FailsClosedAtCapacity(t *testing.T) {
	s := newPrivateNonceSet()
	notAfter := time.Now().Add(time.Minute).Unix()

	// Pretend the set is full rather than inserting a million entries.
	s.count.Store(nonceSetCapacity)

	fresh := "dd" + strings.Repeat("4", 62)
	assert.True(t, s.seenOrRecord(fresh, notAfter),
		"at capacity a fresh nonce must be refused, not admitted by eviction")
	assert.Equal(t, int64(1), s.rejectedAtCapacity.Load(),
		"refusing real traffic must be counted so an operator can see it")
}

// TestNonceSet_ShardsSpreadEvenly checks the striping actually distributes. Nonces
// are uniform over the keyspace, so the first two hex characters are a fine bucket
// selector — but only if the arithmetic is right.
func TestNonceSet_ShardsSpreadEvenly(t *testing.T) {
	s := newPrivateNonceSet()

	const inserts = 4096
	for i := 0; i < inserts; i++ {
		nonce, err := transport.NewNonce()
		require.NoError(t, err)
		s.seenOrRecord(nonce, time.Now().Add(time.Minute).Unix())
	}
	require.Equal(t, int64(inserts), s.Len())

	var used int
	for i := range s.shards {
		s.shards[i].mu.Lock()
		if len(s.shards[i].expiry) > 0 {
			used++
		}
		s.shards[i].mu.Unlock()
	}
	// With 4096 uniform keys over 256 buckets, essentially all should be hit.
	assert.Greater(t, used, 200, "only %d of %d shards used; striping is skewed", used, nonceSetShards)
}

func TestNonceSet_ShortNonceDoesNotPanic(t *testing.T) {
	s := newPrivateNonceSet()
	// Decode rejects a malformed nonce long before here, but the shard selector
	// must not index out of range if one ever reaches it.
	assert.False(t, s.seenOrRecord("", 0))
	assert.False(t, s.seenOrRecord("a", 0))
}
