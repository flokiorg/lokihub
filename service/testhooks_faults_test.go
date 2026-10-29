//go:build testhooks

package service

import (
	"context"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/hubclock"
	"github.com/flokiorg/lokihub/testknobs"
)

func TestStallCommitFault(t *testing.T) {
	on := true
	t.Cleanup(func() { testknobs.SetFaults(testknobs.Faults{}, true); hubclock.SetOffset(0) })
	transfer := transport.Item{ID: "1", Method: nipcash.MethodCashTransfer}
	notAfter := time.Now().Add(time.Minute).Unix()

	_, held := stallCommitFault(context.Background(), transfer, notAfter)
	assert.False(t, held, "no fault: served")

	testknobs.SetFaults(testknobs.Faults{StallCommit: &on}, false)
	_, held = stallCommitFault(context.Background(), transport.Item{ID: "s", Method: nipcash.MethodCashStatus}, notAfter)
	assert.False(t, held, "a read commits nothing and is not held")

	type out struct {
		res  transport.Result
		held bool
	}
	done := make(chan out, 1)
	go func() {
		res, held := stallCommitFault(context.Background(), transfer, notAfter)
		done <- out{res, held}
	}()
	select {
	case <-done:
		t.Fatal("a commit went on while stalled")
	case <-time.After(50 * time.Millisecond):
	}
	// The Hub's clock passes the deadline while the commit is held.
	hubclock.SetOffset(120)
	testknobs.SetFaults(testknobs.Faults{}, true)
	got := <-done
	require.True(t, got.held)
	require.NotNil(t, got.res.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, got.res.Error.Code)
	assert.Equal(t, "1", got.res.ID)

	// Released inside its deadline, the item is served.
	hubclock.SetOffset(0)
	testknobs.SetFaults(testknobs.Faults{StallCommit: &on}, false)
	go func() {
		res, held := stallCommitFault(context.Background(), transfer, notAfter)
		done <- out{res, held}
	}()
	time.Sleep(20 * time.Millisecond)
	testknobs.SetFaults(testknobs.Faults{}, true)
	assert.False(t, (<-done).held)
}
