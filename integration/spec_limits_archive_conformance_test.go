//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/lokicash"
	nipcashclient "github.com/ohstr/nmilat/nipcash/client"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// §Ceilings: "A Hub's `max_consolidate_sources` MUST therefore be low enough that its
// largest permitted item still fits within its own `max_bytes`, and a Hub MUST NOT announce
// a combination where it does not."
//
// Asserted against what the Hub ACTUALLY ANNOUNCES, not against its configured defaults.
// The announcement is what a client believes and packs to, so an incoherent pair there is a
// promise the Hub cannot keep: a client fills an item up to the advertised source cap, and
// the envelope then cannot be encrypted at all. The failure lands on the client, for a
// policy the Hub published.
func TestSpec_Ceilings_AnnouncedLimitsAreSelfConsistent(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "spec-ceilings", nil)
	hubClient := mustConnect(t, hub.Connection)

	ownerPub := mustPubkey(t, newTestPrivkey(t))
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(ownerPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	// Read the announcement the way a client does: recover the hub identity from the bill's
	// own mint signature, then fetch and verify the kind-11190 event against it.
	tok, err := lokicash.Decode(created.CashToken)
	require.NoError(t, err)
	minter, ok := lokicash.VerifyMint(tok)
	require.True(t, ok, "bill carries no verifiable provenance, so no announcement can be found")
	hubXOnly, err := transport.NormalizeNodeIdentity(minter)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A zero-value Client is deliberate and safe: NewBatchSession reads no field of its
	// receiver, so it is a factory hung off Client for namespacing rather than a method
	// needing a connected client. Fetching an announcement needs only an identity to verify
	// against and relays to look on, which is the point — a client can learn a hub's policy
	// before it holds a connection to it.
	session, err := (&nipcashclient.Client{}).NewBatchSession(ctx, hubXOnly, tok.RelayURLs)
	require.NoError(t, err, "fetch the hub's kind-11190 announcement")

	limits := session.Limits()
	t.Logf("announced: max_bytes=%d max_items=%d max_consolidate_sources=%d pad_bucket=%d verify_budget=%d",
		limits.MaxEnvelopeBytes, limits.MaxItems, limits.MaxConsolidateSources,
		limits.PadBucketBytes, limits.MaxVerifyBudget)

	// The load-bearing check: the largest item the announced source cap permits must fit the
	// announced byte cap.
	largest := transport.EstimatedConsolidateItemBytes(limits.MaxConsolidateSources)
	require.LessOrEqual(t, largest, limits.MaxEnvelopeBytes,
		"announced max_consolidate_sources=%d implies a ~%d byte item, over the announced max_bytes=%d — a client packing to the advertised cap could not encrypt the envelope",
		limits.MaxConsolidateSources, largest, limits.MaxEnvelopeBytes)

	// And the announced policy must be one the codec will actually accept, which is the
	// same validation a Hub runs on its own configuration at startup.
	require.NoError(t, limits.Validate(), "the Hub announced a policy its own codec rejects")

	// Padding granularity must not exceed the envelope ceiling, or no envelope could ever be
	// padded — checked explicitly because it is the one limit whose violation only shows up
	// at encode time.
	require.LessOrEqual(t, limits.PadBucketBytes, limits.MaxEnvelopeBytes)
}

// §Archival on Deletion: "The archive MUST NOT be reachable from the NWC surface in any
// way."
//
// A destroyed bill's slices live on in the Hub's archive — deliberately, so an operator can
// account for a bill after it is gone. That record is for the operator's own API, and it
// MUST NOT be readable over NWC: a bill's wallet pubkey travels in clear-text `p` tags, so a
// reachable archive would turn the Hub into a queryable index of every bill it ever issued.
//
// Tested by asking the destroyed bill's OWN connection — the most privileged caller there
// is, since it holds the token — and requiring that no method hands back archived state.
// cash_status's tombstone is the single deliberate exception, and it carries no roster.
func TestSpec_Archive_IsUnreachableOverNWC(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}
	hub, hubAppID, _ := createEphemeralCashHub(t, cfg, "spec-archive-unreachable", nil)
	hubClient := mustConnect(t, hub.Connection)

	beneficiaryPriv := newTestPrivkey(t)
	beneficiaryPub := mustPubkey(t, beneficiaryPriv)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	bill := mustConnectBill(t, created.PairingURI, created.CashToken, beneficiaryPriv)

	// Drain it in full, which empties the bill and has the Hub destroy it.
	invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "spec archive drain")
	proof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
	var claim ClaimFundsResult
	require.NoError(t, bill.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: beneficiaryPub,
		IdentityEvent: eventJSON(t, proof),
	}, &claim))
	require.NotEmpty(t, claim.Preimage)

	// Confirm from the operator's side that the archive DOES hold the record — otherwise
	// this test would pass vacuously against a Hub that simply forgot the bill.
	requireCashWalletDrainedAway(t, admin, hubAppID, created.WalletPubkey, nil)

	// get_balance must not report the archived amount. It is an always-granted method on a
	// live bill, which makes it the likeliest accidental leak.
	//
	// Silence is the expected answer and is conformant — a drained bill is deleted, so there
	// is nothing left to answer — but a reply of zero is conformant too, so both are allowed
	// and only an archived AMOUNT is a failure. Bounded by the suite's own silence window
	// rather than the default context, because waiting 30s to observe an absence tells us
	// nothing the 2s wait does not.
	balCtx, balCancel := context.WithTimeout(context.Background(), drainedWalletSilenceWindow)
	var bal GetBalanceResult
	balErr := bill.Call(balCtx, "get_balance", struct{}{}, &bal)
	balCancel()
	if balErr == nil {
		require.Zero(t, bal.Balance,
			"get_balance reported %d for a destroyed bill; the archive must not be readable over NWC", bal.Balance)
		t.Log("get_balance on a destroyed bill: answered, balance 0")
	} else {
		t.Logf("get_balance on a destroyed bill: silent (%v)", balErr)
	}

	// cash_status is the ONE deliberate exception, and only as a TOMBSTONE: it may say
	// "spent" with a retention deadline, and MUST NOT return the roster the archive still
	// holds.
	//
	// The tombstone shape is asserted, not just the empty roster. An empty roster alone would
	// also be satisfied by a Hub that answered as though this were an ordinary live bill that
	// happened to have no recipients — which is a different and worse behaviour, since it
	// says nothing about the archive being withheld. Requiring error="spent" pins that the
	// reply is the deliberate exception rather than an accidental pass.
	statusCtx, statusCancel := context.WithTimeout(context.Background(), drainedWalletSilenceWindow)
	var status CashStatusResult
	statusErr := bill.Call(statusCtx, constants.NIP47MethodCashStatus, CashStatusParams{Scope: "all"}, &status)
	statusCancel()
	if statusErr == nil {
		require.Empty(t, status.Recipients,
			"cash_status returned %d archived roster row(s) for a destroyed bill; a tombstone may say it is spent, never who held it",
			len(status.Recipients))
		require.Equal(t, "spent", status.Error,
			"cash_status answered a destroyed bill with something other than a tombstone (error=%q); an empty roster is not enough — the reply must identify itself as the retention exception, or this assertion would also pass for a Hub serving the bill as though it were live",
			status.Error)
		t.Logf("cash_status on a destroyed bill: tombstone, error=%q, no roster", status.Error)
	} else {
		t.Logf("cash_status on a destroyed bill: silent (%v)", statusErr)
	}
}
