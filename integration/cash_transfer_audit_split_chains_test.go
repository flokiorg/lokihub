//go:build integration

// cash_transfer_audit_split_chains_test.go is the 2026-07-30 dynamic Cash-Hub
// audit's coverage of CHAINS of splits — split a slice, then split the
// spun-off wallet's own slice again, several generations deep — which the
// mandate flags for verification: does MinTransferMloki inheritance hold
// generations deep, and does money stay conserved across the whole lineage?
// Every generation is driven over the real NWC surface against the real
// backend, with the caller keeping each generation's target keypair so it can
// drive the next generation's split (something the existing splitOffPartial
// helper can't do — it throws its target key away).
package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

// createCashHubWithTransferPolicy provisions a throwaway cash_hub whose
// min_transfer_mloki is set at creation (createEphemeralCashHub leaves it at
// 0), root-funds it, and registers the same child-sweeping t.Cleanup
// createEphemeralCashHub uses.
func createCashHubWithTransferPolicy(t *testing.T, cfg *Config, name string, minTransferMloki int64) *nwcclient.Client {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}
	resp, err := admin.createApp(adminCreateAppRequest{
		Name:                  ephemeralFixtureNamePrefix + " " + name,
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                  "cash_hub",
		CashPerWalletMaxMloki: 10_000_000,
		CashMaxExpSecs:        3600,
		CashMinTransferMloki:  minTransferMloki,
	})
	require.NoError(t, err)
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
	return mustConnect(t, resp.PairingUri)
}

// splitToControlledTarget carves splitAmount off the pubkey slice currently
// registered to (curPriv/curPub) on walletClient's wallet, into a brand-new
// dedicated wallet whose sole recipient is a pubkey the CALLER controls — then
// decrypts the spun-off wallet's connection (exactly as a real recipient would:
// the caller's own privkey + the plaintext new_wallet_pubkey) and returns a
// live client for it plus that wallet's own (priv, pub, walletPubkey). This is
// what lets a test keep splitting the SAME value forward, generation after
// generation.
func splitToControlledTarget(t *testing.T, walletClient *nwcclient.Client, curPriv, curPub, walletPubkey string, splitAmount uint64) (newClient *nwcclient.Client, newPriv, newPub, newWalletPubkey string, remaining uint64) {
	t.Helper()
	newPriv = newTestPrivkey(t)
	newPub = mustPubkey(t, newPriv)
	proof := buildTransferProofEvent(t, curPriv, walletPubkey, "pubkey", newPub, "", splitAmount, nil, time.Now())
	amt := splitAmount
	var res CashTransferResult
	require.NoError(t, walletClient.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: curPub,
		IdentityEvent: eventJSON(t, proof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMloki:   &amt,
	}, &res))
	require.EqualValues(t, splitAmount, res.AmountMloki)
	require.NotEmpty(t, res.NewWalletToken, "a partial split must always spin off a dedicated wallet")
	require.NotNil(t, res.RemainingAmountMloki)

	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, res.NewWalletPubkey, curPriv)
	require.NoError(t, err)
	dec, err := c.Decrypt(res.NewWalletToken)
	require.NoError(t, err)
	tok, err := lokicash.Decode(dec)
	require.NoError(t, err)
	require.Equal(t, res.NewWalletPubkey, tok.WalletPubkey)
	return mustConnect(t, nwcURIFromLokicash(tok)), newPriv, newPub, res.NewWalletPubkey, *res.RemainingAmountMloki
}

