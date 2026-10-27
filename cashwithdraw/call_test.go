package cashwithdraw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// U-23.11 / U-23.12: the commit ends the slice, debits the bill by exactly its
// amount, holds it, sets coins aside, consumes the proof, writes the record and
// a receipt signed inside the transaction; the reply is queued, unsigned, and
// its receipt verifies; a drained bill is deleted after the commit.
func TestU_23_11_CommitAndReply(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000)
	addr := address(t, 0, 20)
	q := w.mustQuote(t, bill, hs[0], addr)
	rid := newRID()
	before := w.snapshot(t)
	billBefore := balance(w, bill.ID)

	p, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, rid, nil))
	require.NoError(t, err)
	assert.Equal(t, nipcash.PayoutQueued, p.State)
	assert.False(t, p.Signed)
	assert.Equal(t, addr, p.Address)
	assert.Equal(t, q.PayoutMillis, p.PayoutMillis)
	assert.Equal(t, q.AmountMillis-q.PayoutMillis, p.FeeMillis)
	assert.Equal(t, uint32(6), p.FinalDepth)
	assert.Empty(t, p.Txid)
	assert.Nil(t, p.Vout)
	xonly, _ := w.node.XOnly(context.TODO())
	require.NoError(t, nipcash.VerifyWithdrawReceipt(p.Receipt, nipcash.ReceiptExpectation{HubXOnly: xonly,
		Target: *bill.WalletPubkey, QuoteID: q.ID, RequestID: rid, Address: addr, PayoutMillis: q.PayoutMillis, SentBy: q.SentBy}))
	body, err := json.Marshal(p)
	require.NoError(t, err)
	_, err = nipcash.ParsePayout(body)
	require.NoError(t, err)

	var wd db.CashWithdrawal
	require.NoError(t, w.svc.DB.Where("request_id = ?", rid).First(&wd).Error)
	assert.Equal(t, int64(21_000_000), wd.HeldMillis)
	assert.Equal(t, db.CashWithdrawQueued, wd.State)
	assert.Equal(t, hs[0].pub, *wd.DeliveryPubkey)
	var rec db.CashRequestRecord
	require.NoError(t, w.svc.DB.Where("request_id = ?", rid).First(&rec).Error)
	assert.Equal(t, wd.ID, *rec.WithdrawalID)
	assert.Equal(t, rec.ID, wd.RecordID)
	assert.Equal(t, hs[0].pub, rec.Authorizer)
	assert.Nil(t, rec.DropAfter)

	after := w.snapshot(t)
	assert.Equal(t, before.withdrawals+1, after.withdrawals)
	assert.Equal(t, before.setAsides+1, after.setAsides)
	assert.Equal(t, before.proofs+1, after.proofs)
	assert.Equal(t, before.used+1, after.used)
	assert.Equal(t, before.events+1, after.events)
	assert.Equal(t, 1, w.funds.signalled)

	// The bill paid exactly the slice amount, and held nothing else: it is
	// drained and was archived and deleted after the commit.
	assert.Equal(t, int64(21_000_000), billBefore)
	var live int64
	require.NoError(t, w.svc.DB.Model(&db.App{}).Where("id = ?", bill.ID).Count(&live).Error)
	assert.Zero(t, live, "a drained bill is deleted")
	var slice db.CashBillSliceArchive
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", bill.ID).First(&slice).Error)
	assert.Equal(t, db.CashSliceStatusRedeemed, slice.Outcome)
}

// A bill with two slices keeps the other, and its balance is debited by the
// withdrawn slice's amount exactly.
func TestCommitDebitsExactlyTheSlice(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000, 5_000_000)
	q := w.mustQuote(t, bill, hs[0], address(t, 1, 32))
	_, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil))
	require.NoError(t, err)
	assert.Equal(t, int64(5_000_000), balance(w, bill.ID))
	var c db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("identity_value = ?", hs[0].pub).First(&c).Error)
	assert.Equal(t, db.CashSliceStatusRedeemed, c.EndedAs)
}

