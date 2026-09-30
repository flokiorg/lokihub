//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// NIP-CASH conformance for requirements about the ITEM and the mint request, sent over a
// real private envelope with privateCall so adversarial shapes actually reach the Hub.
//
// These need asserting precisely because the client-side codec already refuses most of
// them (transport.Envelope.Validate). That means a Hub could have no check at all and
// every well-behaved client would still look correct — and an attacker does not run our
// encoder.

// §Bearer Items: "An item MUST NOT carry both: they authorize differently, and an item
// asserting both leaves a Hub to choose, hiding the sender's mistake either way."
func TestSpec_BearerItems_HubRefusesAnItemCarryingBothProofAndSecret(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "spec-both-auth", nil)
	hubClient := mustConnect(t, hub.Connection)

	ownerPriv := newTestPrivkey(t)
	ownerPub := mustPubkey(t, ownerPriv)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(ownerPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))

	// A real slice proof — ownerPriv genuinely holds this slice — AND a cash secret in
	// params. Either alone is a valid authorization; together they are incoherent, and
	// the Hub must refuse rather than pick one.
	var roster CashStatusResult
	err := privateCall(t, created.CashToken, constants.NIP47MethodCashStatus,
		map[string]any{"cash_secret": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		ownerPriv, &roster)

	require.Error(t, err,
		"the Hub served an item carrying both a proof and a cash secret; it must refuse rather than choose")
	t.Logf("Hub refused a both-authorized item: %v", err)
	require.Empty(t, roster.Recipients, "a refused item must disclose no roster")
}

// §Processing Algorithm: the running recipient sum "MUST NOT exceed the Hub's own
// per-wallet funding ceiling", and §Request: "A request over the cap MUST be rejected
// whole, never partially fulfilled: a caller cannot be left guessing which recipients
// were funded."
//
// The two recipients are each UNDER the ceiling and only their sum exceeds it, so a Hub
// checking per entry rather than on the running sum would pass this mint — which is the
// mistake the requirement exists to prevent.
func TestSpec_MintCash_OverPerWalletCeilingIsRejectedWhole(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}

	// A hub with a deliberately LOW ceiling. createEphemeralCashHub's own is 10,000,000
	// mloki — more than its funded balance — so an over-ceiling mint there would be
	// refused for insufficient funds instead, proving nothing about the ceiling.
	const ceiling = 10_000
	resp, err := admin.createApp(adminCreateAppRequest{
		Name:                  ephemeralFixtureNamePrefix + " spec-ceiling-hub",
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                  "cash_hub",
		CashPerWalletMaxMloki: ceiling,
		CashMaxExpSecs:        happyPathExpirySecs,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral hub app_id=%d (%v)", resp.ID, err)
		}
	})
	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralCashHubFundLoki))
	hubClient := mustConnect(t, resp.PairingUri)

	before, err := admin.listCashWalletClaims(resp.ID)
	require.NoError(t, err)

	const each = uint64(ceiling*2/3 + 1) // under the ceiling alone, over it when summed
	var created MintCashResult
	err = hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: mustPubkey(t, newTestPrivkey(t)), AmountMillis: each},
			{IdentityType: "pubkey", IdentityValue: mustPubkey(t, newTestPrivkey(t)), AmountMillis: each},
		},
		Expiry: happyPathExpirySecs,
	}, &created)
	require.Error(t, err, "a mint whose recipient SUM exceeds the per-wallet ceiling must be refused")
	t.Logf("over-ceiling mint refused: %v", err)

	// Rejected WHOLE. Read from the Hub's own records, because a response cannot prove a
	// negative — a partially-funded wallet would show up here and nowhere else.
	after, err := admin.listCashWalletClaims(resp.ID)
	require.NoError(t, err)
	require.Len(t, after, len(before),
		"a refused mint created %d claim row(s); it must be rejected whole", len(after)-len(before))
	require.Empty(t, created.CashToken, "a refused mint must return no token")
}

// §Lifecycle and Deletion: "a wallet MUST NOT be deleted while any sibling slice is still
// unclaimed, even if the balance momentarily appears to allow it, since that sibling's own
// future redemption still needs the real funds sitting there."
//
// Two recipients, one redeems in full. The bill must survive — and the sibling must then
// actually be able to redeem, which is what makes the first half worth anything. A Hub
// that kept the row but lost the funds would pass a weaker test.
func TestSpec_Lifecycle_BillSurvivesWhileASiblingSliceIsUnclaimed(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}
	hub, hubAppID, _ := createEphemeralCashHub(t, cfg, "spec-sibling-survives", nil)
	hubClient := mustConnect(t, hub.Connection)

	aPriv := newTestPrivkey(t)
	aPub := mustPubkey(t, aPriv)
	bPriv := newTestPrivkey(t)
	bPub := mustPubkey(t, bPriv)
	const each = uint64(happyPathAmountMloki)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: aPub, AmountMillis: each},
			{IdentityType: "pubkey", IdentityValue: bPub, AmountMillis: each},
		},
		Expiry: happyPathExpirySecs,
	}, &created))

	// A redeems its slice in full, which empties A's share but not the bill.
	invA := mintInvoiceFromSimpleWallet(t, cfg, each, "spec sibling A redeem")
	proofA := buildClaimProofEvent(t, aPriv, created.WalletPubkey, invA.PaymentHash, nil, time.Now())
	var resA ClaimFundsResult
	require.NoError(t, privateCall(t, created.CashToken, constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invA.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: aPub,
		IdentityEvent: eventJSON(t, proofA),
	}, aPriv, &resA))
	require.NotEmpty(t, resA.Preimage)

	// The bill must still exist, because B's slice is unclaimed.
	claims, err := admin.listCashWalletClaims(hubAppID)
	require.NoError(t, err)
	var liveForThisBill int
	for _, c := range claims {
		if c.WalletPubkey == created.WalletPubkey && !c.Archived {
			liveForThisBill++
		}
	}
	require.NotZero(t, liveForThisBill,
		"the bill was destroyed while a sibling slice was still unclaimed; that sibling's redemption needs those funds")

	// And the sibling can still redeem — the property the rule exists to protect.
	invB := mintInvoiceFromSimpleWallet(t, cfg, each, "spec sibling B redeem")
	proofB := buildClaimProofEvent(t, bPriv, created.WalletPubkey, invB.PaymentHash, nil, time.Now())
	var resB ClaimFundsResult
	require.NoError(t, privateCall(t, created.CashToken, constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invB.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: bPub,
		IdentityEvent: eventJSON(t, proofB),
	}, bPriv, &resB),
		"the sibling slice must still be redeemable after its co-recipient was paid")
	require.NotEmpty(t, resB.Preimage)
}
