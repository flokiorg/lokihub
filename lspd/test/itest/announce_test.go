//go:build itest

package itest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/transport/auth"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/niplsp"
	"github.com/stretchr/testify/require"
)

// TestAnnouncePublishesCheckedAnnouncement runs flspd with announcing on
// against the devnet's lsp node and a local relay (T8-5 §4): the event on
// the relay parses and is signed by the node key, its terms equal what the
// node answers over LSPS, a fee-menu change republishes, and Unlist
// publishes closed.
//
//	go test -tags itest -run TestAnnounce -timeout 20m ./test/itest/
func TestAnnouncePublishesCheckedAnnouncement(t *testing.T) {
	d := StartDevnet(t)
	ctx := t.Context()

	relay := NewMockRelay("127.0.0.1:0")
	require.NoError(t, relay.Start(ctx))
	t.Cleanup(func() { _ = relay.Stop() })

	pg, err := NewPostgresContainer(t.TempDir() + "/postgres.log")
	require.NoError(t, err)
	require.NoError(t, pg.Start(ctx))
	t.Cleanup(func() {
		if err := pg.Stop(context.Background()); err != nil {
			t.Logf("stopping postgres: %v", err)
		}
	})

	lspRaw, err := d.RawLightningClient("lsp")
	require.NoError(t, err)
	lspInfo, err := lspRaw.GetInfo(ctx, &lnrpc.GetInfoRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, lspInfo.Chains)
	client := niplsp.CheckOptions{Chains: []string{lspInfo.Chains[0].Chain}, Networks: []string{lspInfo.Chains[0].Network}}

	adminSK := nostr.GeneratePrivateKey()
	adminPK, err := nostr.GetPublicKey(adminSK)
	require.NoError(t, err)

	flspd := NewFlspdProcess(t, d, pg, FlspdProcessOptions{
		JitEnabled: true,
		Announce: &config.AnnounceConfig{
			Enabled:   true,
			Title:     "itest LSP",
			Content:   "devnet announcement",
			Languages: []string{"en"},
			Addrs:     []string{"127.0.0.1:9735"},
		},
		Env: []string{
			"NOSTR_ANNOUNCE_RELAYS=" + relay.Addr(),
			"DASHBOARD_ADMIN_PUBKEY=" + adminPK,
		},
	})
	require.NoError(t, flspd.Start())
	t.Cleanup(func() {
		if err := flspd.Stop(); err != nil {
			t.Logf("stopping flspd: %v", err)
		}
	})

	// 1. The first announcement parses and is the node's.
	first := waitForAnnouncement(t, relay, client, 90*time.Second, func(*niplsp.Announcement) bool { return true })
	require.Equal(t, niplsp.StatusOpen, first.Status)
	nodeIDs := first.NodeIDs()
	require.Contains(t, nodeIDs[:], lspInfo.IdentityPubkey, "signed by the node key (one of the 02/03 ids)")
	require.Equal(t, []string{relay.Addr()}, first.Relays)

	// The 02/03 prefix is resolved by connecting: the matching id is reachable.
	clientRaw, err := d.RawLightningClient("client")
	require.NoError(t, err)
	clientLightning, err := d.LightningClient("client")
	require.NoError(t, err)
	require.NoError(t, ConnectPeer(ctx, clientRaw, lspInfo.IdentityPubkey, "flnd-lsp:9735"))
	lspKey := mustDecodeHex(t, lspInfo.IdentityPubkey)
	require.Eventually(t, func() bool {
		connected, err := clientLightning.IsConnected(lspKey)
		return err == nil && connected
	}, 20*time.Second, 500*time.Millisecond, "client must connect to lsp")

	// 2. The live LSPS answers are the announced ones.
	bus, err := NewCustomMessageBus(ctx, clientRaw)
	require.NoError(t, err)
	live := liveAnswers(t, ctx, bus, lspKey)
	diff := niplsp.CheckLive(first, live)
	require.True(t, diff.Same(), "live answers differ: missing %v, changed %+v", diff.MissingProtocol, diff.Changed())

	// 3. A fee-menu change republishes with the new row.
	_, err = pg.Pool().Exec(ctx, `INSERT INTO public.new_channel_params (validity, params) VALUES (7200,
		'{"min_mloki": "11000000", "proportional": 5000, "max_idle_time": 43200, "max_client_to_self_delay": 4320, "min_payment_size_mloki": "0", "max_payment_size_mloki": "10000000000"}'::jsonb)`)
	require.NoError(t, err)
	changed := waitForAnnouncement(t, relay, client, 90*time.Second, func(a *niplsp.Announcement) bool {
		return a.CreatedAt > first.CreatedAt && hasFeeRow(a, 11_000_000, 5000)
	})
	require.True(t, niplsp.CheckLive(changed, liveAnswers(t, ctx, bus, lspKey)).Same())

	// 4. Unlist publishes closed.
	dashboardCall(t, flspd.DashboardAddress(), adminSK, "set_announce_state", map[string]string{"action": "unlist"})
	closed := waitForAnnouncement(t, relay, client, 60*time.Second, func(a *niplsp.Announcement) bool {
		return a.CreatedAt > changed.CreatedAt && a.Status == niplsp.StatusClosed
	})
	require.False(t, closed.Shown())
}

