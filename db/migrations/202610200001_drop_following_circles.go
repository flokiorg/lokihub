package migrations

import (
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/logger"
)

// legacyCirclePolicyFollowing is the policy NIP-CW replaced with follow_set.
const legacyCirclePolicyFollowing = "following"

// MigrateDropFollowingCircles deletes every Circle Identity using the old
// "following" policy, with the circle hubs that use it and their circle
// wallets. NIP-CW no longer reads a provider's kind:3 list, and nothing maps
// one onto a follow set automatically, so these circles cannot keep working.
//
// Apps are deleted the way DeleteCircleHub deletes them, so their
// permissions, memberships and transactions go with them through the same
// cascades. A second run matches nothing.
func MigrateDropFollowingCircles(db *gorm.DB) error {
	if !db.Migrator().HasTable("circle_identities") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var identityIDs []uint
		if err := tx.Table("circle_identities").
			Where("policy = ?", legacyCirclePolicyFollowing).
			Pluck("id", &identityIDs).Error; err != nil {
			return err
		}
		if len(identityIDs) == 0 {
			return nil
		}

		var hubIDs []uint
		if tx.Migrator().HasTable("circle_hub_configs") {
			if err := tx.Table("circle_hub_configs").
				Where("circle_identity_id IN ?", identityIDs).
				Pluck("app_id", &hubIDs).Error; err != nil {
				return err
			}
		}

		if len(hubIDs) > 0 {
			if err := tx.Exec(`DELETE FROM apps WHERE parent_app_id IN ?`, hubIDs).Error; err != nil {
				return err
			}
			if err := tx.Exec(`DELETE FROM apps WHERE id IN ?`, hubIDs).Error; err != nil {
				return err
			}
			// The config cascades from its app; deleting it here too keeps a
			// database without that cascade from blocking the identity delete.
			if err := tx.Exec(`DELETE FROM circle_hub_configs WHERE circle_identity_id IN ?`, identityIDs).Error; err != nil {
				return err
			}
		}

		if tx.Migrator().HasTable("circle_identity_allowed_pubkeys") {
			if err := tx.Exec(`DELETE FROM circle_identity_allowed_pubkeys WHERE circle_identity_id IN ?`, identityIDs).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`DELETE FROM circle_identities WHERE id IN ?`, identityIDs).Error; err != nil {
			return err
		}

		logger.Logger.Warn().
			Int("identities", len(identityIDs)).
			Int("circle_hubs", len(hubIDs)).
			Msg("Deleted circles using the retired \"following\" policy; recreate them as follow-set circles")
		return nil
	})
}
