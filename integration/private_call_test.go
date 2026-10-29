//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/integration/nwcclient"
	"github.com/ohstr/nmilat/nipcash"
	nipcashclient "github.com/ohstr/nmilat/nipcash/client"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/ohstr/nmilat/nipcw"
)

// privateCall places ONE bill-method request over the private transport and returns the
// same error shape nwcclient.Client.Call does, so existing assertions
// (requireNWCErrorCode and friends) keep working unchanged.
//
// It exists because the four bill methods are no longer served on kind 23194 (NIP-CASH
// §It is the ONLY transport for the bill methods), which is what this suite used to call
// them over. The obvious alternative — routing these tests through nmilat's typed client
// — will not do: most of them are ADVERSARIAL and must be able to send params the SDK
// would refuse to build (a short commitment, a non-hex secret, a proof bound to the wrong
// identity). Keeping the envelope hand-built is what preserves that reach.
//
// params is sent verbatim. signerPriv signs the item's kind-23192 SLICE proof; the bill's
// own connection secret, taken from its token, signs the kind-23193 BILL proof. Those are
// different claims — which slice is mine, versus do I hold this bill at all — so a test
// can vary either independently.
func privateCall(t *testing.T, billToken, method string, params any, signerPriv string, result any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return privateCallCtx(t, ctx, billToken, method, params, signerPriv, result)
}

// forgedBillKeys holds marked tokens whose bill proofs a test signs with a key of
// its own: a stranger who knows the wallet pubkey but not the connection secret.
var forgedBillKeys sync.Map

// strangerMark separates a token from the marker naming its forged bill key.
const strangerMark = "#stranger-"

// privateCallCtx is privateCall with the caller's own deadline. Separate because a
// SILENCE assertion needs to bound its own wait — a probe that hangs for the default
// 30s turns "the hub went quiet" into a test that takes half a minute per poll.
func privateCallCtx(t *testing.T, ctx context.Context, billToken, method string, params any, signerPriv string, result any) error {
	t.Helper()

	forgedKey, forged := forgedBillKeys.Load(billToken)
	if i := strings.Index(billToken, strangerMark); i >= 0 {
		billToken = billToken[:i]
	}
	tok, err := nipcash.Decode(billToken)
	require.NoError(t, err, "decode bill token")

	// The hub identity comes from the bill's own mint signature — the only thing in a
	// token that identifies its minting hub, and therefore the only thing an
	// announcement can be verified against.
	minter, ok := nipcash.VerifyProvenance(tok)
	require.True(t, ok, "bill carries no verifiable mint provenance, so its hub cannot be found")
	hubXOnly, err := transport.NormalizeNodeIdentity(minter)
	require.NoError(t, err, "normalize hub identity")

	billKey := tok.Secret
	if forged {
		billKey = forgedKey.(string)
	}
	return privateTargetCall(t, ctx, privateTarget{walletPubkey: tok.WalletPubkey, billKey: billKey, hubXOnly: hubXOnly, relays: tok.RelayURLs},
		method, params, signerPriv, result)
}

// privateTarget is what a private item needs about its target: the wallet pubkey it
// names, the key that signs its bill proof, and the Hub identity and relays that find
// the Hub's announcement.
type privateTarget struct {
	walletPubkey, billKey, hubXOnly string
	relays                          []string
}