// U-23.8: the BAD_REQUEST list before step 1 leaves the request_id unused.
func TestU_23_8_PreStep1(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000)
	q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
	base := func() map[string]any {
		var m map[string]any
		require.NoError(t, json.Unmarshal(request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil), &m))
		return m
	}
	mutations := map[string]func(m map[string]any){
		"no quote":           func(m map[string]any) { delete(m, "quote") },
		"malformed quote":    func(m map[string]any) { m["quote"] = "{}" },
		"no notify":          func(m map[string]any) { delete(m, "notify") },
		"uppercase notify":   func(m map[string]any) { m["notify"] = strings.ToUpper(m["notify"].(string)) },
		"invoice":            func(m map[string]any) { m["invoice"] = "lnbc1" },
		"amount":             func(m map[string]any) { m["amount"] = 1 },
		"idempotency_key":    func(m map[string]any) { m["idempotency_key"] = "x" },
		"remint on pubkey":   func(m map[string]any) { m["remint_commitment"] = strings.Repeat("ab", 32) },
		"caller fee":         func(m map[string]any) { m["fee_millis"] = 0 },
		"caller payout":      func(m map[string]any) { m["payout_millis"] = 0 },
		"caller network fee": func(m map[string]any) { m["network_fee_millis"] = 0 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := base()
			mutate(m)
			before := w.snapshot(t)
			_, err := w.withdraw(t, bill, hs[0], raw(t, m))
			requireCode(t, err, constants.ERROR_BAD_REQUEST, "")
			assert.Equal(t, before, w.snapshot(t), "the request_id stays unused")
		})
	}
}

// U-23.9: the claim proof binds m and q; another quote's q or another signer's
// item proof is UNAUTHORIZED; the id is used only after step 2.
func TestU_23_9_Authorize(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000, 3_000_000)
	q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
	other := w.mustQuote(t, bill, hs[0], address(t, 0, 32))

	// The claim proof is bound to other, the request carries q.
	var m map[string]any
	require.NoError(t, json.Unmarshal(request(t, bill, nipcash.BySigning(hs[0].priv), other, newRID(), nil), &m))
	m["quote"], _ = q.Raw()
	used := w.snapshot(t).used
	_, err := w.withdraw(t, bill, hs[0], raw(t, m))
	requireCode(t, err, constants.ERROR_UNAUTHORIZED, "")
	assert.Equal(t, used, w.snapshot(t).used, "no mark before authorization")

	// The item proof by another key.
	_, err = w.withdraw(t, bill, hs[1], request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil))
	requireCode(t, err, constants.ERROR_UNAUTHORIZED, "")

	// Unknown slice.
	stranger := newHolder()
	_, err = w.withdraw(t, bill, stranger, request(t, bill, nipcash.BySigning(stranger.priv), q, newRID(), nil))
	requireCode(t, err, constants.ERROR_NOT_FOUND, "")

	// A repeated request_id is REQUEST_REUSED at the mark.
	rid := newRID()
	require.NoError(t, cashrequest.New(w.svc.DB).MarkUsed(rid, "cash_transfer"))
	_, err = w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, rid, nil))
	requireCode(t, err, constants.ERROR_REQUEST_REUSED, "")
}

