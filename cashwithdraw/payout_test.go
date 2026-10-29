package cashwithdraw

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// committed is one committed payout of a two-slice bill (the other slice keeps
// the bill alive) or, with drain, of a one-slice bill that is then deleted.
func (w *world) committed(t *testing.T, drain bool) (*db.App, holder, *db.CashWithdrawal) {
	t.Helper()
	amounts := []uint64{21_000_000, 3_000_000}
	if drain {
		amounts = amounts[:1]
	}
	bill, hs := w.mintPubkey(t, amounts...)
	q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
	rid := newRID()
	_, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, rid, nil))
	require.NoError(t, err)
	var wd db.CashWithdrawal
	require.NoError(t, w.svc.DB.Where("request_id = ?", rid).First(&wd).Error)
	return bill, hs[0], &wd
}

func (w *world) setPayout(t *testing.T, wd *db.CashWithdrawal, updates map[string]any) *db.CashWithdrawal {
	t.Helper()
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Where("id = ?", wd.ID).Updates(updates).Error)
	var out db.CashWithdrawal
	require.NoError(t, w.svc.DB.First(&out, wd.ID).Error)
	return &out
}

// U-23.13: the payout per state: tx from the first signature, txid/vout/depth
// from the first broadcast with depth capped at final_depth, none of them under
// cancelled.
func TestU_23_13_PayoutView(t *testing.T) {
	w := newWorld(t)
	_, _, wd := w.committed(t, false)
	view := func(wd *db.CashWithdrawal) *nipcash.Payout {
		p, _, err := w.s.PayoutView(w.svc.DB, wd)
		require.NoError(t, err)
		b, err := json.Marshal(p)
		require.NoError(t, err)
		if p.State != nipcash.PayoutCancelled {
			_, err = nipcash.ParsePayout(b)
			require.NoError(t, err, string(b))
		}
		return p
	}
	p := view(wd)
	assert.Equal(t, nipcash.PayoutQueued, p.State)
	assert.False(t, p.Signed)

	wd = w.setPayout(t, wd, map[string]any{"signed": true})
	p = view(wd)
	assert.True(t, p.Signed)
	assert.Equal(t, nipcash.PayoutQueued, p.State)

	txid := strings.Repeat("ab", 32)
	wd = w.setPayout(t, wd, map[string]any{"state": db.CashWithdrawBroadcast, "txid": txid, "vout": 1, "depth": 0})
	p = view(wd)
	assert.Equal(t, nipcash.PayoutBroadcast, p.State)
	assert.Equal(t, txid, p.Txid)
	require.NotNil(t, p.Vout)
	assert.Equal(t, uint32(1), *p.Vout)
	assert.Equal(t, uint32(0), *p.Depth)

	wd = w.setPayout(t, wd, map[string]any{"state": db.CashWithdrawConfirmed, "depth": 3})
	assert.Equal(t, nipcash.PayoutConfirmed, view(wd).State)

	wd = w.setPayout(t, wd, map[string]any{"state": db.CashWithdrawFinal, "depth": 9, "final_reached": true})
	p = view(wd)
	assert.Equal(t, nipcash.PayoutFinal, p.State)
	assert.Equal(t, uint32(6), *p.Depth, "capped at final_depth")

	// final is sticky through a reorg back to depth 0.
	wd = w.setPayout(t, wd, map[string]any{"depth": 0})
	assert.Equal(t, nipcash.PayoutFinal, view(wd).State)
}

// U-23.14: the cancel request: RESTRICTED before sent_by + 604800 (K-WCAN-4),
// an invalidation scheduled at it for a broadcast payout (K-WCAN-5), none for a
// confirmed one (K-WCAN-6); a repeat writes nothing new; it marks no id.
func TestU_23_14_CancelRequest(t *testing.T) {
	w := newWorld(t)
	_, _, wd := w.committed(t, false)
	_, _, wd2 := w.committed(t, false)
	wd = w.setPayout(t, wd, map[string]any{"state": db.CashWithdrawBroadcast, "txid": strings.Repeat("cd", 32), "vout": 0, "signed": true})
	used := w.snapshot(t).used

	w.mu.Lock()
	w.now = time.Unix(wd.SentBy+604799, 0)
	w.mu.Unlock()
	_, err := w.s.CancelRequest(wd)
	requireCode(t, err, constants.ERROR_RESTRICTED, "")

	w.advance(time.Second)
	got, err := w.s.CancelRequest(wd)
	require.NoError(t, err)
	require.NotNil(t, got.CancelRequestedAt)
	assert.Equal(t, wd.SentBy+604800, *got.CancelRequestedAt)
	assert.Equal(t, db.CashWithdrawCancelRequest, *got.CancelReason)
	assert.Equal(t, db.CashWithdrawBroadcast, got.State, "broadcast until the invalidation is final")
	assert.Equal(t, 3, w.funds.signalled)

	w.advance(time.Hour)
	again, err := w.s.CancelRequest(got)
	require.NoError(t, err)
	assert.Equal(t, *got.CancelRequestedAt, *again.CancelRequestedAt, "a repeat changes nothing")
	assert.Equal(t, 3, w.funds.signalled)
	assert.Equal(t, used, w.snapshot(t).used, "a cancel request marks no id")

	// confirmed: answered, nothing scheduled.
	wd2 = w.setPayout(t, wd2, map[string]any{"state": db.CashWithdrawConfirmed, "txid": strings.Repeat("ef", 32), "vout": 0, "depth": 1})
	w.mu.Lock()
	w.now = time.Unix(wd2.SentBy+604800, 0)
	w.mu.Unlock()
	got2, err := w.s.CancelRequest(wd2)
	require.NoError(t, err)
	assert.Nil(t, got2.CancelRequestedAt)
}

