package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func strPtr(s string) *string { return &s }

// U-6.1: cash_transfer's step order (NIP-CASH §Transferring and Splitting a
// Slice, Processing Algorithm). Each row fails at two adjacent steps; the earlier
// one answers, nothing is claimed, and the id is used exactly when the call got
// past authorization.
func TestCashTransfer_StepOrder(t *testing.T) {
	type built struct {
		params cashTransferParams
		call   *PrivateCall
	}
	cases := []struct {
		name       string
		build      func(t *testing.T, f *transferFixture) built
		wantCode   string
		wantMsg    string
		wantMarked bool
	}{
		{
			name: "an empty zap_target beats a slice that is not there",
			build: func(t *testing.T, f *transferFixture) built {
				p := f.params(t, f.ownerPub, 5_000, false, testRequestID())
				p.IdentityValue = tests.RandomHex32()
				p.ZapTarget = strPtr("")
				return built{p, testPrivateCall(p.IdentityEvent)}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "zap_target",
		},
		{
			name: "no slice beats a proof that does not verify",
			build: func(t *testing.T, f *transferFixture) built {
				p := f.params(t, f.ownerPub, 5_000, false, testRequestID())
				p.IdentityValue = tests.RandomHex32()
				p.IdentityEvent = `{"kind":1}`
				return built{p, testPrivateCall("")}
			},
			wantCode: constants.ERROR_NOT_FOUND,
		},
		{
			name: "a proof by another key beats a malformed new_identity",
			build: func(t *testing.T, f *transferFixture) built {
				p := f.params(t, "not-hex", 5_000, false, testRequestID())
				other := nostr.GeneratePrivateKey()
				p.IdentityEvent = mustMarshal(t, buildTransferProofEvent(t, other, *f.wallet.WalletPubkey, db.CashIdentityPubkey, "not-hex", "", 5_000, nil, time.Now()))
				return built{p, testPrivateCall(p.IdentityEvent)}
			},
			wantCode: constants.ERROR_UNAUTHORIZED,
		},
		{
			name: "an item proof by another key than the claim proof's is UNAUTHORIZED",
			build: func(t *testing.T, f *transferFixture) built {
				to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				p := f.params(t, to, 5_000, false, testRequestID())
				call := testPrivateCall(p.IdentityEvent)
				call.ItemSigner, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return built{p, call}
			},
			wantCode: constants.ERROR_UNAUTHORIZED, wantMsg: "item proof",
		},
		{
			name: "a malformed new_identity beats a zero amount, after the mark",
			build: func(t *testing.T, f *transferFixture) built {
				return built{f.params(t, "not-hex", 0, true, testRequestID()), nil}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "identity_value", wantMarked: true,
		},
		{
			name: "an amount over the slice beats nothing else, after the mark",
			build: func(t *testing.T, f *transferFixture) built {
				to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return built{f.params(t, to, 5_001, true, testRequestID()), nil}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "exceeds", wantMarked: true,
		},
		{
			name: "remainder_commitment on an identity-bound source is refused",
			build: func(t *testing.T, f *transferFixture) built {
				to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				p := f.params(t, to, 2_000, true, testRequestID())
				p.RemainderCommitment = tests.RandomHex32()
				return built{p, nil}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "remainder_commitment", wantMarked: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTransferFixture(t, 5_000)
			b := tc.build(t, f)
			if b.call == nil {
				b.call = testPrivateCall(b.params.IdentityEvent)
			}
			resp := transferWithCall(t, f.svc, f.wallet, b.params, b.call)
			require.NotNil(t, resp.Error)
			assert.Equal(t, tc.wantCode, resp.Error.Code, resp.Error.Message)
			if tc.wantMsg != "" {
				assert.Contains(t, resp.Error.Message, tc.wantMsg)
			}
			assert.Equal(t, tc.wantMarked, isUsed(t, f.svc, b.params.RequestID))
			assert.Nil(t, cashWalletClaimByIdentity(t, f.svc, f.wallet.ID, db.CashIdentityPubkey, f.ownerPub).ClaimedAt)
		})
	}
}

// A full transfer to an identity that already holds an unclaimed slice on this
// wallet lands in one new wallet, so one wallet never holds two live slices for
// one identity.
func TestCashTransfer_FullToAnIdentityAlreadyHereSpinsOff(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	other, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, f.svc.AppsService.CreateCashWalletClaims(f.wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: other, AmountMloki: 1_000},
	}))
	p := f.params(t, other, 5_000, false, testRequestID())
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.Nil(t, resp.Error)
	result := resp.Result.(cashTransferResponse)
	assert.NotEmpty(t, result.NewWalletPubkey, "a spin-off into one new wallet")
	assert.Empty(t, result.RemainderWalletPubkey)
	assert.Nil(t, result.RemainingAmountMillis)
	source := cashWalletClaimByIdentity(t, f.svc, f.wallet.ID, db.CashIdentityPubkey, f.ownerPub)
	assert.NotNil(t, source.ClaimedAt, "the source is claimed terminal")
	kept := cashWalletClaimByIdentity(t, f.svc, f.wallet.ID, db.CashIdentityPubkey, other)
	assert.Nil(t, kept.ClaimedAt, "the identity's own slice here is untouched")
}