// U-23.10: each step-3 row refuses with its code and detail, in order, and
// leaves the slice claimable and the proof unconsumed; the id is used.
func TestU_23_10_PayoutChecks(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000, 7_000_000)
	addr := address(t, 0, 20)
	check := func(t *testing.T, params json.RawMessage, wantCode string, detail nipcash.Detail) {
		t.Helper()
		before := w.snapshot(t)
		_, err := w.withdraw(t, bill, hs[0], params)
		requireCode(t, err, wantCode, detail)
		after := w.snapshot(t)
		assert.Equal(t, before.used+1, after.used, "the id is used")
		before.used = after.used
		assert.Equal(t, before, after, "nothing else changes")
	}
	cred := nipcash.BySigning(hs[0].priv)

	t.Run("signed by another key", func(t *testing.T) {
		q := w.mustQuote(t, bill, hs[0], addr)
		forged := *q.Event
		require.NoError(t, forged.Sign(nostr.GeneratePrivateKey()))
		fq, err := nipcash.ParseWithdrawQuote(mustJSON(t, &forged))
		require.NoError(t, err)
		check(t, request(t, bill, cred, fq, newRID(), nil), constants.ERROR_RESTRICTED, nipcash.DetailQuoteInvalid)
	})
	t.Run("for another slice", func(t *testing.T) {
		q := w.mustQuote(t, bill, hs[1], addr)
		// hs[0] signs a claim on its own slice with hs[1]'s quote.
		check(t, request(t, bill, cred, q, newRID(), nil), constants.ERROR_RESTRICTED, nipcash.DetailQuoteInvalid)
	})
	t.Run("expired", func(t *testing.T) {
		q := w.mustQuote(t, bill, hs[0], addr)
		w.node.setTip(q.HoldsUntil + 1)
		defer w.node.setTip(1000)
		check(t, request(t, bill, cred, q, newRID(), nil), constants.ERROR_RESTRICTED, nipcash.DetailQuoteExpired)
	})
	t.Run("valid at holds_until", func(t *testing.T) {
		// K-WHOLD-1: tip = holds_until still holds (checked through QuoteValidAt).
		assert.True(t, nipcash.QuoteValidAt(1002, 1002))
		assert.False(t, nipcash.QuoteValidAt(1002, 1003))
	})
	t.Run("bound", func(t *testing.T) {
		q := w.mustQuote(t, bill, hs[0], addr)
		w.funds.atBound, w.funds.noCoins = true, true
		defer func() { w.funds.atBound, w.funds.noCoins = false, false }()
		check(t, request(t, bill, cred, q, newRID(), nil), constants.ERROR_RATE_LIMITED, "")
	})
	t.Run("coins", func(t *testing.T) {
		q := w.mustQuote(t, bill, hs[0], addr)
		w.funds.noCoins = true
		defer func() { w.funds.noCoins = false }()
		check(t, request(t, bill, cred, q, newRID(), nil), constants.ERROR_INSUFFICIENT_BALANCE, "")
	})
	t.Run("unfinal", func(t *testing.T) {
		q := w.mustQuote(t, bill, hs[0], addr)
		w.s.Orders = unfinalOrders{}
		defer func() { w.s.Orders = nil }()
		check(t, request(t, bill, cred, q, newRID(), nil), constants.ERROR_RESTRICTED, nipcash.DetailUnfinal)
	})
	t.Run("own wallet address, checked last", func(t *testing.T) {
		own := address(t, 1, 32)
		q := w.mustQuote(t, bill, hs[0], own)
		w.payer.mu.Lock()
		w.payer.scripts = [][]byte{scriptOf(t, own)}
		w.payer.mu.Unlock()
		check(t, request(t, bill, cred, q, newRID(), nil), constants.ERROR_RESTRICTED, nipcash.DetailOwnAddress)
	})
	t.Run("order address", func(t *testing.T) {
		own := address(t, 0, 32)
		q := w.mustQuote(t, bill, hs[0], own)
		w.s.Orders = orderAddress{script: scriptOf(t, own)}
		defer func() { w.s.Orders = nil }()
		check(t, request(t, bill, cred, q, newRID(), nil), constants.ERROR_RESTRICTED, nipcash.DetailOwnAddress)
	})
	t.Run("own address loses to an earlier row", func(t *testing.T) {
		own := address(t, 1, 32)
		q := w.mustQuote(t, bill, hs[0], own)
		w.funds.noCoins = true
		defer func() { w.funds.noCoins = false }()
		check(t, request(t, bill, cred, q, newRID(), nil), constants.ERROR_INSUFFICIENT_BALANCE, "")
	})

	// After all of these the slice is still claimable.
	q := w.mustQuote(t, bill, hs[0], addr)
	_, err := w.withdraw(t, bill, hs[0], request(t, bill, cred, q, newRID(), nil))
	require.NoError(t, err)
}

// nmilat review H3, CT-WD-27: the quote is checked against the slice's current
// amount, not just its identity. A slice is quoted; its holder moves it in
// place to another key and the bill's other, smaller slice moves in place to
// the holder's key: the old quote names the same bill and identity, but
// another amount, and is quote_invalid. The smaller slice stays claimable.
func TestH3_StaleQuoteAgainstASmallerSlice(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 50_000_000, 2_000_000)
	pa, pb := hs[0], hs[1]
	addr := address(t, 0, 20)
	stale := w.mustQuote(t, bill, pa, addr)
	assert.Equal(t, uint64(50_000_000), stale.AmountMillis)

	var a, b db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("identity_value = ?", pa.pub).First(&a).Error)
	require.NoError(t, w.svc.DB.Where("identity_value = ?", pb.pub).First(&b).Error)
	alt := newHolder()
	require.NoError(t, w.svc.DB.Transaction(func(tx *gorm.DB) error {
		if err := apps.ReassignCashSliceIdentityTx(tx, apps.SliceRef{ClaimID: a.ID, IdentityType: a.IdentityType, IdentityValue: a.IdentityValue, TransferCount: a.TransferCount}, db.CashIdentityPubkey, alt.pub, ""); err != nil {
			return err
		}
		return apps.ReassignCashSliceIdentityTx(tx, apps.SliceRef{ClaimID: b.ID, IdentityType: b.IdentityType, IdentityValue: b.IdentityValue, TransferCount: b.TransferCount}, db.CashIdentityPubkey, pa.pub, "")
	}))

	before := w.snapshot(t)
	_, err := w.withdraw(t, bill, pa, request(t, bill, nipcash.BySigning(pa.priv), stale, newRID(), nil))
	requireCode(t, err, constants.ERROR_RESTRICTED, nipcash.DetailQuoteInvalid)
	after := w.snapshot(t)
	assert.Equal(t, before.withdrawals, after.withdrawals)
	assert.Equal(t, before.claimed, after.claimed, "the smaller slice stays claimable")
	assert.Equal(t, int64(52_000_000), balance(w, bill.ID))

	// A fresh quote of the smaller slice pays it.
	fresh := w.mustQuote(t, bill, pa, addr)
	assert.Equal(t, uint64(2_000_000), fresh.AmountMillis)
	_, err = w.withdraw(t, bill, pa, request(t, bill, nipcash.BySigning(pa.priv), fresh, newRID(), nil))
	require.NoError(t, err)
	assert.Equal(t, int64(50_000_000), balance(w, bill.ID))
}

