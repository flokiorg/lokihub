package cashwithdraw

import (
	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// VersionFacts is implemented by a payout engine that holds signed versions:
// the raw transaction a payout reports as tx (the confirmed version, else the
// last signed one), "" before the first signature.
type VersionFacts interface {
	PayoutTxHexTx(tx *gorm.DB, withdrawalID uint) (string, error)
}

// RemintRole is the record bill row of a cancelled payout's re-minted bill.
const RemintRole = "reminted"

// PayoutView is a payout as the reply, every lookup done and every cancel
// answer carry it (NIP-CASH §Response). Under cancelled it carries
// new_wallet_pubkey and, for the caller to fill, the record row its token is
// rebuilt from (returned second): the token is rebuilt and delivered as a
// lookup's, nested-encrypted to the claim signer or in the clear for cash.
func (s *Service) PayoutView(tx *gorm.DB, w *db.CashWithdrawal) (*nipcash.Payout, *db.CashRequestRecordBill, error) {
	cancelled := w.State == db.CashWithdrawCancelled
	broadcast := w.Txid != nil
	depth := uint32(0)
	if w.Depth > 0 {
		depth = uint32(min(w.Depth, w.FinalDepth)) //nolint:gosec // small
	}
	p := &nipcash.Payout{
		State: nipcash.PayoutStateOf(nipcash.PayoutFacts{Cancelled: cancelled, Broadcast: broadcast,
			FinalReached: w.FinalReached, Depth: depth, FinalDepth: uint32(w.FinalDepth)}), //nolint:gosec // small
		Address:      w.Address,
		AmountMillis: uint64(w.AmountMillis), //nolint:gosec // non-negative
		FeeMillis:    uint64(w.FeeMillis),    //nolint:gosec // non-negative
		PayoutMillis: uint64(w.PayoutMillis), //nolint:gosec // non-negative
		SentBy:       w.SentBy,
		FinalDepth:   uint32(w.FinalDepth), //nolint:gosec // small
		Signed:       w.Signed,
		Receipt:      w.ReceiptEvent,
	}
	if cancelled {
		var b db.CashRequestRecordBill
		if err := tx.Where("record_id = ? AND role = ?", w.RecordID, RemintRole).Limit(1).Find(&b).Error; err != nil {
			return nil, nil, err
		}
		if w.RemintWalletPubkey != nil {
			p.NewWalletPubkey = *w.RemintWalletPubkey
		}
		if b.ID == 0 {
			return p, nil, nil
		}
		return p, &b, nil
	}
	if w.Signed {
		if vf, ok := s.Funds.(VersionFacts); ok {
			raw, err := vf.PayoutTxHexTx(tx, w.ID)
			if err != nil {
				return nil, nil, err
			}
			p.Tx = raw
		}
	}
	if broadcast {
		p.Txid = *w.Txid
		if w.Vout != nil {
			v := uint32(*w.Vout) //nolint:gosec // small
			p.Vout = &v
		}
		p.Depth = &depth
	}
	return p, nil, nil
}

// Withdrawal is the payout of a record, or nil.
func (s *Service) Withdrawal(tx *gorm.DB, rec *db.CashRequestRecord) (*db.CashWithdrawal, error) {
	var w db.CashWithdrawal
	q := tx.Where("request_id = ?", rec.RequestID)
	if err := q.Limit(1).Find(&w).Error; err != nil {
		return nil, err
	}
	if w.ID == 0 {
		return nil, nil
	}
	return &w, nil
}

// RequestIDForAuthorizer is the request_id of a kept cash_withdraw record on the
// bill walletPubkey whose authorizer is the pubkey signer — for the slice
// claimID when it is not 0 — or "" when there is none. cash_status rows and a
// destroyed bill's tombstone carry it as withdraw_request_id, for that
// authorizer only.
func RequestIDForAuthorizer(tx *gorm.DB, walletPubkey, signer string, claimID uint) string {
	if signer == "" {
		return ""
	}
	q := tx.Model(&db.CashWithdrawal{}).
		Joins("JOIN cash_request_records ON cash_request_records.id = cash_withdrawals.record_id").
		Where("cash_withdrawals.bill_wallet_pubkey = ? AND cash_request_records.authorizer_kind = ? AND cash_request_records.authorizer = ?",
			walletPubkey, "pubkey", signer)
	if claimID != 0 {
		q = q.Where("cash_withdrawals.claim_id = ?", claimID)
	}
	var ids []string
	if err := q.Order("cash_withdrawals.id DESC").Limit(1).Pluck("cash_withdrawals.request_id", &ids).Error; err != nil || len(ids) == 0 {
		return ""
	}
	return ids[0]
}
