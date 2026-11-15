package itest

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func init() {
	stepRegistry["connect_peer"] = stepConnectPeer
	stepRegistry["open_channel"] = stepOpenChannel
	stepRegistry["mine_blocks"] = stepMineBlocks
	stepRegistry["auto_accept_channels"] = stepAutoAcceptChannels
	stepRegistry["lsps1_create_order"] = stepLsps1CreateOrder
	stepRegistry["wait_for_lsps1_order_state"] = stepWaitForLsps1OrderState
	stepRegistry["kill_lsp"] = stepKillLsp
	stepRegistry["restart_lsp"] = stepRestartLsp
	stepRegistry["count_channels"] = stepCountChannels
	stepRegistry["lsps2_buy"] = stepLsps2Buy
	stepRegistry["create_jit_invoice"] = stepCreateJitInvoice
	stepRegistry["pay_invoice"] = stepPayInvoice
	stepRegistry["set_mempool_fees"] = stepSetMempoolFees
}

// p2pAddr maps a devnet role name to its container-internal P2P address --
// peer connections happen over the devnet's own docker network, not the
// host-published port a test process would dial for gRPC.
func p2pAddr(role string) string {
	return fmt.Sprintf("flnd-%s:9735", role)
}

// --- connect_peer: {from: <role>, to: <role>} ---

type connectPeerParams struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

func stepConnectPeer(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p connectPeerParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.From == "" || p.To == "" {
		return fmt.Errorf("connect_peer: both from and to are required")
	}

	fromRaw, err := sc.raw(p.From)
	if err != nil {
		return err
	}
	toPubkey, err := sc.pubkey(p.To)
	if err != nil {
		return err
	}
	if err := ConnectPeer(sc.Ctx, fromRaw, toPubkey, p2pAddr(p.To)); err != nil {
		return fmt.Errorf("connecting %s -> %s: %w", p.From, p.To, err)
	}

	fromClient, err := sc.client(p.From)
	if err != nil {
		return err
	}
	toPubkeyBytes, err := hex.DecodeString(toPubkey)
	if err != nil {
		return err
	}
	require.Eventually(t, func() bool {
		connected, err := fromClient.IsConnected(toPubkeyBytes)
		return err == nil && connected
	}, 20*time.Second, 500*time.Millisecond, "%s must connect to %s", p.From, p.To)
	return nil
}

// --- open_channel: {from: <role>, to: <role>, capacity_loki: N, min_confs: N} ---
// Idempotent: reuses an existing confirmed from->to channel rather than
// opening a redundant one, since this devnet's chain/peer state is
// long-lived across runs (see test/devnet/README.md) -- piling up
// duplicate channels against the same peer was observed to make
// pathfinding pick the newest, not-yet-graph-synced one and fail with
// NO_ROUTE.

type openChannelParams struct {
	From         string `yaml:"from"`
	To           string `yaml:"to"`
	CapacityLoki int64  `yaml:"capacity_loki"`
	MinConfs     int32  `yaml:"min_confs"`
}

func stepOpenChannel(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p openChannelParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.From == "" || p.To == "" || p.CapacityLoki == 0 {
		return fmt.Errorf("open_channel: from, to, and capacity_loki are all required")
	}
	if p.MinConfs == 0 {
		p.MinConfs = 1
	}

	fromClient, err := sc.client(p.From)
	if err != nil {
		return err
	}
	toPubkeyHex, err := sc.pubkey(p.To)
	if err != nil {
		return err
	}

	channels, err := fromClient.ListChannels()
	if err != nil {
		return err
	}
	for _, ch := range channels {
		if hex.EncodeToString(ch.PeerId) == toPubkeyHex && ch.ConfirmedScid != nil {
			return nil // already have a confirmed channel; nothing to do
		}
	}

	fromRaw, err := sc.raw(p.From)
	if err != nil {
		return err
	}
	if _, err := OpenNormalChannel(sc.Ctx, fromRaw, toPubkeyHex, p.CapacityLoki, p.MinConfs); err != nil {
		return fmt.Errorf("opening channel %s -> %s: %w", p.From, p.To, err)
	}

	require.Eventually(t, func() bool {
		resp, err := fromRaw.ListChannels(sc.Ctx, &lnrpc.ListChannelsRequest{})
		if err != nil {
			return false
		}
		for _, ch := range resp.Channels {
			if ch.RemotePubkey == toPubkeyHex && ch.Active {
				return true
			}
		}
		return false
	}, 30*time.Second, time.Second, "%s<->%s channel must confirm and become active", p.From, p.To)

	return nil
}