// U-23.15: the re-mint binds the slice's identity, its terms and RemintExpiry;
// it is funded from the hold, never from the source; the source becomes
// transferred; it runs once.
func TestU_23_15_RemintPubkey(t *testing.T) {
	w := newWorld(t)
	bill, h, wd := w.committed(t, false)
	billBal := balance(w, bill.ID)
	hubBal := balance(w, w.hub.ID)

	out, err := w.s.Remint(context.TODO(), wd.ID, db.CashWithdrawCancelNoRecord)
	require.NoError(t, err)
	assert.Equal(t, db.CashWithdrawCancelled, out.State)
	assert.Zero(t, out.HeldMillis)
	require.NotNil(t, out.RemintAppID)
	require.NotNil(t, out.NoticeDueAt)
	delay := *out.NoticeDueAt - *out.CancelledAt
	assert.True(t, delay >= 600 && delay < 1200, "notice delay %d", delay)

	assert.Equal(t, billBal, balance(w, bill.ID), "the source is untouched")
	assert.Equal(t, hubBal, balance(w, w.hub.ID))
	assert.Equal(t, int64(21_000_000), balance(w, *out.RemintAppID), "funded from the hold")

	var claims []db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", *out.RemintAppID).Find(&claims).Error)
	require.Len(t, claims, 1)
	assert.Equal(t, db.CashIdentityPubkey, claims[0].IdentityType)
	assert.Equal(t, h.pub, claims[0].IdentityValue)
	var src db.CashWalletClaim
	require.NoError(t, w.svc.DB.First(&src, wd.ClaimID).Error)
	assert.Equal(t, db.CashSliceStatusTransferred, src.EndedAs)

	var app db.App
	require.NoError(t, w.svc.DB.First(&app, *out.RemintAppID).Error)
	assert.Nil(t, app.ExpiresAt, "a slice that never expired gives a bill that never expires")
	reminted, err := IsRemintedBill(w.svc.DB, app.ID)
	require.NoError(t, err)
	assert.True(t, reminted)
	assert.NotEmpty(t, app.CashToken)
	tok, err := nipcash.Decode(app.CashToken)
	require.NoError(t, err)
	assert.Equal(t, uint64(21_000_000), *tok.AttestedAmountMillis)

	// The view carries the new bill and no transaction.
	p, rb, err := w.s.PayoutView(w.svc.DB, out)
	require.NoError(t, err)
	assert.Equal(t, nipcash.PayoutCancelled, p.State)
	assert.Equal(t, *app.WalletPubkey, p.NewWalletPubkey)
	assert.Empty(t, p.Txid)
	require.NotNil(t, rb)
	assert.Equal(t, app.ID, rb.WalletAppID)

	// Once.
	_, err = w.s.Remint(context.TODO(), wd.ID, "")
	assert.ErrorIs(t, err, ErrAlreadyReminted)
	var n int64
	require.NoError(t, w.svc.DB.Model(&db.Transaction{}).Where("app_id = ?", *out.RemintAppID).Count(&n).Error)
	assert.Equal(t, int64(1), n)
}

