package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAnnounceStore is an in-memory ports.AnnounceStateStore using the real
// transition rules.
type fakeAnnounceStore struct {
	states map[string]*core.AnnounceState
	err    error
}

func (f *fakeAnnounceStore) GetAnnounceState(_ context.Context, nodeID []byte) (*core.AnnounceState, error) {
	if f.err != nil {
		return nil, f.err
	}
	if st, ok := f.states[string(nodeID)]; ok {
		return st, nil
	}
	return &core.AnnounceState{NodeID: nodeID, Status: core.AnnounceOpen}, nil
}

func (f *fakeAnnounceStore) ApplyAnnounceAction(ctx context.Context, nodeID []byte, a core.AnnounceAction) (*core.AnnounceState, error) {
	cur, err := f.GetAnnounceState(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	next, err := core.NextAnnounceStatus(cur.Status, a)
	if err != nil {
		return nil, err
	}
	st := &core.AnnounceState{NodeID: nodeID, Status: next, UpdatedAt: time.Unix(1792713600, 0)}
	f.states[string(nodeID)] = st
	return st, nil
}

func (f *fakeAnnounceStore) MarkClosedPublished(_ context.Context, nodeID []byte, at time.Time) (bool, error) {
	st, ok := f.states[string(nodeID)]
	if !ok || st.Status != core.AnnounceClosed || !st.ClosedPublishedAt.IsZero() {
		return false, nil
	}
	st.ClosedPublishedAt = at
	return true, nil
}

func (f *fakeAnnounceStore) MarkPublished(context.Context, []byte, time.Time, []byte) error {
	return nil
}
func (f *fakeAnnounceStore) SetLastCreatedAt(context.Context, []byte, int64) error { return nil }
func (f *fakeAnnounceStore) MarkOffClosedPublished(context.Context, []byte, time.Time) error {
	return nil
}
func (f *fakeAnnounceStore) ClearOffClosed(context.Context, []byte) error            { return nil }
func (f *fakeAnnounceStore) SetSelfCheckBlock(context.Context, []byte, string) error { return nil }

func announceStateHandlers(t *testing.T, announcing bool) (*Handlers, *fakeAnnounceStore, *[]*core.AnnounceState) {
	t.Helper()
	cfg := &config.NodeConfig{Name: "Node A"}
	if announcing {
		cfg.Announce = &config.AnnounceConfig{Enabled: true, Addrs: []string{"203.0.113.7:9735"}}
	}
	node := &core.Node{NodeId: []byte{0x02, 0xaa}, NodeConfig: config.NewNodeConfigRef(cfg)}
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{node})

	s := NewHandlers(mockNodes, nil, nil, nil, "")
	store := &fakeAnnounceStore{states: map[string]*core.AnnounceState{}}
	var changes []*core.AnnounceState
	s.SetAnnounceState(store, func(st *core.AnnounceState) { changes = append(changes, st) })
	s.SetAnnounce([]string{"wss://relay.example.com"}, nil)
	return s, store, &changes
}

func call(t *testing.T, s *Handlers, method, params string) (*AnnounceStateResponse, *lsps.JsonRpcError) {
	t.Helper()
	req := &lsps.JsonRpcRequest{Id: json.RawMessage(`1`), Method: method}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	resp := s.Route(req)
	require.NotNil(t, resp)
	if resp.Error != nil {
		return nil, resp.Error
	}
	out, ok := resp.Result.(AnnounceStateResponse)
	require.True(t, ok)
	return &out, nil
}

