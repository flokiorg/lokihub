package db

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// InboxRingKey is the user_configs row holding the private-transport inbox key
// ring.
const InboxRingKey = "PrivateTransportInbox"

// InboxRing is the inbox key ring: the index of the key in use and the keys
// retired within their grace.
type InboxRing struct {
	Current uint32         `json:"current"`
	Retired []RetiredInbox `json:"retired,omitempty"`
}

// RetiredInbox is a retired inbox key index, opening envelopes until Until
// (unix seconds).
type RetiredInbox struct {
	Index uint32 `json:"index"`
	Until int64  `json:"until"`
}

// LoadInboxRing reads the ring, without the retired keys whose grace ended by
// now. A node that never rotated is at index 0.
func LoadInboxRing(gormDB *gorm.DB, now time.Time) (InboxRing, error) {
	var rows []UserConfig
	if err := gormDB.Where(&UserConfig{Key: InboxRingKey}).Limit(1).Find(&rows).Error; err != nil {
		return InboxRing{}, err
	}
	var r InboxRing
	if len(rows) == 0 || rows[0].Value == "" {
		return r, nil
	}
	if err := json.Unmarshal([]byte(rows[0].Value), &r); err != nil {
		return InboxRing{}, fmt.Errorf("read the inbox key ring: %w", err)
	}
	live := make([]RetiredInbox, 0, len(r.Retired))
	for _, k := range r.Retired {
		if k.Until > now.Unix() {
			live = append(live, k)
		}
	}
	r.Retired = live
	return r, nil
}

// StoreInboxRing writes the ring.
func StoreInboxRing(gormDB *gorm.DB, r InboxRing) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return gormDB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&UserConfig{}).Where(&UserConfig{Key: InboxRingKey}).Update("value", string(raw))
		if res.Error != nil || res.RowsAffected > 0 {
			return res.Error
		}
		return tx.Create(&UserConfig{Key: InboxRingKey, Value: string(raw)}).Error
	})
}

// CurrentInboxIndex is the index of the inbox key in use: what a record that
// notes the inbox it was made under stores (a circle wallet's
// circle_inbox_key_index).
func CurrentInboxIndex(gormDB *gorm.DB) (uint32, error) {
	r, err := LoadInboxRing(gormDB, time.Now())
	return r.Current, err
}