// QuoteForSlice mirrors nmilat's: identity, IA and amount each matter.
func TestQuoteForSlice(t *testing.T) {
	w := newWorld(t)
	bill, hs := w.mintPubkey(t, 21_000_000)
	q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
	xonly, _ := w.node.XOnly(context.TODO())
	require.NoError(t, QuoteForSlice(q, xonly, *bill.WalletPubkey, hs[0].pub, "", 21_000_000))
	for _, err := range []error{
		QuoteForSlice(q, xonly, *bill.WalletPubkey, hs[0].pub, "", 20_999_000),
		QuoteForSlice(q, xonly, *bill.WalletPubkey, hs[0].pub, strings.Repeat("a", 64), 21_000_000),
		QuoteForSlice(q, xonly, strings.Repeat("b", 64), hs[0].pub, "", 21_000_000),
		QuoteForSlice(q, strings.Repeat("c", 64), *bill.WalletPubkey, hs[0].pub, "", 21_000_000),
	} {
		requireCode(t, err, constants.ERROR_RESTRICTED, nipcash.DetailQuoteInvalid)
	}
}

// U-4.11: a fault at every withdraw-commit stage, an error or a crash, leaves
// the slice claimable, the bill's balance, no payout, set-aside, record or
// consumed proof; the id stays used. At BeforeCommit with now = D+1 the commit
// is BAD_REQUEST.
func TestU_4_11_CommitFaults(t *testing.T) {
	for _, stage := range CommitStages {
		for _, mode := range []string{"error", "panic"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				w := newWorld(t)
				bill, hs := w.mintPubkey(t, 21_000_000, 1_000_000)
				q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
				w.s.Hooks = &TestHooks{Before: func(s string) error {
					if s != stage {
						return nil
					}
					if mode == "panic" {
						panic("crash at " + s)
					}
					return errors.New("fault at " + s)
				}}
				before := w.snapshot(t)
				bal := balance(w, bill.ID)
				func() {
					defer func() { _ = recover() }()
					_, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil))
					require.Error(t, err)
				}()
				after := w.snapshot(t)
				assert.Equal(t, before.used+1, after.used, "the id stays used")
				after.used = before.used
				assert.Equal(t, before, after)
				assert.Equal(t, bal, balance(w, bill.ID))

				// The slice is still claimable: the same quote now commits.
				w.s.Hooks = nil
				_, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil))
				require.NoError(t, err)
			})
		}
	}

	t.Run("deadline crossing", func(t *testing.T) {
		w := newWorld(t)
		bill, hs := w.mintPubkey(t, 21_000_000, 1_000_000)
		q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
		start := w.clock()
		meta := w.meta()
		w.s.Hooks = &TestHooks{Before: func(s string) error {
			if s == StageBeforeCommit {
				w.mu.Lock()
				w.now = time.Unix(meta.Deadline+1, 0)
				w.mu.Unlock()
			}
			return nil
		}}
		_, err := w.s.Withdraw(context.TODO(), bill, Caller{ItemSigner: hs[0].pub}, meta,
			request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil), nil)
		requireCode(t, err, constants.ERROR_BAD_REQUEST, "")
		var n int64
		require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Count(&n).Error)
		assert.Zero(t, n)

		// At D exactly it commits.
		w.mu.Lock()
		w.now = start
		w.mu.Unlock()
		meta = w.meta()
		w.s.Hooks = &TestHooks{Before: func(s string) error {
			if s == StageBeforeCommit {
				w.mu.Lock()
				w.now = time.Unix(meta.Deadline, 0)
				w.mu.Unlock()
			}
			return nil
		}}
		_, err = w.s.Withdraw(context.TODO(), bill, Caller{ItemSigner: hs[0].pub}, meta,
			request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil), nil)
		require.NoError(t, err)
	})

	t.Run("receipt signer down", func(t *testing.T) {
		w := newWorld(t)
		bill, hs := w.mintPubkey(t, 21_000_000, 1_000_000)
		q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
		w.node.signErr = errors.New("signer down")
		before := w.snapshot(t)
		_, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil))
		requireCode(t, err, constants.ERROR_INTERNAL, "")
		after := w.snapshot(t)
		after.used = before.used
		assert.Equal(t, before, after)
	})
}

