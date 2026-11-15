package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revenueChannelFixture is one row in the `channels` table, along with the
// values needed to build the matching wire.OutPoint for assertions.
type revenueChannelFixture struct {
	nodeId        []byte
	peerId        []byte
	fundingTxId   []byte
	fundingOut    int64
	confirmedScid int64
}

func (f revenueChannelFixture) channelPoint(t *testing.T) wire.OutPoint {
	t.Helper()
	cp, err := lightning.NewOutPoint(f.fundingTxId, uint32(f.fundingOut))
	require.NoError(t, err)
	return *cp
}

// insertRevenueChannel inserts a confirmed channels row directly via SQL,
// independent of history_store_test.go's HistoryStore-based fixtures, so
// this file can construct exactly the join keys RevenueCliStore's queries
// need without depending on another store's behavior.
func insertRevenueChannel(t *testing.T, f revenueChannelFixture) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO public.channels (nodeid, peerid, confirmed_scid, funding_tx_id, funding_tx_outnum, first_seen, last_update)
		VALUES ($1, $2, $3, $4, $5, now(), now())
		ON CONFLICT (nodeid, funding_tx_id, funding_tx_outnum) DO NOTHING
	`, f.nodeId, f.peerId, f.confirmedScid, f.fundingTxId, f.fundingOut)
	require.NoError(t, err)
}

// insertTaggedPayment inserts a payments row associating a channel-open
// (identified by funding_tx_id/funding_tx_outnum) with an API token — this
// is what the tokenChannelsCte in revenue_cli_store.go joins against.
func insertTaggedPayment(t *testing.T, fundingTxId []byte, fundingOut int64, token string) {
	t.Helper()
	paymentHash := randBytes(t, 32)
	tag := fmt.Sprintf(`{"apiKeyHash": %q}`, token)
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO public.payments (payment_hash, payment_secret, destination, incoming_amount_mloki, outgoing_amount_mloki, funding_tx_id, funding_tx_outnum, tag)
		VALUES ($1, $2, $3, 0, 0, $4, $5, $6::jsonb)
	`, paymentHash, randBytes(t, 32), randBytes(t, 33), fundingTxId, fundingOut, tag)
	require.NoError(t, err)
}

func insertForward(t *testing.T, identifier string, nodeId []byte, chanidIn, chanidOut, amtIn, amtOut int64, resolvedTime time.Time) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO public.forwarding_history (identifier, resolved_time, nodeid, chanid_in, chanid_out, amt_mloki_in, amt_mloki_out)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, identifier, resolvedTime.UnixNano(), nodeId, chanidIn, chanidOut, amtIn, amtOut)
	require.NoError(t, err)
}

func insertOpenChannelHtlc(t *testing.T, nodeId, peerId, fundingTxId []byte, fundingOut int64, forwardAmt, originalAmt, incomingAmt int64, forwardTime time.Time) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO public.open_channel_htlcs (nodeid, peerid, funding_tx_id, funding_tx_outnum, forward_amt_mloki, original_amt_mloki, forward_time, incoming_amt_mloki)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, nodeId, peerId, fundingTxId, fundingOut, forwardAmt, originalAmt, forwardTime.UnixNano(), incomingAmt)
	require.NoError(t, err)
}

// revenueScenario builds two channels on one node — A (funded by a
// token-tagged client payment) and B (a plain, untagged peer, standing in
// for an "external node" in ExportTokenForwardsForExternalNode's sense) —
// plus a single forwarding_history row routing A -> B, which is enough
// fixture for GetForwards, GetOpenChannelHtlcs, and
// ExportTokenForwardsForExternalNode's "send" direction.
type revenueScenario struct {
	nodeId       []byte
	channelA     revenueChannelFixture // token-tagged
	channelB     revenueChannelFixture // external / untagged
	token        string
	identifier   string
	amtIn        int64
	amtOut       int64
	resolvedTime time.Time
}

