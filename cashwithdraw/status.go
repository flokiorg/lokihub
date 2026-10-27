package cashwithdraw

import (
	"github.com/flokiorg/lokihub/db"
)

// EngineStatus is the payout engine's health for the operator: payouts by
// state, the unconfirmed value against its bound's inputs, what is set aside,
// and the alert counters.
type EngineStatus struct {
	Ready                 bool             `json:"ready"`
	Payouts               map[string]int64 `json:"payouts"`
	UnconfirmedMillis     uint64           `json:"unconfirmed_millis"`
	SetAsideSat           int64            `json:"set_aside_sat"`
	EligibleSat           int64            `json:"eligible_sat"`
	FloorSat              int64            `json:"floor_sat"`
	OldestUnbroadcastSecs int64            `json:"oldest_unbroadcast_secs"`
	Bumps                 int64            `json:"bumps_total"`
	Reorgs                int64            `json:"reorgs_total"`
	Invalidations         int64            `json:"invalidations_total"`
	CoinsVanished         int64            `json:"coin_vanished_total"`
	InvariantFailures     int64            `json:"invariant_failures_total"`
}

// Status reads the engine's health.
func (e *Engine) Status() (*EngineStatus, error) {
	st := &EngineStatus{Ready: e.ready.Load(), Payouts: map[string]int64{}, FloorSat: e.floorSat.Load(),
		Bumps: e.Bumps.Load(), Reorgs: e.Reorgs.Load(), Invalidations: e.Invalidations.Load(),
		CoinsVanished: e.Vanished.Load(), InvariantFailures: e.InvariantFailures.Load()}
	var rows []struct {
		State string
		N     int64
	}
	if err := e.S.DB.Model(&db.CashWithdrawal{}).Select("state, COUNT(*) AS n").Group("state").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		st.Payouts[r.State] = r.N
	}
	var err error
	if st.UnconfirmedMillis, err = UnconfirmedValueTx(e.S.DB); err != nil {
		return nil, err
	}
	if err := e.S.DB.Model(&db.CashWithdrawSetAside{}).Select("COALESCE(SUM(amount_sat), 0)").Scan(&st.SetAsideSat).Error; err != nil {
		return nil, err
	}
	if err := e.S.DB.Model(&db.CashWithdrawCoin{}).Where("state = ?", db.CashCoinAvailable).
		Select("COALESCE(SUM(value_sat), 0)").Scan(&st.EligibleSat).Error; err != nil {
		return nil, err
	}
	var oldest int64
	if err := e.S.DB.Model(&db.CashWithdrawal{}).Where("state = ?", db.CashWithdrawQueued).
		Select("COALESCE(MIN(committed_at), 0)").Scan(&oldest).Error; err != nil {
		return nil, err
	}
	if oldest > 0 {
		st.OldestUnbroadcastSecs = e.S.now().Unix() - oldest
	}
	return st, nil
}
