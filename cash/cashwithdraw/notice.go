package cashwithdraw

import (
	"context"

	"github.com/ohstr/nmilat/nipCO"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// noticeStep is step 9: a payout's state notice (NIP-CO's order notice, kind
// 23189, a fresh key, only ["p", notify]) is published once its delay from the
// first-broadcast or re-mint commit has passed, on the call's arrival relay
// (the sender tries the other inbox relays only if it refuses). It is not
// retried: clients poll.
func (e *Engine) noticeStep(ctx context.Context) error {
	var ws []db.CashWithdrawal
	if err := e.S.DB.Where("notice_due_at IS NOT NULL AND notice_due_at <= ?", e.S.now().Unix()).Find(&ws).Error; err != nil {
		return err
	}
	for _, w := range ws {
		ev, err := nipCO.NewOrderNotice(w.Notify)
		if err != nil {
			logger.Logger.Error().Err(err).Uint("withdrawal_id", w.ID).Msg("Could not build a payout's state notice")
		} else if e.Notices != nil {
			if err := e.Notices.SendNotice(ctx, w.ArrivalRelay, ev); err != nil {
				logger.Logger.Warn().Err(err).Uint("withdrawal_id", w.ID).Str("notify_prefix", prefix8(w.Notify)).
					Msg("No relay took a payout's state notice; clients poll")
			}
		}
		if err := e.S.DB.Model(&db.CashWithdrawal{}).Where("id = ?", w.ID).
			Updates(map[string]interface{}{"notice_due_at": nil, "notices_sent": w.NoticesSent + 1}).Error; err != nil {
			return err
		}
	}
	return nil
}
