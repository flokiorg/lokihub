package tests

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// Unlocking concurrently must yield one JWT secret, not a race.
//
// Two requests can reach the unlock path at once — /api/start and /api/unlock
// both take it, and a browser reconnecting will happily fire both. Without
// synchronisation each can observe no stored secret, generate its own, and
// write it; the loser's secret is overwritten while tokens have already been
// signed with it, so whoever holds those tokens is silently logged out. The
// race detector also flags the unguarded read in GetJWTSecret against the
// write here.
func TestConfig_ConcurrentUnlockYieldsOneJWTSecret(t *testing.T) {
	const password = "correct-horse"
	svc, err := tests.CreateTestServiceWithMnemonic(t, "", password)
	require.NoError(t, err)
	defer svc.Remove()
	cfg := svc.Cfg
	require.NoError(t, cfg.SaveUnlockPasswordCheck(password))

	const goroutines = 8
	var wg sync.WaitGroup
	secrets := make([]string, goroutines)
	errs := make([]error, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			if err := cfg.Unlock(password); err != nil {
				errs[i] = err
				return
			}
			s, err := cfg.GetJWTSecret()
			secrets[i], errs[i] = s, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "goroutine %d", i)
	}
	for i, s := range secrets {
		require.NotEmpty(t, s, "goroutine %d got no secret", i)
		assert.Equal(t, secrets[0], s,
			"every caller must see the same JWT secret — a second one silently invalidates tokens already signed with the first")
	}
}
