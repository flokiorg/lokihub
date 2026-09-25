package service

import (
	"context"

	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
)

// cashRetentionConsumer re-syncs the relay gate when a Cash Hub's spent-bill
// retention changes.
//
// Raising retention brings destroyed bills back inside their window, and the
// periodic sweep only ever removes wallets — so without this, a bill made
// answerable again would stay unreachable until the next restart rebuilt the
// registry. Lowering is already handled: the sweep drops what has expired.
//
// Re-running the startup reload is the whole implementation, and it is cheap
// because that is now one indexed query. Add is idempotent, so wallets already
// served are untouched.
type cashRetentionConsumer struct {
	events.EventSubscriber
	svc *service
}

func (s *cashRetentionConsumer) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	if event.Event != "nwc_cash_retention_changed" {
		return
	}
	if s.svc.walletRegistry == nil {
		return
	}

	retained := s.svc.retainedSpentBillPubkeys()
	if len(retained) == 0 {
		return
	}

	s.svc.walletRegistry.Add(retained...)
	logger.Logger.Info().
		Int("retained", len(retained)).
		Int("registry_size", s.svc.walletRegistry.Len()).
		Msg("Re-synced retained spent-bill wallets after a retention change")
}
