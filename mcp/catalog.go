package mcp

import (
	"context"
	"fmt"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/lnclient"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxPageLimit caps every paginated read (M-DOS-1): a caller asking for a huge
// page gets at most this many rows, so a read can't be turned into a DB/memory
// bomb. limit == 0 is treated as the default page, never "unbounded".
const (
	maxPageLimit     = 100
	defaultPageLimit = 25
)

// clampLimit bounds a caller-supplied page size.
func clampLimit(l uint64) uint64 {
	switch {
	case l == 0:
		return defaultPageLimit
	case l > maxPageLimit:
		return maxPageLimit
	default:
		return l
	}
}

// Tool outputs must be JSON objects (outputSchema), so scalar results are wrapped.
type (
	addressResult struct {
		Address string `json:"address"`
	}
	feeResult struct {
		FeeMloki uint64 `json:"fee_mloki"`
	}
)

type pageArgs struct {
	Limit  uint64 `json:"limit,omitempty" jsonschema:"page size (max 100)"`
	Offset uint64 `json:"offset,omitempty"`
}

type appIDArgs struct {
	AppID uint `json:"app_id" jsonschema:"the id of the cash hub or circle"`
}

// registerCatalog wires the parameterized reads and the remaining writes of the
// catalogue (CONTRACTS.md). Tools whose api.API method takes a loaded *db.App
// are wired once an app-resolution seam exists (see deferredTools).
func (s *Server) registerCatalog(srv *mcpsdk.Server, conn *Conn) {
	// ---- Wallet & payments: reads ----
	readToolIn(s, srv, conn, "ListTransactions", "list_transactions",
		"Recent Lightning transactions, newest first (paged, max 100).",
		func(ctx context.Context, a api.API, in pageArgs) (txList, error) {
			r, err := a.ListTransactions(ctx, nil, clampLimit(in.Limit), in.Offset)
			return toTxList(r), err
		})
	readToolIn(s, srv, conn, "ListOnchainTransactions", "list_onchain_transactions",
		"On-chain wallet transactions (paged, max 100).",
		func(ctx context.Context, a api.API, in pageArgs) (onchainTxs, error) {
			txs, err := a.ListOnchainTransactions(ctx, clampLimit(in.Limit), in.Offset)
			return onchainTxs{Transactions: txs}, err
		})
	readToolIn(s, srv, conn, "LookupInvoice", "lookup_invoice",
		"Look up a payment or invoice by its payment hash.",
		func(ctx context.Context, a api.API, in struct {
			PaymentHash string `json:"payment_hash"`
		}) (txView, error) {
			r, err := a.LookupInvoice(ctx, in.PaymentHash)
			return toTxView(r), err
		})
	readToolIn(s, srv, conn, "EstimateInvoiceFee", "estimate_invoice_fee",
		"Estimate the routing fee to pay a Lightning invoice.",
		func(ctx context.Context, a api.API, in struct {
			Invoice string `json:"invoice"`
		}) (feeResult, error) {
			f, err := a.EstimateInvoiceFee(ctx, in.Invoice)
			return feeResult{FeeMloki: f}, err
		})
	readTool(s, srv, conn, "GetUnusedOnchainAddress", "onchain_address",
		"An unused on-chain receive address of this hub.",
		func(ctx context.Context, a api.API) (addressResult, error) {
			addr, err := a.GetUnusedOnchainAddress(ctx)
			return addressResult{Address: addr}, err
		})

	// ---- Wallet & payments: writes ----
	writeTool(s, srv, conn, "CreateInvoice", "make_invoice",
		"Create a Lightning invoice to receive a payment.",
		func(in makeInvoiceArgs) string { return in.IdempotencyKey },
		func(ctx context.Context, a api.API, in makeInvoiceArgs) (txView, error) {
			r, err := a.CreateInvoice(ctx, &api.MakeInvoiceRequest{Amount: in.AmountMloki, Description: in.Description})
			return toTxView(r), err
		})
	writeTool(s, srv, conn, "SendPayment", "send_payment",
		"Pay a Lightning invoice from the hub's main balance.",
		func(in sendPaymentArgs) string { return in.IdempotencyKey },
		func(ctx context.Context, a api.API, in sendPaymentArgs) (paymentView, error) {
			var amt *uint64
			if in.AmountMloki > 0 {
				v := in.AmountMloki
				amt = &v
			}
			r, err := a.SendPayment(ctx, in.Invoice, amt, nil, nil)
			return toPaymentView(r), err
		})
	writeTool(s, srv, conn, "GetNewOnchainAddress", "new_onchain_address",
		"Derive a fresh on-chain receive address.",
		func(in keyOnlyArgs) string { return in.IdempotencyKey },
		func(ctx context.Context, a api.API, _ keyOnlyArgs) (addressResult, error) {
			addr, err := a.GetNewOnchainAddress(ctx)
			return addressResult{Address: addr}, err
		})
	writeTool(s, srv, conn, "SignMessage", "sign_message",
		"Sign a message with the node key.",
		func(in signArgs) string { return in.IdempotencyKey },
		func(ctx context.Context, a api.API, in signArgs) (*api.SignMessageResponse, error) {
			return a.SignMessage(ctx, in.Message)
		})

	// ---- Network & liquidity ----
	readTool(s, srv, conn, "LSPS1ListOrders", "list_lsp_orders",
		"Channel orders placed with LSPs.",
		func(ctx context.Context, a api.API) (*api.LSPS1ListOrdersResponse, error) {
			return a.LSPS1ListOrders(ctx)
		})
	writeTool(s, srv, conn, "ConnectPeer", "connect_peer",
		"Connect to a Lightning peer.",
		func(in connectPeerArgs) string { return in.IdempotencyKey },
		func(ctx context.Context, a api.API, in connectPeerArgs) (okResult, error) {
			return okResult{OK: true}, a.ConnectPeer(ctx, &api.ConnectPeerRequest{Pubkey: in.Pubkey, Address: in.Address, Port: in.Port})
		})
	writeTool(s, srv, conn, "DisconnectPeer", "disconnect_peer",
		"Disconnect a Lightning peer.",
		func(in peerArgs) string { return in.IdempotencyKey },
		func(ctx context.Context, a api.API, in peerArgs) (okResult, error) {
			return okResult{OK: true}, a.DisconnectPeer(ctx, in.PeerID)
		})
	guardedTool(s, srv, conn, "OpenChannel", "open_channel",
		"Open a Lightning channel (commits on-chain funds).",
		func(in openChannelArgs) (string, error) {
			if !isNodePubkey(in.Pubkey) {
				return "", errInvalidArgs("pubkey must be a 66-hex node pubkey")
			}
			if in.AmountLoki <= 0 {
				return "", errInvalidArgs("amount_loki must be greater than 0")
			}
			vis := "private"
			if in.Public {
				vis = "public"
			}
			return fmt.Sprintf("Open a %d loki %s channel to %s (commits on-chain funds)", in.AmountLoki, vis, in.Pubkey), nil
		},
		func(in openChannelArgs) string { return in.IdempotencyKey },
		nil,
		func(ctx context.Context, a api.API, in openChannelArgs) (any, error) {
			return a.OpenChannel(ctx, &api.OpenChannelRequest{Pubkey: in.Pubkey, AmountLoki: in.AmountLoki, Public: in.Public})
		})

	// ---- Cash & circles (by app id) ----
	readToolIn(s, srv, conn, "GetCashHubStats", "cash_hub_stats",
		"Money totals for one cash hub.",
		func(_ context.Context, a api.API, in appIDArgs) (*api.CashHubStatsResponse, error) {
			return a.GetCashHubStats(in.AppID)
		})
	readToolIn(s, srv, conn, "GetCircleHubStats", "circle_hub_stats",
		"Member activity totals for one circle.",
		func(_ context.Context, a api.API, in appIDArgs) (*api.CircleHubStatsResponse, error) {
			return a.GetCircleHubStats(in.AppID)
		})
}

type onchainTxs struct {
	Transactions []lnclient.OnchainTransaction `json:"transactions"`
}

type keyOnlyArgs struct {
	IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
}

type makeInvoiceArgs struct {
	IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
	AmountMloki    uint64 `json:"amount_mloki"`
	Description    string `json:"description,omitempty"`
}

type sendPaymentArgs struct {
	IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
	Invoice        string `json:"invoice"`
	AmountMloki    uint64 `json:"amount_mloki,omitempty" jsonschema:"only for zero-amount invoices"`
}

type signArgs struct {
	IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
	Message        string `json:"message"`
}

type connectPeerArgs struct {
	IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
	Pubkey         string `json:"pubkey"`
	Address        string `json:"address"`
	Port           uint16 `json:"port"`
}

type peerArgs struct {
	IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
	PeerID         string `json:"peer_id"`
}

type openChannelArgs struct {
	IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
	Pubkey         string `json:"pubkey"`
	AmountLoki     int64  `json:"amount_loki"`
	Public         bool   `json:"public,omitempty"`
}
