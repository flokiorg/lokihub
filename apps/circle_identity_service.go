package apps

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

func (svc *appsService) CreateCircleIdentity(name, policy, providerPubkey string) (*db.CircleIdentity, error) {
	if policy != db.CirclePolicyFollowing && policy != db.CirclePolicyAllowlist {
		return nil, fmt.Errorf("%w: circle policy must be %q or %q", constants.ErrInvalidParams,
			db.CirclePolicyFollowing, db.CirclePolicyAllowlist)
	}
	// providerPubkey is optional (allowlist-policy identities commonly leave
	// it empty), but when set it must be a well-formed 64-char lowercase-hex
	// Nostr pubkey — the same format every other circle write path
	// (ReplaceCircleAllowlist, RemoveCircleAllowedPubkey,
	// create_circle_wallet_controller.go) already enforces, so a malformed
	// value can't slip in through this one earlier entry point and then
	// silently never match any relay filter/allowlist lookup downstream.
	if providerPubkey != "" {
		if len(providerPubkey) != 64 || providerPubkey != strings.ToLower(providerPubkey) {
			return nil, fmt.Errorf("%w: provider_pubkey must be a 64-char lowercase-hex string", constants.ErrInvalidParams)
		}
		if _, err := hex.DecodeString(providerPubkey); err != nil {
			return nil, fmt.Errorf("%w: provider_pubkey must be a 64-char lowercase-hex string", constants.ErrInvalidParams)
		}
	}
	identity := db.CircleIdentity{Name: name, Policy: policy, ProviderPubkey: providerPubkey}
	if err := svc.db.Create(&identity).Error; err != nil {
		return nil, fmt.Errorf("failed to save Circle Identity: %w", err)
	}
	return &identity, nil
}

func (svc *appsService) GetCircleIdentity(id uint) (*db.CircleIdentity, error) {
	var identity db.CircleIdentity
	if err := svc.db.First(&identity, id).Error; err != nil {
		return nil, fmt.Errorf("%w: circle identity %d not found", constants.ErrInvalidParams, id)
	}
	return &identity, nil
}

func (svc *appsService) ListCircleIdentities() ([]db.CircleIdentity, error) {
	var identities []db.CircleIdentity
	if err := svc.db.Order("id asc").Find(&identities).Error; err != nil {
		return nil, err
	}
	return identities, nil
}

// DeleteCircleIdentity refuses to delete an identity that's still referenced by
// any circle_hub — mirrors DeleteApp's circle_hub-with-children guard:
// refuse rather than silently break the providers still relying on it. The
// reference count and the delete run inside one transaction so a concurrent
// CreateCircleHub(ExistingID) can't commit a new reference in between —
// on SQLite/Postgres this holds a write lock on circle_hub_configs for
// the duration, closing the race rather than relying on the FK as a backstop.
func (svc *appsService) DeleteCircleIdentity(id uint) error {
	return svc.db.Transaction(func(tx *gorm.DB) error {
		var referencingCount int64
		if err := tx.Model(&db.CircleHubConfig{}).
			Where("circle_identity_id = ?", id).
			Count(&referencingCount).Error; err != nil {
			return err
		}
		if referencingCount > 0 {
			return fmt.Errorf("%w: circle identity still in use by %d circle_hub app(s)",
				constants.ErrInvalidParams, referencingCount)
		}
		return tx.Delete(&db.CircleIdentity{}, id).Error
	})
}