// privateTargetCall places one item at target over the private transport.
func privateTargetCall(t *testing.T, ctx context.Context, target privateTarget, method string, params any, signerPriv string, result any) error {
	t.Helper()
	hubXOnly := target.hubXOnly
	session, err := (&nipcashclient.Client{}).NewBatchSession(ctx, hubXOnly, target.relays)
	require.NoError(t, err, "fetch the hub's kind-11190 announcement")

	limits := session.Limits()
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(2 * time.Minute).Unix()

	rawParams, err := json.Marshal(params)
	require.NoError(t, err, "marshal params")
	// cash_transfer and cash_consolidate are calls: they carry a request_id
	// (NIP-CASH §request_id). A test that sets its own, or none on purpose with
	// an explicit null, keeps it.
	if method == nipcash.MethodCashTransfer || method == nipcash.MethodCashConsolidate {
		rawParams, err = nwcclient.WithRequestID(rawParams)
		require.NoError(t, err)
	}
	if method == nipcash.MethodCashConsolidate {
		rawParams = rebindConsolidateProofs(t, rawParams)
	}
	// An item carrying a claim proof is signed by that proof's signer (NIP-CASH
	// §Claim Proofs); a bearer item stays bearer.
	if signerPriv != "" {
		if claimSigner := claimSignerOf(rawParams); claimSigner != "" {
			signerPriv = claimSigner
		}
	}
	paramsHash, err := transport.CanonicalParamsHash(rawParams)
	require.NoError(t, err)

	binding := transport.ProofBinding{
		Target: target.walletPubkey, HubXOnly: hubXOnly, Method: method,
		ParamsHash: paramsHash, Nonce: nonce, NotAfter: notAfter,
	}
	item := transport.Item{
		ID: "1", Target: target.walletPubkey, Method: method, Params: rawParams,
	}
	// Always a bill proof: required on every item whatever the identity mode.
	billProof, err := transport.BuildBillProof(target.billKey, binding)
	require.NoError(t, err)
	item.BillProof = billProof
	// A slice proof only when a signer was given. Omitting it is how a test reaches the
	// bearer path, where the cash secret inside params is the slice authorization.
	if signerPriv != "" {
		sliceProof, err := transport.BuildItemProof(signerPriv, binding)
		require.NoError(t, err)
		item.Proof = sliceProof
	}

	env := transport.Envelope{
		NotAfter: notAfter,
		Nonce:    nonce, ReplyTo: replyTo, Items: []transport.Item{item},
	}
	plaintext, err := env.Encode(limits)
	if err != nil {
		// The SDK refuses to build what the Hub must refuse (a proof and a cash
		// secret together, say); an adversarial test sends it anyway, unpadded.
		plaintext, err = json.Marshal(env)
	}
	require.NoError(t, err, "encode envelope")

	request, convKey, err := transport.WrapRequest(plaintext, session.Inbox())
	require.NoError(t, err, "wrap request")

	// The response is encrypted under a key DERIVED from the wrap conversation key and
	// reply_to, not under the conversation key itself (NIP-CASH §The Reply Key). Deriving
	// it here rather than reusing convKey is the difference between reading the reply and
	// silently discarding it as "not ours" — which is exactly what this helper did on its
	// first run, and what led to the derivation being written into the spec at all.
	replyKey, err := transport.DeriveReplyKey(convKey, replyTo)
	require.NoError(t, err, "derive the reply key")

	relays := session.Relays()
	require.NotEmpty(t, relays, "the announcement names no relay")
	relay, err := nostr.RelayConnect(ctx, relays[0])
	require.NoError(t, err, "connect to %s", relays[0])
	defer relay.Close()

	// Subscribed BEFORE publishing: a reply to an ephemeral request is not stored, so a
	// subscription opened afterwards can miss it outright.
	sub, err := relay.Subscribe(ctx, []nostr.Filter{{
		Kinds: []int{transport.KindPrivateResponse},
		Tags:  nostr.TagMap{"p": []string{replyTo}},
	}})
	require.NoError(t, err, "subscribe for the reply")
	defer sub.Unsub()

	// nmilat signs a nip01.Event; this suite publishes a go-nostr Event. Same wire
	// shape, two Go types — converted through JSON so the signed id and sig are
	// carried verbatim rather than recomputed.
	reqJSON, err := json.Marshal(request)
	require.NoError(t, err, "marshal the wrapped request")
	var pubEv nostr.Event
	require.NoError(t, json.Unmarshal(reqJSON, &pubEv), "convert the wrapped request")
	require.NoError(t, relay.Publish(ctx, pubEv), "publish the private request")

	for {
		select {
		case ev, open := <-sub.Events:
			if !open {
				return fmt.Errorf("private transport: the relay closed the reply subscription")
			}
			if ev == nil {
				continue
			}
			replyPlain, err := nip44.Decrypt(ev.Content, replyKey)
			if err != nil {
				continue // not ours
			}
			resp, err := transport.DecodeResponse([]byte(replyPlain), nonce, []string{"1"}, limits)
			if err != nil {
				return fmt.Errorf("private transport: decode response: %w", err)
			}
			for _, r := range resp.Results {
				if r.ID != "1" {
					continue
				}
				if r.Error != nil {
					// The same shape the standard transport produced, so every
					// existing code assertion in this suite still applies.
					return &nwcclient.NWCError{Code: r.Error.Code, Message: r.Error.Message, Detail: r.Error.Detail}
				}
				// A value method naming a destroyed bill is answered with its tombstone
				// (NIP-CASH §Serving an Envelope, I4): nothing moved, so never a success.
				if method != nipcash.MethodCashStatus {
					if tomb, ok := nipcash.ParseTombstone(r.Result); ok {
						return &billGoneError{Tombstone: *tomb}
					}
				}
				if result != nil && len(r.Result) > 0 {
					if err := json.Unmarshal(r.Result, result); err != nil {
						return fmt.Errorf("private transport: unmarshal result: %w", err)
					}
				}
				return nil
			}
			// Served but omitted: the hub answered the envelope and said nothing about
			// this item. Deliberately information-free, so it cannot be reported as a
			// code — and it MUST NOT be read as success.
			return errPrivateOmitted
		case <-ctx.Done():
			return fmt.Errorf("private transport: no reply before the deadline: %w", ctx.Err())
		}
	}
}

// errPrivateOmitted is what an omission reaches a test as. A distinct sentinel because
// an omission is not an error code and not a success: it is the hub declining to say
// anything, which a test asserting on a REASON must never accept.
var errPrivateOmitted = errors.New("private transport: the hub omitted this item (information-free by design)")

