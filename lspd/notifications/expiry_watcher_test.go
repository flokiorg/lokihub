package notifications

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExpiryLightningClient implements lightning.Client with real logic
// only for ListChannels/GetBlockHeight (all ExpiryWatcher needs); every
// other method is a no-op stub.
type fakeExpiryLightningClient struct {
	channels    []*lightning.Channel
	blockHeight uint32
}

func (f *fakeExpiryLightningClient) ListChannels() ([]*lightning.Channel, error) {
	return f.channels, nil
}
func (f *fakeExpiryLightningClient) GetBlockHeight() (uint32, error) { return f.blockHeight, nil }

func (f *fakeExpiryLightningClient) GetInfo() (*lightning.GetInfoResult, error) { return nil, nil }
func (f *fakeExpiryLightningClient) IsConnected([]byte) (bool, error)           { return false, nil }
func (f *fakeExpiryLightningClient) OpenChannel(*lightning.OpenChannelRequest) (*wire.OutPoint, error) {
	return nil, nil
}
func (f *fakeExpiryLightningClient) UpdateChannelPolicy(wire.OutPoint, *lightning.RoutingPolicy) error {
	return nil
}
func (f *fakeExpiryLightningClient) IsChannelPolicyApplied(uint64, []byte, uint32) (bool, error) {
	return false, nil
}
func (f *fakeExpiryLightningClient) GetChannel([]byte, wire.OutPoint) (*lightning.GetChannelResult, error) {
	return nil, nil
}
func (f *fakeExpiryLightningClient) WaitChannelPointActive([]byte, wire.OutPoint, time.Time) (*lightning.GetChannelResult, error) {
	return nil, nil
}
func (f *fakeExpiryLightningClient) GetPeerId(*lightning.ShortChannelID) ([]byte, error) {
	return nil, nil
}
func (f *fakeExpiryLightningClient) GetClosedChannels(string, map[string]uint64) (map[string]uint64, error) {
	return nil, nil
}
func (f *fakeExpiryLightningClient) WaitOnline([]byte, time.Time) error        { return nil }
func (f *fakeExpiryLightningClient) WaitChannelActive([]byte, time.Time) error { return nil }
func (f *fakeExpiryLightningClient) ListPeers() ([]*lightning.Peer, error)     { return nil, nil }
func (f *fakeExpiryLightningClient) SubscribeInvoices() (<-chan *lightning.InvoiceUpdate, <-chan error, error) {
	return nil, nil, nil
}
func (f *fakeExpiryLightningClient) SubscribeSingleInvoice(context.Context, []byte) (<-chan *lightning.InvoiceUpdate, <-chan error, error) {
	return nil, nil, nil
}
func (f *fakeExpiryLightningClient) LookupInvoiceState(context.Context, string) (lightning.InvoiceState, error) {
	return 0, nil
}
func (f *fakeExpiryLightningClient) LookupInvoiceStateByHash(context.Context, []byte) (lightning.InvoiceState, error) {
	return 0, nil
}
func (f *fakeExpiryLightningClient) SettleInvoice(context.Context, []byte) error { return nil }
func (f *fakeExpiryLightningClient) CancelInvoice(context.Context, []byte) error { return nil }
func (f *fakeExpiryLightningClient) WalletBalance(context.Context) (*lightning.WalletBalanceResult, error) {
	return nil, nil
}
func (f *fakeExpiryLightningClient) QueryRoutes([]byte, uint64, uint64) ([]*lightning.Route, error) {
	return nil, nil
}
func (f *fakeExpiryLightningClient) IsChannelRoutable(uint64) (bool, error) { return false, nil }
func (f *fakeExpiryLightningClient) GetNodeInfo(string) (*lightning.NodeInfo, error) {
	return nil, nil
}

// fakeExpiryNotifier implements Notifier with real logic only for
// NotifyExpirySoon, recording every call for assertion.
type fakeExpiryNotifier struct {
	mu    sync.Mutex
	calls []expirySoonCall
}

type expirySoonCall struct {
	pubkey      string
	blocks      int
	paymentHash string
}

