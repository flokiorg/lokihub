//go:build testhooks

package testknobs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func on() *bool { v := true; return &v }

func TestFaults_StallReleasesWaiters(t *testing.T) {
	t.Cleanup(func() { SetFaults(Faults{}, true) })
	require.NoError(t, WaitCommit(context.Background()), "no fault: no wait")

	SetFaults(Faults{StallCommit: on()}, false)
	assert.True(t, StallCommit())
	done := make(chan error, 1)
	go func() { done <- WaitCommit(context.Background()) }()
	select {
	case <-done:
		t.Fatal("a held commit went on")
	case <-time.After(50 * time.Millisecond):
	}
	got := SetFaults(Faults{}, true)
	assert.False(t, got["stall_commit"])
	require.NoError(t, <-done)

	ctx, cancel := context.WithCancel(context.Background())
	SetFaults(Faults{HoldPayments: on()}, false)
	cancel()
	assert.ErrorIs(t, WaitPayments(ctx), context.Canceled)
}

func TestFaults_RouteOverBoundIsTakenOnce(t *testing.T) {
	t.Cleanup(func() { SetFaults(Faults{}, true) })
	assert.False(t, TakeRouteOverBound())
	SetFaults(Faults{RouteOverBound: on(), BreakSigner: on()}, false)
	assert.True(t, TakeRouteOverBound())
	assert.False(t, TakeRouteOverBound())
	assert.True(t, SignerBroken())
}

func TestLimits_SpinOffWindowAndReset(t *testing.T) {
	t.Cleanup(func() { SetLimits(Limits{}, true) })
	_, set := LiveWalletCap()
	assert.False(t, set)
	assert.False(t, SpinOffRateSet())

	n, per, zero := 2, int64(60), 0
	SetLimits(Limits{LiveWalletCap: &zero, SpinOffRate: &n, PerSecs: &per}, false)
	capN, set := LiveWalletCap()
	assert.True(t, set)
	assert.Zero(t, capN)
	now := time.Now()
	assert.True(t, AllowSpinOff("a", now))
	assert.True(t, AllowSpinOff("a", now))
	assert.False(t, AllowSpinOff("a", now))
	assert.True(t, AllowSpinOff("b", now))
	assert.True(t, AllowSpinOff("a", now.Add(61*time.Second)))

	SetLimits(Limits{}, true)
	assert.False(t, SpinOffRateSet())
}
