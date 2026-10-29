package service

import (
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/logger"
)

// ApplyFollowSetResolution applies a successful resolution of the follow set
// at (provider, d) to every Circle Identity reading it, and through them to
// every Hub's wallets (NIP-CW §Membership): absent members' Active wallets are
// Suspended, members named again within their grace are Active again. A
// resolution suspending a large share of a Hub's wallets alerts the host.
func ApplyFollowSetResolution(gormDB *gorm.DB, eventPublisher events.EventPublisher, provider, d string, isMember func(string) bool, now time.Time) {
	var identities []db.CircleIdentity
	if err := gormDB.Where("policy = ? AND provider_pubkey = ? AND follow_set_d = ?", db.CirclePolicyFollowSet, provider, d).
		Find(&identities).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Circle membership: failed to list the identities of a follow set")
		return
	}
	for _, identity := range identities {
		ApplyIdentityResolution(gormDB, eventPublisher, identity.ID, isMember, now)
	}
}

// ApplyIdentityResolution applies one resolution of an identity's membership in
// its own transaction.
func ApplyIdentityResolution(gormDB *gorm.DB, eventPublisher events.EventPublisher, identityID uint, isMember func(string) bool, now time.Time) {
	var changes []circlelife.HubMembershipChange
	err := gormDB.Transaction(func(tx *gorm.DB) error {
		var err error
		changes, err = circlelife.ApplyResolution(tx, identityID, isMember, now)
		return err
	})
	if err != nil {
		logger.Logger.Error().Err(err).Uint("identity_id", identityID).Msg("Circle membership: failed to apply a resolution")
		return
	}
	publishMembershipAlerts(eventPublisher, changes)
}

// publishMembershipAlerts raises the host alert for each Hub one resolution
// suspended a large share of (no member pubkey in it).
func publishMembershipAlerts(eventPublisher events.EventPublisher, changes []circlelife.HubMembershipChange) {
	for _, c := range changes {
		if !c.Alert() || eventPublisher == nil {
			continue
		}
		eventPublisher.Publish(&events.Event{Event: "nwc_circle_mass_suspension", Properties: map[string]interface{}{
			"hub_app_id": c.HubID, "suspended": c.Suspended, "active_before": c.ActiveBefore,
		}})
	}
}

// retiredInboxKeys are the private keys of every retired inbox key index an
// Active or Suspended Circle Wallet was issued under (NIP-CW §Hidden Members,
// inbox rotation); the current index is not among them.
func retiredInboxKeys(gormDB *gorm.DB, k keys.Keys) []string {
	var indexes []uint32
	if err := gormDB.Model(&db.App{}).
		Where("kind = ? AND circle_inbox_key_index IS NOT NULL AND circle_inbox_key_index <> ? AND circle_state IN ?", db.AppKindCircleWallet,
			constants.PRIVATE_TRANSPORT_KEY_INDEX, []string{"", circlelife.StateActive, circlelife.StateSuspended}).
		Distinct().Pluck("circle_inbox_key_index", &indexes).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Circle membership: failed to list retired inbox keys")
		return nil
	}
	out := make([]string, 0, len(indexes))
	for _, index := range indexes {
		key, err := k.GetPrivateTransportKey(index)
		if err != nil {
			logger.Logger.Error().Err(err).Uint32("index", index).Msg("Circle membership: failed to derive a retired inbox key")
			continue
		}
		out = append(out, key)
	}
	return out
}
