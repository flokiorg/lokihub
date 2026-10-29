package cashwithdraw

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

func onchainTerms(t *testing.T, tiers ...uint32) string {
	t.Helper()
	k := &nipCO.KindTerms{MinMillis: 1000, MaxMillis: 1_000_000_000}
	for i, n := range tiers {
		tier := nipCO.Tier{N: n}
		if i < len(tiers)-1 {
			up := uint64(1_000_000 * (i + 1))
			tier.UpToMillis = &up
		}
		k.Confirmations = append(k.Confirmations, tier)
	}
	b, err := json.Marshal(nipCO.Terms{Onchain: k})
	require.NoError(t, err)
	return string(b)
}

// final_depth is max(6, the highest n of the hub's on-chain tiers), its current
// terms and those frozen on its on-chain orders still being paid.
func TestDBOrdersFinalDepth(t *testing.T) {
	w := newWorld(t)
	fd, err := w.s.FinalDepth(w.svc.DB, w.hub.ID)
	require.NoError(t, err)
	assert.Equal(t, MinFinalDepth, fd, "no order terms")

	require.NoError(t, w.svc.DB.Create(&db.CashOrderTerms{HubAppID: w.hub.ID, TermsJSON: onchainTerms(t, 3, 12), UpdatedAt: 1}).Error)
	fd, err = w.s.FinalDepth(w.svc.DB, w.hub.ID)
	require.NoError(t, err)
	assert.Equal(t, 12, fd)

	frozen, err := json.Marshal(map[string]any{"kind": nipCO.KindTerms{Confirmations: []nipCO.Tier{{N: 20}}}})
	require.NoError(t, err)
	order := db.CashOrder{RequestID: "r-frozen", HubAppID: w.hub.ID, OrderConnPubkey: "c", BuyerPubkey: "b",
		RecipientType: db.CashIdentityPubkey, RecipientValue: "v", AmountMillis: 1000, Payment: nipCO.PaymentOnchain,
		Notify: "n", TermsJSON: string(frozen), PayBy: 1, Deadline: 1, State: db.CashOrderOpen, CreatedAt: 1, StateAt: 1}
	require.NoError(t, w.svc.DB.Create(&order).Error)
	fd, err = w.s.FinalDepth(w.svc.DB, w.hub.ID)
	require.NoError(t, err)
	assert.Equal(t, 20, fd, "an open order keeps its frozen tiers")

	require.NoError(t, w.svc.DB.Model(&order).Update("state", db.CashOrderMinted).Error)
	fd, err = w.s.FinalDepth(w.svc.DB, w.hub.ID)
	require.NoError(t, err)
	assert.Equal(t, 12, fd)
}

// An order address in any state, burnt included, is own_address.
func TestDBOrdersOrderScript(t *testing.T) {
	w := newWorld(t)
	addr := address(t, 0, 20)
	require.NoError(t, w.svc.DB.Create(&db.CashOrderAddress{Address: addr, HubAppID: w.hub.ID, IssuedAt: 1, Burnt: true}).Error)
	own, err := DBOrders{}.IsOrderScript(w.svc.DB, scriptOf(t, addr))
	require.NoError(t, err)
	assert.True(t, own)
	own, err = DBOrders{}.IsOrderScript(w.svc.DB, scriptOf(t, address(t, 1, 32)))
	require.NoError(t, err)
	assert.False(t, own)
}

