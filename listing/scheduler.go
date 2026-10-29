package listing

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// publishFailures counts listing events no relay accepted
// (listing_publish_failures_total).
var publishFailures = expvar.NewInt("listing_publish_failures_total")

// Run is the scheduler (WP-25 item 4): every tickInterval it acts on the due
// rows, until ctx ends. Restart-safe: everything it needs is in the table.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one scheduler pass: republishes, retries, closed re-sends,
// deletions, the closing of offers that stopped serving, and operator checks.
func (s *Service) Tick(ctx context.Context) {
	now := s.now()
	var due []db.OfferListing
	if err := s.d.DB.Where("due_at <= ? AND phase <> ?", now, db.ListingPhaseDeleted).
		Order("due_at").Limit(100).Find(&due).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Could not read the due listings")
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			return
		}
		if !s.claim(&due[i], now) {
			continue
		}
		if err := s.step(ctx, &due[i]); err != nil && !errors.Is(err, errDeferred) {
			logger.Logger.Warn().Err(err).Uint("listing_id", due[i].ID).Msg("Listing step failed; retrying later")
			_ = s.d.DB.Model(&db.OfferListing{}).Where("id = ? AND due_at = ?", due[i].ID, now+leaseSecs).
				Update("due_at", now+retryFirst).Error
		}
	}
	s.checkOperators(ctx, now)
	s.gauge()
}

// claim takes a due row for this pass (compare-and-set on due_at), so a second
// instance on the same database skips it.
func (s *Service) claim(row *db.OfferListing, now int64) bool {
	res := s.d.DB.Model(&db.OfferListing{}).Where("id = ? AND due_at = ?", row.ID, row.DueAt).
		Update("due_at", now+leaseSecs)
	return res.Error == nil && res.RowsAffected == 1
}

func (s *Service) step(ctx context.Context, row *db.OfferListing) error {
	switch row.Phase {
	case db.ListingPhaseListed:
		if reason := s.stoppedServing(ctx, row); reason != "" {
			logger.Logger.Info().Uint("listing_id", row.ID).Str("reason", reason).Msg("Closing a listing whose offer stopped serving")
			s.d.InvalidateGetInfo(row.ConnAppID)
			if err := s.close(ctx, row.ID); err != nil {
				return err
			}
			if reason == reasonHubDeleted {
				// Nothing serves the listing connection any more.
				return s.revokeConn(row.ID)
			}
			return nil
		}
		if row.FailingSince != nil && row.LastEventJSON != "" && row.LastCreatedAt+RepublishSecs > s.now() {
			return s.resendLast(ctx, row)
		}
		return s.publishVersion(ctx, row.ID, false)
	case db.ListingPhaseClosing:
		unlock := lockListing(row.ID)
		defer unlock()
		cur, err := s.load(row.ID)
		if err != nil {
			return err
		}
		now := s.now()
		switch {
		case cur.DeleteDueAt != nil && now >= *cur.DeleteDueAt:
			return s.publishDeletion(ctx, cur)
		case eventStatusJSON(cur.LastEventJSON) != db.ListingStatusClosed:
			// Marked closing before the clock passed the last version.
			if now <= cur.LastCreatedAt {
				return s.d.DB.Model(cur).Update("due_at", cur.LastCreatedAt+1).Error
			}
			return s.publishClosed(ctx, cur, now)
		default:
			return s.resendClosed(ctx, cur)
		}
	}
	return nil
}

const reasonHubDeleted = "the hub was deleted"

// stoppedServing is why a listed offer must close although nobody pressed
// anything: its hub or connection is gone, the Order connection was retired, the
// circle stopped accepting. "" while it serves.
func (s *Service) stoppedServing(ctx context.Context, row *db.OfferListing) string {
	var hub db.App
	if err := s.d.DB.First(&hub, row.HubAppID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return reasonHubDeleted
	}
	switch row.Type {
	case db.ListingTypeCashOrder:
		if s.d.Orders == nil {
			return ""
		}
		offer, err := s.d.Orders.Order(ctx, row.ConnAppID)
		if err == nil && (offer == nil || offer.Retired) {
			return "the Order connection was retired"
		}
	case db.ListingTypeCircleWallet:
		var n int64
		if row.ConnAppID == 0 {
			return "the listing connection was revoked"
		}
		if err := s.d.DB.Model(&db.App{}).Where("id = ? AND kind = ?", row.ConnAppID, db.AppKindCircleHubConn).
			Count(&n).Error; err == nil && n == 0 {
			return "the listing connection was deleted"
		}
		if cfg, err := s.circleConfig(row.HubAppID); err == nil && cfg.AcceptingJoins != nil && !*cfg.AcceptingJoins {
			return "the circle stopped accepting members"
		}
	}
	return ""
}

// resendLast re-sends the stored last version to its targets (a version no relay
// accepted yet).
func (s *Service) resendLast(ctx context.Context, row *db.OfferListing) error {
	unlock := lockListing(row.ID)
	defer unlock()
	var ev nip01.Event
	if err := json.Unmarshal([]byte(row.LastEventJSON), &ev); err != nil {
		return err
	}
	// The TLV 1 relays of a string lokihub issued are its NWC relays.
	stringRelays := cleanRelays(s.d.Settings.GetRelayUrls())
	answers := s.d.Relays.Publish(ctx, s.targets(ctx, row, stringRelays), &ev)
	return s.recordVersion(row.ID, &ev, answers)
}

func eventStatusJSON(raw string) string {
	var ev nip01.Event
	if json.Unmarshal([]byte(raw), &ev) != nil {
		return ""
	}
	return eventStatus(&ev)
}

// checkOperators runs the operator check every OperatorCheckSecs for listed
// offers naming an operator.
func (s *Service) checkOperators(ctx context.Context, now int64) {
	var rows []db.OfferListing
	if err := s.d.DB.Where("phase = ? AND operator_pubkey IS NOT NULL AND (operator_checked_at IS NULL OR operator_checked_at <= ?)",
		db.ListingPhaseListed, now-OperatorCheckSecs).Limit(20).Find(&rows).Error; err != nil {
		return
	}
	for i := range rows {
		if err := s.checkOperator(ctx, &rows[i]); err != nil {
			logger.Logger.Debug().Err(err).Uint("listing_id", rows[i].ID).Msg("Operator check failed")
		}
	}
}

// listingsGauge is the gauge listings{status}.
var listingsGauge = expvar.NewMap("listings")

func (s *Service) gauge() {
	type count struct {
		Status string
		N      int64
	}
	var counts []count
	if s.d.DB.Model(&db.OfferListing{}).Select("status, count(*) as n").Where("phase <> ?", db.ListingPhaseDeleted).
		Group("status").Scan(&counts).Error != nil {
		return
	}
	for _, st := range []string{db.ListingStatusOpen, db.ListingStatusPaused, db.ListingStatusClosed} {
		v := new(expvar.Int)
		for _, c := range counts {
			if c.Status == st {
				v.Set(c.N)
			}
		}
		listingsGauge.Set(st, v)
	}
}