func TestAnnounceState_GetDefault(t *testing.T) {
	s, _, _ := announceStateHandlers(t, true)
	got, rpcErr := call(t, s, "get_announce_state", "")
	require.Nil(t, rpcErr)
	assert.Equal(t, "open", got.Status)
	assert.True(t, got.Enabled)
	assert.Nil(t, got.UpdatedAt)
	assert.Equal(t, "02aa", got.NodePubkey)
	assert.Equal(t, []string{"wss://relay.example.com"}, got.Relays)

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"updated_at":null`)
}

func TestAnnounceState_PauseResumeUnlist(t *testing.T) {
	s, _, changes := announceStateHandlers(t, true)

	got, rpcErr := call(t, s, "set_announce_state", `{"action":"pause"}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, "paused", got.Status)
	require.NotNil(t, got.UpdatedAt)
	assert.Equal(t, "2026-10-23T00:00:00Z", *got.UpdatedAt)

	got, _ = call(t, s, "get_announce_state", "")
	assert.Equal(t, "paused", got.Status)

	got, rpcErr = call(t, s, "set_announce_state", `{"action":"unlist"}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, "closed", got.Status)

	_, rpcErr = call(t, s, "set_announce_state", `{"action":"pause"}`)
	require.NotNil(t, rpcErr, "pause is refused while unlisted")
	assert.Equal(t, -32602, rpcErr.Code)

	got, rpcErr = call(t, s, "set_announce_state", `{"action":"resume"}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, "open", got.Status)

	statuses := []core.AnnounceStatus{}
	for _, c := range *changes {
		statuses = append(statuses, c.Status)
	}
	assert.Equal(t, []core.AnnounceStatus{core.AnnouncePaused, core.AnnounceClosed, core.AnnounceOpen}, statuses,
		"the hook sees every applied action, not refused ones")
}

func TestAnnounceState_BadRequests(t *testing.T) {
	s, _, changes := announceStateHandlers(t, false)
	for _, params := range []string{`{"action":"delete"}`, `{}`, `not-json`, `{"action":"open"}`} {
		_, rpcErr := call(t, s, "set_announce_state", params)
		require.NotNil(t, rpcErr, params)
		assert.Equal(t, -32602, rpcErr.Code, params)
	}
	assert.Empty(t, *changes)

	got, _ := call(t, s, "get_announce_state", "")
	assert.False(t, got.Enabled, "state is readable while announcing is off")
}

func TestAnnounceState_StoreErrorsAndUnavailable(t *testing.T) {
	s, store, _ := announceStateHandlers(t, true)
	store.err = errors.New("db down")
	_, rpcErr := call(t, s, "get_announce_state", "")
	require.NotNil(t, rpcErr)
	assert.Equal(t, -32603, rpcErr.Code)
	assert.NotContains(t, rpcErr.Message, "db down", "internal errors are not leaked")

	bare := NewHandlers(new(MockNodesService), nil, nil, nil, "")
	_, rpcErr = call(t, bare, "get_announce_state", "")
	require.NotNil(t, rpcErr)
	assert.Equal(t, -32000, rpcErr.Code)
}

type fakePublisher struct {
	status   *core.AnnouncePublisherStatus
	rechecks int
	triggers int
	known    bool
}

func (f *fakePublisher) Status([]byte) *core.AnnouncePublisherStatus { return f.status }
func (f *fakePublisher) Recheck(context.Context, []byte) error {
	f.rechecks++
	if !f.known {
		return errors.New("the publisher does not handle this node")
	}
	return nil
}
func (f *fakePublisher) Trigger() { f.triggers++ }

func TestAnnounceState_PublisherView(t *testing.T) {
	s, _, _ := announceStateHandlers(t, true)

	got, rpcErr := call(t, s, "get_announce_state", "")
	require.Nil(t, rpcErr)
	assert.Nil(t, got.Publisher, "no publisher connected")

	created := time.Unix(1792713600, 0)
	pub := &fakePublisher{known: true, status: &core.AnnouncePublisherStatus{
		Last: &core.AnnouncePublication{
			EventID: "ab", CreatedAt: created, Expiration: created.Add(30 * 24 * time.Hour), Status: core.AnnouncePaused,
			Relays: []core.AnnounceRelayResult{{Relay: "wss://a", Accepted: true}, {Relay: "wss://b", Error: "blocked", RetryAt: created.Add(time.Minute)}},
		},
		SelfCheck: &core.AnnounceSelfCheck{CheckedAt: created, OK: false, Problems: []string{"lsps2_fee[0].min_fee: announced 1, live 2 (worse for the buyer)"}},
		Blocked:   "self-check failed: lsps2_fee[0].min_fee",
	}}
	s.SetAnnouncePublisher(pub)

	got, rpcErr = call(t, s, "get_announce_state", "")
	require.Nil(t, rpcErr)
	require.NotNil(t, got.Publisher)
	ev := got.Publisher.LastEvent
	require.NotNil(t, ev)
	assert.Equal(t, "ab", ev.ID)
	assert.Equal(t, "2026-10-23T00:00:00Z", ev.CreatedAt)
	assert.Equal(t, "2026-11-22T00:00:00Z", ev.Expiration)
	assert.Equal(t, "paused", ev.Status)
	assert.Equal(t, []AnnounceRelayView{{Relay: "wss://a", Accepted: true}, {Relay: "wss://b", Error: "blocked", RetryAt: "2026-10-23T00:01:00Z"}}, ev.Relays)
	assert.False(t, got.Publisher.SelfCheck.OK)
	assert.Len(t, got.Publisher.SelfCheck.Problems, 1)
	assert.Contains(t, got.Publisher.Blocked, "self-check failed")

	got, rpcErr = call(t, s, "recheck_announcement", "")
	require.Nil(t, rpcErr)
	assert.Equal(t, 1, pub.rechecks)
	assert.Equal(t, "open", got.Status)

	pub.known = false
	_, rpcErr = call(t, s, "recheck_announcement", "")
	require.NotNil(t, rpcErr)
	assert.Equal(t, -32000, rpcErr.Code)
}

