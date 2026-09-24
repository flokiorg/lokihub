//go:build integration

package integration

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
	"github.com/flokiorg/lokihub/lokicash"
)

// createCashHubWithRetention builds an ephemeral cash hub with an explicit
// spent-bill retention window, which createEphemeralCashHub cannot express.
// retentionSecs is a pointer for the same reason the API field is: 0 means
// "fall silent immediately", which must stay distinct from "use the default".
func createCashHubWithRetention(t *testing.T, cfg *Config, name string, retentionSecs *int) (*adminClient, uint, string) {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	resp, err := admin.createApp(adminCreateAppRequest{
		Name:                   ephemeralFixtureNamePrefix + " " + name,
		Scopes:                 []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                   "cash_hub",
		CashPerWalletMaxMloki:  10_000_000,
		CashMaxExpSecs:         3600,
		CashSpentRetentionSecs: retentionSecs,
	})
	require.NoError(t, err)

	// LIFO: children are swept before the hub itself, since DeleteApp refuses
	// a cash_hub that still has cash_wallet children.
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral hub app_id=%d (%v)", resp.ID, err)
		}
	})
	t.Cleanup(func() {
		claims, err := admin.listCashWalletClaims(resp.ID)
		if err != nil {
			return
		}
		seen := map[uint]bool{}
		for _, claim := range claims {
			if seen[claim.WalletAppID] {
				continue
			}
			seen[claim.WalletAppID] = true
			_ = admin.deleteCashWallet(resp.ID, claim.WalletAppID)
		}
	})

	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralCashHubFundLoki))
	return admin, resp.ID, resp.PairingUri
}

// strangerURI rewrites a pairing URI to keep its wallet pubkey and relays but
// swap in a freshly generated secret.
//
// This is exactly the attacker's real capability, not a contrived one: a
// wallet pubkey is public on the relay (it is the "p" tag of every request
// ever made against that bill) and anyone can generate their own key and
// encrypt to it. What they cannot do is produce the connection secret that
// came inside the lokicash token. So a client built this way can reach the
// hub and be decrypted by it, and must still learn nothing.
func strangerURI(t *testing.T, pairingURI string) string {
	t.Helper()

	parsed, err := url.Parse(pairingURI)
	require.NoError(t, err)

	query := parsed.Query()
	require.NotEmpty(t, query.Get("secret"), "pairing uri must carry a secret to replace")
	query.Set("secret", newTestPrivkey(t))
	parsed.RawQuery = query.Encode()

	return parsed.String()
}

// mintAndDrainOneBill mints a single-recipient bill on hubClient, redeems it in
// full, and returns a client still holding its (now destroyed) connection.
func mintAndDrainOneBill(t *testing.T, cfg *Config, hubClient *nwcclient.Client) (*nwcclient.Client, string) {
	t.Helper()

	beneficiaryPriv := newTestPrivkey(t)
	beneficiaryPub, err := nostr.GetPublicKey(beneficiaryPriv)
	require.NoError(t, err)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	decoded, err := lokicash.Decode(created.CashToken)
	require.NoError(t, err)

	holder := mustConnect(t, created.PairingURI)

	invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration tombstone drain")
	proof := buildClaimProofEvent(t, beneficiaryPriv, decoded.WalletPubkey, invoice.PaymentHash, nil, time.Now())

	var result ClaimFundsResult
	require.NoError(t, holder.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: beneficiaryPub,
		IdentityEvent: eventJSON(t, proof),
	}, &result))
	require.NotEmpty(t, result.Preimage, "the drain must actually pay out, or the bill is not really spent")

	return holder, created.PairingURI
}

