package persist

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LSPSState maps to the lsps_states table
type LSPSState struct {
	Key   string `gorm:"primaryKey"`
	Value []byte
}

type GormKVStore struct {
	db *gorm.DB
}

func NewGormKVStore(db *gorm.DB) *GormKVStore {
	return &GormKVStore{db: db}
}

func (s *GormKVStore) Read(key string) ([]byte, error) {
	var state LSPSState
	// Use Find instead of First to avoid "record not found" logs which are annoying for a KV store
	result := s.db.Where("key = ?", key).Find(&state)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to read key %s: %w", key, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return state.Value, nil
}

func (s *GormKVStore) Write(key string, data []byte) error {
	state := LSPSState{
		Key:   key,
		Value: data,
	}
	// Upsert
	result := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&state)

	if result.Error != nil {
		return fmt.Errorf("failed to write key %s: %w", key, result.Error)
	}
	return nil
}
