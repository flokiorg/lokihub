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

// TestLsps1OrderSettleAndOpen drives a full LSPS1 manual channel purchase
// against a real flspd binary, real flnd nodes, and a real (private,
// regtest) chain: the client places an order, the LSP returns a bolt11
// invoice, a third party pays it (an ordinary invoice, unlike LSPS2's
// phantom-hop one -- no JIT/zero-conf involved), and flspd's settlement
// service opens the channel once the payment settles. This is the
// Go-level proof for T4-2's lsps1_order_settle_and_open.yaml scenario.
func TestLsps1OrderSettleAndOpen(t *testing.T) {
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

	flspd := NewFlspdProcess(t, d, pg, FlspdProcessOptions{})
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
	payerRaw, err := d.RawLightningClient("payer")
	require.NoError(t, err)
	payerRouter, err := d.RawRouterClient("payer")
	require.NoError(t, err)

	// The LSP initiates this channel open, same as any other flnd-to-flnd
	// open -- the client must have a ChannelAcceptor registered first.
	require.NoError(t, AutoAcceptChannels(ctx, clientRaw))

	require.NoError(t, ConnectPeer(ctx, clientRaw, lspInfo.Pubkey, "flnd-lsp:9735"))
	require.NoError(t, ConnectPeer(ctx, payerRaw, lspInfo.Pubkey, "flnd-lsp:9735"))
	require.Eventually(t, func() bool {
		connected, err := clientLightning.IsConnected(mustDecodeHex(t, lspInfo.Pubkey))
		return err == nil && connected
	}, 20*time.Second, 500*time.Millisecond, "client must connect to lsp")

	// The invoice the LSP issues for this order is an ordinary one (no
	// phantom/JIT hop, unlike LSPS2) -- the payer just needs any working
	// route to the LSP, which its own existing payer<->lsp channel (opened
	// and gossip-synced by earlier tests/devnet setup -- see T4-1's
	// gossip-propagation fix) already provides.
	payerLightning, err := d.LightningClient("payer")
	require.NoError(t, err)
	payerChannels, err := payerLightning.ListChannels()
	require.NoError(t, err)
	var payerToLspChanId uint64
	for _, ch := range payerChannels {
		if hex.EncodeToString(ch.PeerId) == lspInfo.Pubkey && ch.ConfirmedScid != nil {
			payerToLspChanId = uint64(*ch.ConfirmedScid)
			break
		}
	}
	if payerToLspChanId == 0 {
		require.NoError(t, ConnectPeer(ctx, payerRaw, lspInfo.Pubkey, "flnd-lsp:9735"))
		_, err = OpenNormalChannel(ctx, payerRaw, lspInfo.Pubkey, 50_000_000, 1)
		require.NoError(t, err)
		require.NoError(t, d.MineBlocks(6))
	}
	require.Eventually(t, func() bool {
		resp, err := payerRaw.ListChannels(ctx, &lnrpc.ListChannelsRequest{})
		if err != nil {
			return false
		}
		for _, ch := range resp.Channels {
			if ch.RemotePubkey == lspInfo.Pubkey && ch.Active {
				payerToLspChanId = ch.ChanId
				return true
			}
		}
		return false
	}, 30*time.Second, time.Second, "payer<->lsp channel must confirm and become active")
	require.NotZero(t, payerToLspChanId)

	bus, err := NewCustomMessageBus(ctx, clientRaw)
	require.NoError(t, err)
	lsps1Client, err := NewLsps1Client(bus, lspInfo.Pubkey)
	require.NoError(t, err)

	const lspBalanceLoki = 100_000 // inbound liquidity the client is buying
	order, err := lsps1Client.CreateOrder(ctx, "itest-token", lspBalanceLoki, 0)
	require.NoError(t, err)
	require.NotEmpty(t, order.OrderId)
	require.NotNil(t, order.Payment)
	require.NotNil(t, order.Payment.Bolt11)
	require.NotEmpty(t, order.Payment.Bolt11.Invoice, "the LSP must return a bolt11 invoice to pay for the order")
	assert.Equal(t, "CREATED", order.OrderState)

	_, err = PayInvoice(ctx, payerRouter, order.Payment.Bolt11.Invoice, payerToLspChanId, 60*time.Second)
	require.NoError(t, err, "payment must succeed and trigger the settlement service to open the channel")

	require.Eventually(t, func() bool {
		got, err := lsps1Client.GetOrder(ctx, order.OrderId)
		if err != nil {
			return false
		}
		return got.OrderState == "COMPLETED"
	}, 60*time.Second, time.Second, "order must reach COMPLETED once the settlement service processes the payment and opens the channel")

	require.Eventually(t, func() bool {
		channels, err := clientLightning.ListChannels()
		return err == nil && len(channels) > 0
	}, 30*time.Second, time.Second, "client must have a channel with the LSP after settlement")
}
