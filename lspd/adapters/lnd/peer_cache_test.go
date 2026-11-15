package lnd

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeLightningClient embeds the (large) lnrpc.LightningClient interface
// as a nil value and overrides only ListChannels/ListPeers -- the two
// RPCs refreshPeerCache calls. Any other method being invoked would panic
// on the nil embedded interface, which is exactly the point: it proves
// GetPeerId/IsConnected never fall through to a live RPC call once the
// cache is warm.
type fakeLightningClient struct {
	lnrpc.LightningClient

	mu       sync.Mutex
	channels []*lnrpc.Channel
	peers    []*lnrpc.Peer

	listChannelsErr error
	listPeersErr    error
}

func (f *fakeLightningClient) ListChannels(ctx context.Context, req *lnrpc.ListChannelsRequest, opts ...grpc.CallOption) (*lnrpc.ListChannelsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listChannelsErr != nil {
		return nil, f.listChannelsErr
	}
	return &lnrpc.ListChannelsResponse{Channels: f.channels}, nil
}

func (f *fakeLightningClient) ListPeers(ctx context.Context, req *lnrpc.ListPeersRequest, opts ...grpc.CallOption) (*lnrpc.ListPeersResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listPeersErr != nil {
		return nil, f.listPeersErr
	}
	return &lnrpc.ListPeersResponse{Peers: f.peers}, nil
}

func newTestClient(fake *fakeLightningClient) *LndClient {
	return &LndClient{
		client:         fake,
		peersubs:       make(map[string]map[uint64]chan struct{}),
		chansubs:       make(map[string]map[uint64]chan struct{}),
		scidToPeer:     make(map[uint64][]byte),
		connectedPeers: make(map[string]bool),
	}
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

// TestRefreshPeerCache_PopulatesScidToPeerFromAllThreeScidFields covers
// GetPeerId's original three-way lookup (ChanId, ZeroConfConfirmedScid,
// AliasScids), now served from the cache instead of a live scan.
func TestRefreshPeerCache_PopulatesScidToPeerFromAllThreeScidFields(t *testing.T) {
	peerPubkey := "02" + "11" + "22" // arbitrary well-formed-looking hex
	fake := &fakeLightningClient{
		channels: []*lnrpc.Channel{
			{
				RemotePubkey:          peerPubkey,
				ChanId:                100,
				ZeroConfConfirmedScid: 200,
				AliasScids:            []uint64{300, 301},
			},
		},
	}
	c := newTestClient(fake)
	c.refreshPeerCache()

	wantPeer := mustDecodeHex(t, peerPubkey)
	for _, scid := range []uint64{100, 200, 300, 301} {
		got, err := c.GetPeerId(scidPtr(scid))
		require.NoError(t, err)
		assert.Equal(t, wantPeer, got, "scid %d should resolve to the channel's remote pubkey", scid)
	}

	got, err := c.GetPeerId(scidPtr(999))
	require.NoError(t, err)
	assert.Nil(t, got, "an unknown scid must resolve to nil, not error")
}

// TestRefreshPeerCache_PopulatesConnectedPeers covers IsConnected's cache
// path.
func TestRefreshPeerCache_PopulatesConnectedPeers(t *testing.T) {
	onlinePubkey := "02aabbcc"
	fake := &fakeLightningClient{
		peers: []*lnrpc.Peer{{PubKey: onlinePubkey}},
	}
	c := newTestClient(fake)
	c.refreshPeerCache()

	connected, err := c.IsConnected(mustDecodeHex(t, onlinePubkey))
	require.NoError(t, err)
	assert.True(t, connected)

	connected, err = c.IsConnected(mustDecodeHex(t, "02ddeeff"))
	require.NoError(t, err)
	assert.False(t, connected, "a peer absent from ListPeers must read as not connected")
}

// TestRefreshPeerCache_ReplacesStaleEntries proves refreshPeerCache does
// a full replace, not a merge: a channel/peer present in an earlier
// snapshot but absent from the latest one must disappear from the cache,
// not linger.
func TestRefreshPeerCache_ReplacesStaleEntries(t *testing.T) {
	stalePeer := "02aabbccdd"
	fake := &fakeLightningClient{
		channels: []*lnrpc.Channel{{RemotePubkey: stalePeer, ChanId: 100}},
		peers:    []*lnrpc.Peer{{PubKey: stalePeer}},
	}
	c := newTestClient(fake)
	c.refreshPeerCache()

	got, err := c.GetPeerId(scidPtr(100))
	require.NoError(t, err)
	require.NotNil(t, got, "sanity check: first refresh must have populated the entry")
	connected, err := c.IsConnected(mustDecodeHex(t, stalePeer))
	require.NoError(t, err)
	require.True(t, connected)

	// The channel closed and the peer disconnected: next snapshot has
	// neither.
	fake.channels = nil
	fake.peers = nil
	c.refreshPeerCache()

	got, err = c.GetPeerId(scidPtr(100))
	require.NoError(t, err)
	assert.Nil(t, got, "a closed channel's scid must not resolve after the next refresh")
	connected, err = c.IsConnected(mustDecodeHex(t, stalePeer))
	require.NoError(t, err)
	assert.False(t, connected, "a disconnected peer must read as offline after the next refresh")
}

// TestRefreshPeerCache_ListChannelsErrorLeavesConnectedPeersUpdated
// covers the independence between the two halves of refreshPeerCache: a
// ListChannels failure must not prevent ListPeers' result from still
// being applied (P-2's design deliberately treats the two as separate
// concerns, not an all-or-nothing snapshot).
func TestRefreshPeerCache_ListChannelsErrorLeavesConnectedPeersUpdated(t *testing.T) {
	onlinePubkey := "02aabbcc"
	fake := &fakeLightningClient{
		listChannelsErr: assert.AnError,
		peers:           []*lnrpc.Peer{{PubKey: onlinePubkey}},
	}
	c := newTestClient(fake)
	c.refreshPeerCache()

	connected, err := c.IsConnected(mustDecodeHex(t, onlinePubkey))
	require.NoError(t, err)
	assert.True(t, connected, "ListPeers succeeding must still update connectedPeers even though ListChannels failed")
}

// TestRefreshPeerCache_UndecodableRemotePubkeySkipsChannelNotWholeRefresh
// covers a single malformed channel not aborting the rest of the
// refresh.
func TestRefreshPeerCache_UndecodableRemotePubkeySkipsChannelNotWholeRefresh(t *testing.T) {
	goodPubkey := "02aabbcc"
	fake := &fakeLightningClient{
		channels: []*lnrpc.Channel{
			{RemotePubkey: "not-valid-hex", ChanId: 100},
			{RemotePubkey: goodPubkey, ChanId: 200},
		},
	}
	c := newTestClient(fake)
	c.refreshPeerCache()

	got, err := c.GetPeerId(scidPtr(100))
	require.NoError(t, err)
	assert.Nil(t, got, "the malformed-pubkey channel must simply be skipped")

	got, err = c.GetPeerId(scidPtr(200))
	require.NoError(t, err)
	assert.Equal(t, mustDecodeHex(t, goodPubkey), got, "a later, well-formed channel must still be cached")
}

func scidPtr(v uint64) *lightning.ShortChannelID {
	s := lightning.ShortChannelID(v)
	return &s
}
