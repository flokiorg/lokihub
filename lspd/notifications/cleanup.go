package notifications

import (
	"context"
	"time"

	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/rs/zerolog/log"
)

type CleanupService struct {
	store Store
}

// The interval to clean unused promises and buy registrations.
var CleanupInterval time.Duration = time.Hour

// The expiry duration is the time until a non-refreshed webhook url expires.
// Currently set to 4 weeks.
var ExpiryDuration time.Duration = time.Hour * 24 * 28

func NewCleanupService(store Store) *CleanupService {
	return &CleanupService{
		store: store,
	}
}

// Periodically cleans up expired webhook urls.
func (c *CleanupService) Start(ctx context.Context) {
	for {
		before := time.Now().Add(-ExpiryDuration)
		err := c.store.RemoveExpired(ctx, before)
		if err != nil {
			log.Error().
				Err(err).
				Str("component", string(logging.LogComponentNotifications)).
				Str("action", string(logging.LogActionFailure)).
				Time("before", before).
				Msg("Notifications Cleanup: Failed to remove expired webhooks")
		}
		select {
		case <-time.After(CleanupInterval):
			continue
		case <-ctx.Done():
			return
		}
	}
}