// --- mine_blocks: {count: N} ---

type mineBlocksParams struct {
	Count int `yaml:"count"`
}

func stepMineBlocks(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p mineBlocksParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.Count <= 0 {
		return fmt.Errorf("mine_blocks: count must be positive")
	}
	return sc.Devnet.MineBlocks(p.Count)
}

// --- auto_accept_channels: {as: <role>} ---
// flnd (like upstream LND) rejects every incoming channel-open request
// outright unless the responder has registered a ChannelAcceptor.

type autoAcceptChannelsParams struct {
	As string `yaml:"as"`
}

func stepAutoAcceptChannels(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p autoAcceptChannelsParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.As == "" {
		return fmt.Errorf("auto_accept_channels: as is required")
	}
	raw, err := sc.raw(p.As)
	if err != nil {
		return err
	}
	return AutoAcceptChannels(sc.Ctx, raw)
}

// --- lsps2_buy: {as: <role>, lsp: <role>, token: str, payment_size_mloki: N, save_as: name} ---

type lsps2BuyParams struct {
	As               string `yaml:"as"`
	Lsp              string `yaml:"lsp"`
	Token            string `yaml:"token"`
	PaymentSizeMloki uint64 `yaml:"payment_size_mloki"`
	SaveAs           string `yaml:"save_as"`
}

// lsps2BuySaved bundles what later steps need to build/pay the JIT invoice.
type lsps2BuySaved struct {
	Result           *lsps.Lsps2BuyResult
	FeeParams        lsps.Lsps2OpeningFeeParams
	PaymentSizeMloki uint64
}

func stepLsps2Buy(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p lsps2BuyParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.As == "" || p.Lsp == "" || p.PaymentSizeMloki == 0 || p.SaveAs == "" {
		return fmt.Errorf("lsps2_buy: as, lsp, payment_size_mloki, and save_as are all required")
	}

	buyerRaw, err := sc.raw(p.As)
	if err != nil {
		return err
	}
	lspPubkey, err := sc.pubkey(p.Lsp)
	if err != nil {
		return err
	}

	bus, err := NewCustomMessageBus(sc.Ctx, buyerRaw)
	if err != nil {
		return fmt.Errorf("building custom message bus: %w", err)
	}
	client, err := NewLsps2Client(bus, lspPubkey)
	if err != nil {
		return err
	}
	menu, err := client.GetInfo(sc.Ctx, p.Token)
	if err != nil {
		return fmt.Errorf("lsps2 get_info: %w", err)
	}
	if len(menu) == 0 {
		return fmt.Errorf("lsps2 get_info: LSP offered no opening_fee_params options")
	}
	result, err := client.Buy(sc.Ctx, menu[0], p.PaymentSizeMloki)
	if err != nil {
		return fmt.Errorf("lsps2 buy: %w", err)
	}
	return sc.save(p.SaveAs, &lsps2BuySaved{Result: result, FeeParams: menu[0], PaymentSizeMloki: p.PaymentSizeMloki})
}

// --- create_jit_invoice: {as: <role>, from: <save_as ref>, memo: str, save_as: name} ---

type createJitInvoiceParams struct {
	As     string `yaml:"as"`
	From   string `yaml:"from"`
	Memo   string `yaml:"memo"`
	SaveAs string `yaml:"save_as"`
}

func stepCreateJitInvoice(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p createJitInvoiceParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.As == "" || p.From == "" || p.SaveAs == "" {
		return fmt.Errorf("create_jit_invoice: as, from, and save_as are all required")
	}

	saved, err := sc.get(p.From)
	if err != nil {
		return err
	}
	buy, ok := saved.(*lsps2BuySaved)
	if !ok {
		return fmt.Errorf("create_jit_invoice: %q was not saved by lsps2_buy", p.From)
	}

	asRaw, err := sc.raw(p.As)
	if err != nil {
		return err
	}
	bolt11, _, err := CreateJitInvoice(sc.Ctx, asRaw, buy.Result, buy.FeeParams, buy.PaymentSizeMloki, p.Memo)
	if err != nil {
		return fmt.Errorf("creating jit invoice: %w", err)
	}
	return sc.save(p.SaveAs, bolt11)
}

// --- lsps1_create_order: {as: <role>, lsp: <role>, token: str, lsp_balance_loki: N, client_balance_loki: N, save_as: name} ---

type lsps1CreateOrderParams struct {
	As                string `yaml:"as"`
	Lsp               string `yaml:"lsp"`
	Token             string `yaml:"token"`
	LspBalanceLoki    uint64 `yaml:"lsp_balance_loki"`
	ClientBalanceLoki uint64 `yaml:"client_balance_loki"`
	SaveAs            string `yaml:"save_as"`
}