// A full transfer to an identity that once held a slice on this wallet, now
// redeemed, is in place (NIP-CASH §Which outcome a request produces: unconditional
// on the wallet's recipient history). Its old claimed row is history and must not
// block the reassignment.
func TestCashTransfer_FullToAnIdentityThatOnceHeldASliceIsInPlace(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	other, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	claimed := time.Now().Add(-time.Minute)
	require.NoError(t, f.svc.DB.Create(&db.CashWalletClaim{WalletAppID: f.wallet.ID, IdentityType: db.CashIdentityPubkey,
		IdentityValue: other, AmountMloki: 1_000, ClaimedAt: &claimed, PaymentHash: tests.RandomHex32(), Preimage: tests.RandomHex32(),
		EndedAs: db.CashSliceStatusRedeemed}).Error)

	p := f.params(t, other, 5_000, false, testRequestID())
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.Nil(t, resp.Error, "%+v", resp.Error)
	result := resp.Result.(cashTransferResponse)
	assert.Empty(t, result.NewWalletPubkey, "in place, not a spin-off")

	var rows []db.CashWalletClaim
	require.NoError(t, f.svc.DB.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ?",
		f.wallet.ID, db.CashIdentityPubkey, other).Order("id").Find(&rows).Error)
	require.Len(t, rows, 2)
	live := 0
	for _, r := range rows {
		if r.ClaimedAt == nil {
			live++
			assert.EqualValues(t, 5_000, r.AmountMloki, "the reassigned slice")
		}
	}
	assert.Equal(t, 1, live)
}

// A slice of the new identity whose payout is still in flight comes back if the
// payout fails, so a full transfer to it spins off rather than leave two live
// slices for one identity on the wallet.
func TestCashTransfer_FullToAnIdentityWithAPayoutInFlightSpinsOff(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	other, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	claimed := time.Now().Add(-time.Minute)
	require.NoError(t, f.svc.DB.Create(&db.CashWalletClaim{WalletAppID: f.wallet.ID, IdentityType: db.CashIdentityPubkey,
		IdentityValue: other, AmountMloki: 1_000, ClaimedAt: &claimed, PaymentHash: tests.RandomHex32(),
		EndedAs: db.CashSliceStatusRedeemed}).Error)

	p := f.params(t, other, 5_000, false, testRequestID())
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.Nil(t, resp.Error, "%+v", resp.Error)
	assert.NotEmpty(t, resp.Result.(cashTransferResponse).NewWalletPubkey, "a spin-off into one new wallet")
}

// A cash slice's partial split needs a fresh remainder_commitment: missing or
// equal to the source's is BAD_REQUEST.
func TestCashTransfer_CashPartialSplitRemainderCommitment(t *testing.T) {
	for _, tc := range []struct {
		name       string
		commitment func(source string) string
	}{
		{"missing", func(string) string { return "" }},
		{"malformed", func(string) string { return "XYZ" }},
		{"the source's own", func(source string) string { return source }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			hub := tests.CreateCashHub(t, svc, 100_000, 3600)
			wallet := newFundedCashWallet(t, svc, hub, 5_000)
			secret, commitment := cashSecretAndHash(t)
			require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
				{IdentityType: db.CashIdentityCash, IdentityValue: commitment, AmountMloki: 5_000},
			}))
			to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
			amount := uint64(2_000)
			resp := transferWithCall(t, svc, wallet, cashTransferParams{
				CashSecret: secret, AmountMillis: &amount, RequestID: testRequestID(),
				NewIdentity:         cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: to},
				RemainderCommitment: tc.commitment(commitment),
			}, testPrivateCall(""))
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
			assert.Contains(t, resp.Error.Message, "remainder_commitment")
			assert.Nil(t, cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityCash, commitment).ClaimedAt)
		})
	}
}

// A bill past its expiry is RESTRICTED, whatever the request.
func TestCashTransfer_ExpiredBillIsRestricted(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	past := time.Now().Add(-time.Minute)
	require.NoError(t, f.svc.DB.Model(&db.App{}).Where("id = ?", f.wallet.ID).Update("expires_at", past).Error)
	f.wallet.ExpiresAt = &past
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	p := f.params(t, to, 5_000, false, testRequestID())
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
	assert.False(t, isUsed(t, f.svc, p.RequestID))
}

// A claim proof is single-use: once consumed by a committed transfer, the same
// proof is UNAUTHORIZED.
func TestCashTransfer_UsedClaimProofIsUnauthorized(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	p := f.params(t, to, 2_000, true, testRequestID())
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.Nil(t, resp.Error)

	// The remainder is the owner's again, on a new bill; the same proof on the old
	// bill is refused before anything else, as already consumed or as naming a
	// slice no longer there.
	p.RequestID = testRequestID()
	resp = transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.NotNil(t, resp.Error)
	assert.Contains(t, []string{constants.ERROR_UNAUTHORIZED, constants.ERROR_RESTRICTED}, resp.Error.Code)
}