func TestHandleUpdateConfig_TriggersPublisher(t *testing.T) {
	node := announceTestNode()
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{node})
	s := NewHandlers(mockNodes, nil, nil, nil, "")
	pub := &fakePublisher{}
	s.SetAnnouncePublisher(pub)

	resp := s.handleUpdateConfig(updateConfigRequest(t, config.NodeConfig{Name: "Node A", TargetConf: 6}))
	require.Nil(t, resp.Error)
	assert.Equal(t, 1, pub.triggers)

	resp = s.handleUpdateConfig(updateConfigRequest(t, config.NodeConfig{Name: "Node A", TargetConf: 6,
		Announce: &config.AnnounceConfig{Enabled: true}}))
	require.NotNil(t, resp.Error)
	assert.Equal(t, 1, pub.triggers, "a refused update does not trigger")
}

func fleetAnnounceHandlers(t *testing.T) (*Handlers, *fakeAnnounceStore, *core.Node, *core.Node) {
	t.Helper()
	on := func(name string) *config.NodeConfigRef {
		return config.NewNodeConfigRef(&config.NodeConfig{Name: name, Announce: &config.AnnounceConfig{Enabled: true, Addrs: []string{"203.0.113.7:9735"}}})
	}
	n1 := &core.Node{NodeId: []byte{0x02, 0x01}, NodeConfig: on("Node 1")}
	n2 := &core.Node{NodeId: []byte{0x03, 0x02}, NodeConfig: on("Node 2")}
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{n1, n2})
	s := NewHandlers(mockNodes, nil, nil, nil, "")
	store := &fakeAnnounceStore{states: map[string]*core.AnnounceState{}}
	s.SetAnnounceState(store, nil)
	return s, store, n1, n2
}

func TestAnnounceState_PausingNode2ChangesOnlyNode2(t *testing.T) {
	s, store, n1, n2 := fleetAnnounceHandlers(t)

	got, rpcErr := call(t, s, "set_announce_state", `{"action":"pause","node":"0302"}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, "0302", got.NodePubkey)
	assert.Equal(t, "paused", got.Status)

	st1, _ := store.GetAnnounceState(context.Background(), n1.NodeId)
	st2, _ := store.GetAnnounceState(context.Background(), n2.NodeId)
	assert.Equal(t, core.AnnounceOpen, st1.Status, "node 1 untouched")
	assert.Equal(t, core.AnnouncePaused, st2.Status)

	got, rpcErr = call(t, s, "get_announce_state", `{"node":"0201"}`)
	require.Nil(t, rpcErr)
	assert.Equal(t, "open", got.Status)
}

func TestAnnounceState_FleetRefusesMissingOrUnknownNode(t *testing.T) {
	s, store, _, _ := fleetAnnounceHandlers(t)
	for _, tc := range []struct{ method, params string }{
		{"set_announce_state", `{"action":"pause"}`},
		{"set_announce_state", `{"action":"pause","node":"02ff"}`},
		{"get_announce_state", ""},
		{"recheck_announcement", `{"node":"02ff"}`},
	} {
		_, rpcErr := call(t, s, tc.method, tc.params)
		require.NotNil(t, rpcErr, "%s %s", tc.method, tc.params)
		assert.Equal(t, -32602, rpcErr.Code)
	}
	assert.Empty(t, store.states, "no node changed")
}
