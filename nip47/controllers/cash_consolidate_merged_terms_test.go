package controllers

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Consolidating Tokens and §Security Considerations: a many-source
// operation takes the conservative bound on every inherited term, so the merged
// expiry is the EARLIEST of the sources (CASH-consolidating-tokens-09,
// CASH-security-considerations-06/-07). The merged bill's provenance must also
// attest exactly the merged total, or the bill cannot be verified
// (CASH-the-cash-token-43).
func TestHandleCashConsolidateEvent_MergedBillTakesEarliestExpiryAndAttestsTotal(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	_, secretHash, err := cashwallet.GenerateCashSecret()
	require.NoError(t, err)

	// tests.MockInvoice is fixed at 123,000 mloki; see
	// TestHandleCashConsolidateEvent_CashTarget_HappyPath.
	const amount1, amount2 = uint64(123_000), uint64(1_000)
	s1 := guardSource(t, svc, hub, ownerPub, amount1, 0, 0)
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")
	s2 := guardSource(t, svc, hub, ownerPub, amount2, 0, 0)
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	// The SECOND source expires first, so neither "first source" nor "latest"
	// gives the right answer.
	later := time.Now().Add(10 * time.Hour).Truncate(time.Second)
	earliest := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", s1.ID).Update("expires_at", later).Error)
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", s2.ID).Update("expires_at", earliest).Error)

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: int64(amount1)},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: int64(amount2)},
	}

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityCash, secretHash, "", amount1, nil, time.Now())
	proof2 := buildTransferProofEvent(t, ownerPriv, *s2.WalletPubkey, db.CashIdentityCash, secretHash, "", amount2, nil, time.Now())
	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof2)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: secretHash},
	})
	require.Nil(t, resp.Error, "%+v", resp.Error)
	result := resp.Result.(cashConsolidateResponse)

	var merged db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", result.NewWalletPubkey).First(&merged).Error)
	require.NotNil(t, merged.ExpiresAt)
	assert.True(t, merged.ExpiresAt.Equal(earliest), "merged expiry %v, want the earliest source's %v", merged.ExpiresAt, earliest)

	token, err := lokicash.Decode(result.NewWalletToken)
	require.NoError(t, err)
	require.NotNil(t, token.AttestedAmount)
	assert.Equal(t, amount1+amount2, *token.AttestedAmount)
	// The signature is recoverable, so a signature over any other amount still
	// "verifies", just to some other key. Provenance holds only if it recovers
	// to this node's own key.
	minter, ok := lokicash.VerifyMint(token)
	require.True(t, ok)
	nodeKey := hex.EncodeToString(mockLN.SigningKey.PubKey().SerializeCompressed())
	assert.Equal(t, nodeKey, minter, "the merged bill's provenance must be this node's signature over the amount it attests")
}