func newRevenueScenario(t *testing.T) revenueScenario {
	t.Helper()
	nodeId := randBytes(t, 33)
	s := revenueScenario{
		nodeId: nodeId,
		channelA: revenueChannelFixture{
			nodeId: nodeId, peerId: randBytes(t, 33),
			fundingTxId: randBytes(t, 32), fundingOut: 0,
			confirmedScid: int64(700_000)<<40 | 1<<16,
		},
		channelB: revenueChannelFixture{
			nodeId: nodeId, peerId: randBytes(t, 33),
			fundingTxId: randBytes(t, 32), fundingOut: 1,
			confirmedScid: int64(700_001)<<40 | 1<<16,
		},
		token:        "token-" + randIdentifier(t),
		identifier:   randIdentifier(t),
		amtIn:        1000,
		amtOut:       990,
		resolvedTime: time.Unix(0, time.Now().UnixNano()).Truncate(time.Second),
	}
	insertRevenueChannel(t, s.channelA)
	insertRevenueChannel(t, s.channelB)
	insertTaggedPayment(t, s.channelA.fundingTxId, s.channelA.fundingOut, s.token)
	insertForward(t, s.identifier, s.nodeId, s.channelA.confirmedScid, s.channelB.confirmedScid, s.amtIn, s.amtOut, s.resolvedTime)
	return s
}

func TestRevenueCliStore_GetForwards_HappyPath(t *testing.T) {
	store := NewCliStore(testPool)
	s := newRevenueScenario(t)

	start := uint64(s.resolvedTime.Add(-time.Minute).UnixNano())
	end := uint64(s.resolvedTime.Add(time.Minute).UnixNano())

	forwards, err := store.GetForwards(context.Background(), start, end)
	require.NoError(t, err)

	var found *RevenueForward
	for _, f := range forwards {
		if f.Identifier == s.identifier {
			found = f
		}
	}
	require.NotNil(t, found, "GetForwards must return the fixture forward")
	assert.Equal(t, s.nodeId, found.Nodeid)
	assert.Equal(t, s.channelA.peerId, found.PeeridIn)
	assert.Equal(t, s.channelB.peerId, found.PeeridOut)
	assert.EqualValues(t, s.amtIn, found.AmtMlokiIn)
	assert.EqualValues(t, s.amtOut, found.AmtMlokiOut)
	assert.Equal(t, s.channelA.channelPoint(t), found.ChannelPointIn)
	assert.Equal(t, s.channelB.channelPoint(t), found.ChannelPointOut)
	require.NotNil(t, found.SendToken, "channel A is the inbound leg and is token-tagged, so send_token must be populated")
	assert.Equal(t, s.token, *found.SendToken)
	assert.Nil(t, found.ReceiveToken, "channel B (the outbound leg) isn't tagged")
}

// TestRevenueCliStore_GetForwards_SanityCheckFails covers the guard that
// runs before every query: a forwarding_history row whose channel can't be
// resolved (e.g. channel sync never recorded it) must fail loudly rather
// than silently produce an incomplete report.
//
// sanityCheck's query has no nodeid filter — it scans every forward in the
// requested time range regardless of which node's report is being
// generated. To avoid poisoning every other "now"-windowed test in this
// file with this test's deliberately-orphaned row (which is never cleaned
// up, since these tests share testPool for the whole run), this fixture
// uses a fixed, far-in-the-past resolved_time with a tightly scoped window,
// instead of "now".
func TestRevenueCliStore_GetForwards_SanityCheckFails(t *testing.T) {
	store := NewCliStore(testPool)
	nodeId := randBytes(t, 33)
	resolvedTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	// No channels rows at all for this chanid_in/chanid_out — orphaned forward.
	insertForward(t, randIdentifier(t), nodeId, 123456789, 987654321, 1000, 990, resolvedTime)

	start := uint64(resolvedTime.Add(-time.Minute).UnixNano())
	end := uint64(resolvedTime.Add(time.Minute).UnixNano())

	_, err := store.GetForwards(context.Background(), start, end)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be associated to their channels")
}

