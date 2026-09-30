package service

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AUDIT-B SEC-A / F1 — the cross-envelope lift, composed.
//
// Two halves, in two repos, each individually defensible:
//
//   nmilat  proof.go:217-223  proofRequiredTags omits "expiration", so a proof's
//                             declared not_after is never compared to the
//                             envelope's. The proof's ONLY clock is created_at,
//                             good for ProofFreshnessPast = 5 MINUTES.
//   lokihub private_nonce_set.go:107-116  a nonce whose recorded not_after has
//                             lapsed is REFRESHED and re-admitted, not refused.
//                             The comment says "the freshness check rejects a
//                             stale envelope before it reaches here" — true of
//                             the same envelope, false of a NEW envelope that
//                             merely reuses the nonce with a fresh window.
//
// Composed: the nonce, which is the only thing pinning a proof to one envelope,
// stops being a burnt value after at most MaxNotAfterWindow = 120s, while the
// proof stays valid for 300s. So for ~3 minutes an aggregator holding someone
// else's signed item can re-submit it inside an envelope of its OWN, with its own
// reply_to — and the results come back to the aggregator, not to the signer.
func TestAuditBSecA_LapsedNonceReadmitsALiftedBillProof(t *testing.T) {
	pt, seal := newUnwrapFixture(t)

	// The honest signer's bill: a connection secret, and the pubkey the hub would
	// compare against (db.App.AppPubkey; private_dispatch.go:99/334-353).
	connSecret := nostr.GeneratePrivateKey()
	connPub, err := nostr.GetPublicKey(connSecret)
	require.NoError(t, err)

	target := strings.Repeat("cc", 32)
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	paramsHash, err := transport.CanonicalParamsHash(json.RawMessage(`{}`))
	require.NoError(t, err)

	// Envelope 1, as the signer built it: nonce N, a window that has now lapsed.
	t1 := time.Now().Add(-5 * time.Second)
	billProof, err := transport.BuildBillProof(connSecret, transport.ProofBinding{
		Target:     target,
		HubXOnly:   pt.nodeXOnly,
		Method:     "cash_status",
		ParamsHash: paramsHash,
		Nonce:      nonce,
		NotAfter:   t1.Unix(),
	})
	require.NoError(t, err)

	item := transport.Item{
		ID: "1", Target: target, Method: "cash_status",
		Params:    json.RawMessage(`{}`),
		Proof:     json.RawMessage(`{"kind":23192}`),
		BillProof: billProof,
	}
	replyTo1, err := transport.NewNonce()
	require.NoError(t, err)
	env1 := transport.Envelope{
		Version: transport.EnvelopeVersion, NotAfter: t1.Unix(),
		Nonce: nonce, ReplyTo: replyTo1, Items: []transport.Item{item},
	}
	_, _, err = pt.unwrap(seal(t, env1), transport.DefaultLimits(), t1.Add(-time.Second))
	require.NoError(t, err, "envelope 1 must be accepted: it is the honest one")
	require.EqualValues(t, 1, pt.nonces.Len())

	// CONTROL A — the replay set is genuinely working. An independent envelope with
	// a LIVE window is refused on its second delivery. Without this, the result below
	// could be a broken fixture rather than a finding.
	ctrlNonce, err := transport.NewNonce()
	require.NoError(t, err)
	ctrl := env1
	ctrl.Nonce = ctrlNonce
	ctrl.NotAfter = time.Now().Add(60 * time.Second).Unix()
	_, _, err = pt.unwrap(seal(t, ctrl), transport.DefaultLimits(), time.Now())
	require.NoError(t, err, "CONTROL A: first delivery")
	_, _, err = pt.unwrap(seal(t, ctrl), transport.DefaultLimits(), time.Now())
	require.Error(t, err, "CONTROL A: the nonce set must refuse a replay inside the window")
	assert.Contains(t, err.Error(), "already seen")
	t.Log("AUDITB-SECA-F1 CONTROL A: a nonce whose window is still LIVE is refused on re-delivery, " +
		"so the replay set is functioning; what follows is specifically the lapsed-window branch")

	// The attacker: an aggregator that was handed `item` for envelope 1 and kept it.
	// Envelope 2 is ITS envelope — its own reply_to, its own 120s window — reusing
	// only the nonce, which is all the proof binds.
	now := time.Now()
	replyTo2, err := transport.NewNonce()
	require.NoError(t, err)
	env2 := transport.Envelope{
		Version:  transport.EnvelopeVersion,
		NotAfter: now.Add(transport.MaxNotAfterWindow).Unix(),
		Nonce:    nonce, // the lapsed one
		ReplyTo:  replyTo2,
		Items:    []transport.Item{item, withID(item, "2"), withID(item, "3")},
	}
	_, _, err = pt.unwrap(seal(t, env2), transport.DefaultLimits(), now)
	require.Error(t, err,
		"the hub re-admitted a nonce it had already accepted, because that nonce's own envelope "+
			"window had lapsed — but the PROOF bound to it is still verifiable for "+
			"ProofFreshnessPast, so whoever was handed that signed item can resubmit it in an "+
			"envelope of their own, with their own reply_to, and receive the results")
	require.Contains(t, err.Error(), "already seen",
		"and it must be refused AS A REPLAY, not by some incidental check")
	t.Logf("AUDITB-SECA-F1: the nonce stays burnt %s after its first envelope's not_after, so the "+
		"lift into a different reply_to (%s… vs %s…) is refused",
		now.Sub(t1).Round(time.Second), replyTo2[:8], replyTo1[:8])

	// The proof ITSELF is still perfectly valid — which is the point. Nothing about it
	// expired; the binding did its job. Verified against the ORIGINAL envelope's own
	// values, exactly the call private_dispatch.go makes at gate 2.5.
	signer, err := transport.VerifyBillProof(item.BillProof, transport.ProofBinding{
		Target:     item.Target,
		HubXOnly:   pt.nodeXOnly,
		Nonce:      nonce,
		Method:     item.Method,
		ParamsHash: paramsHash,
		NotAfter:   t1.Unix(),
	}, time.Now())
	require.NoError(t, err, "the honest proof must still verify against its own envelope")
	require.Equal(t, connPub, signer)
	t.Logf("AUDITB-SECA-F1: the proof is still valid and still verifies as the bill's own "+
		"connection key (%s…) — only its REUSE elsewhere is refused", signer[:8])

	// CONTROL B — the one that matters. A FRESH nonce is accepted by the replay set
	// (so the attacker's only obstacle really is the nonce) but the proof then
	// REJECTS. That is what makes this a not_after/nonce-lifetime defect and not a
	// claim that the verifier accepts anything.
	fresh, err := transport.NewNonce()
	require.NoError(t, err)
	env3 := env2
	env3.Nonce = fresh
	_, _, err = pt.unwrap(seal(t, env3), transport.DefaultLimits(), now)
	require.NoError(t, err, "CONTROL B: a fresh nonce is of course admitted")
	_, err = transport.VerifyBillProof(item.BillProof, transport.ProofBinding{
		Target: item.Target, HubXOnly: pt.nodeXOnly, Nonce: fresh,
		Method: item.Method, ParamsHash: paramsHash, NotAfter: env3.NotAfter,
	}, time.Now())
	require.ErrorIs(t, err, transport.ErrProofWrongNonce,
		"CONTROL B: with a fresh nonce the lift fails — reusing the lapsed nonce is the whole exploit")
	t.Log("AUDITB-SECA-F1 CONTROL B: the same proof in an envelope with a FRESH nonce is refused " +
		"(ErrProofWrongNonce). The nonce is the only envelope binding, and the hub stops enforcing it.")
}

