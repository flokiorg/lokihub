package cashwithdraw

import (
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// ErrPayoutNotFound: no payout with that id on that Cash Hub.
var ErrPayoutNotFound = errors.New("payout not found")

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
	CancelRequestedAt  *int64  `json:"cancel_requested_at,omitempty"`
	CancelledAt        *int64  `json:"cancelled_at,omitempty"`
	FinalAt            *int64  `json:"final_at,omitempty"`
	RemintAppID        *uint   `json:"remint_app_id,omitempty"`
	RemintWalletPubkey *string `json:"remint_wallet_pubkey,omitempty"`
	CommittedAt        int64   `json:"committed_at"`
}

// PayoutVersion is one transaction of the payout's batch: every fee bump and
// any invalidation, with whether it pays this payout and at which output.
type PayoutVersion struct {
	Seq              int      `json:"seq"`
	Kind             string   `json:"kind"`
	State            string   `json:"state"`
	Txid             *string  `json:"txid,omitempty"`
	Pays             bool     `json:"pays"`
	Vout             *int     `json:"vout,omitempty"`
	Inputs           []string `json:"inputs"`
	Vsize            *int     `json:"vsize,omitempty"`
	FeeSat           *int64   `json:"fee_sat,omitempty"`
	FeeRateSatKvb    *int64   `json:"fee_rate_sat_kvb,omitempty"`
	CreatedAt        int64    `json:"created_at"`
	SignedAt         *int64   `json:"signed_at,omitempty"`
	FirstBroadcastAt *int64   `json:"first_broadcast_at,omitempty"`
	LastBroadcastAt  *int64   `json:"last_broadcast_at,omitempty"`
	ConfirmHeight    *int64   `json:"confirm_height,omitempty"`
}

// PayoutFact is one archive fact of the payout (NIP-CASH §Archival on
// Deletion).
type PayoutFact struct {
	At             int64   `json:"at"`
	State          string  `json:"state"`
	Txid           *string `json:"txid,omitempty"`
	Vout           *int    `json:"vout,omitempty"`
	Depth          *int    `json:"depth,omitempty"`
	MinerFeeMillis *int64  `json:"miner_fee_millis,omitempty"`
}

// PayoutDetail is one payout with every version and archive fact. Nothing in
// it is an action: no operator action changes a payout.
type PayoutDetail struct {
	PayoutRow
	Versions []PayoutVersion `json:"versions"`
	Facts    []PayoutFact    `json:"facts"`
}

// GetPayout is one of hubAppID's payouts with its versions and archive facts.
func (s *Service) GetPayout(hubAppID, id uint) (*PayoutDetail, error) {
	var w db.CashWithdrawal
	err := s.DB.Where("id = ? AND hub_app_id = ?", id, hubAppID).First(&w).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPayoutNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &PayoutDetail{PayoutRow: payoutRow(w), Versions: []PayoutVersion{}, Facts: []PayoutFact{}}
	if w.BatchID != nil {
		var vs []db.CashWithdrawVersion
		if err := s.DB.Where("batch_id = ?", *w.BatchID).Order("seq").Find(&vs).Error; err != nil {
			return nil, err
		}
		var outs []db.CashWithdrawVersionOutput
		if err := s.DB.Where("withdrawal_id = ?", w.ID).Find(&outs).Error; err != nil {
			return nil, err
		}
		vout := map[uint]int{}
		for _, o := range outs {
			vout[o.VersionID] = o.Vout
		}
		for _, v := range vs {
			pv := PayoutVersion{Seq: v.Seq, Kind: v.Kind, State: v.State, Txid: v.Txid, Inputs: []string{},
				Vsize: v.Vsize, FeeSat: v.FeeSat, FeeRateSatKvb: v.FeeRateSatKvb, CreatedAt: v.CreatedAt,
				SignedAt: v.SignedAt, FirstBroadcastAt: v.FirstBroadcastAt, LastBroadcastAt: v.LastBroadcastAt,
				ConfirmHeight: v.ConfirmHeight}
			if n, ok := vout[v.ID]; ok {
				pv.Pays, pv.Vout = true, &n
			}
			_ = json.Unmarshal([]byte(v.Inputs), &pv.Inputs)
			d.Versions = append(d.Versions, pv)
		}
	}
	var evs []db.CashWithdrawEvent
	if err := s.DB.Where("withdrawal_id = ?", w.ID).Order("at, id").Find(&evs).Error; err != nil {
		return nil, err
	}
	for _, e := range evs {
		d.Facts = append(d.Facts, PayoutFact{At: e.At, State: e.State, Txid: e.Txid, Vout: e.Vout, Depth: e.Depth, MinerFeeMillis: e.MinerFeeMillis})
	}
	return d, nil
}

func payoutRow(w db.CashWithdrawal) PayoutRow {
	r := PayoutRow{ID: w.ID, RequestID: w.RequestID, BillWalletPubkey: w.BillWalletPubkey, State: w.State,
		Address: w.Address, AmountMillis: w.AmountMillis, FeeMillis: w.FeeMillis, HubFeeMillis: w.HubFeeMillis,
		NetworkFeeMillis: w.NetworkFeeMillis, PayoutMillis: w.PayoutMillis, SentBy: w.SentBy, FinalDepth: w.FinalDepth,
		Depth: min(w.Depth, w.FinalDepth), Signed: w.Signed, Txid: w.Txid, Vout: w.Vout, HeldMillis: w.HeldMillis,
		CancelReason: w.CancelReason, CancelRequestedAt: w.CancelRequestedAt, CancelledAt: w.CancelledAt,
		FinalAt: w.FinalAt, RemintAppID: w.RemintAppID, RemintWalletPubkey: w.RemintWalletPubkey, CommittedAt: w.CommittedAt}
	if w.State == db.CashWithdrawCancelled {
		r.Txid, r.Vout = nil, nil
	}
	return r
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
		out[i] = payoutRow(w)
	}
	return out, nil
}
