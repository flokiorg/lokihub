package lsps

import (
	"context"
	"time"

	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/rs/zerolog/log"
)

// OrderCleanupService runs a background job to expire stale LSPS1 orders
// AND stale LSPS2 buy registrations (Phase 7, T7-6) -- both are reservations
// against the same finite on-chain wallet balance, so both need the same
// "flip to EXPIRED once past expires_at" sweep. Kept as one service/one
// ticker rather than two, since they run on the same cadence and share the
// same failure-handling shape.
type OrderCleanupService struct {
	store    ports.OpeningStore
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewOrderCleanupService creates a new cleanup service
func NewOrderCleanupService(store ports.OpeningStore, interval time.Duration) *OrderCleanupService {
	return &OrderCleanupService{
		store:    store,
		interval: interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start begins the cleanup loop
func (s *OrderCleanupService) Start() {
	log.Info().Msgf("OrderCleanupService: Starting with interval %v", s.interval)

	// Run immediately on startup
	s.runCleanup()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	defer close(s.doneCh)

	for {
		select {
		case <-ticker.C:
			s.runCleanup()
		case <-s.stopCh:
			log.Info().Msg("OrderCleanupService: Stopping")
			return
		}
	}
}

// Stop signals the cleanup service to stop
func (s *OrderCleanupService) Stop() {
	close(s.stopCh)
	<-s.doneCh
}

func (s *OrderCleanupService) runCleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_ = ctx // For future use if needed

	count, err := s.store.ExpireStaleOrders()
	if err != nil {
		log.Error().Err(err).Msg("OrderCleanupService: Failed to expire stale orders")
	} else if count > 0 {
		log.Info().Int64("expired_count", count).Msg("OrderCleanupService: Expired stale LSPS1 orders")
	} else {
		log.Debug().Msg("OrderCleanupService: No stale LSPS1 orders to expire")
	}

	// Phase 7, T7-6: LSPS2 buy registrations get the same sweep. A
	// separate error/count check -- one table's failure must not skip the
	// other's sweep.
	regCount, err := s.store.ExpireStaleLsps2Registrations()
	if err != nil {
		log.Error().Err(err).Msg("OrderCleanupService: Failed to expire stale LSPS2 registrations")
		return
	}

	if regCount > 0 {
		log.Info().Int64("expired_count", regCount).Msg("OrderCleanupService: Expired stale LSPS2 buy registrations")
	} else {
		log.Debug().Msg("OrderCleanupService: No stale LSPS2 registrations to expire")
	}
}