// AUDIT-B SEC-A / F1c — the nonce set is not even needed: the sweeper DELETES a
// lapsed nonce, after which the second envelope is not a "refresh" but a first
// sighting. Same outcome by a shorter route, and it survives any fix that only
// tightens the refresh branch.
func TestAuditBSecA_SweeperErasesTheBindingEntirely(t *testing.T) {
	s := newPrivateNonceSet()
	nonce := "dd" + strings.Repeat("4", 62)

	require.False(t, s.seenOrRecord(nonce, time.Now().Add(-2*time.Second).Unix()),
		"first sighting is admitted, as it must be")
	require.True(t, s.seenOrRecord(nonce, time.Now().Add(-2*time.Second).Unix()),
		"a nonce whose own window has lapsed must STILL be refused on re-delivery: the envelope's "+
			"window expiring says nothing about the proof bound to that nonce, which outlives it")

	s2 := newPrivateNonceSet()
	require.False(t, s2.seenOrRecord(nonce, time.Now().Add(-2*time.Second).Unix()))
	require.EqualValues(t, 0, s2.sweepExpired(time.Now()),
		"the sweeper must NOT reclaim a nonce whose proof can still verify — reclaiming it turns "+
			"the next envelope from a refused replay into an unseen first sighting, which is the "+
			"same lift by a shorter route and survives any fix that only tightens the refresh branch")
	require.EqualValues(t, 1, s2.Len())
	require.True(t, s2.seenOrRecord(nonce, time.Now().Add(120*time.Second).Unix()),
		"and the nonce is still known, so the replay is still refused")
	t.Log("AUDITB-SECA-F1c: a nonce is burnt for ProofFreshnessPast (5m), not for its own " +
		"not_after (<=120s), so it outlives every proof bound to it")
}

func withID(i transport.Item, id string) transport.Item {
	i.ID = id
	return i
}

var _ = hex.EncodeToString
