package service

import (
	"context"
	"fmt"
	"github.com/flokiorg/lokihub/hubclock"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/logger"
)

// Order jobs (NIP-CO §Paying and Minting): pay_by, the chain scan and mint
// retries every 30 s; retention every 5 minutes.
const (
	orderJobInterval       = 30 * time.Second
	orderRetentionInterval = 5 * time.Minute
)

// newOrderEngine builds the order engine and routes order invoices to it.
func (svc *service) newOrderEngine(gormDB *gorm.DB, k keys.Keys, cfg config.Config, eventPublisher events.EventPublisher) *cashorder.Engine {
	e := &cashorder.Engine{
		DB: gormDB, Keys: k, Apps: apps.NewAppsService(gormDB, eventPublisher, k, cfg),
		Transactions: svc.transactionsService, LN: svc.GetLNClient, Relays: cfg.GetRelayUrls,
		Horizon: func() int64 {
			h, _ := cashrequest.HorizonTx(gormDB)
			return max(h, cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds)
		},
	}
	svc.transactionsService.SetOrderIntake(e.IntakeLightning, e.SelfPayLightning)
	svc.nip47Service.SetOrderIntake(e.IntakeLightning, e.SelfPayLightning)
	svc.nip47Service.SetOrderPayBy(e.EndWindowIfDue)
	return e
}

// startOrderJobs publishes order notices through pool and runs the order jobs;
// a startup sweep first re-reads every open Lightning order's invoice.
func (svc *service) startOrderJobs(ctx context.Context, pool *nostr.SimplePool) {
	// A payout's state notice is an order notice too, sent through this pool.
	svc.noticePool.Store(pool)
	e := svc.orders
	if e == nil {
		return
	}
	e.SetNoticePublisher(func(ctx context.Context, relay string, ev *nip01.Event) error {
		return svc.publishNotice(ctx, pool, relay, ev)
	})
	go func() {
		e.SweepLightning(ctx)
		ticker := time.NewTicker(orderJobInterval)
		defer ticker.Stop()
		retention := time.NewTicker(orderRetentionInterval)
		defer retention.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.RunPayBy(ctx)
				_ = e.ScanChain(ctx) // logged inside; retried next tick
				e.RetryMints(ctx)
			case <-retention.C:
				e.RunRetention()
			case <-hubclock.Moved():
				e.RunPayBy(ctx)
				e.RunRetention()
			}
		}
	}()
}

// publishNotice publishes an order notice (an order's, or a payout's state
// notice) on the relay its call arrived on, and on the Hub's other relays only
// if that one refuses (NIP-CO §Order Notice).
func (svc *service) publishNotice(ctx context.Context, pool *nostr.SimplePool, arrivedOn string, ev *nip01.Event) error {
	tags := make(nostr.Tags, 0, len(ev.Tags))
	for _, t := range ev.Tags {
		tags = append(tags, nostr.Tag(t))
	}
	out := nostr.Event{ID: ev.ID, PubKey: ev.PubKey, CreatedAt: nostr.Timestamp(ev.CreatedAt), Kind: ev.Kind, //nolint:gosec // a unix time
		Tags: tags, Content: ev.Content, Sig: ev.Sig}
	try := func(url string) bool {
		relay, err := pool.EnsureRelay(url)
		if err != nil {
			return false
		}
		return relay.Publish(ctx, out) == nil
	}
	if arrivedOn != "" && try(arrivedOn) {
		return nil
	}
	for _, url := range svc.cfg.GetRelayUrls() {
		if url != arrivedOn && try(url) {
			return nil
		}
	}
	logger.Logger.Debug().Msg("No relay accepted a notice")
	return fmt.Errorf("no relay accepted the notice")
}
