package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Session D, Security Auditor A — surface 1, the ~8 KB window.
//
// transport.MaxNIP44Plaintext is 65535; a hub's announced max_bytes defaults to
// 57344. Between them sits a window in which an oversize envelope is still
// ENCRYPTABLE, so NIP-44 will not refuse it on the hub's behalf. The round's brief
// records this as untested in either direction and unasserted by anyone.
//
// These tests establish what actually happens, and deliberately assert BOTH halves:
// that the window really is encryptable (so the hub's own check is the only thing
// standing there), and that the hub does in fact enforce its own announced cap.

// auditDSecAWindowTransport is a privateTransport holding a real inbox key, plus a
// sealer that encrypts ARBITRARY plaintext to it — unlike the existing
// newUnwrapFixture, whose sealer goes through Envelope.Encode and therefore cannot
// produce anything the limits already refuse. The attacker here does not use the SDK.
func auditDSecAWindowTransport(t *testing.T) (*privateTransport, func(t *testing.T, plaintext []byte) *nostr.Event) {
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

	sealRaw := func(t *testing.T, plaintext []byte) *nostr.Event {
		t.Helper()
		ephemeralPriv := nostr.GeneratePrivateKey()
		ephemeralPub, err := nostr.GetPublicKey(ephemeralPriv)
		require.NoError(t, err)
		ck, err := nip44.GenerateConversationKey(inboxPub, ephemeralPriv)
		require.NoError(t, err)
		ciphertext, err := nip44.Encrypt(string(plaintext), ck)
		require.NoError(t, err, "the window is only interesting if NIP-44 accepts the plaintext")
		return &nostr.Event{
			Kind:    transport.KindPrivateRequest,
			PubKey:  ephemeralPub,
			Tags:    nostr.Tags{nostr.Tag{"p", inboxPub}},
			Content: ciphertext,
		}
	}
	return pt, sealRaw
}

// auditDSecAEnvelopeOfExactly builds a structurally VALID envelope plaintext whose
// marshalled length is exactly n bytes, by sizing the pad field by hand.
//
// Structural validity matters: if the oversize envelope were malformed, a rejection
// would prove nothing about the size check — it could be the JSON decode failing. This
// one would be served if it were not too large, which is what makes the assertion
// meaningful.
func auditDSecAEnvelopeOfExactly(t *testing.T, n int) []byte {
	t.Helper()
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)

	env := transport.Envelope{
		NotAfter: time.Now().Add(60 * time.Second).Unix(),
		Nonce:    nonce,
		ReplyTo:  replyTo,
		Items: []transport.Item{{
			ID:        "1",
			Target:    strings.Repeat("cc", 32),
			Method:    "cash_status",
			Params:    json.RawMessage(`{}`),
			Proof:     json.RawMessage(`{"kind":23192}`),
			BillProof: json.RawMessage(`{"kind":23193}`),
		}},
	}

	bare, err := json.Marshal(env)
	require.NoError(t, err)
	const padFieldOverhead = len(`,"pad":""`)
	fill := n - len(bare) - padFieldOverhead
	require.GreaterOrEqual(t, fill, 0,
		"target %d is smaller than a minimal envelope (%d bytes)", n, len(bare)+padFieldOverhead)
	env.Pad = strings.Repeat("0", fill)

	out, err := json.Marshal(env)
	require.NoError(t, err)
	require.Len(t, out, n, "the hand-sized plaintext must be exactly the requested length")
	return out
}