// awaitTombstone polls cash_status until the bill reports itself spent.
//
// Polled, not asserted once, for the same reason requireSpentBillSilent polls:
// the hub answers the request that empties a bill BEFORE destroying it, so for
// a moment afterwards the bill is still there and still returns its roster.
func awaitTombstone(t *testing.T, holder *nwcclient.Client) CashStatusResult {
	t.Helper()

	deadline := time.Now().Add(drainedWalletDeleteWindow)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), drainedWalletSilenceWindow)
		var status CashStatusResult
		err := holder.Call(ctx, constants.NIP47MethodCashStatus, struct{}{}, &status)
		cancel()

		if err == nil && status.Error != "" {
			return status
		}
		if time.Now().After(deadline) {
			require.Fail(t,
				"a spent bill must answer its holder with a tombstone",
				"still not reporting spent %s after the last slice was paid out (last err: %v, last result: %+v)",
				drainedWalletDeleteWindow, err, status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestCashStatus_SpentBill_AnswersHolderAndStaysSilentToStrangers is the
// live-wire counterpart to nip47/cash_tombstone_test.go, and it pins the two
// halves of the design against each other over a real relay and a real node.
//
// Both halves matter, and they pull in opposite directions:
//
//   - The holder MUST get a definitive answer. Without it, silence is the only
//     signal, and silence cannot be told apart from a hub that is down — which
//     forces every client to guess, and one of the two guesses tells someone
//     their money is gone during an ordinary outage.
//
//   - Everyone else MUST still get silence. The moment the hub answers a
//     caller who never held the bill, it becomes an oracle for the existence
//     of every bill it ever issued, which is precisely what destroying the
//     bill was meant to prevent.
//
// The stranger here is not a stand-in. It holds the real wallet pubkey and
// reaches the real hub over the real relay; the only thing it lacks is the
// connection secret from the token. That is exactly the position of anyone
// watching the relay.
func TestCashStatus_SpentBill_AnswersHolderAndStaysSilentToStrangers(t *testing.T) {
	cfg := requireConfig(t)

	retention := 3600
	_, _, hubURI := createCashHubWithRetention(t, cfg, "tombstone-hub", &retention)
	hubClient := mustConnect(t, hubURI)

	holder, billURI := mintAndDrainOneBill(t, cfg, hubClient)

	// Half one: the holder gets a definitive answer.
	status := awaitTombstone(t, holder)
	require.Equal(t, "spent", status.Error,
		"a destroyed bill must name its state rather than leaving the caller to infer it from silence")
	require.Empty(t, status.Recipients,
		"a tombstone must not also carry the roster — the bill and its slices are gone")
	require.NotNil(t, status.RetainedUntil,
		"the caller must learn when this hub will stop answering, or it cannot tell a future silence from a fresh outage")
	require.Greater(t, *status.RetainedUntil, time.Now().Unix(),
		"retained_until must be in the future while the hub is still answering")
	require.LessOrEqual(t, *status.RetainedUntil, time.Now().Add(time.Duration(retention)*time.Second).Unix()+5,
		"retained_until must reflect the hub's configured window, not an unbounded promise")

	// Half two, and the load-bearing one: a caller who never held this bill
	// gets nothing, even though the hub is demonstrably still answering about
	// it (we just read the tombstone above, so a silence here cannot be blamed
	// on the bill having aged out or the hub being down).
	stranger := mustConnect(t, strangerURI(t, billURI))

	ctx, cancel := context.WithTimeout(context.Background(), drainedWalletSilenceWindow)
	defer cancel()

	var strangerStatus CashStatusResult
	err := stranger.Call(ctx, constants.NIP47MethodCashStatus, struct{}{}, &strangerStatus)
	require.Error(t, err,
		"a caller who does not hold this bill's connection must not be answered at all")
	require.True(t, errors.Is(err, context.DeadlineExceeded),
		"the answer to a stranger must be silence, not an error: any reply at all confirms this hub once served that pubkey, "+
			"turning the tombstone into an existence oracle for every bill the hub has issued (got: %v)", err)

	// The control, and the reason this test proves what it claims to.
	//
	// Silence is cheap to produce by accident: the bill could have aged out of
	// retention, its pubkey could have left the relay-side registry, or the
	// backend could simply have stopped answering between the two calls. Any
	// of those would make the assertion above pass for a reason that has
	// nothing to do with who was asking.
	//
	// So ask again as the holder, after the stranger has already been refused.
	// A tombstone here proves that throughout the stranger's attempt the bill
	// was still retained, still registered, and the hub still answering — and
	// note that decryption cannot be what refused them either, since the hub
	// builds its cipher from the requester's own pubkey (see
	// nip47/cash_tombstone.go) and therefore reads a stranger's request
	// perfectly well. By elimination, the only thing that turned an answer
	// into silence is the identity of the caller.
	var stillAnswering CashStatusResult
	require.NoError(t, holder.Call(ctxT(t), constants.NIP47MethodCashStatus, struct{}{}, &stillAnswering),
		"the hub must still be answering its holder after refusing the stranger, or the silence above proves nothing")
	require.Equal(t, "spent", stillAnswering.Error,
		"the bill must still be within retention after the stranger's attempt, or the silence above is explained by the window closing rather than by the gate")
}

// TestCashStatus_SpentBill_RetentionDisabled_StaysSilent pins the opt-out.
//
// A hub configured with no retention window keeps the behaviour that predates
// the tombstone: the bill is destroyed and the hub goes quiet immediately,
// even for the holder. This is a real setting, not a degenerate one — it is
// what an operator picks who would rather answer nothing at all — so it needs
// its own live proof that the tombstone path genuinely stays switched off
// rather than merely being configured off.
func TestCashStatus_SpentBill_RetentionDisabled_StaysSilent(t *testing.T) {
	cfg := requireConfig(t)

	disabled := 0
	_, _, hubURI := createCashHubWithRetention(t, cfg, "tombstone-off-hub", &disabled)
	hubClient := mustConnect(t, hubURI)

	holder, _ := mintAndDrainOneBill(t, cfg, hubClient)

	requireSpentBillSilent(t, func(ctx context.Context) error {
		var status CashStatusResult
		return holder.Call(ctx, constants.NIP47MethodCashStatus, struct{}{}, &status)
	})
}
