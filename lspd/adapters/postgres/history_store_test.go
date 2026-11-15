package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// randBytes generates n fresh random bytes, so tests sharing testPool don't
// collide with each other's rows (nodeid/peerid/funding_tx_id/identifier
// are all part of one unique constraint or another in these tables).
func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return buf
}

// randIdentifier generates a fresh, hex-safe forwarding_history identifier
// (a varchar column — raw random bytes cast to a string can contain an
// embedded NUL, which Postgres text columns reject outright).
func randIdentifier(t *testing.T) string {
	t.Helper()
	return "fwd-" + hex.EncodeToString(randBytes(t, 8))
}

func scidPtr(v uint64) *lightning.ShortChannelID {
	s := lightning.ShortChannelID(v)
	return &s
}

// TestHistoryStore_UpdateChannels_InsertThenUpsert covers UpdateChannels'
// CopyFrom-into-temp-table-then-INSERT-ON-CONFLICT path: a first call must
// insert a fresh row, and a second call for the same (nodeid, funding_tx_id,
// funding_tx_outnum) must update alias_scid/confirmed_scid/last_update in
// place rather than erroring or duplicating — but must NOT touch first_seen,
// since that column is deliberately excluded from the UPDATE SET clause.
func TestHistoryStore_UpdateChannels_InsertThenUpsert(t *testing.T) {
	store := NewHistoryStore(testPool)
	ctx := context.Background()

	nodeId := randBytes(t, 33)
	peerId := randBytes(t, 33)
	cp, err := lightning.NewOutPoint(randBytes(t, 32), 0)
	require.NoError(t, err)

	firstUpdate := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	require.NoError(t, store.UpdateChannels(ctx, []*history.ChannelUpdate{{
		NodeID:        nodeId,
		PeerId:        peerId,
		AliasScid:     scidPtr(111),
		ConfirmedScid: nil,
		ChannelPoint:  cp,
		LastUpdate:    firstUpdate,
	}}))

	var firstSeen, lastUpdate time.Time
	var aliasScid, confirmedScid *int64
	row := testPool.QueryRow(ctx,
		`SELECT first_seen, last_update, alias_scid, confirmed_scid FROM public.channels
		 WHERE nodeid=$1 AND funding_tx_id=$2 AND funding_tx_outnum=$3`,
		nodeId, cp.Hash[:], cp.Index)
	require.NoError(t, row.Scan(&firstSeen, &lastUpdate, &aliasScid, &confirmedScid))
	require.NotNil(t, aliasScid)
	assert.EqualValues(t, 111, *aliasScid)
	assert.Nil(t, confirmedScid, "unconfirmed channel must store a NULL confirmed_scid, not zero")

	// Channel confirms: second update carries a confirmed_scid and a later
	// last_update, simulating the next channel-sync tick.
	secondUpdate := time.Now().Truncate(time.Millisecond)
	require.NoError(t, store.UpdateChannels(ctx, []*history.ChannelUpdate{{
		NodeID:        nodeId,
		PeerId:        peerId,
		AliasScid:     scidPtr(111),
		ConfirmedScid: scidPtr(800_000<<40 | 5<<16 | 1),
		ChannelPoint:  cp,
		LastUpdate:    secondUpdate,
	}}))

	var firstSeen2, lastUpdate2 time.Time
	var confirmedScid2 *int64
	row = testPool.QueryRow(ctx,
		`SELECT first_seen, last_update, confirmed_scid FROM public.channels
		 WHERE nodeid=$1 AND funding_tx_id=$2 AND funding_tx_outnum=$3`,
		nodeId, cp.Hash[:], cp.Index)
	require.NoError(t, row.Scan(&firstSeen2, &lastUpdate2, &confirmedScid2))
	require.NotNil(t, confirmedScid2)
	assert.EqualValues(t, 800_000<<40|5<<16|1, *confirmedScid2, "confirmed_scid must be set by the upsert")
	assert.WithinDuration(t, firstSeen, firstSeen2, time.Millisecond, "first_seen must not change on upsert — it isn't in the UPDATE SET clause")
	assert.WithinDuration(t, secondUpdate, lastUpdate2, time.Millisecond, "last_update must advance to the new value on upsert")
}

