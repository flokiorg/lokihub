package cashwallet

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/tests"
)

// newProvenanceTestSourceWallet creates a funded, single-recipient cash_wallet
// under hub for split provenance tests — mirrors create_test.go's own Split
// test fixtures.
func newProvenanceTestSourceWallet(t *testing.T, svc *tests.TestService, hub *db.App) *db.App {
	t.Helper()
	sourceWallet, _, err := svc.AppsService.CreateApp(
		"source-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, sourceWallet.ID, 200_000, "sourcefundtxhash")
	return sourceWallet
}

// TestCreate_WithMintProvenance mints a wallet with SignMint set and asserts
// the issued token carries a mint signature that recovers to the node's own
// pubkey over the wallet's attested amount — the full provenance path end to
// end (node sign -> zbase32 decode -> TLV -> VerifyMint recover).
func TestCreate_WithMintProvenance(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	minterPubkey := hex.EncodeToString(priv.PubKey().SerializeCompressed())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.SigningKey = priv
	mockLN.Pubkey = minterPubkey

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 1800,
	})
	require.NoError(t, err)

	tok, err := lokicash.Decode(result.CashToken)
	require.NoError(t, err)
	require.NotNil(t, tok.MintSignature)
	require.NotNil(t, tok.AttestedAmount)
	assert.Equal(t, uint64(1000), *tok.AttestedAmount)

	recovered, ok := lokicash.VerifyMint(tok)
	require.True(t, ok)
	assert.Equal(t, minterPubkey, recovered)
}

// TestCreate_WithoutMintProvenance is the default: no signature is attached and
// the token is fully spendable without one.
// TestCreate_AlwaysSigned replaces a test that asserted a mint could produce an
// UNSIGNED token, which is no longer a reachable state and must not be.
//
// Provenance is the only thing a token carries that identifies its minting hub, and
// that identity is the only thing a client can verify a transport announcement
// against — so an unsigned bill can never reach the private transport, which is the
// only transport that serves bill methods. An unsigned bill would be unspendable.
func TestCreate_AlwaysSigned(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	// No opt-in of any kind: signing is not a caller's choice any more.
	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 1800,
	})
	require.NoError(t, err)

	tok, err := lokicash.Decode(result.CashToken)
	require.NoError(t, err)
	assert.NotNil(t, tok.MintSignature, "every minted bill must carry provenance")
	assert.NotNil(t, tok.AttestedAmount, "provenance without an attested amount is meaningless")
}

// TestCreate_SigningFailureAbortsWithNothingCommitted is the inverse of a test that
// asserted a signing failure "never fails the mint". It now must, and the important
// half is what is NOT left behind.
//
// Signing is obtained before any funds move, precisely so this can be a clean refusal.
// Had it stayed where it was — after the transfer — a failure could only ever be
// swallowed, because reporting it would tell the caller a mint failed while a funded
// wallet existed that they did not know about.
func TestCreate_SigningFailureAbortsWithNothingCommitted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A node that cannot sign. Cleared explicitly, because NewMockLn now supplies a
	// key by default — minting is impossible without one, so "cannot sign" is a
	// deliberate condition rather than the absence of setup.
	svc.LNClient.(*tests.MockLn).SigningKey = nil

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	hubBefore := queries.GetIsolatedBalance(svc.DB, hub.ID)

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 1800,
	})
	require.Error(t, err, "a mint that cannot be signed must fail, not degrade to an unspendable bill")
	assert.Nil(t, result)

	// Nothing moved: the refusal happened before the transfer.
	assert.Equal(t, hubBefore, queries.GetIsolatedBalance(svc.DB, hub.ID),
		"a refused mint must leave the hub's balance untouched")
}

// TestSplit_WithMintProvenance splits off a slice with SignMint set and
// asserts the resulting token carries a mint signature that recovers to the
// node's own pubkey over the SPLIT-OFF wallet's own carved amount (not the
// source's) — Split is the third and last wallet-creation path to gain
// provenance support, after Commit and Consolidate.
func TestSplit_WithMintProvenance(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	minterPubkey := hex.EncodeToString(priv.PubKey().SerializeCompressed())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.SigningKey = priv
	mockLN.Pubkey = minterPubkey
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: 1000},
	}

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	sourceWallet := newProvenanceTestSourceWallet(t, svc, hub)

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	result, err := Split(context.TODO(), newTestDeps(svc), SplitParams{
		HubApp:           hub,
		SourceWalletApp:  sourceWallet,
		AmountMloki:      1000,
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPubkey,
	})
	require.NoError(t, err)

	tok, err := lokicash.Decode(result.CashToken)
	require.NoError(t, err)
	require.NotNil(t, tok.MintSignature)
	require.NotNil(t, tok.AttestedAmount)
	assert.Equal(t, uint64(1000), *tok.AttestedAmount)

	recovered, ok := lokicash.VerifyMint(tok)
	require.True(t, ok)
	assert.Equal(t, minterPubkey, recovered)
}

