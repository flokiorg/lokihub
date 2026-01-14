package persist

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	err = db.AutoMigrate(&LSPSState{})
	require.NoError(t, err)

	return db
}

func TestGormKVStore_ReadWrite(t *testing.T) {
	db := setupTestDB(t)
	store := NewGormKVStore(db)

	key := "test_key"
	value := []byte("test_value")

	// Read non-existent key
	val, err := store.Read(key)
	require.NoError(t, err)
	require.Nil(t, val)

	// Write key
	err = store.Write(key, value)
	require.NoError(t, err)

	// Read key back
	val, err = store.Read(key)
	require.NoError(t, err)
	require.Equal(t, value, val)

	// Update key
	newValue := []byte("new_value")
	err = store.Write(key, newValue)
	require.NoError(t, err)

	// Read updated key
	val, err = store.Read(key)
	require.NoError(t, err)
	require.Equal(t, newValue, val)
}
