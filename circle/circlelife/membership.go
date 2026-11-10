package circlelife

import (
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// SuspendAlertPercent is the share of a Hub's Active wallets one resolution may
// suspend before the host is alerted (NIP-CW §Membership: a Hub SHOULD alert the
// host when one resolution would suspend a large share of its wallets).
const SuspendAlertPercent = 20

// HubMembershipChange is what one successful resolution did to one Hub.
type HubMembershipChange struct {
	HubID        uint
	ActiveBefore int
	Suspended    int
	Resumed      int
}

// Alert reports whether the suspensions reached SuspendAlertPercent of the
// Hub's Active wallets.
func (c HubMembershipChange) Alert() bool {
	return c.Suspended > 0 && c.Suspended*100 >= c.ActiveBefore*SuspendAlertPercent
}

// ApplyResolution applies one successful resolution of a Circle Identity's
// membership (isMember) to every wallet of every Hub using it, inside tx
// (NIP-CW §Membership): an Active wallet whose member is no longer named is
// Suspended, its grace starting; a Suspended one whose member is named again
// within its grace returns to Active. An unresolved or failed refresh must
// never reach here. Expired and Closing wallets are not touched.
func ApplyResolution(tx *gorm.DB, identityID uint, isMember func(pubkey string) bool, now time.Time) ([]HubMembershipChange, error) {
	var configs []db.CircleHubConfig
	if err := tx.Where("circle_identity_id = ?", identityID).Find(&configs).Error; err != nil {
		return nil, err
	}
	changes := make([]HubMembershipChange, 0, len(configs))
	for _, cfg := range configs {
		if err := db.LockApp(tx, cfg.AppID); err != nil {
			return nil, err
		}
		var memberships []db.CircleWalletMembership
		if err := tx.Where("circle_hub_app_id = ?", cfg.AppID).Find(&memberships).Error; err != nil {
			return nil, err
		}
		change := HubMembershipChange{HubID: cfg.AppID}
		for _, m := range memberships {
			var wallet db.App
			if err := tx.Where("id = ?", m.WalletAppID).Limit(1).Find(&wallet).Error; err != nil {
				return nil, err
			}
			if wallet.ID == 0 {
				continue
			}
			state := EffectiveState(&wallet, now)
			if state == StateActive {
				change.ActiveBefore++
			}
			named := isMember(m.RequesterPubkey)
			var event Event
			switch {
			case state == StateActive && !named:
				event = EventSuspend
			case state == StateSuspended && named && wallet.CircleGraceEndsAt != nil && now.Unix() < *wallet.CircleGraceEndsAt:
				event = EventResume
			default:
				continue
			}
			if err := Transition(tx, &wallet, event, now, cfg.GraceSecs); err != nil {
				return nil, err
			}
			if event == EventSuspend {
				change.Suspended++
			} else {
				change.Resumed++
			}
		}
		if change.Alert() {
			logger.Logger.Warn().Uint("hub_app_id", cfg.AppID).Int("suspended", change.Suspended).Int("active_before", change.ActiveBefore).
				Msg("One membership resolution suspended a large share of a circle's wallets")
		}
		changes = append(changes, change)
	}
	return changes, nil
}