// mustConnectStranger is a bill connection held by someone who knows the bill's
// wallet pubkey and relays (public on the relay) but not its connection secret:
// its bill proofs are signed with a key of its own.
func mustConnectStranger(t *testing.T, token string) *billConn {
	t.Helper()
	marked := token + strangerMark + nostr.GeneratePrivateKey()[:8]
	forgedBillKeys.Store(marked, nostr.GeneratePrivateKey())
	return &billConn{t: t, token: marked}
}

// billGoneError is a value method answered with the destroyed bill's tombstone.
type billGoneError struct{ Tombstone nipcash.Tombstone }

func (e *billGoneError) Error() string {
	return fmt.Sprintf("private transport: the bill is destroyed (spent, reason %q)", e.Tombstone.Reason)
}

// isBillGone reports a value method answered with a tombstone.
func isBillGone(err error) bool {
	var gone *billGoneError
	return errors.As(err, &gone)
}

// requirePrivateOmitted asserts the hub said nothing about an item, which is the correct
// answer for an unknown bill, a proof that did not verify, or possession it could not
// confirm.
func requirePrivateOmitted(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, errPrivateOmitted, "expected an omission, got %v", err)
}

// billConn is a bill's connection that routes the four bill methods over the private
// transport and everything else over kind 23194, unchanged.
//
// It exists to keep this suite's ~116 existing call sites intact. They were written as
// `shared.Call(ctx, constants.NIP47MethodCashRedeem, params, &out)` against a connection
// built from a pairing URI, and every one of them broke when those methods stopped being
// served on 23194. Routing at the CONNECTION rather than at each call means a migrated
// test changes one line — how it connects — instead of every call it makes.
//
// Embedding *nwcclient.Client is deliberate: Call is shadowed, and everything else
// (WalletPubkey, Close, the non-bill methods) keeps working as before.
type billConn struct {
	*nwcclient.Client
	t          *testing.T
	token      string
	signerPriv string
}

// billMethods is the set routed privately — exactly NIP-CASH §Which Methods a Hub Serves.
var billMethods = map[string]bool{
	"cash_status":      true,
	"cash_redeem":      true,
	"cash_transfer":    true,
	"cash_consolidate": true,
}

func (b *billConn) Call(ctx context.Context, method string, params, result any) error {
	if billMethods[method] {
		return privateCall(b.t, b.token, method, params, b.signerPriv, result)
	}
	return b.Client.Call(ctx, method, params, result)
}

// ActAs sets which identity signs the kind-23192 slice proof on later bill-method calls.
//
// Needed because one bill is routinely exercised by several recipients in the same test,
// and the private transport authorizes per item: the identity that used to be implicit in
// a shared connection now has to be named. Returns b so a call can be chained inline.
func (b *billConn) ActAs(signerPriv string) *billConn {
	b.signerPriv = signerPriv
	return b
}

// Bearer makes later calls carry NO slice proof, which is how a cash-mode bill is
// authorized: the secret inside params is that authorization (§Bearer Items).
func (b *billConn) Bearer() *billConn {
	b.signerPriv = ""
	return b
}

// mustConnectBill dials a bill by its TOKEN — required rather than preferred, since the
// private transport recovers the hub identity from the token's own mint signature, which a
// pairing URI does not carry.
func mustConnectBill(t *testing.T, token, signerPriv string) *billConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := nwcclient.ConnectBill(ctx, token)
	require.NoError(t, err, "connect to the bill")
	client.Logger = t.Logf
	t.Cleanup(client.Close)
	return &billConn{
		Client:     client,
		t:          t,
		token:      token,
		signerPriv: signerPriv,
	}
}

// billCaller is what a bill-method test actually needs: something that can place a call.
//
// Satisfied by both *nwcclient.Client (kind 23194) and *billConn (private transport), so a
// helper that takes one works either way. Introduced because migrating this suite onto the
// private transport otherwise meant editing every helper signature that happens to pass a
// bill connection around — and those helpers do not care which transport carries the call.
type billCaller interface {
	Call(ctx context.Context, method string, params, result any) error
}

// privateJoinCall sends a join to the Circle Wallet Hub of circlehub1 over the
// private transport (NIP-CW §Private Join), the only transport that serves it. The
// suite's proofs are bound to the request's terms here, and the item proof is
// signed by the proof's signer, the joining pubkey.
func privateJoinCall(t *testing.T, ctx context.Context, circlehub1 string, params, result any) error {
	t.Helper()
	conn, err := nipcw.DecodeCircleHubConnection(circlehub1)
	require.NoError(t, err)
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	raw, signer := bindJoinProof(t, raw)
	if signer == "" {
		signer = nostr.GeneratePrivateKey()
	}
	return privateTargetCall(t, ctx, privateTarget{walletPubkey: conn.Pubkey, billKey: conn.Secret, hubXOnly: conn.HubIdentity, relays: conn.Relays},
		nipcw.MethodCreateCircleWallet, json.RawMessage(raw), signer, result)
}
