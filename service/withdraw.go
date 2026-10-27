package service

import (
	"context"
	"errors"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// withdrawRetentionInterval is how often withdraw records past their retention
// are dropped (the cash record job's period).
const withdrawRetentionInterval = 5 * time.Minute

// withdrawPruneBatch bounds one retention pass.
const withdrawPruneBatch = 500

// setupWithdraw builds the on-chain withdrawal service and hands it to the
// private dispatch, and starts its record retention. Without a payout engine
// (Funds) no hub offers withdrawals; committed payouts are still answered.
func (svc *service) setupWithdraw() {
	svc.withdraw = &cashwithdraw.Service{
		DB:     svc.db,
		Cfg:    svc.cfg,
		Keys:   svc.keys,
		Apps:   apps.NewAppsService(svc.db, svc.eventPublisher, svc.keys, svc.cfg),
		Events: svc.eventPublisher,
		LN:     func() lnclient.LNClient { return svc.lnClient },
		IA:     apps.NewIdentityAuthorityManager(svc.db),
	}
	// A Cash Hub stays while a payout of it is owed or its record is kept.
	apps.RegisterCashHubDeleteBlocker("cash_withdraw", svc.withdraw.HubDeleteBlockers)
	// The payout engine is the withdrawal service's Funds; it runs for the
	// app's life and idles while the node lacks the on-chain capability.
	svc.withdrawEngine = cashwithdraw.NewEngine(svc.withdraw)
	svc.withdrawEngine.Notices = relayNoticeSender{relays: svc.cfg.GetRelayUrls}
	if w, ok := svc.nip47Service.(interface{ SetWithdraw(*cashwithdraw.Service) }); ok {
		w.SetWithdraw(svc.withdraw)
	}
	if svc.ctx == nil {
		return
	}
	go svc.withdrawEngine.Run(svc.ctx)
	go func() {
		ticker := time.NewTicker(withdrawRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-svc.ctx.Done():
				return
			case <-ticker.C:
				if _, err := svc.withdraw.PruneRecords(withdrawPruneBatch); err != nil {
					logger.Logger.Error().Err(err).Msg("Failed to drop withdraw records past their retention")
				}
			}
		}
	}()
}

// GetWithdrawService is the on-chain withdrawal service.
func (svc *service) GetWithdrawService() *cashwithdraw.Service { return svc.withdraw }

// relayNoticeSender publishes a payout's state notice on the call's arrival
// relay, and on the other configured relays only if it refuses.
type relayNoticeSender struct{ relays func() []string }

func (s relayNoticeSender) SendNotice(ctx context.Context, arrival string, ev *nip01.Event) error {
	published, err := toGoNostrEvent(ev)
	if err != nil {
		return err
	}
	try := func(url string) bool {
		if url == "" {
			return false
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		relay, err := nostr.RelayConnect(cctx, url)
		if err != nil {
			return false
		}
		defer relay.Close()
		return relay.Publish(cctx, *published) == nil
	}
	if try(arrival) {
		return nil
	}
	for _, url := range s.relays() {
		if url != arrival && try(url) {
			return nil
		}
	}
	return errors.New("no relay took the state notice")
}

// GetWithdrawEngine is the payout engine.
func (svc *service) GetWithdrawEngine() *cashwithdraw.Engine { return svc.withdrawEngine }
