// see ./tests/config_test.go for config tests with DB coverage for both SQlite & Postgres
package config

import (
	"strconv"
	"testing"

	"github.com/flokiorg/lokihub/db/migrations"
	"github.com/flokiorg/lokihub/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCheckCache_NoEncryptionKey(t *testing.T) {
	logger.Init(strconv.Itoa(int(4)))

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	err = migrations.Migrate(db)
	require.NoError(t, err)

	cfg, err := NewConfig(&AppConfig{
		Workdir: ".test",
	}, db)
	require.NoError(t, err)

	err = cfg.SetUpdate("key", "value", "")
	require.NoError(t, err)

	require.Equal(t, cfg.cache["key"][""], "")

	value, err := cfg.Get("key", "")
	require.NoError(t, err)
	require.Equal(t, "value", value)

	require.Equal(t, cfg.cache["key"][""], "value")

	// test we can access the cached value without the db
	cfg.db = nil
	value, err = cfg.Get("key", "")
	require.NoError(t, err)
	require.Equal(t, "value", value)
}

func TestCheckUnlockPasswordCache(t *testing.T) {
	logger.Init(strconv.Itoa(int(4)))
	unlockPassword := "123"

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	err = migrations.Migrate(db)
	require.NoError(t, err)

	cfg, err := NewConfig(&AppConfig{
		Workdir: ".test",
	}, db)
	require.NoError(t, err)
	err = cfg.ChangeUnlockPassword("", unlockPassword)
	require.NoError(t, err)
	err = cfg.SaveUnlockPasswordCheck(unlockPassword)
	require.NoError(t, err)

	// check cache
	assert.Nil(t, cfg.cache["UnlockPasswordCheck"])
	// check password
	assert.True(t, cfg.CheckUnlockPassword(unlockPassword))
	assert.NotNil(t, cfg.cache["UnlockPasswordCheck"])

	// check hash cache
	cacheValue, ok := cfg.cache["UnlockPasswordCheck"]
	require.True(t, ok)
	assert.Equal(t, 1, len(cacheValue))

	assert.False(t, cfg.CheckUnlockPassword(unlockPassword+"1"))

	// check hash cache - length should not have changed because decrypt failed with invalid password
	cacheValue2, ok := cfg.cache["UnlockPasswordCheck"]
	require.True(t, ok)
	assert.Equal(t, 1, len(cacheValue2))
	require.Equal(t, cacheValue, cacheValue2)

	// change the password
	newUnlockPassword := unlockPassword + "1"
	err = cfg.ChangeUnlockPassword(unlockPassword, newUnlockPassword)
	require.NoError(t, err)
	assert.Equal(t, 0, len(cfg.cache["UnlockPasswordCheck"]))

	// test we can access the cached value without the db
	assert.True(t, cfg.CheckUnlockPassword(newUnlockPassword))
	assert.NotNil(t, cfg.cache["UnlockPasswordCheck"])
	cfg.db = nil
	assert.True(t, cfg.CheckUnlockPassword(newUnlockPassword))

	// should panic when trying to access the db for an uncached value
	hitPanic := false
	func() {
		defer func() {
			// ensure the app cannot panic if firing events to Loki API fails
			if r := recover(); r != nil {
				hitPanic = true
			}
		}()
		assert.False(t, cfg.CheckUnlockPassword(unlockPassword))
	}()
	assert.True(t, hitPanic)
}

// TestGetRelayUrls_NeverYieldsAnEmptyRelay pins the fix for A-4.
//
// strings.Split("", ",") returns []string{""} — one element holding the empty
// string, not an empty slice — so an unset Relay config used to produce a hub
// whose every minted bill carried a single relay hint of "". That bill is the
// worst available shape: structurally valid, checksum verifies, nipcash.Decode
// accepts it, and the client's own guard misses it too (NewNWCClient rejects
// ZERO relays by name, but "" passes its length check and then passes
// url.Parse, which returns no error for an empty string).
//
// A correctly configured hub must be unaffected, which the real-list cases below
// assert: this may only ever change the misconfigured case.
func TestGetRelayUrls_NeverYieldsAnEmptyRelay(t *testing.T) {
	logger.Init(strconv.Itoa(int(4)))

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.Migrate(db))

	cfg, err := NewConfig(&AppConfig{Workdir: ".test"}, db)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		set  string
		want []string
	}{
		{"unset entirely", "", nil},
		{"empty string", "", nil},
		{"only a comma", ",", nil},
		{"only whitespace", "   ", nil},
		{"whitespace and commas", " , , ", nil},
		{"one real relay", "wss://r1.example", []string{"wss://r1.example"}},
		{"two real relays", "wss://r1.example,wss://r2.example",
			[]string{"wss://r1.example", "wss://r2.example"}},
		{"trailing comma", "wss://r1.example,", []string{"wss://r1.example"}},
		{"padded entries", " wss://r1.example , wss://r2.example ",
			[]string{"wss://r1.example", "wss://r2.example"}},
		{"real relay plus an empty slot", "wss://r1.example,,wss://r2.example",
			[]string{"wss://r1.example", "wss://r2.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, cfg.SetUpdate("Relay", tc.set, ""))
			got := cfg.GetRelayUrls()
			require.Equal(t, tc.want, got)
			for i, u := range got {
				require.NotEmpty(t, u, "entry %d must never be the empty string", i)
			}
		})
	}
}