// U-23.15 (K-WREM): RemintExpiry with an expiring slice and the hub's default.
func TestRemintExpiry(t *testing.T) {
	w := newWorld(t)
	require.NoError(t, w.svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", w.hub.ID).Update("max_exp_secs", 3600).Error)
	_, _, wd := w.committed(t, false)
	sliceExp := w.clock().Unix() + 100
	wd = w.setPayout(t, wd, map[string]any{"slice_expires_at": sliceExp})
	out, err := w.s.Remint(context.TODO(), wd.ID, "")
	require.NoError(t, err)
	var app db.App
	require.NoError(t, w.svc.DB.First(&app, *out.RemintAppID).Error)
	require.NotNil(t, app.ExpiresAt)
	assert.Equal(t, max(sliceExp, *out.CancelledAt+3600), app.ExpiresAt.Unix())
}

// U-23.15: a cash-mode slice is re-minted to remint_commitment, never the old
// one; its archived source and the bill's reason become transferred.
func TestU_23_15_RemintCashAndArchive(t *testing.T) {
	w := newWorld(t)
	bill, secret := w.mintCash(t, 21_000_000)
	res, err := w.s.Quote(context.TODO(), bill, Caller{CashMode: true, LimitKey: "e"}, raw(t, map[string]any{"address": address(t, 0, 20)}))
	require.NoError(t, err)
	q, err := nipcash.ParseWithdrawQuote(res.Quote)
	require.NoError(t, err)
	target, err := nipcash.NewCashTarget()
	require.NoError(t, err)
	_, err = w.s.Withdraw(context.TODO(), bill, Caller{CashMode: true}, w.meta(), request(t, bill, nipcash.BySecret(secret), q, newRID(), target), nil)
	require.NoError(t, err)

	var archive db.CashBillArchive
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", bill.ID).First(&archive).Error)
	require.NotNil(t, archive.Reason)
	assert.Equal(t, "redeemed", *archive.Reason)

	var wd db.CashWithdrawal
	require.NoError(t, w.svc.DB.First(&wd).Error)
	out, err := w.s.Remint(context.TODO(), wd.ID, "")
	require.NoError(t, err)
	var c db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", *out.RemintAppID).First(&c).Error)
	assert.Equal(t, db.CashIdentityCash, c.IdentityType)
	assert.Equal(t, target.Commitment(), c.IdentityValue)
	assert.NotEqual(t, wd.IdentityValue, c.IdentityValue, "never the old commitment")

	var slice db.CashBillSliceArchive
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", bill.ID).First(&slice).Error)
	assert.Equal(t, db.CashSliceStatusTransferred, slice.Outcome)
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", bill.ID).First(&archive).Error)
	assert.Equal(t, "transferred", *archive.Reason)

	// The view's re-minted bill is in the clear for cash: no delivery key.
	assert.Nil(t, wd.DeliveryPubkey)
}

// The archive's reason is rewritten only when the withdrawn slice gave it.
func TestRemintKeepsAnotherSlicesReason(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000, 3_000_000)
	q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
	rid := newRID()
	_, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, rid, nil))
	require.NoError(t, err)
	// The other slice ends later (redeemed by Lightning, say) and drains the bill.
	later := time.Now().Add(time.Minute)
	require.NoError(t, w.svc.DB.Model(&db.CashWalletClaim{}).Where("identity_value = ?", hs[1].pub).
		Updates(map[string]any{"claimed_at": later, "ended_as": db.CashSliceStatusRedeemed}).Error)
	require.NoError(t, w.svc.DB.Create(&db.Transaction{AppId: &bill.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
		State: constants.TRANSACTION_STATE_SETTLED, AmountMloki: 3_000_000, PaymentHash: newRID()}).Error)
	deleted, err := cashwallet.AutoDeleteDrained(w.svc.DB, w.svc.EventPublisher, bill.ID)
	require.NoError(t, err)
	require.True(t, deleted)

	var wd db.CashWithdrawal
	require.NoError(t, w.svc.DB.Where("request_id = ?", rid).First(&wd).Error)
	_, err = w.s.Remint(context.TODO(), wd.ID, "")
	require.NoError(t, err)
	var archive db.CashBillArchive
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", bill.ID).First(&archive).Error)
	assert.Equal(t, "redeemed", *archive.Reason, "the other slice gave the reason")
	var slice db.CashBillSliceArchive
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ? AND claim_id = ?", bill.ID, wd.ClaimID).First(&slice).Error)
	assert.Equal(t, db.CashSliceStatusTransferred, slice.Outcome)
}

// U-4.12: a fault at every re-mint stage leaves the payout not cancelled, its
// hold intact and no new app; the next attempt re-mints once.
func TestU_4_12_RemintFaults(t *testing.T) {
	for _, stage := range RemintStages {
		for _, mode := range []string{"error", "panic"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				w := newWorld(t)
				_, _, wd := w.committed(t, false)
				w.s.Hooks = &TestHooks{Before: func(s string) error {
					if s != "Remint:"+stage {
						return nil
					}
					if mode == "panic" {
						panic("crash")
					}
					return errors.New("fault")
				}}
				apps := w.snapshot(t).apps
				func() {
					defer func() { _ = recover() }()
					_, err := w.s.Remint(context.TODO(), wd.ID, "")
					require.Error(t, err)
				}()
				var cur db.CashWithdrawal
				require.NoError(t, w.svc.DB.First(&cur, wd.ID).Error)
				assert.NotEqual(t, db.CashWithdrawCancelled, cur.State)
				assert.Equal(t, cur.AmountMillis, cur.HeldMillis)
				assert.Equal(t, apps, w.snapshot(t).apps)
				w.s.Hooks = nil
				out, err := w.s.Remint(context.TODO(), wd.ID, "")
				require.NoError(t, err)
				assert.Equal(t, db.CashWithdrawCancelled, out.State)
				assert.Equal(t, apps+1, w.snapshot(t).apps)
			})
		}
	}
}

