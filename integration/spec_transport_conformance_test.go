//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// NIP-CASH conformance at the WIRE level: what this Hub does and does not serve on
// kind 23194.
//
// Every test names the normative sentence it checks and asserts it against the running
// Hub over a real relay. A requirement counts as met only if the test here would fail
// when it is violated — agreeing with the implementation by reading it is not evidence.
// The standard transport served these four methods for months while the spec called the
// private one OPTIONAL, and neither statement looked wrong on its own.

// §It is the ONLY transport for the bill methods: "A Hub MUST serve `cash_status`,
// `cash_redeem`, `cash_transfer` and `cash_consolidate` over the private transport, and
// MUST NOT serve them over kind 23194. A request for one of them on the standard
// transport MUST be refused with `NOT_IMPLEMENTED`."
//
// This is the MUST NOT half, dialled exactly the way these methods used to be called:
// the bill's own connection, as an ordinary NIP-47 client.
//
// Params are deliberately empty. The refusal must happen on the METHOD, before anything
// inspects what was asked — a Hub that validated params first would still be routing the
// method, and would leak that by complaining about the params instead.
func TestSpec_StandardTransport_RefusesEveryBillMethod(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "spec-standard-transport", nil)
	hubClient := mustConnect(t, hub.Connection)

	ownerPriv := newTestPrivkey(t)
	ownerPub := mustPubkey(t, ownerPriv)
	_, billConn, _ := mintPubkeySource(t, hubClient, ownerPub, 50_000)
	// Deliberately a PLAIN kind-23194 connection, not a billConn: this test asserts the
	// hub REFUSES bill methods there. A connection that routed them privately would pass
	// every case while testing nothing.
	billClient := mustConnect(t, billConn)

	for _, method := range []string{
		constants.NIP47MethodCashStatus,
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer,
		constants.NIP47MethodCashConsolidate,
	} {
		t.Run(method, func(t *testing.T) {
			var out map[string]any
			err := billClient.Call(ctxT(t), method, map[string]any{}, &out)
			require.Error(t, err, "%s was SERVED over kind 23194", method)

			// A silent drop is its own conformance failure: the spec asks for
			// NOT_IMPLEMENTED precisely so a caller can tell "this Hub does not serve
			// it here" from "this Hub is unreachable".
			requireNWCErrorCode(t, err, constants.ERROR_NOT_IMPLEMENTED)
			require.Contains(t, strings.ToLower(err.Error()), "private",
				"%s refusal should point the caller at the private transport", method)
		})
	}
}

// §It is the ONLY transport for the bill methods: "`mint_cash` stays on the standard
// transport and MUST NOT be offered on the private one."
//
// The positive half of the same rule, and worth asserting rather than assuming: a change
// that moved bill methods off 23194 could as easily have taken mint_cash with them. It
// must still work here, on the Hub's own connection.
func TestSpec_StandardTransport_StillServesMintCash(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "spec-mint-stays", nil)
	hubClient := mustConnect(t, hub.Connection)

	ownerPub := mustPubkey(t, newTestPrivkey(t))
	walletPubkey, conn, _ := mintPubkeySource(t, hubClient, ownerPub, 40_000)
	require.NotEmpty(t, walletPubkey, "mint_cash must still be served on kind 23194")
	require.NotEmpty(t, conn, "a mint must return a usable connection")
}

// §Response: "**A Hub MUST NOT return an empty `cash_token`.**"
//
// The spec spells out why this is stated at all: token encoding can fail where URI
// construction cannot — a long relay URL can exceed the TLV length limits — and the
// tempting response is to emit `""` and lean on `pairing_uri` still being valid. A
// caller receiving `""` has no bill to hand on.
func TestSpec_MintCash_NeverReturnsAnEmptyCashToken(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "spec-nonempty-token", nil)
	hubClient := mustConnect(t, hub.Connection)

	ownerPub := mustPubkey(t, newTestPrivkey(t))
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(ownerPub, 30_000),
		Expiry:     happyPathExpirySecs,
	}, &created))

	require.NotEmpty(t, created.CashToken, "mint_cash returned an empty cash_token")
	require.True(t, strings.HasPrefix(created.CashToken, "lokicash1"),
		"cash_token should be a lokicash1... string, got %q", created.CashToken)
}