func (f *fakeExpiryNotifier) NotifyExpirySoon(pubkey string, timeout int, _ interface{}, paymentHash string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, expirySoonCall{pubkey: pubkey, blocks: timeout, paymentHash: paymentHash})
	return true, nil
}

func (f *fakeExpiryNotifier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// blocksNotified returns the blocksToExpiry value passed on each call, in
// order. Note NotifyExpirySoon's "timeout" param carries the HTLC's actual
// remaining block count, not which milestone threshold was crossed — a
// single check crossing multiple milestones at once (e.g. blocksToExpiry
// under all three thresholds) reports that same value once per milestone.
func (f *fakeExpiryNotifier) blocksNotified() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, c := range f.calls {
		out = append(out, c.blocks)
	}
	return out
}

func (f *fakeExpiryNotifier) Notify(string, string, interface{}) (bool, error) { return true, nil }
func (f *fakeExpiryNotifier) NotifyAsync(string, string, interface{})          {}
func (f *fakeExpiryNotifier) NotifyWebhookRegistered(string, *core.Lsps5Webhook) bool {
	return true
}
func (f *fakeExpiryNotifier) NotifyOrderStateChanged(string, string, string, string, string) (bool, error) {
	return true, nil
}
func (f *fakeExpiryNotifier) NotifyLiquidityManagementRequest(string, interface{}) (bool, error) {
	return true, nil
}
func (f *fakeExpiryNotifier) NotifyOnionMessageIncoming(string, interface{}) (bool, error) {
	return true, nil
}

func pendingHtlcChannel(peerId []byte, expirationHeight uint32) []*lightning.Channel {
	return []*lightning.Channel{
		{
			PeerId: peerId,
			PendingHtlcs: []lightning.HTLC{
				{
					Incoming:         true,
					HashLock:         []byte{0xaa, 0xbb},
					ExpirationHeight: expirationHeight,
				},
			},
		},
	}
}

func TestCheckExpiries_NotifiesFirstMilestoneCrossed(t *testing.T) {
	// blocksToExpiry = 200 - 100 = 100: crosses 144's threshold (100<=144)
	// but not 72's (100>72) — only the 144 milestone should fire.
	client := &fakeExpiryLightningClient{
		channels:    pendingHtlcChannel([]byte{1, 2, 3}, 200),
		blockHeight: 100,
	}
	notifier := &fakeExpiryNotifier{}
	w := NewExpiryWatcher(client, notifier, nil)

	w.checkExpiries()

	assert.Equal(t, []int{100}, notifier.blocksNotified(), "exactly one notification, for the crossed 144 milestone")
}

// TestCheckExpiries_NotifiesAllMilestonesCrossedInOneCheck is the direct
// regression test for the bug this test file was written alongside fixing:
// checkExpiries used to unconditionally `break` after considering only the
// first (least urgent, 144) threshold, so if a check interval was long
// enough for an HTLC to blow past multiple milestones between checks (or
// simply started already deep into its expiry window), only the 144
// notification would ever fire — 72 ("urgent") and 24 ("critical") were
// unreachable in practice, since threshold 144 is always evaluated first
// and its "already notified" state caused an immediate break on every
// subsequent check.
func TestCheckExpiries_NotifiesAllMilestonesCrossedInOneCheck(t *testing.T) {
	// blocksToExpiry = 220 - 200 = 20: at or below every milestone
	// (144, 72, 24) simultaneously.
	client := &fakeExpiryLightningClient{
		channels:    pendingHtlcChannel([]byte{1, 2, 3}, 220),
		blockHeight: 200,
	}
	notifier := &fakeExpiryNotifier{}
	w := NewExpiryWatcher(client, notifier, nil)

	w.checkExpiries()

	assert.Equal(t, []int{20, 20, 20}, notifier.blocksNotified(),
		"every milestone already crossed (144, 72, and 24) must fire its own notification in a single check")
}