// U-23.17 / K-WRET: the record is kept while the payout is open, while a
// re-minted bill lives, and until max(deadline + H, end + tombstone window);
// dropping it deletes the record and the payout and keeps the archive facts.
func TestU_23_17_Retention(t *testing.T) {
	w := newWorld(t)
	require.NoError(t, w.svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", w.hub.ID).Update("spent_retention_secs", 7200).Error)
	_, _, wd := w.committed(t, false)
	_, _, wd2 := w.committed(t, false)
	var rec db.CashRequestRecord
	require.NoError(t, w.svc.DB.First(&rec, wd.RecordID).Error)

	// Open: kept whatever the time.
	w.advance(30 * 24 * time.Hour)
	n, err := w.s.PruneRecords(10)
	require.NoError(t, err)
	assert.Zero(t, n)

	end := w.clock().Unix()
	w.setPayout(t, wd, map[string]any{"state": db.CashWithdrawFinal, "final_at": end, "final_reached": true, "held_millis": 0})
	w.mu.Lock()
	w.now = time.Unix(end+7200, 0)
	w.mu.Unlock()
	n, err = w.s.PruneRecords(10)
	require.NoError(t, err)
	assert.Zero(t, n, "kept through end + window")

	events := w.snapshot(t).events
	w.advance(time.Second)
	n, err = w.s.PruneRecords(10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	var left int64
	require.NoError(t, w.svc.DB.Model(&db.CashRequestRecord{}).Where("id = ?", rec.ID).Count(&left).Error)
	assert.Zero(t, left)
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Where("id = ?", wd.ID).Count(&left).Error)
	assert.Zero(t, left)
	assert.Equal(t, events, w.snapshot(t).events, "archive facts stay")

	// A re-minted bill that lives keeps the record.
	out, err := w.s.Remint(context.TODO(), wd2.ID, "")
	require.NoError(t, err)
	w.advance(400 * 24 * time.Hour)
	n, err = w.s.PruneRecords(10)
	require.NoError(t, err)
	assert.Zero(t, n)
	blocks, err := w.s.BlocksHubDelete(w.hub.ID)
	require.NoError(t, err)
	assert.True(t, blocks)
	require.NoError(t, w.svc.DB.Delete(&db.App{}, *out.RemintAppID).Error)
	n, err = w.s.PruneRecords(10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// A re-minted bill cannot be deleted by its owner.
func TestRemintedBillGuard(t *testing.T) {
	w := newWorld(t)
	_, _, wd := w.committed(t, false)
	out, err := w.s.Remint(context.TODO(), wd.ID, "")
	require.NoError(t, err)
	ok, err := IsRemintedBill(w.svc.DB, *out.RemintAppID)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = IsRemintedBill(w.svc.DB, wd.BillAppID)
	require.NoError(t, err)
	assert.False(t, ok)
}

// U-23.16 (service half): the withdraw object only while offered, min and max
// omitted when unset; withdraw_request_id only for the authorizer.
func TestU_23_16_StatusTermsAndRequestID(t *testing.T) {
	w := newWorld(t)
	st := w.s.StatusTerms(w.hub.ID)
	require.NotNil(t, st)
	require.NoError(t, st.Validate())
	b, _ := json.Marshal(st)
	assert.Contains(t, string(b), `"min_millis"`)
	_, err := w.s.PutTerms(w.hub.ID, TermsInput{Enabled: true, FeeBaseMillis: 1})
	require.NoError(t, err)
	b, _ = json.Marshal(w.s.StatusTerms(w.hub.ID))
	assert.NotContains(t, string(b), "min_millis")
	assert.NotContains(t, string(b), "max_millis")
	assert.Contains(t, string(b), `"script_types":[[0,20],[0,32],[1,32]]`)

	bill, h, wd := w.committed(t, false)
	assert.Equal(t, wd.RequestID, RequestIDForAuthorizer(w.svc.DB, *bill.WalletPubkey, h.pub, wd.ClaimID))
	stranger, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	assert.Empty(t, RequestIDForAuthorizer(w.svc.DB, *bill.WalletPubkey, stranger, wd.ClaimID))
	// Gone with the record.
	require.NoError(t, w.svc.DB.Delete(&db.CashRequestRecord{}, wd.RecordID).Error)
	assert.Empty(t, RequestIDForAuthorizer(w.svc.DB, *bill.WalletPubkey, h.pub, wd.ClaimID))
}
