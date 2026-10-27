package cashwithdraw

import (
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/db"
)

// CancelRequest applies a cancel request to an authorized payout (NIP-CASH
// §Cancelling a Payout): RESTRICTED before sent_by + 7 days; from then a
// broadcast payout is marked for invalidation once (a repeat changes nothing)
// and the engine woken. It is not a call: no request_id is marked. The caller
// answers with the payout's current state, which stays broadcast until the
// invalidation is final.
func (s *Service) CancelRequest(w *db.CashWithdrawal) (*db.CashWithdrawal, error) {
	now := s.now().Unix()
	state := nipcash.PayoutStateOf(nipcash.PayoutFacts{Cancelled: w.State == db.CashWithdrawCancelled,
		Broadcast: w.Txid != nil, FinalReached: w.FinalReached, Depth: uint32(max(w.Depth, 0)), FinalDepth: uint32(w.FinalDepth)}) //nolint:gosec // small
	invalidate, err := nipcash.CancelRequestAnswer(nipcash.CancelFacts{AboutToSign: w.Signed, Broadcast: w.Txid != nil,
		State: state, SentBy: w.SentBy}, now)
	if err != nil {
		return nil, err
	}
	if !invalidate {
		return w, nil
	}
	reason := db.CashWithdrawCancelRequest
	res := s.DB.Model(&db.CashWithdrawal{}).
		Where("id = ? AND cancel_requested_at IS NULL AND state = ?", w.ID, db.CashWithdrawBroadcast).
		Updates(map[string]interface{}{"cancel_requested_at": now, "cancel_reason": reason})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 1 && s.Funds != nil {
		s.Funds.Signal()
	}
	var fresh db.CashWithdrawal
	if err := s.DB.First(&fresh, w.ID).Error; err != nil {
		return nil, err
	}
	return &fresh, nil
}
