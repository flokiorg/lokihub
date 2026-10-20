package apps

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

// maxFollowSetDLength bounds a follow set's `d` identifier.
const maxFollowSetDLength = 64

// ValidateFollowSetD checks a follow set's `d` identifier: non-empty, at most
// maxFollowSetDLength bytes, no surrounding spaces.
func ValidateFollowSetD(d string) error {
	if strings.TrimSpace(d) == "" {
		return fmt.Errorf("%w: the follow set needs a name (its d identifier)", constants.ErrInvalidParams)
	}
	if d != strings.TrimSpace(d) || len(d) > maxFollowSetDLength {
		return fmt.Errorf("%w: the follow set name must be at most %d characters, without surrounding spaces", constants.ErrInvalidParams, maxFollowSetDLength)
	}
	return nil
}

func (svc *appsService) CreateCircleIdentity(name, policy, providerPubkey, followSetD string) (*db.CircleIdentity, error) {
	if policy != db.CirclePolicyFollowSet && policy != db.CirclePolicyAllowlist {
		return nil, fmt.Errorf("%w: circle policy must be %q or %q", constants.ErrInvalidParams,
			db.CirclePolicyFollowSet, db.CirclePolicyAllowlist)
	}
	// NIP-CW §Data Model: a follow_set circle records the set's address, its
	// provider pubkey and its d identifier. Without both it admits nobody and
	// can never be fixed in place.
	if policy == db.CirclePolicyFollowSet {
		if providerPubkey == "" {
			return nil, fmt.Errorf("%w: a follow_set circle needs the provider_pubkey that publishes the set", constants.ErrInvalidParams)
		}
		if err := ValidateFollowSetD(followSetD); err != nil {
			return nil, err
		}
	} else {
		followSetD = ""
	}
	// providerPubkey is optional for an allowlist circle, but when set it
	// must be a well-formed 64-char lowercase-hex
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
	if policy == db.CirclePolicyFollowSet {
		// Two identities on one address would read the same set, and for a set
		// this hub publishes, each edit would overwrite the other's.
		var existing int64
		if err := svc.db.Model(&db.CircleIdentity{}).
			Where("policy = ? AND provider_pubkey = ? AND follow_set_d = ?", policy, providerPubkey, followSetD).
			Count(&existing).Error; err != nil {
			return nil, err
		}
		if existing > 0 {
			return nil, fmt.Errorf("%w: a circle identity for this follow set already exists, reuse it instead", constants.ErrInvalidParams)
		}
	}
	identity := db.CircleIdentity{Name: name, Policy: policy, ProviderPubkey: providerPubkey, FollowSetD: followSetD}
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