// TestSplitInTwo_WithMintProvenance_BothWalletsSigned asserts that BOTH the
// carved and remainder wallets carry their own, independently-valid mint
// signature over their own respective amounts — the spec's "each wallet —
// freshly minted, split-off, or consolidated — carries its own signature"
// claim (§Mint Provenance), specifically for the two-wallet split case.
func TestSplitInTwo_WithMintProvenance_BothWalletsSigned(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	minterPubkey := hex.EncodeToString(priv.PubKey().SerializeCompressed())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.SigningKey = priv
	mockLN.Pubkey = minterPubkey
	// Two internal transfers (carved + remainder) both pay from the same
	// source wallet, so they need two distinct-hash invoices to avoid the
	// mock's "already paid" duplicate-hash guard.
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: 3000},
	}

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	sourceWallet := newProvenanceTestSourceWallet(t, svc, hub)

	carvedPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	remainderPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	result, _, err := SplitInTwo(context.TODO(), newTestDeps(svc), SplitInTwoParams{
		HubApp:                 hub,
		SourceWalletApp:        sourceWallet,
		CarvedIdentityType:     db.CashIdentityPubkey,
		CarvedIdentityValue:    carvedPubkey,
		CarvedAmountMloki:      2000,
		RemainderIdentityType:  db.CashIdentityPubkey,
		RemainderIdentityValue: remainderPubkey,
		RemainderAmountMloki:   3000,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Carved)
	require.NotNil(t, result.Remainder)

	carvedTok, err := lokicash.Decode(result.Carved.CashToken)
	require.NoError(t, err)
	require.NotNil(t, carvedTok.MintSignature)
	require.NotNil(t, carvedTok.AttestedAmount)
	assert.Equal(t, uint64(2000), *carvedTok.AttestedAmount)
	recoveredCarved, ok := lokicash.VerifyMint(carvedTok)
	require.True(t, ok)
	assert.Equal(t, minterPubkey, recoveredCarved)

	remainderTok, err := lokicash.Decode(result.Remainder.CashToken)
	require.NoError(t, err)
	require.NotNil(t, remainderTok.MintSignature)
	require.NotNil(t, remainderTok.AttestedAmount)
	assert.Equal(t, uint64(3000), *remainderTok.AttestedAmount)
	recoveredRemainder, ok := lokicash.VerifyMint(remainderTok)
	require.True(t, ok)
	assert.Equal(t, minterPubkey, recoveredRemainder)
}

// TestSplit_SigningFailureAbortsWithNothingCommitted is the inverse of a test that
// asserted a signing failure "never fails the split". A split mints a new bill, and an
// unsigned bill cannot reach the private transport — the only transport serving bill
// methods — so it would be unspendable. Refusing is the correct outcome.
//
// As with Create, the signature is obtained before the source wallet is drained, so a
// refusal leaves the source untouched.
func TestSplit_SigningFailureAbortsWithNothingCommitted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A node that cannot sign, cleared explicitly — NewMockLn supplies a key by
	// default now, so this is a deliberate condition, not missing setup.
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.SigningKey = nil
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: 1000},
	}

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	sourceWallet := newProvenanceTestSourceWallet(t, svc, hub)

	sourceBefore := queries.GetIsolatedBalance(svc.DB, sourceWallet.ID)

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	result, err := Split(context.TODO(), newTestDeps(svc), SplitParams{
		HubApp:           hub,
		SourceWalletApp:  sourceWallet,
		AmountMloki:      1000,
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPubkey,
	})
	require.Error(t, err, "a split that cannot be signed must fail, not produce an unspendable bill")
	assert.Nil(t, result)

	// The source was never drained: the refusal happened before the transfer.
	assert.Equal(t, sourceBefore, queries.GetIsolatedBalance(svc.DB, sourceWallet.ID),
		"a refused split must leave the source wallet's balance untouched")
}
