package migrations

import (
	"strings"

	"gorm.io/gorm"
)

// MigrateReplaceNostrBandGeneralRelay swaps "wss://relay.nostr.band" for
// "wss://nos.lol" inside an existing install's "GeneralRelay" config value.
// relay.nostr.band proved unreliable for kind:3/NIP-65 discovery in
// practice; nos.lol is now the default (see constants.DefaultGeneralRelays).
// Only touches installs that actually have nostr.band in their current list
// — a user who removed it (or never had it) is left untouched.
func MigrateReplaceNostrBandGeneralRelay(db *gorm.DB) error {
	if !db.Migrator().HasTable("user_configs") {
		return nil // fresh DB; the new default is seeded directly
	}

	var count int64
	if err := db.Table("user_configs").Where("key = ?", "GeneralRelay").Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	var value string
	if err := db.Table("user_configs").Where("key = ?", "GeneralRelay").Pluck("value", &value).Error; err != nil {
		return err
	}

	relays := strings.Split(value, ",")
	changed := false
	for i, r := range relays {
		if strings.TrimSpace(r) == "wss://relay.nostr.band" {
			relays[i] = "wss://nos.lol"
			changed = true
		}
	}
	if !changed {
		return nil
	}

	// Deduplicate in case the user already had nos.lol configured alongside
	// nostr.band, preserving order.
	seen := make(map[string]struct{}, len(relays))
	deduped := make([]string, 0, len(relays))
	for _, r := range relays {
		trimmed := strings.TrimSpace(r)
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		deduped = append(deduped, trimmed)
	}

	return db.Exec(`UPDATE user_configs SET value = ? WHERE key = 'GeneralRelay'`, strings.Join(deduped, ",")).Error
}
