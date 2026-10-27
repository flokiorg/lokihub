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

	"github.com/flokiorg/lokihub/tests"
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
		nonces:       newMemServedNonces(),
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
	assert.Contains(t, err.Error(), "already served")
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

// U-9.2: served nonces are persisted, so a replay inside not_after is dropped
// even by a transport built after a restart.
func TestServedNonces_PersistedAcrossRestart(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pt, seal := newUnwrapFixture(t)
	pt.nonces = dbServedNonces{db: svc.DB}
	event := seal(t, testEnvelope(t))
	_, _, err = pt.unwrap(event, transport.DefaultLimits(), time.Now())
	require.NoError(t, err)

	restarted := &privateTransport{inboxPrivKey: pt.inboxPrivKey, inboxXOnly: pt.inboxXOnly,
		nodeXOnly: pt.nodeXOnly, nonces: dbServedNonces{db: svc.DB}}
	_, _, err = restarted.unwrap(event, transport.DefaultLimits(), time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already served")
}

// U-9.3: an envelope refused at E4 leaves its nonce unserved, so the corrected
// envelope with the same nonce runs.
func TestServedNonces_RefusalLeavesTheNonceUnserved(t *testing.T) {
	pt, seal := newUnwrapFixture(t)
	env := testEnvelope(t)
	env.NotAfter = time.Now().Add(time.Hour).Unix()
	_, _, err := pt.unwrap(seal(t, env), transport.DefaultLimits(), time.Now())
	var refusal *envelopeRefusal
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "BAD_REQUEST", refusal.code)
	require.NotNil(t, refusal.header)
	assert.Equal(t, env.ReplyTo, refusal.header.ReplyTo)
	assert.Zero(t, pt.nonces.Len())

	env.NotAfter = time.Now().Add(time.Minute).Unix()
	_, _, err = pt.unwrap(seal(t, env), transport.DefaultLimits(), time.Now())
	require.NoError(t, err)
}

// Two deliveries of one envelope racing each other: exactly one runs.
func TestServedNonces_OneOfConcurrentDeliveriesRuns(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	store := dbServedNonces{db: svc.DB}
	nonce := "cc" + strings.Repeat("3", 62)
	notAfter := time.Now().Add(time.Minute).Unix()

	const racers = 16
	var wg sync.WaitGroup
	wins := make(chan bool, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wins <- store.MarkServed(nonce, notAfter)
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for ok := range wins {
		if ok {
			n++
		}
	}
	assert.Equal(t, 1, n)
	assert.True(t, store.Seen(nonce))
}

// Expired nonces are pruned; live ones stay.
func TestServedNonces_PruneForgetsOnlyLapsed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	store := dbServedNonces{db: svc.DB}
	require.True(t, store.MarkServed("aa"+strings.Repeat("1", 62), time.Now().Add(-time.Minute).Unix()))
	require.True(t, store.MarkServed("bb"+strings.Repeat("2", 62), time.Now().Add(time.Minute).Unix()))
	n, err := store.prune(time.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assert.False(t, store.Seen("aa"+strings.Repeat("1", 62)))
	assert.True(t, store.Seen("bb"+strings.Repeat("2", 62)))
}