// TestAuditD_SecA_TheWindowIsRealAndEncryptable is the first half of the question: is
// there actually a range NIP-44 accepts and the hub's policy does not?
func TestAuditD_SecA_TheWindowIsRealAndEncryptable(t *testing.T) {
	limits := transport.DefaultLimits()
	require.NoError(t, limits.Validate())
	require.Equal(t, 57344, limits.MaxEnvelopeBytes, "the default announced cap")
	require.Equal(t, 65535, transport.MaxNIP44Plaintext, "NIP-44's hard plaintext ceiling")

	ck, err := nip44.GenerateConversationKey(
		auditDSecAMustPub(t, nostr.GeneratePrivateKey()), nostr.GeneratePrivateKey())
	require.NoError(t, err)

	// The window: 57345 .. 65535. Both ends encrypt.
	for _, n := range []int{limits.MaxEnvelopeBytes + 1, 60000, transport.MaxNIP44Plaintext} {
		plaintext := auditDSecAEnvelopeOfExactly(t, n)
		ct, err := nip44.Encrypt(string(plaintext), ck)
		require.NoError(t, err, "%d bytes must still encrypt — that is what makes the window a window", n)
		assert.NotEmpty(t, ct)
		t.Logf("plaintext %d bytes -> ciphertext %d bytes (over the %d announced cap, under the %d NIP-44 ceiling)",
			n, len(ct), limits.MaxEnvelopeBytes, transport.MaxNIP44Plaintext)
	}

	// One byte past the ceiling, NIP-44 itself refuses — the documented behaviour the
	// MaxNIP44Plaintext constant records.
	_, err = nip44.Encrypt(strings.Repeat("x", transport.MaxNIP44Plaintext+1), ck)
	require.Error(t, err, "65536 must be refused by NIP-44 itself")
	t.Logf("NIP-44 refuses %d bytes: %v", transport.MaxNIP44Plaintext+1, err)
}

// TestAuditD_SecA_HubEnforcesItsOwnAnnouncedCap is the half that matters: inside the
// window, the hub must refuse on its own authority.
func TestAuditD_SecA_HubEnforcesItsOwnAnnouncedCap(t *testing.T) {
	pt, sealRaw := auditDSecAWindowTransport(t)
	limits := transport.DefaultLimits()

	// CONTROL: exactly at the cap is accepted, so a later rejection is about SIZE and
	// not about anything else in the envelope.
	atCap := auditDSecAEnvelopeOfExactly(t, limits.MaxEnvelopeBytes)
	env, _, err := pt.unwrap(sealRaw(t, atCap), limits, time.Now())
	require.NoError(t, err, "control: a plaintext of exactly max_bytes must be accepted")
	require.NotNil(t, env)
	require.Len(t, env.Items, 1)

	// The window itself.
	for _, n := range []int{limits.MaxEnvelopeBytes + 1, 60000, transport.MaxNIP44Plaintext} {
		t.Run(fmt.Sprintf("plaintext_%d", n), func(t *testing.T) {
			plaintext := auditDSecAEnvelopeOfExactly(t, n)
			got, _, err := pt.unwrap(sealRaw(t, plaintext), limits, time.Now())
			require.Error(t, err, "%d bytes is over the announced cap and must be refused", n)
			require.Nil(t, got)
			assert.ErrorIs(t, err, transport.ErrEnvelopeTooLarge,
				"the refusal must be the size check, not an incidental parse failure")
			t.Logf("%d bytes rejected: %v", n, err)
		})
	}
}

// TestAuditD_SecA_OversizeConsumesItsNonce checks the side-effect ordering.
//
// An oversize envelope is refused at E5, past E4, and NIP-CASH §Serving an
// Envelope counts a nonce served once its envelope passes E4, refused or not:
// the same nonce sent again, even at a legal size, is a replay and is dropped
// at E3. The nonce sits inside the NIP-44 ciphertext, so no one but the client
// can spend it ahead of time.
func TestAuditD_SecA_OversizeConsumesItsNonce(t *testing.T) {
	pt, sealRaw := auditDSecAWindowTransport(t)
	limits := transport.DefaultLimits()

	oversize := auditDSecAEnvelopeOfExactly(t, 60000)
	var decoded transport.Envelope
	require.NoError(t, json.Unmarshal(oversize, &decoded))
	nonce := decoded.Nonce

	_, _, err := pt.unwrap(sealRaw(t, oversize), limits, time.Now())
	require.ErrorIs(t, err, transport.ErrEnvelopeTooLarge)
	var refusal *envelopeRefusal
	require.ErrorAs(t, err, &refusal, "an E5 refusal is answered, not dropped")

	legal := auditDSecAEnvelopeOfExactly(t, limits.PadBucketBytes)
	var legalEnv transport.Envelope
	require.NoError(t, json.Unmarshal(legal, &legalEnv))
	legalEnv.Nonce = nonce
	reseal, err := json.Marshal(legalEnv)
	require.NoError(t, err)

	got, _, err := pt.unwrap(sealRaw(t, reseal), limits, time.Now())
	require.Error(t, err, "the E5 refusal served the nonce, so its reuse is a replay")
	require.Nil(t, got)
	assert.Contains(t, err.Error(), "already served")

	// The refused envelope itself, replayed, is dropped rather than refused again.
	_, _, err = pt.unwrap(sealRaw(t, oversize), limits, time.Now())
	require.Error(t, err)
	assert.False(t, errors.As(err, &refusal), "a replayed refused envelope is dropped at E3")
}