// U-23.20 / U-23.21: concurrent withdrawals of one slice under different ids
// commit once; one id sent twice commits once, the other REQUEST_REUSED.
func TestU_23_20_Concurrency(t *testing.T) {
	w := newWorld(t)
	for i := range 20 {
		bill, hs := w.mintPubkey(t, 21_000_000, 1_000_000)
		q := w.mustQuote(t, bill, hs[0], address(t, 0, 20))
		sameID := i%2 == 1
		rid := newRID()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j := range 2 {
			params := request(t, bill, nipcash.BySigning(hs[0].priv), q, rid, nil)
			if !sameID {
				params = request(t, bill, nipcash.BySigning(hs[0].priv), q, newRID(), nil)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[j] = w.withdraw(t, bill, hs[0], params)
			}()
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
				continue
			}
			c, _ := code(err)
			if sameID {
				assert.Contains(t, []string{constants.ERROR_REQUEST_REUSED}, c, "%v", err)
			} else {
				assert.Contains(t, []string{constants.ERROR_RESTRICTED, constants.ERROR_UNAUTHORIZED, constants.ERROR_INTERNAL}, c, "%v", err)
			}
		}
		assert.Equal(t, 1, ok, fmt.Sprintf("run %d: exactly one commits", i))
		var n int64
		require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Where("bill_app_id = ?", bill.ID).Count(&n).Error)
		assert.Equal(t, int64(1), n)
		assert.Equal(t, int64(1_000_000), balance(w, bill.ID), "debited once")
	}
}

// A cash-mode slice: the secret authorizes, remint_commitment is required and
// never the slice's own.
func TestCashModeWithdraw(t *testing.T) {
	w := newWorld(t)
	bill, secret := w.mintCash(t, 21_000_000)
	res, err := w.s.Quote(context.TODO(), bill, Caller{CashMode: true, LimitKey: "e"}, raw(t, map[string]any{"address": address(t, 0, 20)}))
	require.NoError(t, err)
	q, err := nipcash.ParseWithdrawQuote(res.Quote)
	require.NoError(t, err)

	_, err = nipcash.CashWithdrawParams{Quote: q, Credential: nipcash.BySecret(secret), RequestID: newRID(), Notify: newRID()}.Request(*bill.WalletPubkey)
	require.ErrorIs(t, err, nipcash.ErrRemintRequired)

	// Without remint_commitment the Hub refuses before step 1.
	var m map[string]any
	target, err := nipcash.NewCashTarget()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(request(t, bill, nipcash.BySecret(secret), q, newRID(), target), &m))
	delete(m, "remint_commitment")
	_, err = w.s.Withdraw(context.TODO(), bill, Caller{CashMode: true}, w.meta(), raw(t, m), nil)
	requireCode(t, err, constants.ERROR_BAD_REQUEST, "")

	// A wrong secret is UNAUTHORIZED.
	other := newRID()
	m["remint_commitment"] = target.Commitment()
	m["cash_secret"] = other
	_, err = w.s.Withdraw(context.TODO(), bill, Caller{CashMode: true}, w.meta(), raw(t, m), nil)
	requireCode(t, err, constants.ERROR_UNAUTHORIZED, "")

	p, err := w.s.Withdraw(context.TODO(), bill, Caller{CashMode: true}, w.meta(), request(t, bill, nipcash.BySecret(secret), q, newRID(), target), nil)
	require.NoError(t, err)
	assert.Equal(t, nipcash.PayoutQueued, p.State)
	var wd db.CashWithdrawal
	require.NoError(t, w.svc.DB.First(&wd).Error)
	assert.Equal(t, target.Commitment(), *wd.RemintCommitment)
	assert.Nil(t, wd.DeliveryPubkey)
}

// unfinalOrders reads every slice as ordered and unfinal.
type unfinalOrders struct{ DBOrders }

func (unfinalOrders) OrderedUnfinal(*gorm.DB, *db.CashWalletClaim, int) (bool, error) {
	return true, nil
}

// orderAddress reads one script as an order address.
type orderAddress struct {
	DBOrders
	script []byte
}

func (o orderAddress) IsOrderScript(_ *gorm.DB, s []byte) (bool, error) {
	return string(s) == string(o.script), nil
}

func mustJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