func stepLsps1CreateOrder(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p lsps1CreateOrderParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.As == "" || p.Lsp == "" || p.LspBalanceLoki == 0 || p.SaveAs == "" {
		return fmt.Errorf("lsps1_create_order: as, lsp, lsp_balance_loki, and save_as are all required")
	}

	asRaw, err := sc.raw(p.As)
	if err != nil {
		return err
	}
	lspPubkey, err := sc.pubkey(p.Lsp)
	if err != nil {
		return err
	}

	bus, err := NewCustomMessageBus(sc.Ctx, asRaw)
	if err != nil {
		return fmt.Errorf("building custom message bus: %w", err)
	}
	client, err := NewLsps1Client(bus, lspPubkey)
	if err != nil {
		return err
	}
	order, err := client.CreateOrder(sc.Ctx, p.Token, p.LspBalanceLoki, p.ClientBalanceLoki)
	if err != nil {
		return fmt.Errorf("lsps1 create_order: %w", err)
	}
	if order.Payment == nil || order.Payment.Bolt11 == nil || order.Payment.Bolt11.Invoice == "" {
		return fmt.Errorf("lsps1 create_order: LSP did not return a payable invoice")
	}
	return sc.save(p.SaveAs, order)
}

// --- wait_for_lsps1_order_state: {as: <role>, lsp: <role>, order: <save_as ref>, state: str, timeout_seconds: N} ---