// TestAuditD_SecA_HonestClientCannotBuildIntoTheWindow is the outbound half of the same
// question: a client using the SDK is told locally rather than discovering silence.
func TestAuditD_SecA_HonestClientCannotBuildIntoTheWindow(t *testing.T) {
	limits := transport.DefaultLimits()

	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)

	// An envelope whose BARE body already exceeds the cap, built out of item params
	// rather than pad (Encode discards any incoming pad).
	big := strings.Repeat("y", limits.MaxEnvelopeBytes)
	env := transport.Envelope{
		NotAfter: time.Now().Add(60 * time.Second).Unix(),
		Nonce:    nonce,
		ReplyTo:  replyTo,
		Items: []transport.Item{{
			ID:        "1",
			Target:    strings.Repeat("cc", 32),
			Method:    "cash_status",
			Params:    json.RawMessage(`{"filler":"` + big + `"}`),
			Proof:     json.RawMessage(`{"kind":23192}`),
			BillProof: json.RawMessage(`{"kind":23193}`),
		}},
	}
	_, err = env.Encode(limits)
	require.Error(t, err, "the SDK must refuse to build an envelope the hub would drop")
	assert.ErrorIs(t, err, transport.ErrEnvelopeTooLarge)
	t.Logf("Encode refuses locally: %v", err)
}

// TestAuditD_SecA_ReplyDirectionNeverExceedsTheCap is the reply half of the window.
//
// The hub encodes its own replies, so the concern is the mirror image: can a reply land
// in the window and be published as an event a conformant client will then refuse to
// decode? EncodeResponse must refuse first.
func TestAuditD_SecA_ReplyDirectionNeverExceedsTheCap(t *testing.T) {
	limits := transport.DefaultLimits()
	nonce, err := transport.NewNonce()
	require.NoError(t, err)

	// One result over the reply cap, which is NIP-44's ceiling (65535 bytes): a reply
	// is never bounded by the request's max_bytes (NIP-CASH §Addressing the Response).
	oversizeResult := transport.Result{
		ID:         "r1",
		ResultType: "cash_status",
		Result:     json.RawMessage(`{"filler":"` + strings.Repeat("z", 65535) + `"}`),
	}
	reply := transport.ResponseEnvelope{
		ReqNonce: nonce,
		Results:  []transport.Result{oversizeResult}, Seq: 1, Total: 1,
	}
	_, err = reply.EncodeResponse(limits)
	require.Error(t, err, "a reply over the cap must not be encodable")

	// And the hub's own chunker surfaces it as an error rather than publishing something
	// oversize or silently dropping the result.
	_, err = chunkResults(nonce, []transport.Result{oversizeResult}, limits)
	require.Error(t, err, "chunkResults must refuse a single unfittable result")
	assert.Contains(t, err.Error(), "does not fit the envelope limit")
	t.Logf("chunkResults refuses: %v", err)
}

func auditDSecAMustPub(t *testing.T, priv string) string {
	t.Helper()
	pub, err := nostr.GetPublicKey(priv)
	require.NoError(t, err)
	return pub
}
