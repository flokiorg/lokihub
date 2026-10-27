package cashwithdraw

import (
	"github.com/flokiorg/lokihub/db"
)

// PayoutRow is one payout as the operator lists it: no notify, the address in
// full (the operator's own records), the re-minted bill when cancelled.
type PayoutRow struct {
	ID                 uint    `json:"id"`
	RequestID          string  `json:"request_id"`
	BillWalletPubkey   string  `json:"bill_wallet_pubkey"`
	State              string  `json:"state"`
	Address            string  `json:"address"`
	AmountMillis       int64   `json:"amount_millis"`
	FeeMillis          int64   `json:"fee_millis"`
	HubFeeMillis       int64   `json:"hub_fee_millis"`
	NetworkFeeMillis   int64   `json:"network_fee_millis"`
	PayoutMillis       int64   `json:"payout_millis"`
	SentBy             int64   `json:"sent_by"`
	FinalDepth         int     `json:"final_depth"`
	Depth              int     `json:"depth"`
	Signed             bool    `json:"signed"`
	Txid               *string `json:"txid,omitempty"`
	Vout               *int    `json:"vout,omitempty"`
	HeldMillis         int64   `json:"held_millis"`
	CancelReason       *string `json:"cancel_reason,omitempty"`
	RemintWalletPubkey *string `json:"remint_wallet_pubkey,omitempty"`
	CommittedAt        int64   `json:"committed_at"`
}

// ListPayouts lists a Cash Hub's payouts, newest first (at most limit).
func (s *Service) ListPayouts(hubAppID uint, limit int) ([]PayoutRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var ws []db.CashWithdrawal
	if err := s.DB.Where("hub_app_id = ?", hubAppID).Order("id DESC").Limit(limit).Find(&ws).Error; err != nil {
		return nil, err
	}
	out := make([]PayoutRow, len(ws))
	for i, w := range ws {
		out[i] = PayoutRow{ID: w.ID, RequestID: w.RequestID, BillWalletPubkey: w.BillWalletPubkey, State: w.State,
			Address: w.Address, AmountMillis: w.AmountMillis, FeeMillis: w.FeeMillis, HubFeeMillis: w.HubFeeMillis,
			NetworkFeeMillis: w.NetworkFeeMillis, PayoutMillis: w.PayoutMillis, SentBy: w.SentBy, FinalDepth: w.FinalDepth,
			Depth: min(w.Depth, w.FinalDepth), Signed: w.Signed, Txid: w.Txid, Vout: w.Vout, HeldMillis: w.HeldMillis,
			CancelReason: w.CancelReason, RemintWalletPubkey: w.RemintWalletPubkey, CommittedAt: w.CommittedAt}
		if w.State == db.CashWithdrawCancelled {
			out[i].Txid, out[i].Vout = nil, nil
		}
	}
	return out, nil
}
