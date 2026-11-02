package itest

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLsps2JitHappyPath drives a full LSPS2 JIT-channel purchase against a
// real flspd binary, real flnd nodes, and a real (private, regtest) chain:
// the client asks the LSP for a quote, creates an invoice with a routing
// hint through the not-yet-real channel, a third party pays it, and flspd's
// HTLC interceptor opens the channel on receipt and forwards the payment.
// This is the Go-level proof for what T4-2's lsps2_jit_happy_path.yaml
// scenario formalizes.
func TestLsps2JitHappyPath(t *testing.T) {
	d := StartDevnet(t)
	ctx := t.Context()

	pg, err := NewPostgresContainer(t.TempDir() + "/postgres.log")
	require.NoError(t, err)
	require.NoError(t, pg.Start(ctx))
	t.Cleanup(func() {
		if err := pg.Stop(context.Background()); err != nil {
			t.Logf("stopping postgres: %v", err)
		}
	})

	flspd := NewFlspdProcess(t, d, pg, FlspdProcessOptions{JitEnabled: true})
	require.NoError(t, flspd.Start())
	t.Cleanup(func() {
		if err := flspd.Stop(); err != nil {
			t.Logf("stopping flspd: %v", err)
		}
	})

	lspClient, err := d.LightningClient("lsp")
	require.NoError(t, err)
	lspInfo, err := lspClient.GetInfo()
	require.NoError(t, err)

	clientLightning, err := d.LightningClient("client")
	require.NoError(t, err)
	clientRaw, err := d.RawLightningClient("client")
	require.NoError(t, err)
	payerLightning, err := d.LightningClient("payer")
	require.NoError(t, err)
	payerRaw, err := d.RawLightningClient("payer")
	require.NoError(t, err)
	payerRouter, err := d.RawRouterClient("payer")
	require.NoError(t, err)

	// flnd (like upstream LND) rejects every incoming channel-open request
	// outright unless the responder has registered a ChannelAcceptor — the
	// client must be running one before the LSP's JIT open ever lands.
	require.NoError(t, AutoAcceptChannels(ctx, clientRaw))

	// Peer connections happen container-to-container over the devnet's own
	// docker network, so the address is the compose service name, not the
	// host-published port my Go process itself dials. ConnectPeer only
	// initiates the connection — wait for it to actually establish before
	// relying on it (e.g. to open a channel), since on a freshly (re)booted
	// devnet it isn't instant.
	require.NoError(t, ConnectPeer(ctx, clientRaw, lspInfo.Pubkey, "flnd-lsp:9735"))
	require.NoError(t, ConnectPeer(ctx, payerRaw, lspInfo.Pubkey, "flnd-lsp:9735"))
	require.Eventually(t, func() bool {
		connected, err := clientLightning.IsConnected(mustDecodeHex(t, lspInfo.Pubkey))
		return err == nil && connected
	}, 20*time.Second, 500*time.Millisecond, "client must connect to lsp")
	require.Eventually(t, func() bool {
		connected, err := payerLightning.IsConnected(mustDecodeHex(t, lspInfo.Pubkey))
		return err == nil && connected
	}, 20*time.Second, 500*time.Millisecond, "payer must connect to lsp")

	const paymentSizeMloki = 5_000_000_000 // 5,000,000 loki gross

	// JIT only covers the LSP's not-yet-real *last* hop to the client — the
	// payer still needs an existing, funded, confirmed channel to the LSP to
	// route the payment through in the first place. Capacity must comfortably
	// exceed the payment size, leaving room for the channel reserve (~1%) and
	// routing fees — a payment equal to the full channel capacity fails with
	// FAILURE_REASON_INSUFFICIENT_BALANCE.
	//
	// Idempotent on purpose: this devnet's chain and peer connections are
	// long-lived across test runs (see StartDevnet/README), so opening an
	// unconditional new channel every run piles up redundant ones against
	// the same peer — observed to make pathfinding pick the newest,
	// not-yet-graph-synced channel and fail the payment with NO_ROUTE.
	//
	// Reuse requires enough *local balance* to cover this run's payment,
	// not just an existing confirmed channel: this same channel gets
	// reused (and its balance permanently spent down, since every
	// successful run really does move paymentSizeMloki through it) across
	// every run against this long-lived devnet, and once it drops below
	// paymentSizeMloki, every subsequent attempt fails instantly with
	// FAILURE_REASON_NO_ROUTE / flnd's own "insufficient local balance" --
	// root-caused 2026-07-16 after this exact symptom had been chased
	// across multiple earlier sessions as a mysterious, unrelated
	// payer-side pathfinding bug (see BACKLOG.md). Require comfortably
	// more than the payment size so the channel's own ~1% reserve doesn't
	// immediately re-trigger the same failure.
	minPayerBalanceLoki := int64(2 * (paymentSizeMloki / 1000))
	sufficientPayerChannel := func() (uint64, bool) {
		resp, err := payerRaw.ListChannels(ctx, &lnrpc.ListChannelsRequest{})
		if err != nil {
			return 0, false
		}
		for _, ch := range resp.Channels {
			if ch.RemotePubkey == lspInfo.Pubkey && ch.Active && ch.LocalBalance >= minPayerBalanceLoki {
				return ch.ChanId, true
			}
		}
		return 0, false
	}

	var payerToLspChanId uint64
	if id, ok := sufficientPayerChannel(); ok {
		payerToLspChanId = id
	} else {
		_, err = OpenNormalChannel(ctx, payerRaw, lspInfo.Pubkey, 50_000_000, 1)
		require.NoError(t, err)
		require.NoError(t, d.MineBlocks(6))
	}
	// Wait for the channel to be both confirmed AND marked active — a
	// channel can have a confirmed SCID for a moment before flnd's own
	// link/router state considers it usable, and sending a payment through
	// it too early fails with FAILURE_REASON_INSUFFICIENT_BALANCE even
	// though the channel is well-funded.
	require.Eventually(t, func() bool {
		id, ok := sufficientPayerChannel()
		if ok {
			payerToLspChanId = id
		}
		return ok
	}, 30*time.Second, time.Second, "payer<->lsp channel must confirm, activate, and have enough balance")
	require.NotZero(t, payerToLspChanId)
	require.Eventually(t, func() bool {
		channels, err := lspClient.ListChannels()
		return err == nil && len(channels) > 0
	}, 30*time.Second, time.Second, "payer<->lsp channel must confirm and activate")

	bus, err := NewCustomMessageBus(ctx, clientRaw)
	require.NoError(t, err)

	lsps2Client, err := NewLsps2Client(bus, lspInfo.Pubkey)
	require.NoError(t, err)

	menu, err := lsps2Client.GetInfo(ctx, "itest-token")
	require.NoError(t, err)
	require.NotEmpty(t, menu, "LSP must offer at least one opening_fee_params option")

	buyResult, err := lsps2Client.Buy(ctx, menu[0], paymentSizeMloki)
	require.NoError(t, err)
	require.NotEmpty(t, buyResult.JITChannelScid)
	assert.Equal(t, lspInfo.Pubkey, buyResult.LspNodeId)

	bolt11, _, err := CreateJitInvoice(ctx, clientRaw, buyResult, menu[0], paymentSizeMloki, "itest JIT purchase")
	require.NoError(t, err)
	require.NotEmpty(t, bolt11)

	// flspd's own interceptor waits up to 60s for the zero-conf channel's
	// balance to sync into the router before resuming "best effort" (see
	// interceptor/intercept_handler.go's routeTimeout) — give the payment
	// comfortably longer than that, or a client timeout can race a genuine
	// server-side completion and report failure for a payment that actually
	// went through (observed firsthand: state=COMPLETED in the LSP's own
	// log, FAILURE_REASON_TIMEOUT on the payer's side, from a 60s timeout
	// on both ends expiring within the same second).
	_, err = PayInvoice(ctx, payerRouter, bolt11, payerToLspChanId, 100*time.Second)
	require.NoError(t, err, "payment must succeed and trigger the JIT channel open")

	// The channel should now actually exist between lsp and client.
	require.Eventually(t, func() bool {
		channels, err := clientLightning.ListChannels()
		return err == nil && len(channels) > 0
	}, 30*time.Second, time.Second, "client must have a channel with the LSP after the JIT open")
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}
