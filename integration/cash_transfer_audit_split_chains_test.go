//go:build integration

// cash_transfer_audit_split_chains_test.go covers CHAINS of splits — split a slice, then split the
// spun-off wallet's own slice again, several generations deep — which the
// mandate flags for verification: does MinTransferMillis inheritance hold
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
// min_transfer_millis is set at creation (createEphemeralCashHub leaves it at
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
// decryptSplitWallet decrypts a nested-encrypted split token (delivered to the
// caller keyed to their own privkey) and connects to the resulting wallet.
//
// The two keys are SEPARATE parameters because they are separate roles, and conflating
// them is wrong in a real case: callerPriv decrypts the delivered token (delivery is keyed
// to whoever made the split), while signerPriv signs the derived bill's item proofs and must
// be whoever owns the SLICE on it. For a remainder those are the same party; for a
// carve-off delivered to the caller but owned by the new target they are not.
func decryptSplitWallet(t *testing.T, walletPubkey, encToken, callerPriv, signerPriv string) billCaller {
	t.Helper()
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPubkey, callerPriv)
	require.NoError(t, err)
	dec, err := c.Decrypt(encToken)
	require.NoError(t, err)
	tok, err := lokicash.Decode(dec)
	require.NoError(t, err)
	require.Equal(t, walletPubkey, tok.WalletPubkey)
	return mustConnectBill(t, nwcURIFromLokicash(tok), dec, signerPriv)
}

// splitToControlledTarget performs a partial split and returns BOTH resulting
// wallets: the carved piece (newClient, for the new identity) and the caller's
// own remainder (remainderClient) — which, under the two-wallet split model, is
// its OWN fresh wallet, not the source connection.
func splitToControlledTarget(t *testing.T, walletClient billCaller, curPriv, curPub, walletPubkey string, splitAmount uint64) (newClient billCaller, newPriv, newPub, newWalletPubkey string, remainderClient billCaller, remaining uint64) {
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
		AmountMillis:  &amt,
	}, &res))
	require.EqualValues(t, splitAmount, res.AmountMillis)
	require.NotEmpty(t, res.NewWalletToken, "a partial split must always spin off a dedicated wallet")
	require.NotNil(t, res.RemainingAmountMillis)
	require.NotEmpty(t, res.RemainderWalletToken, "a partial split delivers the remainder as its own new wallet")

	// Delivery is keyed to the CALLER for both wallets, but ownership differs: the carved
	// piece belongs to the new target, the remainder stays with the caller. So each client
	// signs with whoever owns the slice on it — which is what the next generation of this
	// chain then splits from.
	newClient = decryptSplitWallet(t, res.NewWalletPubkey, res.NewWalletToken, curPriv, newPriv)
	remainderClient = decryptSplitWallet(t, res.RemainderWalletPubkey, res.RemainderWalletToken, curPriv, curPriv)
	return newClient, newPriv, newPub, res.NewWalletPubkey, remainderClient, *res.RemainingAmountMillis
}

// TestAudit_CashSplitChain_InheritanceAndConservation splits the same value
// forward six generations deep, each generation carving everything except a
// one-floor remainder off into a fresh wallet, and asserts at every depth:
//
//   - the inherited min_transfer_millis floor is still enforced (a below-floor
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

	// billCaller, not *billConn: the loop below reassigns this from splitToControlledTarget,
	// which hands back the next generation's connection through the same interface.
	var curClient billCaller = mustConnectBill(t, created.PairingURI, created.CashToken, owner0Priv)
	curPriv, curPub, curWalletPubkey := owner0Priv, owner0Pub, created.WalletPubkey
	curAmount := originalAmount

	// Each "leaf" is a wallet left holding exactly one floor after the value
	// moved forward off it — collected so we can prove conservation at the end.
	type leaf struct {
		client billCaller
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
			AmountMillis:  &belowFloor,
		}, &rejRes)
		requireNWCErrorCode(t, rejErr, constants.ERROR_BAD_REQUEST)
		require.ErrorContains(t, rejErr, "min_transfer_millis",
			"gen %d below-floor split must be rejected for the inherited floor, not another reason", gen)

		// Move everything except exactly one floor forward into a new wallet.
		splitAmount := curAmount - uint64(floor)
		newClient, newPriv, newPub, newWalletPubkey, remainderClient, remaining := splitToControlledTarget(t, curClient, curPriv, curPub, curWalletPubkey, splitAmount)
		require.EqualValues(t, uint64(floor), remaining, "each hop must leave exactly one floor behind")

		// The remainder is now its OWN fresh dedicated wallet holding exactly one
		// floor — the source wallet was consumed by the split, not left as a leaf.
		var leafBal CashStatusResult
		require.NoError(t, remainderClient.Call(ctxT(t), constants.NIP47MethodCashStatus, CashStatusParams{Scope: "all"}, &leafBal))
		require.EqualValues(t, floor, unclaimedMillis(leafBal), "gen %d remainder wallet must hold exactly the floor", gen)
		leaves = append(leaves, leaf{client: remainderClient, amount: uint64(floor)})

		// Advance to the spun-off wallet for the next generation.
		curClient, curPriv, curPub, curWalletPubkey = newClient, newPriv, newPub, newWalletPubkey
		curAmount = splitAmount
		t.Logf("gen %d: moved %d forward, %d left behind (new wallet %s)", gen, splitAmount, floor, curWalletPubkey[:8])
	}

	// Conservation: sum(all leaves) + final forward balance == original.
	var total uint64
	for i, l := range leaves {
		var b CashStatusResult
		require.NoError(t, l.client.Call(ctxT(t), constants.NIP47MethodCashStatus, CashStatusParams{Scope: "all"}, &b))
		require.EqualValues(t, l.amount, unclaimedMillis(b), "leaf %d balance drifted", i)
		total += unclaimedMillis(b)
	}
	var finalBal CashStatusResult
	require.NoError(t, curClient.Call(ctxT(t), constants.NIP47MethodCashStatus, CashStatusParams{Scope: "all"}, &finalBal))
	total += uint64(unclaimedMillis(finalBal))
	require.EqualValues(t, originalAmount, total,
		"CONSERVATION VIOLATION across a %d-generation split chain: leaves + final != original", generations)
	require.EqualValues(t, originalAmount-uint64(floor)*generations, unclaimedMillis(finalBal),
		"final forward wallet must hold original minus one floor per hop")

	// Liveness: the final forward wallet is genuinely redeemable for its whole
	// balance by the identity the last split left it at.
	redeemInv := mintInvoiceFromSimpleWallet(t, cfg, uint64(unclaimedMillis(finalBal)), "audit chain final redeem")
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
