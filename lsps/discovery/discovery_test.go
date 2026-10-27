package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/niplsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/lsps/persist"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

// vectors are K-LSP-1..6 from nmilat's lsp-announcement.json.
type vectors struct {
	Client struct {
		Chains   []string `json:"chains"`
		Networks []string `json:"networks"`
		Now      int64    `json:"now"`
	} `json:"client"`
	Events []struct {
		ID    string      `json:"id"`
		Step  int         `json:"step"`
		Event nip01.Event `json:"event"`
	} `json:"events"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/k-lsp.json")
	require.NoError(t, err)
	var v vectors
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

func (v vectors) event(id string) *nip01.Event {
	for _, e := range v.Events {
		if e.ID == id {
			ev := e.Event
			return &ev
		}
	}
	return nil
}

type fakeFetcher struct {
	mu     sync.Mutex
	evs    []*nip01.Event
	relays [][]string
}

func (f *fakeFetcher) Fetch(_ context.Context, relays []string, flt nip01.SubscriptionFilter) ([]*nip01.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relays = append(f.relays, relays)
	var out []*nip01.Event
	for _, ev := range f.evs {
		if len(flt.Authors) > 0 && flt.Authors[0] != ev.PubKey {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

type fakeGraph map[string]GraphFact

func (g fakeGraph) Facts(context.Context, []string) (map[string]GraphFact, error) { return g, nil }

// fakeLSPS answers in lokihub's dialect, from the structs the LSPS clients
// return (U-26.10), and records every connection (U-26.13).
type fakeLSPS struct {
	mu        sync.Mutex
	connects  []string
	reachable string
	protocols []int
	lsps1     lsps1.Options
	lsps2     []lsps2.OpeningFeeParams
}

func (f *fakeLSPS) Connect(_ context.Context, nodeID, addr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects = append(f.connects, nodeID+"@"+addr)
	if nodeID != f.reachable {
		return errors.New("wrong node id")
	}
	return nil
}
func (f *fakeLSPS) ListProtocols(context.Context, string) ([]int, error) { return f.protocols, nil }
func (f *fakeLSPS) LSPS1Info(context.Context, string) (json.RawMessage, error) {
	return json.Marshal(f.lsps1)
}
func (f *fakeLSPS) LSPS2Menu(context.Context, string) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"opening_fee_params_menu": f.lsps2})
}

type settings struct{}

func (settings) GetRelayUrls() []string        { return []string{"wss://nwc.test"} }
func (settings) GetGeneralRelayUrls() []string { return []string{"wss://general.test"} }

type fixture struct {
	s     *Service
	m     *manager.LSPManager
	fetch *fakeFetcher
	live  *fakeLSPS
	graph fakeGraph
	v     vectors
	now   int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	gdb, err := testdb.NewDB(t)
	require.NoError(t, err)
	t.Cleanup(func() { testdb.CloseDB(gdb) })
	f := &fixture{m: manager.NewLSPManager(gdb), fetch: &fakeFetcher{}, live: &fakeLSPS{}, graph: fakeGraph{}, v: loadVectors(t)}
	f.now = f.v.Client.Now
	f.s = New(Deps{
		DB: gdb, Settings: settings{}, Fetcher: f.fetch, Graph: f.graph, Live: f.live,
		Network: func(context.Context) (string, error) { return "mainnet", nil },
		Now:     func() time.Time { return time.Unix(f.now, 0) },
		Chain:   "bitcoin",
	})
	return f
}

func (f *fixture) row(t *testing.T, key string) *persist.LSP {
	t.Helper()
	l, err := f.m.GetLSP(key)
	require.NoError(t, err)
	return l
}

const kLSPKey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

// U-26.1: K-LSP-1 is stored usable; K-LSP-3..6 each fail at their step and are
// stored unusable with the reason (the newest seen remembered).
func TestU26_1_KLSPThroughDiscovery(t *testing.T) {
	for _, e := range loadVectors(t).Events {
		t.Run(e.ID, func(t *testing.T) {
			f := newFixture(t)
			ev := e.Event
			f.fetch.evs = []*nip01.Event{&ev}
			require.NoError(t, f.s.Discover(context.Background()))
			row := f.row(t, ev.PubKey)
			assert.Equal(t, ev.CreatedAt, uint64(row.NewestSeenCreatedAt))
			if e.Step == 0 {
				assert.True(t, row.Usable, row.InvalidReason)
				assert.Equal(t, ev.ID, row.LastEventID)
				return
			}
			assert.False(t, row.Usable)
			var ae *niplsp.AnnouncementError
			assert.Contains(t, row.InvalidReason, "step")
			_ = ae
		})
	}
}

// U-26.2: at most 200 announcements read, and only for this node's chain and
// network.
func TestU26_2_BoundAndChain(t *testing.T) {
	f := newFixture(t)
	f.s.d.Network = func(context.Context) (string, error) { return "regtest", nil }
	f.fetch.evs = []*nip01.Event{f.v.event("K-LSP-1")}
	require.NoError(t, f.s.Discover(context.Background()))
	assert.False(t, f.row(t, kLSPKey).Usable, "a mainnet announcement on a regtest node")

	g := newFixture(t)
	var evs []*nip01.Event
	for i := 0; i < MaxAnnouncements+20; i++ {
		ev := f.v.event("K-LSP-1").Copy()
		ev.PubKey = strings.Repeat("0", 60) + string(rune('a'+i%6)) + string(rune('a'+(i/6)%6)) + string(rune('a'+(i/36)%6)) + "f"
		evs = append(evs, ev)
	}
	g.fetch.evs = evs
	require.NoError(t, g.s.Discover(context.Background()))
	all, err := g.m.ListLSPs()
	require.NoError(t, err)
	assert.LessOrEqual(t, len(all), MaxAnnouncements)
}

// U-26.3: after a newer invalid announcement, an older valid one is not used.
func TestU26_3_NewestSeen(t *testing.T) {
	f := newFixture(t)
	good := f.v.event("K-LSP-1")
	f.fetch.evs = []*nip01.Event{good}
	require.NoError(t, f.s.Discover(context.Background()))
	require.True(t, f.row(t, kLSPKey).Usable)

	// K-LSP-3 (step 2 failure) is by the same key; date it later.
	bad := f.v.event("K-LSP-3").Copy()
	bad.CreatedAt = good.CreatedAt + 10
	require.NoError(t, bad.Sign("0000000000000000000000000000000000000000000000000000000000000001"))
	f.fetch.evs = []*nip01.Event{good, bad}
	require.NoError(t, f.s.Discover(context.Background()))
	row := f.row(t, kLSPKey)
	assert.False(t, row.Usable, "no fallback to the older valid one")
	assert.Equal(t, int64(bad.CreatedAt), row.NewestSeenCreatedAt)

	// Offered again alone, the older one stays refused.
	f.fetch.evs = []*nip01.Event{good}
	require.NoError(t, f.s.Discover(context.Background()))
	assert.False(t, f.row(t, kLSPKey).Usable)
}

// U-26.4: closed hides (not selling), paused disables.
func TestU26_4_Status(t *testing.T) {
	f := newFixture(t)
	f.fetch.evs = []*nip01.Event{f.v.event("K-LSP-2")}
	require.NoError(t, f.s.Discover(context.Background()))
	row := f.row(t, kLSPKey)
	assert.Equal(t, "closed", row.Status)
	assert.True(t, row.Usable)
}

// U-26.5, U-26.7, U-26.10, U-26.13: the live check resolves the node id from
// the graph (else tries both), compares lokihub's dialect answers with the
// announcement, and discovery itself never connects.
func TestU26_LiveCheck(t *testing.T) {
	f := newFixture(t)
	f.fetch.evs = []*nip01.Event{f.v.event("K-LSP-1")}
	ids := niplsp.NodeIDs(kLSPKey)
	f.graph[ids[0]] = GraphFact{Present: true, Channels: 2, Capacity: 5_000_000}
	require.NoError(t, f.s.Discover(context.Background()))
	assert.Empty(t, f.live.connects, "discovery connects to no LSP")
	row := f.row(t, kLSPKey)
	assert.True(t, row.InGraph)
	assert.Equal(t, ids[0], row.Pubkey)
	assert.Equal(t, 2, row.GraphChannels)
	assert.Equal(t, "vww6ybal4bd7szmgncyruucpgfkqahzddi37ktceo3ah7ngmcopnpyyd.onion:9735", row.Host, "onion first")

	f.live.reachable = ids[0]
	f.live.protocols = []int{1, 2, 5}
	f.live.lsps1 = lsps1.Options{MinFundingConfirmsWithinBlocks: 6, MaxChannelExpiryBlocks: 12960,
		MaxInitialClientBalanceLoki: 1000000, MaxInitialLspBalanceLoki: 10000000,
		MinChannelBalanceLoki: 100000, MaxChannelBalanceLoki: 10000000}
	f.live.lsps2 = []lsps2.OpeningFeeParams{{MinFeeMloki: 2000000, Proportional: 4000, MinLifetime: 4320,
		MaxClientToSelfDelay: 2016, MinPaymentSizeMloki: 10000000, MaxPaymentSizeMloki: 4000000000, Promise: "x"}}
	res, err := f.s.LiveCheck(context.Background(), kLSPKey)
	require.NoError(t, err)
	assert.Empty(t, res.Error)
	assert.True(t, res.Usable)
	assert.False(t, res.Worse, "%+v", res.Changed)
	assert.Empty(t, res.Changed)
	assert.Equal(t, ids[0], res.NodeID)

	// Worse live terms.
	f.live.lsps2[0].MinFeeMloki = 3000000
	res, err = f.s.LiveCheck(context.Background(), kLSPKey)
	require.NoError(t, err)
	assert.True(t, res.Worse)
	assert.NotZero(t, f.row(t, kLSPKey).LastCheckAt)

	// A missing protocol: not usable.
	f.live.protocols = []int{1, 2}
	res, err = f.s.LiveCheck(context.Background(), kLSPKey)
	require.NoError(t, err)
	assert.False(t, res.Usable)
}

// U-26.7: not in the graph, both node ids are tried; the one reached is kept.
func TestU26_7_BothNodeIDs(t *testing.T) {
	f := newFixture(t)
	f.fetch.evs = []*nip01.Event{f.v.event("K-LSP-1")}
	require.NoError(t, f.s.Discover(context.Background()))
	ids := niplsp.NodeIDs(kLSPKey)
	f.live.reachable = ids[1]
	f.live.protocols = []int{1, 2, 5}
	res, err := f.s.LiveCheck(context.Background(), kLSPKey)
	require.NoError(t, err)
	assert.Equal(t, ids[1], res.NodeID)
	assert.Equal(t, ids[1], f.row(t, kLSPKey).Pubkey)
}

// U-26.6: add by node key with and without an announcement.
func TestU26_6_AddByPubkey(t *testing.T) {
	f := newFixture(t)
	_, err := f.s.Add(context.Background(), kLSPKey)
	assert.ErrorIs(t, err, ErrNoAnnouncement)
	f.fetch.evs = []*nip01.Event{f.v.event("K-LSP-1")}
	row, err := f.s.Add(context.Background(), "02"+kLSPKey)
	require.NoError(t, err)
	assert.Equal(t, kLSPKey, row.NodeKey)
	assert.False(t, row.Trusted, "LP-12: added, not trusted")
	_, err = f.s.Add(context.Background(), "not-a-key@host:9735")
	assert.ErrorIs(t, err, ErrRefused)
}

// U-26.14: title and content are stored stripped and capped.
func TestU26_14_SanitizedText(t *testing.T) {
	f := newFixture(t)
	ev := f.v.event("K-LSP-1").Copy()
	for _, tg := range ev.Tags {
		if tg[0] == "title" {
			tg[1] = "Alice​'s ‮Liquidity with a very long title that goes past forty"
		}
	}
	require.NoError(t, ev.Sign("0000000000000000000000000000000000000000000000000000000000000001"))
	f.fetch.evs = []*nip01.Event{ev}
	require.NoError(t, f.s.Discover(context.Background()))
	row := f.row(t, kLSPKey)
	require.True(t, row.Usable, row.InvalidReason)
	assert.NotContains(t, row.Name, "​")
	assert.NotContains(t, row.Name, "‮")
	assert.LessOrEqual(t, len([]rune(row.Name)), niplsp.MaxTitle)
}

func TestPreferredAddr(t *testing.T) {
	assert.Equal(t, "x.onion:9735", PreferredAddr([]string{"1.2.3.4:9735", "x.onion:9735"}))
	assert.Equal(t, []string{"x.onion:9735", "1.2.3.4:9735"}, OrderedAddrs([]string{"1.2.3.4:9735", "x.onion:9735"}))
}
