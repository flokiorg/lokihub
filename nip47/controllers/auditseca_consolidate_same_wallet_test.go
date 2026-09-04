package controllers

// Security Auditor A — independent finding (2026-08-31 circle/cash round).
// FIXED same round: the dedup guard in cash_consolidate_controller.go is now
// keyed on (wallet, identity), not the wallet alone.
//
// NIP-CASH §Consolidating Tokens, `sources` field: "MUST contain at least two
// distinct, unredeemed source slices; the same slice MUST NOT appear twice."
// A "slice" is a (wallet, identity) pair (§Data Model / §Terminology) — NOT a
// wallet. Immediately after a multi-recipient mint_cash call, and before
// either recipient redeems/transfers/splits, TWO distinct slices can still
// share one wallet_pubkey/connection (§Data Model: "A wallet's total funding
// MUST equal the sum of its slices" — the plural "slices" on one wallet is
// the normal, expected shape of a freshly-minted multi-recipient wallet).
// §Consolidating Tokens also confirms authorization is per-source, not
// per-connection/per-wallet: "the calling connection's own identity need not
// match, or even be among, the sources being consolidated" — nothing in the
// spec ties one wallet_pubkey to at most one source.
//
// The guard used to key on the source wallet app alone, so a caller who
// legitimately controlled two different recipients' still-unclaimed slices
// on the SAME multi-recipient cash_wallet (e.g. named twice under two
// different pubkeys in one mint_cash call) could not consolidate them
// together in one call, even though each is a distinct, unredeemed,
// individually-proven slice exactly as NIP-CASH defines "sources". No funds
// were ever at risk from this — it was a false rejection / availability gap,
// not a fund-safety bug. (The 2026-08-29 consolidate round's
// independent-security-audit-consolidate-2026-08-29b.md reviewed the same
// line from a double-spend/custody-batching angle and correctly marked it
// "SAFE" on that axis — this finding was a different angle on the same line.)
//
// This file only ADDS regression tests; it does not itself change the guard.

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditSecA_Consolidate_TwoDistinctSlicesOnSameWallet_Succeeds proves the
// fix: two distinct, unredeemed, individually-proven slices sharing one
// wallet_pubkey (the normal shape of a freshly-minted multi-recipient Cash
// wallet) can now be consolidated together into one merged wallet.
//
// Amounts are 123_000/1_000 mloki, not an arbitrary split: cashwallet's own
// internal-transfer funding step (fundInternal) pays a real bolt11 invoice
// per source, and MockLn.MakeInvoice ignores its requested-amount argument
// entirely unless MakeInvoiceQueue is pre-populated (a pre-existing,
// documented mock limitation — see security-audit-scope-2026-08-30.md §7) —
// so, exactly like the pre-existing TestConsolidate_HappyPath_RecordsSourceLineage,
// this test queues two distinct canned invoices (tests.MockInvoice /
// tests.MockLNClientHoldTransaction.Invoice) whose amounts these claims must
// match, so each of the two sequential internal transfers out of the SAME
// source wallet pays a genuinely distinct payment_hash for the correct amount.
func TestAuditSecA_Consolidate_TwoDistinctSlicesOnSameWallet_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)

	// One multi-recipient cash_wallet, exactly as mint_cash would produce for
	// a 2-recipient request: two distinct slices, same WalletPubkey/app.
	sharedWallet := guardCashWallet(t, svc, hub)
	priv1 := nostr.GeneratePrivateKey()
	pub1, _ := nostr.GetPublicKey(priv1)
	priv2 := nostr.GeneratePrivateKey()
	pub2, _ := nostr.GetPublicKey(priv2)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(sharedWallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pub1, AmountMloki: 123_000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pub2, AmountMloki: 1_000},
	}))
	tests.FundApp(svc, sharedWallet.ID, 200_000, "shared-wallet-fund")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: int64(123_000)},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: int64(1_000)},
	}

	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// Two independently valid proofs, one per identity, both naming the SAME
	// source wallet_pubkey — exactly what a caller who controls both privkeys
	// (priv1, priv2) would legitimately submit per NIP-CASH's per-source
	// authorization model.
	proof1 := buildTransferProofEvent(t, priv1, *sharedWallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", 123_000, nil, time.Now())
	proof2 := buildTransferProofEvent(t, priv2, *sharedWallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", 1_000, nil, time.Now())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *sharedWallet.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: pub1, IdentityEvent: mustMarshal(t, proof1)},
			{WalletPubkey: *sharedWallet.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: pub2, IdentityEvent: mustMarshal(t, proof2)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})

	require.Nil(t, resp.Error, "a spec-conformant two-distinct-slices-one-wallet consolidate must now succeed")
	result, ok := resp.Result.(cashConsolidateResponse)
	require.True(t, ok)
	assert.Equal(t, uint64(124_000), result.AmountMillis, "the merged wallet must carry the full sum of both slices")

	// Both source slices are now claimed (spun off to the merged wallet), not
	// left dangling.
	claim1 := cashWalletClaimByIdentity(t, svc, sharedWallet.ID, db.CashIdentityPubkey, pub1)
	require.NotNil(t, claim1)
	assert.NotNil(t, claim1.ClaimedAt, "slice 1 must be claimed after a successful consolidate")
	claim2 := cashWalletClaimByIdentity(t, svc, sharedWallet.ID, db.CashIdentityPubkey, pub2)
	require.NotNil(t, claim2)
	assert.NotNil(t, claim2.ClaimedAt, "slice 2 must be claimed after a successful consolidate")
}

// TestAuditSecA_Consolidate_SameSliceTwice_StillRejected is the control: the
// guard the fix narrowed still correctly rejects the SAME (wallet, identity)
// slice named twice in one request — this is the actual "same slice MUST NOT
// appear twice" rule NIP-CASH requires, which the granularity fix must not
// weaken.
func TestAuditSecA_Consolidate_SameSliceTwice_StillRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)

	sharedWallet := guardCashWallet(t, svc, hub)
	priv1 := nostr.GeneratePrivateKey()
	pub1, _ := nostr.GetPublicKey(priv1)
	priv2 := nostr.GeneratePrivateKey()
	pub2, _ := nostr.GetPublicKey(priv2)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(sharedWallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pub1, AmountMloki: 60_000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pub2, AmountMloki: 40_000},
	}))
	tests.FundApp(svc, sharedWallet.ID, 100_000, "shared-wallet-fund")

	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof1a := buildTransferProofEvent(t, priv1, *sharedWallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", 60_000, nil, time.Now())
	proof1b := buildTransferProofEvent(t, priv1, *sharedWallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", 60_000, nil, time.Now())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *sharedWallet.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: pub1, IdentityEvent: mustMarshal(t, proof1a)},
			{WalletPubkey: *sharedWallet.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: pub1, IdentityEvent: mustMarshal(t, proof1b)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})

	require.NotNil(t, resp.Error, "the same (wallet, identity) slice named twice must still be rejected")
	assert.Contains(t, resp.Error.Message, "same source slice appears twice")

	claim1 := cashWalletClaimByIdentity(t, svc, sharedWallet.ID, db.CashIdentityPubkey, pub1)
	require.NotNil(t, claim1)
	assert.Nil(t, claim1.ClaimedAt, "slice must remain unclaimed after the rejected request")
	claim2 := cashWalletClaimByIdentity(t, svc, sharedWallet.ID, db.CashIdentityPubkey, pub2)
	require.NotNil(t, claim2)
	assert.Nil(t, claim2.ClaimedAt)
}