// §Scope Surface: "A Cash Wallet connection MUST NOT be granted `pay_invoice`,
// `lookup_invoice`, or `list_transactions`."
//
// The danger is concrete, not theoretical: a bill is a bearer instrument, so whoever holds
// the string holds it. pay_invoice would let any recipient spend the HUB's balance;
// list_transactions would hand every co-recipient the others' payout history on a
// connection they all share.
//
// BOTH halves are asserted, and the second is the load-bearing one. The word in the spec is
// "granted", not "advertised", and those are different questions — the sibling test below
// makes the same point in the other direction. An earlier version of this test read only
// get_info, which means a Hub that quietly stopped advertising pay_invoice while still
// SERVING it would have passed here, with every bill holder able to spend the Hub's
// balance. Reading the advertisement cannot detect that; attempting the call can.
//
// Attempting pay_invoice for real needs care, because if the Hub IS broken the call would
// move money. So the invoice is deliberately unpayable garbage, which makes the two
// outcomes distinguishable without ever risking a payment:
//
//   - scope absent (correct): RESTRICTED, decided from the app's permission rows before
//     anything parses the invoice;
//   - scope present (the bug): some payment-layer complaint about the invoice instead —
//     which proves the grant exists, and is exactly the finding, with nothing paid.
//
// So the assertion is on the CODE, not merely on an error being returned. "It errored" is
// satisfied by both outcomes and would hide the bug.
func TestSpec_ScopeSurface_BillGrantsNoPaymentOrHistoryMethods(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "spec-scope-surface", nil)
	hubClient := mustConnect(t, hub.Connection)

	ownerPub := mustPubkey(t, newTestPrivkey(t))
	_, billConnURI, _ := mintPubkeySource(t, hubClient, ownerPub, 50_000)
	billClient := mustConnect(t, billConnURI)

	forbidden := map[string]string{
		"pay_invoice":       "a bearer bill's holder could spend the Hub's balance",
		"lookup_invoice":    "not this connection's to read",
		"list_transactions": "would disclose every co-recipient's payout history",
		// get_balance joined this set: a bill's balance is the TOTAL funded across
		// every recipient, so serving it handed that figure to anyone holding the
		// shared connection with no proof required — the same disclosure get_budget
		// is already carved out of the always-granted list for. A recipient's own
		// entitlement comes from cash_status, scoped and proof-gated.
		"get_balance": "would disclose the bill's total funded amount across every recipient",
	}

	// Half one: the advertisement.
	var info struct {
		Methods []string `json:"methods"`
	}
	require.NoError(t, billClient.Call(ctxT(t), "get_info", map[string]any{}, &info))
	t.Logf("bill connection advertises: %v", info.Methods)
	for _, m := range info.Methods {
		if why, bad := forbidden[m]; bad {
			t.Errorf("bill connection advertises %q — %s", m, why)
		}
	}

	// Half two: the grant. Asked of the Hub, not of its self-description.
	params := map[string]any{
		// Not a bolt11 invoice by any reading, so it cannot be paid even if the scope is
		// there. A RESTRICTED refusal must arrive before anything tries.
		"pay_invoice":       map[string]any{"invoice": "not-an-invoice-and-must-never-be-paid"},
		"lookup_invoice":    map[string]any{"payment_hash": strings.Repeat("0", 64)},
		"list_transactions": map[string]any{},
		"get_balance":       map[string]any{},
	}
	for method, why := range forbidden {
		t.Run(method, func(t *testing.T) {
			var discard map[string]any
			err := billClient.Call(ctxT(t), method, params[method], &discard)
			require.Error(t, err,
				"a bill connection SERVED %s — %s", method, why)

			var nwcErr *nwcclient.NWCError
			require.True(t, errors.As(err, &nwcErr),
				"expected a coded refusal from the Hub, got %T: %v", err, err)
			t.Logf("%s -> %s: %s", method, nwcErr.Code, nwcErr.Message)

			require.Equal(t, constants.ERROR_RESTRICTED, nwcErr.Code,
				"%s was refused with %q, not RESTRICTED — the scope is GRANTED and the method reached its handler (%s). "+
					"Any code other than RESTRICTED means the refusal came from the method rather than from authorization.",
				method, nwcErr.Code, why)
		})
	}
}

