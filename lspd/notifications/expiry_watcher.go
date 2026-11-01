package notifications

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/rs/zerolog/log"
)

const (
	expiryCheckInterval = 10 * time.Minute
)

// expiryMilestones are the block thresholds at which we notify, in descending order.
// Each HTLC gets at most one notification per milestone (warning → urgent → critical).
var expiryMilestones = []int{144, 72, 24}

type milestoneKey struct {
	pubkey      string
	paymentHash string
	threshold   int
}

// ExpiryWatcher monitors pending HTLCs and notifies clients when expiry is approaching.
type ExpiryWatcher struct {
	client              lightning.Client
	notificationService Notifier
	nodeConfig          *config.NodeConfig
	quit                chan struct{}
	wg                  sync.WaitGroup

	mu                 sync.Mutex
	notifiedMilestones map[milestoneKey]struct{}
}

// NewExpiryWatcher creates a new expiry watcher service
func NewExpiryWatcher(
	client lightning.Client,
	notificationService Notifier,
	nodeConfig *config.NodeConfig,
) *ExpiryWatcher {
	return &ExpiryWatcher{
		client:              client,
		notificationService: notificationService,
		nodeConfig:          nodeConfig,
		quit:                make(chan struct{}),
		notifiedMilestones:  make(map[milestoneKey]struct{}),
	}
}

// Start begins the monitoring loop
func (w *ExpiryWatcher) Start() {
	log.Info().Msg("ExpiryWatcher: started")
	w.wg.Add(1)
	go w.checkLoop()
}

// Stop stops the monitoring loop
func (w *ExpiryWatcher) Stop() {
	close(w.quit)
	w.wg.Wait()
	log.Info().Msg("ExpiryWatcher: stopped")
}

func (w *ExpiryWatcher) checkLoop() {
	defer w.wg.Done()

	ticker := time.NewTicker(expiryCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.quit:
			return
		case <-ticker.C:
			w.checkExpiries()
		}
	}
}

func (w *ExpiryWatcher) checkExpiries() {
	channels, err := w.client.ListChannels()
	if err != nil {
		log.Error().Err(err).Msg("ExpiryWatcher: Failed to list channels")
		return
	}

	currentBlockHeight, err := w.client.GetBlockHeight()
	if err != nil {
		log.Error().Err(err).Msg("ExpiryWatcher: Failed to get block height")
		return
	}

	// Track which (pubkey, paymentHash) pairs are still live so we can
	// evict stale milestone entries for HTLCs that have settled or expired.
	liveKeys := make(map[milestoneKey]struct{})

	for _, ch := range channels {
		pubkey := hex.EncodeToString(ch.PeerId)

		for _, htlc := range ch.PendingHtlcs {
			if !htlc.Incoming {
				continue
			}

			paymentHash := hex.EncodeToString(htlc.HashLock)
			blocksToExpiry := int(htlc.ExpirationHeight) - int(currentBlockHeight)

			// Mark all milestones for this HTLC as live so we don't evict them yet.
			for _, threshold := range expiryMilestones {
				liveKeys[milestoneKey{pubkey, paymentHash, threshold}] = struct{}{}
			}

			if blocksToExpiry <= 0 {
				continue
			}

			// Fire a notification for every milestone this HTLC has crossed
			// since the last check that hasn't been notified yet — not just
			// the first (least urgent) one. Thresholds are descending, so
			// once blocksToExpiry exceeds one, it exceeds every remaining
			// (smaller, more urgent) one too, and we can stop.
			for _, threshold := range expiryMilestones {
				if blocksToExpiry > threshold {
					break
				}

				key := milestoneKey{pubkey, paymentHash, threshold}
				w.mu.Lock()
				_, already := w.notifiedMilestones[key]
				if !already {
					w.notifiedMilestones[key] = struct{}{}
				}
				w.mu.Unlock()

				if !already {
					log.Info().
						Str("pubkey", pubkey).
						Str("payment_hash", paymentHash).
						Int("blocks_to_expiry", blocksToExpiry).
						Int("milestone", threshold).
						Msg("ExpiryWatcher: notifying expiry milestone")
					w.notificationService.NotifyExpirySoon(pubkey, blocksToExpiry, nil, paymentHash)
				}
			}
		}
	}

	// Evict milestone entries for HTLCs that are no longer pending.
	w.mu.Lock()
	for key := range w.notifiedMilestones {
		if _, live := liveKeys[key]; !live {
			delete(w.notifiedMilestones, key)
		}
	}
	w.mu.Unlock()
}
