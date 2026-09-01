//go:build integration

// cash_consolidate_test.go drives mint provenance and cash_consolidate end to
// end over a real relay against a real running instance — the black-box
// counterpart to the in-process unit tests in lokicash/, cashwallet/, and
// nip47/controllers/. Provenance is verified by recovering the signer from a
// real minted token and matching it to the node's own get_info pubkey;
// consolidation is verified by real internal transfers actually moving funds
// into one merged wallet that then redeems for the full sum.
package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

func TestCashMintProvenance(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "mint-provenance-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	recipientPub, err := nostr.GetPublicKey(newTestPrivkey(t))
	require.NoError(t, err)

	mintSigned := func(t *testing.T, amount uint64) lokicash.Token {
		t.Helper()
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients:    onePubkeyRecipient(recipientPub, amount),
			Expiry:        happyPathExpirySecs,
			MintSignature: true,
		}, &created))
		decoded, err := lokicash.Decode(created.CashToken)
		require.NoError(t, err)
		return decoded
	}

	t.Run("SignedTokenVerifiesToAStableNodeIdentity", func(t *testing.T) {
		// The real node signs over the real wire; VerifyMint recovers the
		// signer offline. We don't have the node's raw pubkey handy (get_info
		// only reveals it with GET_INFO scope), but the recovered minter must
		// be a valid compressed pubkey, must match the attested amount, and
		// must be identical across two independently-minted tokens — i.e. the
		// same node identity, stable.
		a := mintSigned(t, happyPathAmountMloki)
		require.NotNil(t, a.MintSignature, "token should carry a mint signature")
		require.NotNil(t, a.AttestedAmount)
		assert.EqualValues(t, happyPathAmountMloki, *a.AttestedAmount)
		minterA, ok := lokicash.VerifyMint(a)
		require.True(t, ok, "mint signature must verify")
		assert.Len(t, minterA, 66, "recovered minter must be a compressed secp256k1 pubkey (hex)")

		b := mintSigned(t, happyPathAmountMloki*2)
		minterB, ok := lokicash.VerifyMint(b)
		require.True(t, ok)
		assert.Equal(t, minterA, minterB, "both tokens must recover to the same node identity")
		assert.EqualValues(t, happyPathAmountMloki*2, *b.AttestedAmount)
	})

	t.Run("UnsignedTokenCarriesNoProvenance", func(t *testing.T) {
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(recipientPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
			// MintSignature omitted (default false)
		}, &created))

		decoded, err := lokicash.Decode(created.CashToken)
		require.NoError(t, err)
		assert.Nil(t, decoded.MintSignature)
		_, ok := lokicash.VerifyMint(decoded)
		assert.False(t, ok)
	})
}

func TestCashConsolidate(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "consolidate-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	// One caller owns a slice on each of three separately-minted wallets under
	// the same hub.
	callerPriv := newTestPrivkey(t)
	callerPub, err := nostr.GetPublicKey(callerPriv)
	require.NoError(t, err)

	amounts := []uint64{happyPathAmountMloki, happyPathAmountMloki * 2, happyPathAmountMloki * 3}
	var sum uint64
	type src struct {
		walletPubkey string
		conn         string
		amount       uint64
	}
	sources := make([]src, len(amounts))
	for i, amt := range amounts {
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(callerPub, amt),
			Expiry:     happyPathExpirySecs,
		}, &created))
		sources[i] = src{walletPubkey: created.WalletPubkey, conn: created.PairingURI, amount: amt}
		sum += amt
	}

	// The merged wallet will be owned by this fresh identity.
	newPriv := newTestPrivkey(t)
	newPub, err := nostr.GetPublicKey(newPriv)
	require.NoError(t, err)

	// Build a per-source proof binding the caller to that source slice AND to
	// the merged new_identity.
	params := CashConsolidateParams{
		NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
	}
	for _, s := range sources {
		proof := buildTransferProofEvent(t, callerPriv, s.walletPubkey, "pubkey", newPub, "", s.amount, nil, time.Now())
		params.Sources = append(params.Sources, ConsolidateSourceParam{
			WalletPubkey:  s.walletPubkey,
			IdentityType:  "pubkey",
			IdentityValue: callerPub,
			IdentityEvent: eventJSON(t, proof),
		})
	}

	// Call cash_consolidate over one source's own shared connection.
	callConn := mustConnect(t, sources[0].conn)
	var result CashConsolidateResult
	require.NoError(t, callConn.Call(ctxT(t), constants.NIP47MethodCashConsolidate, params, &result))
	assert.EqualValues(t, sum, result.AmountMillis)
	require.NotEmpty(t, result.NewWalletPubkey)
	require.NotEmpty(t, result.NewWalletToken)

	// Decrypt the merged token (nested-encrypted to new_identity) and connect.
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, result.NewWalletPubkey, newPriv)
	require.NoError(t, err)
	decryptedToken, err := c.Decrypt(result.NewWalletToken)
	require.NoError(t, err)
	mergedToken, err := lokicash.Decode(decryptedToken)
	require.NoError(t, err)
	require.Equal(t, result.NewWalletPubkey, mergedToken.WalletPubkey)

	merged := mustConnect(t, nwcURIFromLokicash(mergedToken))

	// The merged wallet holds exactly the sum.
	var mergedBalance GetBalanceResult
	require.NoError(t, merged.Call(ctxT(t), "get_balance", struct{}{}, &mergedBalance))
	assert.EqualValues(t, sum, mergedBalance.Balance)

	// Every source is drained to zero.
	for _, s := range sources {
		srcConn := mustConnect(t, s.conn)
		var b GetBalanceResult
		require.NoError(t, srcConn.Call(ctxT(t), "get_balance", struct{}{}, &b))
		assert.EqualValues(t, 0, b.Balance, "source wallet must be drained after consolidation")
	}

	// The merged wallet redeems for the full sum — the funds really moved.
	invoice := mintInvoiceFromSimpleWallet(t, cfg, sum, "integration consolidate redeem")
	redeemProof := buildClaimProofEvent(t, newPriv, mergedToken.WalletPubkey, invoice.PaymentHash, nil, time.Now())
	var redeem ClaimFundsResult
	require.NoError(t, merged.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: newPub,
		IdentityEvent: eventJSON(t, redeemProof),
	}, &redeem))
	require.NotEmpty(t, redeem.Preimage)
}