// §Scope Surface: a bill's kind-23194 connection MUST NOT advertise the four bill
// methods, because it cannot serve them.
//
// get_info answers "what may I call on THIS connection". These four are served over the
// private transport only, so advertising them promised a caller something this transport
// refuses — every client following get_info was sent down a path that cannot work. This
// conformance pass is what turned that up; nothing failed, clients were simply misled.
//
// Nothing is lost by dropping them: the bill-method set is FIXED by §Which Methods a Hub
// Serves rather than discovered, so a client holding a bill already knows what it may
// call. Discovering the transport is the kind-11190 announcement's job.
//
// Both halves are asserted here, because the tempting way to satisfy the first would be
// to drop the scopes that grant them — which would refuse every real call while making
// this test pass. Advertising and authorizing are different questions.
func TestSpec_ScopeSurface_BillDoesNotAdvertiseWhatItCannotServe(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "spec-no-advertise", nil)
	hubClient := mustConnect(t, hub.Connection)

	ownerPriv := newTestPrivkey(t)
	ownerPub := mustPubkey(t, ownerPriv)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(ownerPub, 50_000),
		Expiry:     happyPathExpirySecs,
	}, &created))

	billClient := mustConnect(t, created.PairingURI)
	var info struct {
		Methods        []string `json:"methods"`
		PrivateMethods []string `json:"private_methods"`
	}
	require.NoError(t, billClient.Call(ctxT(t), "get_info", map[string]any{}, &info))
	t.Logf("bill advertises %v, private_methods %v", info.Methods, info.PrivateMethods)

	// Removing the four from `methods` left no wire-level signal that they exist — the
	// kind-11190 announcement carries only inbox, limits and relays. `private_methods`
	// closes that: `methods` stays strictly "callable here", and the bill methods are
	// reported next to it as served elsewhere.
	for _, m := range []string{
		constants.NIP47MethodCashStatus,
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer,
		constants.NIP47MethodCashConsolidate,
	} {
		require.Contains(t, info.PrivateMethods, m,
			"a bill must still say these exist, or a client has no way to discover them")
	}
	// And the two lists must not overlap: a method is callable here or it is not.
	for _, m := range info.PrivateMethods {
		require.NotContains(t, info.Methods, m,
			"%q appears in both methods and private_methods", m)
	}

	private := map[string]bool{
		constants.NIP47MethodCashStatus:      true,
		constants.NIP47MethodCashRedeem:      true,
		constants.NIP47MethodCashTransfer:    true,
		constants.NIP47MethodCashConsolidate: true,
	}
	for _, m := range info.Methods {
		if private[m] {
			t.Errorf("get_info advertises %q, which this connection refuses — a client following it is sent down a path that cannot work", m)
		}
	}

	// Both directions on what remains.
	//
	// get_balance must be GONE: a bill's balance is the total funded across every
	// recipient, and advertising it invites any holder of the shared connection to
	// read that figure with no proof — the disclosure get_budget is already carved
	// out for.
	require.NotContains(t, info.Methods, "get_balance",
		"a bill must not advertise get_balance — its balance is every recipient's total")

	// get_info must REMAIN, and the filter must not have removed too much. It is
	// load-bearing rather than incidental: private_methods above is the only wire
	// signal that the bill methods exist at all, and it rides on get_info.
	require.Contains(t, info.Methods, "get_info",
		"get_info must stay — it carries private_methods, which is how a holder discovers the bill methods")

	// And the methods are still SERVED privately — dropping them from get_info must not
	// have dropped the authorization that makes them work.
	var roster CashStatusResult
	require.NoError(t, privateCall(t, created.CashToken, constants.NIP47MethodCashStatus,
		struct{}{}, ownerPriv, &roster),
		"cash_status must still work over the private transport; get_info only stopped advertising it")
	require.Len(t, roster.Recipients, 1)
}

// §Info Events: "A Cash Wallet MUST NOT publish a kind-13194 NIP-47 info event."
//
// Asserted against the relay itself rather than the Hub's own reporting. A bill's wallet
// pubkey travels in clear-text p tags, so an info event published under it would be a
// public, enumerable marker for every bill the Hub ever minted.
func TestSpec_InfoEvents_BillPublishesNoKind13194(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "spec-no-info-event", nil)
	hubClient := mustConnect(t, hub.Connection)

	ownerPub := mustPubkey(t, newTestPrivkey(t))
	walletPubkey, _, _ := mintPubkeySource(t, hubClient, ownerPub, 30_000)

	found := billInfoEventsOnRelay(t, cfg, walletPubkey)
	if found > 0 {
		t.Errorf("relay holds %d kind-13194 info event(s) authored by bill %s; a bill must publish none",
			found, walletPubkey)
	}
}

// billInfoEventsOnRelay counts kind-13194 events on the relay authored by walletPubkey.
//
// Asked of the RELAY rather than of the Hub: what matters is whether such an event is
// publicly retrievable under the bill's pubkey, which is the thing an observer would
// enumerate. A Hub reporting "I publish none" would not settle that.
func billInfoEventsOnRelay(t *testing.T, cfg *Config, walletPubkey string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, cfg.RelayURL)
	require.NoError(t, err, "connect to %s", cfg.RelayURL)
	defer relay.Close()

	events, err := relay.QuerySync(ctx, nostr.Filter{
		Kinds:   []int{13194},
		Authors: []string{walletPubkey},
	})
	require.NoError(t, err, "query kind-13194 for %s", walletPubkey)
	return len(events)
}

// nwcclient is imported for the client type used above.
var _ = nwcclient.Client{}
