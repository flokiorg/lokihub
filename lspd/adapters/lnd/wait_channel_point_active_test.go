package lnd

import (
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testChannelPoint(t *testing.T) wire.OutPoint {
	t.Helper()
	var h chainhash.Hash
	copy(h[:], []byte("wait-channel-point-active-32byt"))
	return wire.OutPoint{Hash: h, Index: 0}
}

// TestWaitChannelPointActive_AlreadyActiveReturnsImmediately covers the
// no-wait path: if the channel is already active by the time this is
// called, it must return without ever subscribing (no goroutine, no
// signal wait).
func TestWaitChannelPointActive_AlreadyActiveReturnsImmediately(t *testing.T) {
	peerPubkey := "02aabbcc"
	point := testChannelPoint(t)
	fake := &fakeLightningClient{
		channels: []*lnrpc.Channel{
			{RemotePubkey: peerPubkey, ChannelPoint: point.String(), Active: true, ChanId: 100, LocalConstraints: &lnrpc.ChannelConstraints{}},
		},
	}
	c := newTestClient(fake)

	result, err := c.WaitChannelPointActive(mustDecodeHex(t, peerPubkey), point, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.NotNil(t, result)

	// No subscription should have been left registered.
	c.submtx.RLock()
	_, subscribed := c.chansubs[point.String()]
	c.submtx.RUnlock()
	assert.False(t, subscribed, "an already-active channel must not leave a dangling subscription")
}

// TestWaitChannelPointActive_BecomesActiveAfterEvent is the core
// event-driven case (P-1): the channel isn't active yet, so
// WaitChannelPointActive subscribes; once something (in production,
// listenChannelEvents' ACTIVE_CHANNEL handler) sends on that exact
// channel-point's subscription, it must return successfully rather than
// waiting out the deadline.
func TestWaitChannelPointActive_BecomesActiveAfterEvent(t *testing.T) {
	peerPubkey := "02aabbcc"
	point := testChannelPoint(t)
	fake := &fakeLightningClient{
		channels: []*lnrpc.Channel{
			{RemotePubkey: peerPubkey, ChannelPoint: point.String(), Active: false, ChanId: 100, LocalConstraints: &lnrpc.ChannelConstraints{}},
		},
	}
	c := newTestClient(fake)

	// Wait for WaitChannelPointActive to register its subscription, then
	// flip the channel active and fire the same signal
	// listenChannelEvents would.
	go func() {
		for {
			c.submtx.RLock()
			subs, ok := c.chansubs[point.String()]
			var sub chan struct{}
			for _, s := range subs {
				sub = s
			}
			c.submtx.RUnlock()
			if ok && sub != nil {
				// Replace the slice wholesale (a new *lnrpc.Channel, not
				// a mutation of the one ListChannels already handed the
				// caller) so this update can't race a concurrent read of
				// the previously-returned Channel's fields.
				fake.mu.Lock()
				fake.channels = []*lnrpc.Channel{
					{RemotePubkey: peerPubkey, ChannelPoint: point.String(), Active: true, ChanId: 100, LocalConstraints: &lnrpc.ChannelConstraints{}},
				}
				fake.mu.Unlock()
				sub <- struct{}{}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	result, err := c.WaitChannelPointActive(mustDecodeHex(t, peerPubkey), point, time.Now().Add(5*time.Second))
	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestWaitChannelPointActive_DeadlineExceeded covers the timeout path: if
// nothing ever signals, the wait must return once the deadline passes,
// not block forever.
func TestWaitChannelPointActive_DeadlineExceeded(t *testing.T) {
	peerPubkey := "02aabbcc"
	point := testChannelPoint(t)
	fake := &fakeLightningClient{
		channels: []*lnrpc.Channel{
			{RemotePubkey: peerPubkey, ChannelPoint: point.String(), Active: false, ChanId: 100, LocalConstraints: &lnrpc.ChannelConstraints{}},
		},
	}
	c := newTestClient(fake)

	_, err := c.WaitChannelPointActive(mustDecodeHex(t, peerPubkey), point, time.Now().Add(50*time.Millisecond))
	assert.Error(t, err)

	// The subscription must be cleaned up after giving up, not leaked.
	c.submtx.RLock()
	_, subscribed := c.chansubs[point.String()]
	c.submtx.RUnlock()
	assert.False(t, subscribed, "a timed-out wait must clean up its subscription")
}

// TestWaitChannelPointActive_InitialListChannelsErrorPropagates covers
// the case where the very first check fails outright -- it must return
// the error immediately, never subscribing at all.
func TestWaitChannelPointActive_InitialListChannelsErrorPropagates(t *testing.T) {
	peerPubkey := "02aabbcc"
	point := testChannelPoint(t)
	fake := &fakeLightningClient{listChannelsErr: assert.AnError}
	c := newTestClient(fake)

	_, err := c.WaitChannelPointActive(mustDecodeHex(t, peerPubkey), point, time.Now().Add(time.Second))
	assert.Error(t, err)

	c.submtx.RLock()
	_, subscribed := c.chansubs[point.String()]
	c.submtx.RUnlock()
	assert.False(t, subscribed, "an initial lookup error must not leave a subscription registered")
}