// waitForAnnouncement returns the newest kind-10735 event on the relay that
// parses for client and satisfies ok.
func waitForAnnouncement(t *testing.T, relay *MockRelay, client niplsp.CheckOptions, timeout time.Duration, ok func(*niplsp.Announcement) bool) *niplsp.Announcement {
	t.Helper()
	var found *niplsp.Announcement
	var refused error
	deadline := time.Now().Add(timeout)
	for found == nil && refused == nil && time.Now().Before(deadline) {
		for _, ev := range relay.Events() {
			if ev.Kind != niplsp.Kind {
				continue
			}
			raw, err := json.Marshal(ev)
			require.NoError(t, err)
			var e nip01.Event
			require.NoError(t, json.Unmarshal(raw, &e))
			client.Now = time.Now().Unix()
			a, err := niplsp.ParseAnnouncement(&e, client)
			if err != nil {
				refused = fmt.Errorf("event %s: %w", e.ID, err)
				break
			}
			if ok(a) && (found == nil || a.CreatedAt > found.CreatedAt) {
				found = a
			}
		}
		if found == nil && refused == nil {
			time.Sleep(time.Second)
		}
	}
	require.NoError(t, refused, "flspd published an announcement a client refuses")
	require.NotNil(t, found, "no matching announcement on the relay within %s", timeout)
	return found
}

func hasFeeRow(a *niplsp.Announcement, minFeeMillis uint64, proportional uint32) bool {
	for _, r := range a.LSPS2 {
		if r.MinFeeMillis == minFeeMillis && uint64(r.Proportional) == uint64(proportional) {
			return true
		}
	}
	return false
}

// liveAnswers asks the LSP what a client asks before buying.
func liveAnswers(t *testing.T, ctx context.Context, bus *CustomMessageBus, lsp []byte) niplsp.Live {
	t.Helper()
	protocols, err := bus.Call(ctx, lsp, "lsps0.list_protocols", struct{}{}, 15*time.Second)
	require.NoError(t, err)
	lsps1, err := bus.Call(ctx, lsp, "lsps1.get_info", struct{}{}, 15*time.Second)
	require.NoError(t, err)
	lsps2, err := bus.Call(ctx, lsp, "lsps2.get_info", struct{}{}, 15*time.Second)
	require.NoError(t, err)
	var live niplsp.Live
	live.Protocols, err = niplsp.ParseListProtocols(protocols)
	require.NoError(t, err)
	live.LSPS1, err = niplsp.ParseLSPS1Info(lsps1)
	require.NoError(t, err)
	live.LSPS2Menu, err = niplsp.ParseLSPS2Menu(lsps2)
	require.NoError(t, err)
	return live
}

// dashboardCall makes one admin JSON-RPC call, NIP-98 signed by sk.
func dashboardCall(t *testing.T, addr, sk, method string, params any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(t, err)
	url := "http://" + addr
	ev := nostr.Event{
		Kind:      auth.KindHttpAuth,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"u", url}, {"m", http.MethodPost}, {"payload", auth.ComputePayloadHash(body)}},
	}
	require.NoError(t, ev.Sign(sk))
	evJSON, err := json.Marshal(ev)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, url+"/", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(evJSON))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Nil(t, out.Error, fmt.Sprintf("%s failed", method))
	return out.Result
}