// An ordered slice is unfinal while one of its on-chain payments is not minted
// at final_depth confirmations.
func TestDBOrdersUnfinal(t *testing.T) {
	w := newWorld(t)
	hub := w.hub.ID
	p := db.CashOrderPayment{HubAppID: &hub, PaymentKey: "oc:aa:0", ValueMillis: 1000, SeenAt: 1,
		Confirmations: 3, State: db.CashOrderPaymentMinted}
	require.NoError(t, w.svc.DB.Create(&p).Error)
	ids := fmt.Sprintf("[%d]", p.ID)
	claim := &db.CashWalletClaim{Ordered: true, UnfinalPaymentIDs: &ids}

	unfinal, err := DBOrders{}.OrderedUnfinal(w.svc.DB, claim, 6)
	require.NoError(t, err)
	assert.True(t, unfinal, "3 of 6 confirmations")
	require.NoError(t, w.svc.DB.Model(&p).Update("confirmations", 6).Error)
	unfinal, err = DBOrders{}.OrderedUnfinal(w.svc.DB, claim, 6)
	require.NoError(t, err)
	assert.False(t, unfinal)
	require.NoError(t, w.svc.DB.Model(&p).Update("state", db.CashOrderPaymentReorged).Error)
	unfinal, err = DBOrders{}.OrderedUnfinal(w.svc.DB, claim, 6)
	require.NoError(t, err)
	assert.True(t, unfinal, "a reorged payment never reaches final_depth")
	unfinal, err = DBOrders{}.OrderedUnfinal(w.svc.DB, &db.CashWalletClaim{UnfinalPaymentIDs: &ids}, 6)
	require.NoError(t, err)
	assert.False(t, unfinal, "not ordered")
	gone := "[999999]"
	unfinal, err = DBOrders{}.OrderedUnfinal(w.svc.DB, &db.CashWalletClaim{Ordered: true, UnfinalPaymentIDs: &gone}, 6)
	require.NoError(t, err)
	assert.False(t, unfinal, "a payment row past its retention is long final")
}

// The withdraw call refuses an ordered slice whose payment is unfinal, through
// the slice's own columns.
func TestDBOrdersUnfinalRefusesWithdraw(t *testing.T) {
	w := newWorld(t)
	hub := w.hub.ID
	p := db.CashOrderPayment{HubAppID: &hub, PaymentKey: "oc:bb:0", ValueMillis: 1000, SeenAt: 1,
		Confirmations: 1, State: db.CashOrderPaymentMinted}
	require.NoError(t, w.svc.DB.Create(&p).Error)
	ids := fmt.Sprintf("[%d]", p.ID)
	bill, hs := w.mintPubkey(t, 21_000_000)
	require.NoError(t, w.svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ?", bill.ID).
		Updates(map[string]any{"ordered": true, "unfinal_payment_ids": ids}).Error)
	q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
	_, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil))
	requireCode(t, err, constants.ERROR_RESTRICTED, nipcash.DetailUnfinal)

	require.NoError(t, w.svc.DB.Model(&p).Update("confirmations", 6).Error)
	q = w.mustQuote(t, bill, hs[0], address(t, 0, 20))
	_, err = w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil))
	require.NoError(t, err)
	var wd db.CashWithdrawal
	require.NoError(t, w.svc.DB.Last(&wd).Error)
	assert.True(t, wd.Ordered)
	require.NotNil(t, wd.UnfinalPaymentIDs)
	assert.Equal(t, ids, *wd.UnfinalPaymentIDs)
}

// A re-minted bill keeps the source slice's ordered mark and order payments and
// is marked re-minted on its slice.
func TestRemintKeepsOrderMarks(t *testing.T) {
	w := newWorld(t)
	_, _, wd := w.committed(t, false)
	ids := "[7,9]"
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Where("id = ?", wd.ID).
		Updates(map[string]any{"ordered": true, "unfinal_payment_ids": ids}).Error)
	out, err := w.s.Remint(context.TODO(), wd.ID, "")
	require.NoError(t, err)
	var c db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", *out.RemintAppID).First(&c).Error)
	assert.True(t, c.Ordered)
	assert.True(t, c.Reminted)
	require.NotNil(t, c.UnfinalPaymentIDs)
	assert.Equal(t, ids, *c.UnfinalPaymentIDs)

	// Once its holder ends the slice, the guard lifts.
	require.NoError(t, w.svc.DB.Model(&c).Update("claimed_at", time.Now()).Error)
	ok, err := IsRemintedBill(w.svc.DB, *out.RemintAppID)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestMergePaymentIDs(t *testing.T) {
	a, b := "[3,1]", "[1,2]"
	got, err := db.MergePaymentIDs(&a, nil, &b)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "[1,2,3]", *got)
	got, err = db.MergePaymentIDs(nil, nil)
	require.NoError(t, err)
	assert.Nil(t, got)
	bad := "x"
	_, err = db.MergePaymentIDs(&bad)
	assert.Error(t, err)
}
