package service

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// servedNonces remembers the nonce of every envelope this hub served (NIP-CASH
// §Serving an Envelope, E3) until its not_after passes.
type servedNonces interface {
	// Seen reports a nonce already served. A store that cannot answer says
	// true, so the envelope is dropped: fail closed.
	Seen(nonce string) bool
	// MarkServed records nonce as served. False when it was served already or
	// could not be recorded; the envelope is then dropped.
	MarkServed(nonce string, notAfter int64) bool
	Len() int64
}

// dbServedNonces keeps served nonces in private_served_nonces, so a replay
// inside not_after after a restart is still dropped at E3.
type dbServedNonces struct{ db *gorm.DB }

func (s dbServedNonces) Seen(nonce string) bool {
	var n int64
	if err := s.db.Model(&db.PrivateServedNonce{}).Where("nonce = ?", nonce).Count(&n).Error; err != nil {
		logger.Logger.Warn().Err(err).Msg("Could not read the served-nonce table; dropping the envelope")
		return true
	}
	return n > 0
}

// MarkServed inserts the nonce in its own transaction before any item runs. The
// primary key decides two deliveries of one envelope racing each other.
func (s dbServedNonces) MarkServed(nonce string, notAfter int64) bool {
	err := s.db.Create(&db.PrivateServedNonce{Nonce: nonce, NotAfter: notAfter}).Error
	if err != nil && !db.IsDuplicateKeyError(err) {
		logger.Logger.Warn().Err(err).Msg("Could not record a served envelope nonce; dropping the envelope")
	}
	return err == nil
}

func (s dbServedNonces) Len() int64 {
	var n int64
	_ = s.db.Model(&db.PrivateServedNonce{}).Count(&n).Error
	return n
}

// prune forgets every nonce whose not_after has passed: E2 drops such an
// envelope before E3 is asked.
func (s dbServedNonces) prune(now time.Time) (int64, error) {
	result := s.db.Where("not_after < ?", now.Unix()).Delete(&db.PrivateServedNonce{})
	return result.RowsAffected, result.Error
}

// servedNoncePruneInterval is how often expired served nonces are deleted.
const servedNoncePruneInterval = time.Minute

func (s dbServedNonces) startPruner(ctx context.Context, group *errgroup.Group) {
	group.Go(func() error {
		ticker := time.NewTicker(servedNoncePruneInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case now := <-ticker.C:
				if n, err := s.prune(now); err != nil {
					logger.Logger.Warn().Err(err).Msg("Failed to prune served envelope nonces")
				} else if n > 0 {
					logger.Logger.Debug().Int64("pruned", n).Msg("Pruned expired served envelope nonces")
				}
			}
		}
	})
}
