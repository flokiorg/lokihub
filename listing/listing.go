package listing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ohstr/nmilat/nip01"
	nmlisting "github.com/ohstr/nmilat/nipcash/listing"
	"github.com/ohstr/nmilat/nipcw"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// CreateRequest lists an offer: an Order connection (Type cash_order, ConnAppID)
// or a circle (Type circle_wallet, CircleHubAppID). It takes record ids only.
type CreateRequest struct {
	Type           string `json:"type"`
	ConnAppID      *uint  `json:"conn_app_id"`
	CircleHubAppID *uint  `json:"circle_hub_app_id"`
	Fields
}

// rowLocks serializes build-and-record per listing within this process; the
// scheduler's lease (due_at) keeps a second instance away.
var rowLocks sync.Map

func lockListing(id uint) func() {
	m, _ := rowLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Create lists an offer and publishes its first version at once (WP-25 item 1).
func (s *Service) Create(ctx context.Context, req CreateRequest) (*View, error) {
	var (
		hub     db.App
		connApp *db.App
		typ     string
		isCF    bool
	)
	switch {
	case req.ConnAppID != nil && req.CircleHubAppID != nil:
		return nil, refused("name one offer: an Order connection or a circle")
	case req.ConnAppID != nil:
		var app db.App
		if err := s.d.DB.First(&app, *req.ConnAppID).Error; err != nil {
			return nil, refused("no such connection")
		}
		if err := refuseKind(&app); err != nil {
			return nil, err
		}
		if app.Kind == db.AppKindCircleHub {
			// A circle named by its app id is listed as a circle.
			id := app.ID
			return s.Create(ctx, CreateRequest{Type: db.ListingTypeCircleWallet, CircleHubAppID: &id, Fields: req.Fields})
		}
		if s.d.Orders == nil {
			return nil, refused("only Order connections and circles are listed, and this Hub has no Order connections yet")
		}
		offer, err := s.d.Orders.Order(ctx, app.ID)
		if err != nil || offer == nil {
			return nil, refused("not an Order connection")
		}
		if offer.Retired {
			return nil, refused("a retired Order connection is never listed")
		}
		if err := s.d.DB.First(&hub, offer.HubAppID).Error; err != nil {
			return nil, refused("the Order connection's Cash Hub is gone")
		}
		typ, connApp = db.ListingTypeCashOrder, &app
	case req.CircleHubAppID != nil:
		if err := s.d.DB.First(&hub, *req.CircleHubAppID).Error; err != nil {
			return nil, refused("no such circle")
		}
		if err := refuseKind(&hub); err != nil {
			return nil, err
		}
		if hub.Kind != db.AppKindCircleHub {
			return nil, refused("not a circle")
		}
		cfg, err := s.circleConfig(hub.ID)
		if err != nil {
			return nil, refused("the circle has no configuration")
		}
		isCF = cfg.CircleIdentity.Policy == db.CirclePolicyFollowSet
		typ = db.ListingTypeCircleWallet
	default:
		return nil, refused("name an Order connection or a circle to list")
	}
	if req.Type != "" && req.Type != typ {
		return nil, refused("type %q does not match the offer (%s)", req.Type, typ)
	}
	if err := req.Fields.validate(isCF, typ == db.ListingTypeCircleWallet); err != nil {
		return nil, err
	}
	if hub.WalletPubkey == nil {
		return nil, refused("the offer has no wallet pubkey")
	}

	d := *hub.WalletPubkey
	if connApp != nil {
		d = *connApp.WalletPubkey
	}
	var row db.OfferListing
	err := s.d.DB.Transaction(func(tx *gorm.DB) error {
		var existing db.OfferListing
		found := tx.Where("d = ?", d).Limit(1).Find(&existing).RowsAffected > 0
		if found && existing.Phase != db.ListingPhaseDeleted {
			return refused("this offer is already listed (listing %d)", existing.ID)
		}
		connID := uint(0)
		if connApp != nil {
			connID = connApp.ID
		} else {
			conn, err := createCircleConnTx(tx, s.d.Keys, &hub, req.Title)
			if err != nil {
				return err
			}
			connID = conn.ID
		}
		row = existing
		row.Type, row.HubAppID, row.ConnAppID, row.D = typ, hub.ID, connID, d
		applyFields(&row, req.Fields)
		row.Status, row.Phase = db.ListingStatusOpen, db.ListingPhaseListed
		row.OpenRelays, row.ClosedRelays = "", ""
		row.LastOpenCreatedAt, row.LastOpenExpiresAt = nil, nil
		row.ClosedAt, row.DeleteDueAt, row.DeletedAt = nil, nil, nil
		row.FailingSince, row.RetryDelay, row.DueAt = nil, 0, 0
		row.OperatorVerified, row.OperatorCheckedAt = nil, nil
		return tx.Save(&row).Error
	})
	if err != nil {
		return nil, err
	}
	s.d.InvalidateGetInfo(row.ConnAppID)
	if err := s.ensureRelayList(ctx); err != nil {
		logger.Logger.Warn().Err(err).Msg("Could not publish the node's relay list")
	}
	if err := s.publishVersion(ctx, row.ID, true); err != nil && !errors.Is(err, errDeferred) {
		// Nothing was published: the offer is not listed, and a circle's new
		// listing connection is dropped again.
		if abandonErr := s.abandon(row.ID); abandonErr != nil {
			logger.Logger.Error().Err(abandonErr).Uint("listing_id", row.ID).Msg("Could not abandon an unpublished listing")
		}
		return nil, err
	}
	return s.Get(ctx, row.ID)
}

// abandon parks a listing whose first version was never published.
func (s *Service) abandon(id uint) error {
	unlock := lockListing(id)
	defer unlock()
	now := s.now()
	if err := s.d.DB.Model(&db.OfferListing{}).Where("id = ? AND last_open_created_at IS NULL", id).Updates(map[string]any{
		"phase": db.ListingPhaseDeleted, "status": db.ListingStatusClosed, "deleted_at": now, "due_at": neverDue,
	}).Error; err != nil {
		return err
	}
	return s.revokeConnLocked(id)
}

// refuseKind refuses what is never listed (H-CASH-410): a Cash Hub connection, a
// bill, a circle member's wallet, a listing connection, any other connection.
func refuseKind(app *db.App) error {
	switch app.Kind {
	case db.AppKindCashHub:
		return refused("a Cash Hub connection is never listed; sell through an Order connection")
	case db.AppKindCircleHub:
		return nil
	case db.AppKindCashWallet, db.AppKindCircleWallet, db.AppKindCircleHubConn:
		return refused("a %s is never listed", app.Kind)
	}
	// Anything else may only be an Order connection, which the OrderSource
	// decides.
	return nil
}

func applyFields(row *db.OfferListing, f Fields) {
	row.Title, row.Content, row.Image, row.Location = f.Title, f.Content, f.Image, f.Location
	row.Languages, row.Geohashes = encodeList(f.Languages), encodeList(f.Geohashes)
	row.Hints, row.FollowSetA = f.Hints, f.FollowSetA
	row.OperatorPubkey, row.OperatorRelay = nil, nil
	if f.OperatorPubkey != "" {
		p := f.OperatorPubkey
		row.OperatorPubkey = &p
		if f.OperatorRelay != "" {
			r := f.OperatorRelay
			row.OperatorRelay = &r
		}
	}
}

// Update edits a listed offer's fields and republishes at once.
func (s *Service) Update(ctx context.Context, id uint, f Fields) (*View, error) {
	unlock := lockListing(id)
	row, err := s.load(id)
	if err != nil {
		unlock()
		return nil, err
	}
	if row.Phase != db.ListingPhaseListed {
		unlock()
		return nil, refused("only a listed offer is edited")
	}
	isCF := false
	if row.Type == db.ListingTypeCircleWallet {
		if cfg, err := s.circleConfig(row.HubAppID); err == nil {
			isCF = cfg.CircleIdentity.Policy == db.CirclePolicyFollowSet
		}
	}
	if err := f.validate(isCF, row.Type == db.ListingTypeCircleWallet); err != nil {
		unlock()
		return nil, err
	}
	operatorChanged := ptr(row.OperatorPubkey) != f.OperatorPubkey
	applyFields(row, f)
	if operatorChanged {
		row.OperatorVerified, row.OperatorCheckedAt = nil, nil
	}
	err = s.d.DB.Save(row).Error
	unlock()
	if err != nil {
		return nil, err
	}
	if err := s.publishVersion(ctx, id, true); err != nil && !errors.Is(err, errDeferred) {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Pause and Resume set status paused or open (H-CASH-433) and republish.
func (s *Service) Pause(ctx context.Context, id uint) (*View, error) {
	return s.setStatus(ctx, id, db.ListingStatusPaused)
}

// Resume sets status open again.
func (s *Service) Resume(ctx context.Context, id uint) (*View, error) {
	return s.setStatus(ctx, id, db.ListingStatusOpen)
}

func (s *Service) setStatus(ctx context.Context, id uint, status string) (*View, error) {
	unlock := lockListing(id)
	row, err := s.load(id)
	if err == nil && row.Phase != db.ListingPhaseListed {
		err = refused("only a listed offer is paused or resumed")
	}
	if err == nil {
		err = s.d.DB.Model(row).Update("status", status).Error
	}
	unlock()
	if err != nil {
		return nil, err
	}
	if err := s.publishVersion(ctx, id, true); err != nil && !errors.Is(err, errDeferred) {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Unlist publishes the closed version, then the deletion after the last open
// version's effective expiry, leaving get_info unchanged: the offer keeps
// serving whoever holds its string (WP-25 item 8; owner LS-3, OQ-42).
func (s *Service) Unlist(ctx context.Context, id uint) (*View, error) {
	if err := s.close(ctx, id); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// RetireOrder runs steps (b) and (c) for the listing of an Order connection
// whose retirement is committed: its get_info already answers "cash_order": {}.
func (s *Service) RetireOrder(ctx context.Context, connAppID uint) error {
	return s.closeWhere(ctx, "type = ? AND conn_app_id = ?", db.ListingTypeCashOrder, connAppID)
}

// CloseCircle runs steps (b) and (c) for a circle whose accepting false is
// committed (WP-19's "Close to new members").
func (s *Service) CloseCircle(ctx context.Context, hubAppID uint) error {
	return s.closeWhere(ctx, "type = ? AND hub_app_id = ?", db.ListingTypeCircleWallet, hubAppID)
}

func (s *Service) closeWhere(ctx context.Context, where string, args ...any) error {
	var rows []db.OfferListing
	if err := s.d.DB.Where(where, args...).Where("phase = ?", db.ListingPhaseListed).Find(&rows).Error; err != nil {
		return err
	}
	var errs []error
	for _, r := range rows {
		s.d.InvalidateGetInfo(r.ConnAppID)
		if err := s.close(ctx, r.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RevokeConnection deletes a listed circle's own connection (WP-25 item 6),
// unlisting first while the listing is open or paused. The members' invite (the
// hub's own string) is untouched; no info event is published or deleted.
func (s *Service) RevokeConnection(ctx context.Context, id uint) (*View, error) {
	row, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if row.Type != db.ListingTypeCircleWallet {
		return nil, refused("only a circle's listing connection is revoked here")
	}
	if row.Phase == db.ListingPhaseListed {
		if err := s.close(ctx, id); err != nil {
			return nil, err
		}
	}
	if err := s.revokeConn(id); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) revokeConn(id uint) error {
	unlock := lockListing(id)
	defer unlock()
	return s.revokeConnLocked(id)
}

// revokeConnLocked is revokeConn for a caller holding the listing's lock.
func (s *Service) revokeConnLocked(id uint) error {
	row, err := s.load(id)
	if err != nil {
		return err
	}
	if row.Type != db.ListingTypeCircleWallet || row.ConnAppID == 0 {
		return nil
	}
	conn := row.ConnAppID
	err = s.d.DB.Transaction(func(tx *gorm.DB) error {
		if err := deleteCircleConnTx(tx, conn); err != nil {
			return err
		}
		return tx.Model(&db.OfferListing{}).Where("id = ?", id).Update("conn_app_id", 0).Error
	})
	if err == nil {
		s.d.InvalidateGetInfo(conn)
	}
	return err
}

func (s *Service) load(id uint) (*db.OfferListing, error) {
	var row db.OfferListing
	if err := s.d.DB.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

func (s *Service) circleConfig(hubID uint) (*db.CircleHubConfig, error) {
	var cfg db.CircleHubConfig
	if err := s.d.DB.Preload("CircleIdentity").Where("app_id = ?", hubID).First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

// errDeferred is a publish handed to the scheduler: the clock is not yet past
// the previous version's created_at (never post-dated, H-CASH-415).
var errDeferred = errors.New("publish deferred until the clock passes the previous version")

// build builds the fields of the next version of row (WP-25 item 2), signing the
// listed string afresh from the app rows.
func (s *Service) build(ctx context.Context, row *db.OfferListing) (nmlisting.ListingFields, []string, error) {
	var f nmlisting.ListingFields
	network, err := s.d.Signer.Network(ctx)
	if err != nil {
		return f, nil, fmt.Errorf("%w: %v", ErrCannotSign, err)
	}
	f.Network = network
	var stringRelays []string
	switch row.Type {
	case db.ListingTypeCashOrder:
		if s.d.Orders == nil {
			return f, nil, refused("Order connections are not available")
		}
		offer, err := s.d.Orders.Order(ctx, row.ConnAppID)
		if err != nil || offer == nil {
			return f, nil, fmt.Errorf("the Order connection: %w", err)
		}
		f.Order, f.Summary = offer.Conn, offer.Summary
		if row.Hints {
			f.Hints = offer.Hints
		}
		stringRelays = offer.Conn.Relays
	case db.ListingTypeCircleWallet:
		var hub, conn db.App
		if err := s.d.DB.First(&hub, row.HubAppID).Error; err != nil {
			return f, nil, fmt.Errorf("the circle: %w", err)
		}
		if row.ConnAppID == 0 || s.d.DB.First(&conn, row.ConnAppID).Error != nil {
			return f, nil, refused("the circle's listing connection was revoked")
		}
		secret, err := s.d.Keys.GetCashPairingKey(conn.ID)
		if err != nil {
			return f, nil, err
		}
		stringRelays = cleanRelays(s.d.Settings.GetRelayUrls())
		encoded, err := s.d.CircleString(ctx, *hub.WalletPubkey, stringRelays, secret, label(row.Title))
		if err != nil {
			return f, nil, fmt.Errorf("%w: %v", ErrCannotSign, err)
		}
		c, err := nipcw.DecodeCircleHubConnection(encoded)
		if err != nil {
			return f, nil, err
		}
		f.Circle = &c
		cfg, err := s.circleConfig(hub.ID)
		if err != nil {
			return f, nil, err
		}
		switch cfg.CircleIdentity.Policy {
		case db.CirclePolicyAllowlist:
			f.Summary = []string{nmlisting.SummaryAllowlist}
		case db.CirclePolicyFollowSet:
			f.Summary = []string{nmlisting.SummaryFollowSet}
			if row.FollowSetA {
				f.FollowSet = cfg.CircleIdentity.FollowSetAddress()
			}
		}
	default:
		return f, nil, fmt.Errorf("unknown listing type %q", row.Type)
	}
	f.Title, f.Content, f.Image, f.Location = row.Title, row.Content, row.Image, row.Location
	f.Languages, f.Geohashes = decodeList(row.Languages), decodeList(row.Geohashes)
	f.Status = nmlisting.Status(row.Status)
	if row.OperatorPubkey != nil {
		f.Operator = &nmlisting.Ref{Pubkey: *row.OperatorPubkey, Relay: s.operatorRelayHint(ctx, row)}
	}
	return f, stringRelays, nil
}

// operatorRelayHint is the p tag's relay hint (OQ-RC6): the one entered, else the
// first write relay of the operator's newest 10002, else the first listing relay.
func (s *Service) operatorRelayHint(ctx context.Context, row *db.OfferListing) string {
	if row.OperatorRelay != nil && *row.OperatorRelay != "" {
		return *row.OperatorRelay
	}
	if w := s.writeRelaysOf(ctx, *row.OperatorPubkey, s.lookupRelays()); len(w) > 0 {
		return w[0]
	}
	if l := s.ListingRelays(); len(l) > 0 {
		return l[0]
	}
	return ""
}

// lookupRelays are where lokihub reads other people's lists: the listing and
// general relays.
func (s *Service) lookupRelays() []string {
	return union(s.ListingRelays(), s.d.Settings.GetGeneralRelayUrls())
}

// targets are the relays a version goes to (WP-25 item 3; H-CASH-437,
// H-CASH-464): the listing relays, the operator's write relays when p is set,
// and the TLV 1 relays.
func (s *Service) targets(ctx context.Context, row *db.OfferListing, stringRelays []string) []string {
	var operator []string
	if row.OperatorPubkey != nil {
		operator = s.writeRelaysOf(ctx, *row.OperatorPubkey, s.lookupRelays())
	}
	return union(s.ListingRelays(), operator, stringRelays)
}

func label(title string) string {
	const maxLabelBytes = 255
	if len(title) <= maxLabelBytes {
		return title
	}
	cut := maxLabelBytes
	for cut > 0 && (title[cut]&0xC0) == 0x80 {
		cut--
	}
	return title[:cut]
}

func (s *Service) signer(ctx context.Context) func(*nip01.Event) error {
	return func(ev *nip01.Event) error { return s.d.Signer.Sign(ctx, ev) }
}

// publishVersion builds, signs and publishes the next version of a listed offer,
// then records it. wait lets an admin call wait briefly for the clock to pass
// the previous created_at; otherwise the publish goes to the scheduler.
func (s *Service) publishVersion(ctx context.Context, id uint, wait bool) error {
	unlock := lockListing(id)
	defer unlock()
	row, err := s.load(id)
	if err != nil {
		return err
	}
	if row.Phase != db.ListingPhaseListed {
		return nil
	}
	createdAt := s.now()
	if createdAt <= row.LastCreatedAt {
		gap := time.Duration(row.LastCreatedAt+1-createdAt) * time.Second
		if !wait || gap > maxWaitForClock {
			return errors.Join(errDeferred, s.d.DB.Model(row).Update("due_at", row.LastCreatedAt+1).Error)
		}
		select {
		case <-time.After(gap):
		case <-ctx.Done():
			return ctx.Err()
		}
		if createdAt = s.now(); createdAt <= row.LastCreatedAt {
			return errors.Join(errDeferred, s.d.DB.Model(row).Update("due_at", row.LastCreatedAt+1).Error)
		}
	}
	fields, stringRelays, err := s.build(ctx, row)
	if err != nil {
		return err
	}
	fields.Expiration = createdAt + ExpirationSecs
	ev, err := nmlisting.NewListing(fields, createdAt, s.now(), s.signer(ctx))
	if err != nil {
		if errors.Is(err, nmlisting.ErrListingField) || errors.Is(err, nmlisting.ErrGeohashPrecision) {
			return refused("%v", err)
		}
		return fmt.Errorf("%w: %v", ErrCannotSign, err)
	}
	answers := s.d.Relays.Publish(ctx, s.targets(ctx, row, stringRelays), ev)
	return s.recordVersion(row.ID, ev, answers)
}

// recordVersion stores a published version and its relays' answers, and when it
// is next due.
func (s *Service) recordVersion(id uint, ev *nip01.Event, answers map[string]error) error {
	raw, _ := json.Marshal(ev)
	now := s.now()
	createdAt := int64(ev.CreatedAt) //nolint:gosec // a unix time
	return s.d.DB.Transaction(func(tx *gorm.DB) error {
		row, err := lockRow(tx, id)
		if err != nil {
			return err
		}
		ok := acceptedRelays(answers)
		updates := map[string]any{
			"last_created_at": createdAt,
			"last_event_id":   ev.ID,
			"last_event_json": string(raw),
			"relays_ok":       len(ok),
			"relays_total":    len(answers),
		}
		if st := eventStatus(ev); st == db.ListingStatusOpen || st == db.ListingStatusPaused {
			exp := createdAt + ExpirationSecs
			updates["last_open_created_at"] = createdAt
			updates["last_open_expires_at"] = exp
			updates["open_relays"] = encodeList(union(decodeList(row.OpenRelays), ok))
		}
		s.backoff(row, updates, len(ok) > 0, createdAt+RepublishSecs, now, "listing version")
		return tx.Model(&db.OfferListing{}).Where("id = ?", id).Updates(updates).Error
	})
}

// backoff sets the next due time: next when the publish was accepted somewhere,
// else 30 s doubling to 10 min, with an ERROR after an hour unaccepted.
func (s *Service) backoff(row *db.OfferListing, updates map[string]any, ok bool, next, now int64, what string) {
	if ok {
		updates["failing_since"] = nil
		updates["retry_delay"] = 0
		updates["due_at"] = next
		return
	}
	since := now
	if row.FailingSince != nil {
		since = *row.FailingSince
	}
	delay := row.RetryDelay * 2
	if delay < retryFirst {
		delay = retryFirst
	}
	delay = min(delay, retryMax)
	updates["failing_since"] = since
	updates["retry_delay"] = delay
	updates["due_at"] = now + delay
	publishFailures.Add(1)
	if now-since >= alertAfter {
		logger.Logger.Error().Uint("listing_id", row.ID).Str("what", what).Int64("failing_secs", now-since).
			Msg("No relay has accepted this listing event for over an hour")
	} else {
		logger.Logger.Warn().Uint("listing_id", row.ID).Str("what", what).Int64("retry_in", delay).
			Msg("No relay accepted the listing event; retrying")
	}
}

func acceptedRelays(answers map[string]error) []string {
	var ok []string
	for u, err := range answers {
		if err == nil {
			ok = append(ok, u)
		}
	}
	slices.Sort(ok)
	return ok
}

func eventStatus(ev *nip01.Event) string {
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == "status" {
			return t[1]
		}
	}
	return ""
}

func setTag(ev *nip01.Event, name, value string) {
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == name {
			t[1] = value
			return
		}
	}
}

func ptr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// close publishes the closed version (WP-25 item 8 (b); H-CASH-417): every tag
// of the last version kept, status closed, expiration at least the last open
// version's effective expiry. The deletion follows at that expiry (c).
func (s *Service) close(ctx context.Context, id uint) error {
	unlock := lockListing(id)
	defer unlock()
	row, err := s.load(id)
	if err != nil {
		return err
	}
	if row.Phase != db.ListingPhaseListed {
		return nil
	}
	now := s.now()
	if row.LastEventJSON == "" || row.LastOpenExpiresAt == nil {
		// Never published: nothing to retract.
		return s.d.DB.Model(row).Updates(map[string]any{
			"status": db.ListingStatusClosed, "phase": db.ListingPhaseDeleted,
			"closed_at": now, "deleted_at": now, "due_at": neverDue,
		}).Error
	}
	createdAt := now
	if createdAt <= row.LastCreatedAt {
		createdAt = row.LastCreatedAt + 1
		if wait := time.Duration(createdAt-now) * time.Second; wait <= maxWaitForClock {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			// Mark it closing now; the scheduler signs the closed version once
			// the clock passes the last one.
			return s.d.DB.Model(row).Updates(map[string]any{
				"status": db.ListingStatusClosed, "phase": db.ListingPhaseClosing,
				"closed_at": now, "delete_due_at": *row.LastOpenExpiresAt, "due_at": createdAt,
				"closed_relays": "",
			}).Error
		}
	}
	return s.publishClosed(ctx, row, createdAt)
}

// publishClosed signs and sends the closed version of row at createdAt.
func (s *Service) publishClosed(ctx context.Context, row *db.OfferListing, createdAt int64) error {
	var prevEv nip01.Event
	if err := json.Unmarshal([]byte(row.LastEventJSON), &prevEv); err != nil {
		return err
	}
	prev := prevEv.Copy()
	lastOpenExp := *row.LastOpenExpiresAt
	exp := min(max(lastOpenExp, createdAt+1), createdAt+ExpirationSecs)
	setTag(prev, "expiration", strconv.FormatInt(exp, 10))
	ev, err := nmlisting.ClosedListing(&nmlisting.Listing{Event: prev, NodeIdentity: prev.PubKey}, createdAt, s.signer(ctx))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCannotSign, err)
	}
	answers := s.d.Relays.Publish(ctx, union(s.ListingRelays(), decodeList(row.OpenRelays)), ev)
	raw, _ := json.Marshal(ev)
	now := s.now()
	return s.d.DB.Transaction(func(tx *gorm.DB) error {
		cur, err := lockRow(tx, row.ID)
		if err != nil {
			return err
		}
		ok := acceptedRelays(answers)
		updates := map[string]any{
			"status": db.ListingStatusClosed, "phase": db.ListingPhaseClosing,
			"last_created_at": createdAt, "last_event_id": ev.ID, "last_event_json": string(raw),
			"closed_relays": encodeList(ok), "relays_ok": len(ok), "relays_total": len(answers),
			"delete_due_at": lastOpenExp,
		}
		if cur.ClosedAt == nil {
			updates["closed_at"] = now
		}
		cur.ClosedRelays = encodeList(ok)
		s.scheduleClosing(cur, updates, lastOpenExp, now)
		return tx.Model(&db.OfferListing{}).Where("id = ?", row.ID).Updates(updates).Error
	})
}

// pendingClosed are the relays that accepted an open or paused version but not
// yet the closed one (U-25.RC4).
func pendingClosed(row *db.OfferListing) []string {
	closed := decodeList(row.ClosedRelays)
	var out []string
	for _, r := range decodeList(row.OpenRelays) {
		if !slices.Contains(closed, r) {
			out = append(out, r)
		}
	}
	return out
}

// scheduleClosing is when a closing row is next due: a re-send while some relay
// still lacks the closed version, else the deletion.
func (s *Service) scheduleClosing(row *db.OfferListing, updates map[string]any, deleteDue, now int64) {
	if len(pendingClosed(row)) == 0 || now >= deleteDue {
		updates["failing_since"] = nil
		updates["retry_delay"] = 0
		updates["due_at"] = deleteDue
		return
	}
	s.backoff(row, updates, false, deleteDue, now, "closed version")
	if d, _ := updates["due_at"].(int64); d > deleteDue {
		updates["due_at"] = deleteDue
	}
}

// resendClosed re-sends the stored closed version to the relays still lacking it.
func (s *Service) resendClosed(ctx context.Context, row *db.OfferListing) error {
	var ev nip01.Event
	if err := json.Unmarshal([]byte(row.LastEventJSON), &ev); err != nil {
		return err
	}
	answers := s.d.Relays.Publish(ctx, pendingClosed(row), &ev)
	now := s.now()
	return s.d.DB.Transaction(func(tx *gorm.DB) error {
		cur, err := lockRow(tx, row.ID)
		if err != nil {
			return err
		}
		cur.ClosedRelays = encodeList(union(decodeList(cur.ClosedRelays), acceptedRelays(answers)))
		updates := map[string]any{"closed_relays": cur.ClosedRelays}
		s.scheduleClosing(cur, updates, *cur.DeleteDueAt, now)
		return tx.Model(&db.OfferListing{}).Where("id = ?", row.ID).Updates(updates).Error
	})
}

// neverDue parks a row the scheduler has nothing more to do for.
const neverDue = int64(1) << 62

// publishDeletion publishes the NIP-09 deletion of the listing's address at
// delete_due_at (WP-25 item 8 (c); H-CASH-463), then revokes a circle's
// listing connection.
func (s *Service) publishDeletion(ctx context.Context, row *db.OfferListing) error {
	createdAt := s.now()
	if createdAt <= row.LastCreatedAt {
		return s.d.DB.Model(row).Update("due_at", row.LastCreatedAt+1).Error
	}
	identity, err := s.d.Signer.Identity(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCannotSign, err)
	}
	ev, err := nmlisting.NewListingDeletion(nmlisting.Address(identity, row.D), createdAt, s.signer(ctx))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCannotSign, err)
	}
	answers := s.d.Relays.Publish(ctx,
		union(s.ListingRelays(), decodeList(row.OpenRelays), decodeList(row.ClosedRelays)), ev)
	now := s.now()
	if accepted(answers) == 0 {
		updates := map[string]any{}
		s.backoff(row, updates, false, 0, now, "deletion")
		return s.d.DB.Model(&db.OfferListing{}).Where("id = ?", row.ID).Updates(updates).Error
	}
	if err := s.d.DB.Model(&db.OfferListing{}).Where("id = ?", row.ID).Updates(map[string]any{
		"phase": db.ListingPhaseDeleted, "deleted_at": now, "last_created_at": createdAt,
		"due_at": neverDue, "failing_since": nil, "retry_delay": 0,
	}).Error; err != nil {
		return err
	}
	if row.Type == db.ListingTypeCircleWallet {
		return s.revokeConnLocked(row.ID)
	}
	return nil
}
