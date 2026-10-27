package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"
)

// AUDIT-B SEC-A / F1, the cross-envelope lift, after served nonces became a
// persisted set kept until each envelope's not_after (NIP-CASH §Serving an
// Envelope, E3).
//
// Forgetting a nonce once its not_after has passed is safe because an item proof
// binds the envelope's not_after as well as its nonce: an aggregator that kept a
// signed item and re-sends it in an envelope of its own reusing the lapsed nonce
// must give that envelope a fresh not_after (E2 drops a lapsed one), and the
// proof then no longer matches it.
func TestAuditBSecA_LapsedNonceCannotCarryALiftedBillProof(t *testing.T) {
	pt, seal := newUnwrapFixture(t)

	connSecret := nostr.GeneratePrivateKey()
	target := strings.Repeat("cc", 32)
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	paramsHash, err := transport.CanonicalParamsHash(json.RawMessage(`{}`))
	require.NoError(t, err)

	// Envelope 1, as the signer built it: nonce N, a window that has now lapsed.
	t1 := time.Now().Add(-5 * time.Second)
	billProof, err := transport.BuildBillProof(connSecret, transport.ProofBinding{
		Target: target, HubXOnly: pt.nodeXOnly, Method: "cash_status",
		ParamsHash: paramsHash, Nonce: nonce, NotAfter: t1.Unix(),
	})
	require.NoError(t, err)
	item := transport.Item{ID: "1", Target: target, Method: "cash_status",
		Params: json.RawMessage(`{}`), Proof: json.RawMessage(`{"kind":23192}`), BillProof: billProof}
	replyTo1, err := transport.NewNonce()
	require.NoError(t, err)
	env1 := transport.Envelope{Version: transport.EnvelopeVersion, NotAfter: t1.Unix(),
		Nonce: nonce, ReplyTo: replyTo1, Items: []transport.Item{item}}
	_, _, err = pt.unwrap(seal(t, env1), transport.DefaultLimits(), t1.Add(-time.Second))
	require.NoError(t, err, "envelope 1 is the honest one")

	// Replayed inside its window: dropped at E3.
	_, _, err = pt.unwrap(seal(t, env1), transport.DefaultLimits(), t1.Add(-time.Second))
	require.Error(t, err)

	// Replayed after it: dropped at E2.
	_, _, err = pt.unwrap(seal(t, env1), transport.DefaultLimits(), time.Now())
	require.Error(t, err)

	// The attacker's envelope reuses the nonce with a fresh window. Even with the
	// nonce forgotten, the lifted proof does not bind this envelope.
	pt.nonces = newMemServedNonces()
	replyTo2, err := transport.NewNonce()
	require.NoError(t, err)
	env2 := transport.Envelope{Version: transport.EnvelopeVersion, NotAfter: time.Now().Add(testHorizon).Unix(),
		Nonce: nonce, ReplyTo: replyTo2, Items: []transport.Item{item}}
	got, _, err := pt.unwrap(seal(t, env2), transport.DefaultLimits(), time.Now())
	require.NoError(t, err)
	_, err = transport.VerifyBillProof(got.Items[0].BillProof, transport.ProofBinding{
		Target: target, HubXOnly: pt.nodeXOnly, Nonce: got.Nonce, Method: "cash_status",
		ParamsHash: paramsHash, NotAfter: got.NotAfter,
	}, time.Now())
	require.ErrorIs(t, err, transport.ErrProofWrongNonce, "the lifted proof binds the first envelope's not_after")
}