// TestAudit_CashSplitChain_InheritanceAndConservation splits the same value
// forward six generations deep, each generation carving everything except a
// one-floor remainder off into a fresh wallet, and asserts at every depth:
//
//   - the inherited min_transfer_mloki floor is still enforced (a below-floor
//     split on the generation-N wallet is rejected), proving the floor rode the
//     whole lineage down, not just the first hop; and
//   - money is conserved: every left-behind remainder holds exactly the floor
//     amount, the final forward wallet holds exactly the expected balance, and
//     the grand total equals the original slice — no value created or destroyed
//     anywhere along the chain.
func TestAudit_CashSplitChain_InheritanceAndConservation(t *testing.T) {
	cfg := requireConfig(t)
	const floor = int64(happyPathAmountMloki) // 5_000
	hubClient := createCashHubWithTransferPolicy(t, cfg, "audit-split-chain", floor)

	const generations = 6
	originalAmount := uint64(floor) * 16 // 80_000 — room for 6 leave-one-floor-behind hops

	owner0Priv := newTestPrivkey(t)
	owner0Pub := mustPubkey(t, owner0Priv)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(owner0Pub, originalAmount),
		Expiry:     happyPathExpirySecs,
	}, &created))

	curClient := mustConnect(t, created.PairingURI)
	curPriv, curPub, curWalletPubkey := owner0Priv, owner0Pub, created.WalletPubkey
	curAmount := originalAmount

	// Each "leaf" is a wallet left holding exactly one floor after the value
	// moved forward off it — collected so we can prove conservation at the end.
	type leaf struct {
		client *nwcclient.Client
		amount uint64
	}
	var leaves []leaf

	for gen := 1; gen <= generations; gen++ {
		// Inheritance probe: a below-floor split on THIS generation's wallet must
		// be rejected FOR THE FLOOR — proving the floor was inherited all the way
		// down here. The proof's target and the new_identity must match so the
		// rejection is genuinely the floor, not a proof-binding mismatch.
		belowFloor := uint64(floor - 1)
		rejTargetPub := mustPubkey(t, newTestPrivkey(t))
		rejProof := buildTransferProofEvent(t, curPriv, curWalletPubkey, "pubkey", rejTargetPub, "", belowFloor, nil, time.Now())
		var rejRes CashTransferResult
		rejErr := curClient.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, rejProof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: rejTargetPub},
			AmountMloki:   &belowFloor,
		}, &rejRes)
		requireNWCErrorCode(t, rejErr, constants.ERROR_BAD_REQUEST)
		require.ErrorContains(t, rejErr, "min_transfer_mloki",
			"gen %d below-floor split must be rejected for the inherited floor, not another reason", gen)

		// Move everything except exactly one floor forward into a new wallet.
		splitAmount := curAmount - uint64(floor)
		newClient, newPriv, newPub, newWalletPubkey, remaining := splitToControlledTarget(t, curClient, curPriv, curPub, curWalletPubkey, splitAmount)
		require.EqualValues(t, uint64(floor), remaining, "each hop must leave exactly one floor behind")

		// The source wallet is now a leaf holding exactly one floor.
		var leafBal GetBalanceResult
		require.NoError(t, curClient.Call(ctxT(t), "get_balance", struct{}{}, &leafBal))
		require.EqualValues(t, floor, leafBal.Balance, "gen %d source wallet must hold exactly the floor remainder", gen)
		leaves = append(leaves, leaf{client: curClient, amount: uint64(floor)})

		// Advance to the spun-off wallet for the next generation.
		curClient, curPriv, curPub, curWalletPubkey = newClient, newPriv, newPub, newWalletPubkey
		curAmount = splitAmount
		t.Logf("gen %d: moved %d forward, %d left behind (new wallet %s)", gen, splitAmount, floor, curWalletPubkey[:8])
	}

	// Conservation: sum(all leaves) + final forward balance == original.
	var total uint64
	for i, l := range leaves {
		var b GetBalanceResult
		require.NoError(t, l.client.Call(ctxT(t), "get_balance", struct{}{}, &b))
		require.EqualValues(t, l.amount, b.Balance, "leaf %d balance drifted", i)
		total += uint64(b.Balance)
	}
	var finalBal GetBalanceResult
	require.NoError(t, curClient.Call(ctxT(t), "get_balance", struct{}{}, &finalBal))
	total += uint64(finalBal.Balance)
	require.EqualValues(t, originalAmount, total,
		"CONSERVATION VIOLATION across a %d-generation split chain: leaves + final != original", generations)
	require.EqualValues(t, originalAmount-uint64(floor)*generations, finalBal.Balance,
		"final forward wallet must hold original minus one floor per hop")

	// Liveness: the final forward wallet is genuinely redeemable for its whole
	// balance by the identity the last split left it at.
	redeemInv := mintInvoiceFromSimpleWallet(t, cfg, uint64(finalBal.Balance), "audit chain final redeem")
	redeemProof := buildClaimProofEvent(t, curPriv, curWalletPubkey, redeemInv.PaymentHash, nil, time.Now())
	var redeemRes ClaimFundsResult
	require.NoError(t, curClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       redeemInv.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: curPub,
		IdentityEvent: eventJSON(t, redeemProof),
	}, &redeemRes))
	require.NotEmpty(t, redeemRes.Preimage)
}