func TestHistoryStore_UpdateChannels_EmptySliceIsNoop(t *testing.T) {
	store := NewHistoryStore(testPool)
	assert.NoError(t, store.UpdateChannels(context.Background(), nil))
}

// TestHistoryStore_InsertForwards_DoesNotOverwriteOnConflict is the
// regression-shaped test for InsertForwards' `ON CONFLICT DO NOTHING`:
// inserting a forward with an identifier that already exists (same nodeid)
// must silently keep the original row, not overwrite it — InsertForwards is
// for forwards flspd hasn't seen before, UpdateForwards is the one that's
// allowed to mutate existing rows.
func TestHistoryStore_InsertForwards_DoesNotOverwriteOnConflict(t *testing.T) {
	store := NewHistoryStore(testPool)
	ctx := context.Background()
	nodeId := randBytes(t, 33)
	identifier := randIdentifier(t)

	// A fixed past date, not time.Now(): InChannel/OutChannel here (1, 2)
	// don't correspond to any row in `channels`, and sanityCheck's query in
	// revenue_cli_store.go has no nodeid filter — a "now"-timed orphaned
	// forward here would spuriously fail every revenue_cli_store_test.go
	// test using an overlapping "now"-based time window in the same run.
	resolvedTime := time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.InsertForwards(ctx, []*history.Forward{{
		Identifier:   identifier,
		InChannel:    lightning.ShortChannelID(1),
		OutChannel:   lightning.ShortChannelID(2),
		InMloki:      1000,
		OutMloki:     990,
		ResolvedTime: resolvedTime,
	}}, nodeId))

	// Same identifier, different amounts — must be ignored.
	require.NoError(t, store.InsertForwards(ctx, []*history.Forward{{
		Identifier:   identifier,
		InChannel:    lightning.ShortChannelID(1),
		OutChannel:   lightning.ShortChannelID(2),
		InMloki:      9999,
		OutMloki:     9999,
		ResolvedTime: resolvedTime,
	}}, nodeId))

	var inMloki, outMloki int64
	row := testPool.QueryRow(ctx,
		`SELECT amt_mloki_in, amt_mloki_out FROM public.forwarding_history WHERE nodeid=$1 AND identifier=$2`,
		nodeId, identifier)
	require.NoError(t, row.Scan(&inMloki, &outMloki))
	assert.EqualValues(t, 1000, inMloki, "InsertForwards must not overwrite an existing row's amounts")
	assert.EqualValues(t, 990, outMloki)
}

// TestHistoryStore_UpdateForwards_OverwritesOnConflict is InsertForwards'
// counterpart: UpdateForwards must overwrite an existing row for the same
// (nodeid, identifier), which is how flspd corrects an already-recorded
// forward (e.g. once its final resolved amount is known).
func TestHistoryStore_UpdateForwards_OverwritesOnConflict(t *testing.T) {
	store := NewHistoryStore(testPool)
	ctx := context.Background()
	nodeId := randBytes(t, 33)
	identifier := randIdentifier(t)
	// Fixed past date — see the comment in
	// TestHistoryStore_InsertForwards_DoesNotOverwriteOnConflict for why.
	resolvedTime := time.Date(2019, 6, 2, 0, 0, 0, 0, time.UTC)

	require.NoError(t, store.InsertForwards(ctx, []*history.Forward{{
		Identifier:   identifier,
		InChannel:    lightning.ShortChannelID(1),
		OutChannel:   lightning.ShortChannelID(2),
		InMloki:      1000,
		OutMloki:     990,
		ResolvedTime: resolvedTime,
	}}, nodeId))

	newResolvedTime := resolvedTime.Add(time.Minute)
	require.NoError(t, store.UpdateForwards(ctx, []*history.Forward{{
		Identifier:   identifier,
		InChannel:    lightning.ShortChannelID(3),
		OutChannel:   lightning.ShortChannelID(4),
		InMloki:      5000,
		OutMloki:     4900,
		ResolvedTime: newResolvedTime,
	}}, nodeId))

	var inMloki, outMloki, chanIn, chanOut int64
	row := testPool.QueryRow(ctx,
		`SELECT amt_mloki_in, amt_mloki_out, chanid_in, chanid_out FROM public.forwarding_history WHERE nodeid=$1 AND identifier=$2`,
		nodeId, identifier)
	require.NoError(t, row.Scan(&inMloki, &outMloki, &chanIn, &chanOut))
	assert.EqualValues(t, 5000, inMloki, "UpdateForwards must overwrite the existing row")
	assert.EqualValues(t, 4900, outMloki)
	assert.EqualValues(t, 3, chanIn)
	assert.EqualValues(t, 4, chanOut)
}

