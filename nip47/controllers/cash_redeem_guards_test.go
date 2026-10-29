package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// redeemWithItemSigner runs f's redeem as a private item signed by itemSigner.
func redeemWithItemSigner(t *testing.T, f *redeemFixture, itemSigner string) *models.Response {
	t.Helper()
	params := f.params(t)
	content, _ := json.Marshal(map[string]interface{}{"method": constants.NIP47MethodCashRedeem, "params": params})
	req := &models.Request{}
	require.NoError(t, json.Unmarshal(content, req))
	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, f.svc.DB.Create(ev).Error)
	call := testPrivateCall(params.IdentityEvent)
	call.ItemSigner = itemSigner
	var resp *models.Response
	NewTestNip47Controller(f.svc).HandleCashRedeemEvent(context.TODO(), req, ev.ID, f.wallet,
		func(r *models.Response, _ nostr.Tags) { resp = r }, nostr.Tags{}, call)
	require.NotNil(t, resp)
	return resp
}

// NIP-CASH §Claim Proofs (CASH-14): an item proof by any key other than the
// claim proof's signer is UNAUTHORIZED, so a captured claim proof cannot be
// re-sent in another sender's item.
func TestCashRedeem_ItemSignedByAnotherKeyIsUnauthorized(t *testing.T) {
	f := newRedeemFixture(t)
	other, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	resp := redeemWithItemSigner(t, f, other)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, resp.Error.Code)
	assert.Nil(t, f.slice(t).ClaimedAt, "the slice stays claimable")
	assert.Zero(t, f.consumedProofs(t))
}

func TestCashRedeem_ItemSignedByTheClaimSignerPays(t *testing.T) {
	f := newRedeemFixture(t)
	resp := redeemWithItemSigner(t, f, f.pub)
	require.Nil(t, resp.Error, "%+v", resp.Error)
	assert.NotNil(t, f.slice(t).ClaimedAt)
}

// NIP-CASH §Redeeming a Slice, step 1 (CASH-63): a slice past its wallet's
// expires_at is RESTRICTED and nothing is paid; one still inside it pays.
func TestCashRedeem_ExpiredBillIsRestricted(t *testing.T) {
	for _, c := range []struct {
		name    string
		offset  time.Duration
		refused bool
	}{
		{"a second past expires_at", -time.Second, true},
		{"a minute past expires_at", -time.Minute, true},
		{"expires_at a few seconds ahead", 3 * time.Second, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRedeemFixture(t)
			at := time.Now().Add(c.offset).Truncate(time.Second)
			require.NoError(t, f.svc.DB.Model(&db.App{}).Where("id = ?", f.wallet.ID).Update("expires_at", at).Error)
			f.wallet.ExpiresAt = &at
			resp := handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, f.params(t))
			if !c.refused {
				require.Nil(t, resp.Error, "%+v", resp.Error)
				return
			}
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
			assert.Nil(t, f.slice(t).ClaimedAt)
		})
	}
}

// NIP-CASH §Redeeming a Slice, step 4: a slice pays exactly once; a second
// redeem of a paid slice is RESTRICTED.
func TestCashRedeem_SecondRedeemAfterPayoutIsRestricted(t *testing.T) {
	f := newRedeemFixture(t)
	controller := NewTestNip47Controller(f.svc)
	first := handleClaimFundsFor(t, f.svc, controller, f.wallet, f.params(t))
	require.Nil(t, first.Error, "%+v", first.Error)
	again := handleClaimFundsFor(t, f.svc, controller, f.wallet, f.params(t))
	require.NotNil(t, again.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, again.Error.Code)
}
