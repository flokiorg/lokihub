//go:build integration

// admin_create_cash_wallet_test.go covers the admin HTTP/Wails mint path
// (api.CreateCashWallet, POST /api/apps/:id/cash-wallets) — a separate code
// path from the NWC-facing mint_cash method every other fixture in this
// suite mints through. It had solid mocked-LN unit coverage but zero
// integration/live-node coverage (security-audit-scope-2026-08-30.md §7,
// QA/test-coverage finding M-3, circle-cash-audit-2026-08-31 round): the
// real internal-transfer funding step, the real mint_signature LND
// SignMessage call, and NIP-CASH's "MUST be atomic" guarantee for this path
// had never been proven against a live node.
package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/lokicash"
)

// TestAdminCreateCashWallet_RedeemsIdenticallyToNWCMinted proves a wallet
// minted via the admin HTTP path behaves identically to one minted via NWC
// mint_cash: same connection shape (cash_token decodes to the same
// pairing_uri, matching requireLokicashMatchesPairingURI's own invariant),
// same balance, and — the part that actually proves real fund movement, not
// just a plausible-looking response — a real cash_redeem against it succeeds
// exactly like it would for an NWC-minted wallet.
func TestAdminCreateCashWallet_RedeemsIdenticallyToNWCMinted(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}
	_, hubAppID, _ := createEphemeralCashHub(t, cfg, "admin-mint-hub", nil)

	beneficiaryPriv := newTestPrivkey(t)
	beneficiaryPub, err := nostr.GetPublicKey(beneficiaryPriv)
	require.NoError(t, err)

	resp, err := admin.createCashWallet(hubAppID, adminCreateCashWalletRequest{
		Recipients: []adminCashWalletRecipient{
			{IdentityType: "pubkey", IdentityValue: beneficiaryPub, AmountMloki: happyPathAmountMloki},
		},
		ExpirySecs: happyPathExpirySecs,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.PairingURI)
	require.NotEmpty(t, resp.CashToken)
	require.Len(t, resp.Recipients, 1)
	require.EqualValues(t, happyPathAmountMloki, resp.Recipients[0].AmountMloki)
	t.Cleanup(func() { _ = admin.deleteCashWallet(hubAppID, resp.AppID) })

	requireLokicashMatchesPairingURI(t, resp.PairingURI, resp.CashToken)

	decoded, err := lokicash.Decode(resp.CashToken)
	require.NoError(t, err)

	child := mustConnect(t, resp.PairingURI)

	var balance GetBalanceResult
	require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balance))
	require.EqualValues(t, happyPathAmountMloki, balance.Balance,
		"admin-minted wallet must be pre-funded with exactly the requested amount, same as an NWC-minted one")

	// The real proof: an actual cash_redeem, moving real money, succeeds
	// exactly as it would for an NWC-minted wallet (mirrors
	// TestClaimFunds' own happy-path shape).
	invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration admin-mint redeem")
	proof := buildClaimProofEvent(t, beneficiaryPriv, decoded.WalletPubkey, invoice.PaymentHash, nil, time.Now())
	var result ClaimFundsResult
	require.NoError(t, child.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: beneficiaryPub,
		IdentityEvent: eventJSON(t, proof),
	}, &result))
	require.NotEmpty(t, result.Preimage, "cash_redeem against an admin-minted wallet must actually pay out")

	var balanceAfter GetBalanceResult
	require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balanceAfter))
	require.EqualValues(t, 0, balanceAfter.Balance, "the slice must be fully drained after redemption")
}

// TestAdminCreateCashWallet_MintSignatureVerifiesAgainstLiveNode proves the
// admin path's mint_signature field (added 2026-08-30, previously this path
// had no provenance field at all) produces a token that verifies against the
// live node's real signing key — mirrors
// TestCashMintProvenance/SignedTokenVerifiesToAStableNodeIdentity's own
// pattern (cash_consolidate_test.go), but through the admin path.
func TestAdminCreateCashWallet_MintSignatureVerifiesAgainstLiveNode(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}
	_, hubAppID, _ := createEphemeralCashHub(t, cfg, "admin-mint-provenance-hub", nil)

	beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
	require.NoError(t, err)

	resp, err := admin.createCashWallet(hubAppID, adminCreateCashWalletRequest{
		Recipients: []adminCashWalletRecipient{
			{IdentityType: "pubkey", IdentityValue: beneficiaryPub, AmountMloki: happyPathAmountMloki},
		},
		ExpirySecs:    happyPathExpirySecs,
		MintSignature: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.deleteCashWallet(hubAppID, resp.AppID) })

	decoded, err := lokicash.Decode(resp.CashToken)
	require.NoError(t, err)
	require.NotNil(t, decoded.MintSignature, "admin-minted token with mint_signature:true must carry a mint signature")
	require.NotNil(t, decoded.AttestedAmount)
	assert.EqualValues(t, happyPathAmountMloki, *decoded.AttestedAmount)

	minter, verified := lokicash.VerifyMint(decoded)
	require.True(t, verified, "admin-minted token's provenance signature must verify against the live node's real signing key")
	assert.Len(t, minter, 66, "recovered minter must be a compressed secp256k1 pubkey (hex)")
}

// TestAdminCreateCashWallet_NoMintSignature_NoProvenance is the control:
// omitting mint_signature (the default) produces a token with no
// provenance, same as the NWC path's own default.
func TestAdminCreateCashWallet_NoMintSignature_NoProvenance(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}
	_, hubAppID, _ := createEphemeralCashHub(t, cfg, "admin-mint-no-provenance-hub", nil)

	beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
	require.NoError(t, err)

	resp, err := admin.createCashWallet(hubAppID, adminCreateCashWalletRequest{
		Recipients: []adminCashWalletRecipient{
			{IdentityType: "pubkey", IdentityValue: beneficiaryPub, AmountMloki: happyPathAmountMloki},
		},
		ExpirySecs: happyPathExpirySecs,
		// MintSignature omitted (default false)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.deleteCashWallet(hubAppID, resp.AppID) })

	decoded, err := lokicash.Decode(resp.CashToken)
	require.NoError(t, err)
	assert.Nil(t, decoded.MintSignature)
	_, verified := lokicash.VerifyMint(decoded)
	assert.False(t, verified)
}