func TestHistoryStore_FetchLndForwardOffset_NoRows(t *testing.T) {
	store := NewHistoryStore(testPool)
	nodeId := randBytes(t, 33) // never used by any forward

	offset, err := store.FetchLndForwardOffset(context.Background(), nodeId)
	assert.NoError(t, err)
	assert.Nil(t, offset)
}

func TestHistoryStore_FetchLndForwardOffset_ReturnsMostRecent(t *testing.T) {
	store := NewHistoryStore(testPool)
	ctx := context.Background()
	nodeId := randBytes(t, 33)

	// Fixed past dates — see the comment in
	// TestHistoryStore_InsertForwards_DoesNotOverwriteOnConflict for why.
	older := time.Date(2019, 6, 3, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2019, 6, 3, 1, 0, 0, 0, time.UTC)
	require.NoError(t, store.InsertForwards(ctx, []*history.Forward{
		{Identifier: randIdentifier(t), InChannel: 1, OutChannel: 2, InMloki: 1, OutMloki: 1, ResolvedTime: older},
		{Identifier: randIdentifier(t), InChannel: 1, OutChannel: 2, InMloki: 1, OutMloki: 1, ResolvedTime: newer},
	}, nodeId))

	offset, err := store.FetchLndForwardOffset(ctx, nodeId)
	require.NoError(t, err)
	require.NotNil(t, offset)
	assert.Equal(t, newer.UnixNano(), offset.UnixNano(), "offset must be the MAX resolved_time, not e.g. the first or last inserted row")
}

func TestHistoryStore_AddOpenChannelHtlc(t *testing.T) {
	store := NewHistoryStore(testPool)
	ctx := context.Background()

	nodeId := randBytes(t, 33)
	peerId := randBytes(t, 33)
	cp, err := lightning.NewOutPoint(randBytes(t, 32), 3)
	require.NoError(t, err)
	forwardTime := time.Unix(0, time.Now().UnixNano())

	require.NoError(t, store.AddOpenChannelHtlc(ctx, &history.OpenChannelHtlc{
		NodeId:              nodeId,
		PeerId:              peerId,
		ChannelPoint:        cp,
		OriginalAmountMloki: 6_000_000,
		ForwardAmountMloki:  5_990_000,
		IncomingAmountMloki: 6_000_000,
		ForwardTime:         forwardTime,
	}))

	var gotPeerId []byte
	var forwardAmt, originalAmt, incomingAmt int64
	var fundingTxId []byte
	var fundingTxOutnum int64
	row := testPool.QueryRow(ctx,
		`SELECT peerid, forward_amt_mloki, original_amt_mloki, incoming_amt_mloki, funding_tx_id, funding_tx_outnum
		 FROM public.open_channel_htlcs WHERE nodeid=$1`, nodeId)
	require.NoError(t, row.Scan(&gotPeerId, &forwardAmt, &originalAmt, &incomingAmt, &fundingTxId, &fundingTxOutnum))
	assert.Equal(t, peerId, gotPeerId)
	assert.EqualValues(t, 5_990_000, forwardAmt)
	assert.EqualValues(t, 6_000_000, originalAmt)
	assert.EqualValues(t, 6_000_000, incomingAmt)
	assert.Equal(t, cp.Hash[:], fundingTxId)
	assert.EqualValues(t, cp.Index, fundingTxOutnum)
}