func TestRevenueCliStore_GetOpenChannelHtlcs_HappyPath(t *testing.T) {
	store := NewCliStore(testPool)
	s := newRevenueScenario(t)
	insertOpenChannelHtlc(t, s.nodeId, s.channelB.peerId, s.channelB.fundingTxId, s.channelB.fundingOut, 5_990_000, 6_000_000, 6_000_000, s.resolvedTime)

	start := uint64(s.resolvedTime.Add(-time.Minute).UnixNano())
	end := uint64(s.resolvedTime.Add(time.Minute).UnixNano())

	htlcs, err := store.GetOpenChannelHtlcs(context.Background(), start, end)
	require.NoError(t, err)

	var found *OpenChannelHtlc
	for _, h := range htlcs {
		if string(h.Peerid) == string(s.channelB.peerId) {
			found = h
		}
	}
	require.NotNil(t, found, "GetOpenChannelHtlcs must return the htlc for the channel used as chanid_out by a forward in range")
	assert.Equal(t, s.nodeId, found.Nodeid)
	assert.EqualValues(t, 5_990_000, found.ForwardAmountMloki)
	assert.EqualValues(t, 6_000_000, found.OriginalAmountMloki)
	assert.EqualValues(t, 6_000_000, found.IncomingAmountMloki)
	require.NotNil(t, found.ChannelPoint)
	assert.Equal(t, s.channelB.channelPoint(t), *found.ChannelPoint)
}

func TestRevenueCliStore_ExportTokenForwardsForExternalNode_Send(t *testing.T) {
	store := NewCliStore(testPool)
	s := newRevenueScenario(t)

	start := uint64(s.resolvedTime.Add(-time.Minute).UnixNano())
	end := uint64(s.resolvedTime.Add(time.Minute).UnixNano())

	exported, err := store.ExportTokenForwardsForExternalNode(context.Background(), start, end, s.nodeId, s.channelB.peerId)
	require.NoError(t, err)
	require.Len(t, exported, 1)
	assert.Equal(t, s.token, exported[0].Token)
	assert.Equal(t, "send", exported[0].Direction, "token-tagged channel A is the inbound leg, channel B (the queried external node) is outbound -> this is money leaving to the external node")
	assert.EqualValues(t, s.amtOut, exported[0].AmountMloki, "'send' direction reports amt_mloki_out, the amount that left via the external-node leg")
	assert.Equal(t, s.nodeId, exported[0].NodeId)
	assert.Equal(t, s.channelB.peerId, exported[0].ExternalNodeId)
}

// TestRevenueCliStore_ExportTokenForwardsForExternalNode_Receive covers the
// opposite direction: money arrives FROM the external node and is forwarded
// OUT to a token-tagged client channel.
func TestRevenueCliStore_ExportTokenForwardsForExternalNode_Receive(t *testing.T) {
	store := NewCliStore(testPool)
	nodeId := randBytes(t, 33)

	externalChan := revenueChannelFixture{
		nodeId: nodeId, peerId: randBytes(t, 33),
		fundingTxId: randBytes(t, 32), fundingOut: 0,
		confirmedScid: int64(710_000)<<40 | 1<<16,
	}
	clientChan := revenueChannelFixture{
		nodeId: nodeId, peerId: randBytes(t, 33),
		fundingTxId: randBytes(t, 32), fundingOut: 1,
		confirmedScid: int64(710_001)<<40 | 1<<16,
	}
	token := "token-" + randIdentifier(t)
	insertRevenueChannel(t, externalChan)
	insertRevenueChannel(t, clientChan)
	insertTaggedPayment(t, clientChan.fundingTxId, clientChan.fundingOut, token)

	resolvedTime := time.Unix(0, time.Now().UnixNano()).Truncate(time.Second)
	identifier := randIdentifier(t)
	const amtIn, amtOut = 2000, 1980
	insertForward(t, identifier, nodeId, externalChan.confirmedScid, clientChan.confirmedScid, amtIn, amtOut, resolvedTime)

	start := uint64(resolvedTime.Add(-time.Minute).UnixNano())
	end := uint64(resolvedTime.Add(time.Minute).UnixNano())

	exported, err := store.ExportTokenForwardsForExternalNode(context.Background(), start, end, nodeId, externalChan.peerId)
	require.NoError(t, err)
	require.Len(t, exported, 1)
	assert.Equal(t, token, exported[0].Token)
	assert.Equal(t, "receive", exported[0].Direction)
	assert.EqualValues(t, amtIn, exported[0].AmountMloki, "'receive' direction reports amt_mloki_in, the amount that arrived via the external-node leg")
}