func TestCheckExpiries_EscalatesAcrossMultipleChecks(t *testing.T) {
	peerId := []byte{1, 2, 3}
	client := &fakeExpiryLightningClient{blockHeight: 100}
	notifier := &fakeExpiryNotifier{}
	w := NewExpiryWatcher(client, notifier, nil)

	// First check: 100 blocks to expiry, crosses only 144.
	client.channels = pendingHtlcChannel(peerId, 200)
	w.checkExpiries()
	assert.Equal(t, []int{100}, notifier.blocksNotified())

	// Second check, later: 50 blocks to expiry now, crosses 72 too (and
	// still 144, but that must not fire again).
	client.blockHeight = 150
	w.checkExpiries()
	assert.Equal(t, []int{100, 50}, notifier.blocksNotified(),
		"the second check must escalate to the 72 milestone without re-notifying 144")
}

func TestCheckExpiries_DoesNotReNotifySameMilestoneOnRepeatedChecks(t *testing.T) {
	client := &fakeExpiryLightningClient{
		channels:    pendingHtlcChannel([]byte{1, 2, 3}, 220),
		blockHeight: 200, // 20 blocks to expiry, crosses all 3 milestones
	}
	notifier := &fakeExpiryNotifier{}
	w := NewExpiryWatcher(client, notifier, nil)

	w.checkExpiries()
	require.Equal(t, 3, notifier.callCount())

	w.checkExpiries()
	assert.Equal(t, 3, notifier.callCount(), "an unchanged HTLC must not be re-notified on the next check")
}

func TestCheckExpiries_SkipsAlreadyExpiredHtlcs(t *testing.T) {
	client := &fakeExpiryLightningClient{
		channels:    pendingHtlcChannel([]byte{1, 2, 3}, 100),
		blockHeight: 150, // blocksToExpiry = -50
	}
	notifier := &fakeExpiryNotifier{}
	w := NewExpiryWatcher(client, notifier, nil)

	w.checkExpiries()

	assert.Equal(t, 0, notifier.callCount())
}

func TestCheckExpiries_SkipsOutgoingHtlcs(t *testing.T) {
	client := &fakeExpiryLightningClient{
		channels: []*lightning.Channel{
			{
				PeerId: []byte{1, 2, 3},
				PendingHtlcs: []lightning.HTLC{
					{Incoming: false, HashLock: []byte{0xaa}, ExpirationHeight: 220},
				},
			},
		},
		blockHeight: 200,
	}
	notifier := &fakeExpiryNotifier{}
	w := NewExpiryWatcher(client, notifier, nil)

	w.checkExpiries()

	assert.Equal(t, 0, notifier.callCount(), "outgoing HTLCs are not this node's obligation to notify about")
}

func TestCheckExpiries_EvictsMilestonesForSettledHtlcs(t *testing.T) {
	peerId := []byte{1, 2, 3}
	client := &fakeExpiryLightningClient{
		channels:    pendingHtlcChannel(peerId, 220),
		blockHeight: 200,
	}
	notifier := &fakeExpiryNotifier{}
	w := NewExpiryWatcher(client, notifier, nil)

	w.checkExpiries()
	w.mu.Lock()
	notifiedCount := len(w.notifiedMilestones)
	w.mu.Unlock()
	require.Equal(t, 3, notifiedCount)

	// The HTLC settles/resolves and is no longer pending.
	client.channels = []*lightning.Channel{{PeerId: peerId}}
	w.checkExpiries()

	w.mu.Lock()
	defer w.mu.Unlock()
	assert.Empty(t, w.notifiedMilestones, "milestone entries for a no-longer-pending HTLC must be evicted")
}

// TestExpiryWatcher_StartStop_ReturnsPromptly covers Start/Stop/checkLoop,
// previously entirely untested: checkLoop's real ticker is a 10-minute
// expiryCheckInterval, far too long to wait out in a test, so this instead
// proves the *other* branch of checkLoop's select -- the quit channel --
// actually stops the goroutine immediately rather than only on a future
// tick. A regression here (e.g. checkLoop's select missing the quit case)
// would hang this test until it times out, not silently pass.
func TestExpiryWatcher_StartStop_ReturnsPromptly(t *testing.T) {
	client := &fakeExpiryLightningClient{blockHeight: 1000}
	notifier := &fakeExpiryNotifier{}
	w := NewExpiryWatcher(client, notifier, nil)

	w.Start()

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return promptly -- checkLoop failed to exit on the quit signal")
	}
}
