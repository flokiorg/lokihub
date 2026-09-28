package service

import (
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// TestWireContract_SDKWrapIsAcceptedByThisHub is the cross-repo contract test, and
// it exists because of a real divergence that self-consistent tests on each side
// could never have caught.
//
// NIP-CASH described a private request as a NIP-59 gift wrap. It is not: this hub
// subscribes to kind 23190 and NIP-44-decrypts the event content directly. A client
// following the spec would have been discarded at the kind filter, before any
// decryption, and received silence with nothing to diagnose.
//
// Both sides were individually tested and both passed. What was missing is this:
// the SDK's own request builder (transport.WrapRequest), judged by THIS hub's real
// acceptance and unwrap code — not by a client the hub's own fixture hand-rolls.
// newUnwrapFixture's `seal` helper replicates the construction rather than calling
// the SDK, which is exactly how the two could drift apart unnoticed.
//
// It also crosses an implementation boundary that is easy to forget is there: the
// SDK computes its ECDH with nmilat's own nip44 over btcec keys, while this hub
// uses go-nostr's nip44 over hex strings. A conversation key that disagreed
// between those two would break every request while every unit test still passed.
func TestWireContract_SDKWrapIsAcceptedByThisHub(t *testing.T) {
	pt, _ := newUnwrapFixture(t)

	env := testEnvelope(t)
	plaintext, err := env.Encode(transport.DefaultLimits())
	require.NoError(t, err)

	// Built by the SDK exactly as a real client would, with no help from this repo.
	wrapped, _, err := transport.WrapRequest(plaintext, pt.inboxXOnly)
	require.NoError(t, err, "the SDK must be able to address this hub's inbox")

	event, err := toGoNostrEvent(wrapped)
	require.NoError(t, err)

	// Gate 1: the hub's cheap pre-checks — kind, p-tag, minimum ciphertext length.
	require.True(t, pt.acceptsPrivateEvent(event),
		"this hub rejected an SDK-built request before decrypting it; the two sides disagree on the wire format")

	// Gate 2: the expensive path — ECDH, decrypt, decode, freshness, replay.
	got, conversationKey, err := pt.unwrap(event, transport.DefaultLimits(), time.Now())
	require.NoError(t, err, "this hub could not unwrap an SDK-built request")
	require.NotNil(t, got)
	assert.Equal(t, env.Nonce, got.Nonce, "unwrapped a different envelope than was sent")
	require.Len(t, got.Items, len(env.Items))
	assert.NotEqual(t, [32]byte{}, conversationKey, "a zero conversation key would break every reply")

	// The reply key both sides must independently agree on. The SDK derives it from
	// the event it published; the hub from the event it received. If these differed
	// the request would succeed and the response would be unreadable — a failure
	// that would only ever show up end to end.
	hubReplyKey, err := transport.DeriveReplyKey(conversationKey, got.ReplyTo)
	require.NoError(t, err)

	clientConversationKey, err := transport.ConversationKeyFor(event.PubKey, pt.inboxPrivKey)
	require.NoError(t, err)
	assert.Equal(t, conversationKey, clientConversationKey,
		"nmilat's ECDH and go-nostr's ECDH disagree for the same key pair")

	clientReplyKey, err := transport.DeriveReplyKey(clientConversationKey, env.ReplyTo)
	require.NoError(t, err)
	assert.Equal(t, hubReplyKey, clientReplyKey, "the two sides derived different reply keys")
}

// TestWireContract_ReplayIsRefusedAcrossTheBoundary: the SDK generates a fresh
// nonce per envelope, and the hub dedupes on it. Sending the SAME wrapped event
// twice must be refused the second time — proving the hub's replay set sees the
// nonce the SDK actually put in, not one the fixture invented.
func TestWireContract_ReplayIsRefusedAcrossTheBoundary(t *testing.T) {
	pt, _ := newUnwrapFixture(t)

	plaintext, err := testEnvelope(t).Encode(transport.DefaultLimits())
	require.NoError(t, err)
	wrapped, _, err := transport.WrapRequest(plaintext, pt.inboxXOnly)
	require.NoError(t, err)
	event, err := toGoNostrEvent(wrapped)
	require.NoError(t, err)

	_, _, err = pt.unwrap(event, transport.DefaultLimits(), time.Now())
	require.NoError(t, err, "first delivery must succeed")

	_, _, err = pt.unwrap(event, transport.DefaultLimits(), time.Now())
	require.Error(t, err, "a replayed envelope must be refused")
	assert.Contains(t, err.Error(), "already seen")
}

// TestWireContract_HubRejectsAWrapAddressedElsewhere: an SDK request addressed to a
// DIFFERENT inbox must not be accepted here, even though it is otherwise perfectly
// well formed. This is the p-tag gate doing its job, and it is what stops one hub
// processing traffic meant for another.
func TestWireContract_HubRejectsAWrapAddressedElsewhere(t *testing.T) {
	pt, _ := newUnwrapFixture(t)

	otherInbox := nostr.GeneratePrivateKey()
	otherInboxPub, err := nostr.GetPublicKey(otherInbox)
	require.NoError(t, err)
	require.NotEqual(t, pt.inboxXOnly, otherInboxPub)

	plaintext, err := testEnvelope(t).Encode(transport.DefaultLimits())
	require.NoError(t, err)
	wrapped, _, err := transport.WrapRequest(plaintext, otherInboxPub)
	require.NoError(t, err)
	event, err := toGoNostrEvent(wrapped)
	require.NoError(t, err)

	assert.False(t, pt.acceptsPrivateEvent(event),
		"a request addressed to another hub's inbox must be dropped")
}

// TestWireContract_MultiBillEnvelopeSurvivesTheWire is F2's cross-boundary half:
// several DIFFERENT bills, each proof signed by a different key, in ONE event.
//
// This is the entire purpose of the transport, and before this test nothing
// anywhere exercised it — every transport test used a single target, and the SDK's
// own helper generated a fresh random hub per item, so no test even built a
// coherent multi-item envelope.
func TestWireContract_MultiBillEnvelopeSurvivesTheWire(t *testing.T) {
	pt, _ := newUnwrapFixture(t)

	const bills = 5
	env := testEnvelope(t)
	env.Items = env.Items[:0]

	targets := make([]string, 0, bills)
	for i := 0; i < bills; i++ {
		// Each bill is a different wallet, authorised by a different key — the
		// shape a holder consolidating or redeeming several bills actually sends.
		billPriv := nostr.GeneratePrivateKey()
		billTarget, err := nostr.GetPublicKey(billPriv)
		require.NoError(t, err)
		targets = append(targets, billTarget)

		params := `{}`
		hash, err := transport.CanonicalParamsHash([]byte(params))
		require.NoError(t, err)
		proof, err := transport.BuildItemProof(billPriv, transport.ProofBinding{
			Target:     billTarget,
			HubXOnly:   pt.nodeXOnly, // one hub for every item, as required
			Method:     "cash_status",
			ParamsHash: hash,
			Nonce:      env.Nonce,
			NotAfter:   env.NotAfter,
		})
		require.NoError(t, err)

		env.Items = append(env.Items, transport.Item{
			ID:     strings.Repeat("i", i+1),
			Target: billTarget,
			Method: "cash_status",
			Params: []byte(params),
			Proof:  proof,
		})
	}

	plaintext, err := env.Encode(transport.DefaultLimits())
	require.NoError(t, err, "a %d-bill envelope must fit the default limits", bills)

	wrapped, _, err := transport.WrapRequest(plaintext, pt.inboxXOnly)
	require.NoError(t, err)
	event, err := toGoNostrEvent(wrapped)
	require.NoError(t, err)

	require.True(t, pt.acceptsPrivateEvent(event))
	got, _, err := pt.unwrap(event, transport.DefaultLimits(), time.Now())
	require.NoError(t, err)

	require.Len(t, got.Items, bills, "every bill must survive the round trip")
	for i, item := range got.Items {
		assert.Equal(t, targets[i], item.Target, "item %d addressed the wrong bill", i)
		require.NotEmpty(t, item.Proof, "item %d lost its proof", i)
	}

	// And the whole batch is ONE relay event. That is the property the transport
	// exists for: the number of bills a holder has stops being public.
	assert.Equal(t, transport.KindPrivateRequest, event.Kind)
}

// TestWireContract_ServableMethodSetsAgree pins the two halves of one rule to each
// other.
//
// The SDK validates an envelope locally against transport.IsServableMethod so a
// client learns about an unservable item before spending a round trip. This hub
// decides what it actually serves from privateServableMethods. Those are two
// copies of one decision, in two repos, and if they drift the failure is silent in
// the worst direction: a client is told its item is fine, sends it, and gets
// omission — which is information-free, so it cannot tell that from "this bill
// isn't here".
//
// Direction matters, so both are asserted:
//   - anything the SDK permits, this hub MUST serve, or clients are misled;
//   - anything this hub serves, the SDK MUST permit, or clients are blocked from a
//     working method.
func TestWireContract_ServableMethodSetsAgree(t *testing.T) {
	// Both sides are now checked through their exported predicates: the allowlist itself
	// lives in nip47, beside the controllers it gates, so this test asks each side the
	// same question rather than reading either one's internals.
	for _, method := range []string{
		constants.NIP47MethodCashStatus,
		constants.NIP47MethodListRecipients,
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer,
		constants.NIP47MethodCashConsolidate,
		constants.NIP47MethodCreateCircleWallet,
		constants.NIP47MethodMintCash,
		models.GET_BALANCE_METHOD,
		models.PAY_INVOICE_METHOD,
		models.CREATE_CONNECTION_METHOD,
		models.GET_INFO_METHOD,
	} {
		hubServes := nip47.IsPrivateServableMethod(method)
		assert.Equal(t, hubServes, transport.IsServableMethod(method),
			"the SDK and this hub disagree about whether %q is servable over the private transport", method)
	}

	// mint_cash in particular: excluded on both sides, for the same reason — it is
	// the hub owner's method and the only one with no retry idempotency, which is
	// what makes a bounded in-memory replay set safe.
	assert.False(t, nip47.IsPrivateServableMethod(constants.NIP47MethodMintCash))
	assert.False(t, transport.IsServableMethod(constants.NIP47MethodMintCash))
}