type waitForLsps1OrderStateParams struct {
	As             string `yaml:"as"`
	Lsp            string `yaml:"lsp"`
	Order          string `yaml:"order"`
	State          string `yaml:"state"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

func stepWaitForLsps1OrderState(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p waitForLsps1OrderStateParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.As == "" || p.Lsp == "" || p.Order == "" || p.State == "" || p.TimeoutSeconds == 0 {
		return fmt.Errorf("wait_for_lsps1_order_state: as, lsp, order, state, and timeout_seconds are all required")
	}

	saved, err := sc.get(p.Order)
	if err != nil {
		return err
	}
	order, ok := saved.(*lsps.Lsps1CreateOrderResponse)
	if !ok {
		return fmt.Errorf("wait_for_lsps1_order_state: %q was not saved by lsps1_create_order", p.Order)
	}

	asRaw, err := sc.raw(p.As)
	if err != nil {
		return err
	}
	lspPubkey, err := sc.pubkey(p.Lsp)
	if err != nil {
		return err
	}
	bus, err := NewCustomMessageBus(sc.Ctx, asRaw)
	if err != nil {
		return fmt.Errorf("building custom message bus: %w", err)
	}
	client, err := NewLsps1Client(bus, lspPubkey)
	if err != nil {
		return err
	}

	var lastState string
	reached := assert.Eventually(t, func() bool {
		got, err := client.GetOrder(sc.Ctx, order.OrderId)
		if err != nil {
			return false
		}
		lastState = got.OrderState
		return got.OrderState == p.State
	}, time.Duration(p.TimeoutSeconds)*time.Second, time.Second)
	if !reached {
		return fmt.Errorf("wait_for_lsps1_order_state: order %s did not reach state %s within %ds (last seen: %s)", order.OrderId, p.State, p.TimeoutSeconds, lastState)
	}
	return nil
}

// --- pay_invoice: {as: <role>, invoice: <save_as ref>, outgoing_channel_to: <role>, timeout_seconds: N} ---

type payInvoiceParams struct {
	As                string `yaml:"as"`
	Invoice           string `yaml:"invoice"`
	OutgoingChannelTo string `yaml:"outgoing_channel_to"`
	TimeoutSeconds    int    `yaml:"timeout_seconds"`
}

func stepPayInvoice(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p payInvoiceParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.As == "" || p.Invoice == "" || p.TimeoutSeconds == 0 {
		return fmt.Errorf("pay_invoice: as, invoice, and timeout_seconds are all required")
	}

	saved, err := sc.get(p.Invoice)
	if err != nil {
		return err
	}
	var bolt11 string
	switch v := saved.(type) {
	case string:
		bolt11 = v
	case *lsps.Lsps1CreateOrderResponse:
		if v.Payment == nil || v.Payment.Bolt11 == nil || v.Payment.Bolt11.Invoice == "" {
			return fmt.Errorf("pay_invoice: %q (an lsps1_create_order result) has no payable invoice", p.Invoice)
		}
		bolt11 = v.Payment.Bolt11.Invoice
	default:
		return fmt.Errorf("pay_invoice: %q was not saved as an invoice string or an lsps1_create_order result", p.Invoice)
	}

	var outgoingChanId uint64
	if p.OutgoingChannelTo != "" {
		toPubkeyHex, err := sc.pubkey(p.OutgoingChannelTo)
		if err != nil {
			return err
		}
		asClient, err := sc.client(p.As)
		if err != nil {
			return err
		}
		channels, err := asClient.ListChannels()
		if err != nil {
			return err
		}
		for _, ch := range channels {
			if hex.EncodeToString(ch.PeerId) == toPubkeyHex && ch.ConfirmedScid != nil {
				outgoingChanId = uint64(*ch.ConfirmedScid)
				break
			}
		}
		if outgoingChanId == 0 {
			return fmt.Errorf("pay_invoice: no confirmed channel from %s to %s to pin as the outgoing channel", p.As, p.OutgoingChannelTo)
		}
	}

	router, err := sc.router(p.As)
	if err != nil {
		return err
	}
	_, err = PayInvoice(sc.Ctx, router, bolt11, outgoingChanId, time.Duration(p.TimeoutSeconds)*time.Second)
	return err
}

// --- set_mempool_fees: {fastest_fee: N, half_hour_fee: N, hour_fee: N, economy_fee: N, minimum_fee: N} ---
// Fault injection (T4-3): overwrites every tier flspd-under-test's fake
// mempool API reports, simulating a fee-congestion condition (or a return
// to normal). All 5 fields are required -- the real API always reports a
// complete schedule, so a scenario should too, rather than leaving some
// tiers at whatever they happened to default/were last set to. Takes
// effect on the *next* fee query flspd makes; there's no caching to wait
// out on either side.

type setMempoolFeesParams struct {
	FastestFee  *float64 `yaml:"fastest_fee"`
	HalfHourFee *float64 `yaml:"half_hour_fee"`
	HourFee     *float64 `yaml:"hour_fee"`
	EconomyFee  *float64 `yaml:"economy_fee"`
	MinimumFee  *float64 `yaml:"minimum_fee"`
}

func stepSetMempoolFees(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p setMempoolFeesParams
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.FastestFee == nil || p.HalfHourFee == nil || p.HourFee == nil || p.EconomyFee == nil || p.MinimumFee == nil {
		return fmt.Errorf("set_mempool_fees: fastest_fee, half_hour_fee, hour_fee, economy_fee, and minimum_fee are all required")
	}
	sc.Mempool.SetFees(*p.FastestFee, *p.HalfHourFee, *p.HourFee, *p.EconomyFee, *p.MinimumFee)
	return nil
}

// --- kill_lsp: {} ---
// Fault injection (T4-3): SIGKILL's the flspd-under-test process, simulating
// a crash rather than a graceful shutdown -- see FlspdProcess.Kill's own
// doc comment, this is deliberately what the F-1/F-8 recovery scenarios
// need, not Stop's clean SIGTERM path.

func stepKillLsp(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p struct{}
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	return sc.Flspd.Kill()
}

// --- restart_lsp: {} ---
// Starts a fresh flspd instance with the exact same config after kill_lsp,
// so it reconnects to the same flnd role and Postgres database and runs
// its startup recovery sweep -- see FlspdProcess.Restart, which this
// mirrors minus the redundant Kill (kill_lsp is its own explicit step, so
// a scenario can assert on state in between the two).

func stepRestartLsp(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p struct{}
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	return sc.Flspd.Start()
}

// --- count_channels: {as: <role>, peer: <role>, save_as: name} ---
// Snapshots the current channel count between two roles. This devnet's
// chain/peer state is long-lived across test runs (see
// test/devnet/README.md), so channels accumulate across unrelated runs --
// an absolute channel_count assertion isn't reliable on its own. Pair
// this step (taken before the action under test) with
// channel_count_delta (checked after) to assert on *this scenario's own*
// effect regardless of pre-existing history.
func stepCountChannels(t *testing.T, sc *ScenarioContext, params *yaml.Node) error {
	var p struct {
		As     string `yaml:"as"`
		Peer   string `yaml:"peer"`
		SaveAs string `yaml:"save_as"`
	}
	if err := decodeParamsStrict(params, &p); err != nil {
		return err
	}
	if p.As == "" || p.Peer == "" || p.SaveAs == "" {
		return fmt.Errorf("count_channels: as, peer, and save_as are all required")
	}

	count, err := sc.channelCount(p.As, p.Peer)
	if err != nil {
		return err
	}
	return sc.save(p.SaveAs, count)
}
