package regtest

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
)

// Hooks are the chain and payer operations the consumers' live tests drive through
// lokihub's test hooks (a testhooks build only). Blocks they mine leave out every
// withheld transaction.
type Hooks struct {
	Chain *Chain
	Payer *Node

	mu       sync.Mutex
	withheld map[chainhash.Hash]bool
}

// NewHooks wraps a dialled chain node and payer.
func NewHooks(chain *Chain, payer *Node) *Hooks {
	return &Hooks{Chain: chain, Payer: payer, withheld: map[chainhash.Hash]bool{}}
}

// Withhold adds txid to, or removes it from, the set every mined block leaves out.
func (h *Hooks) Withhold(txid string, on bool) error {
	hash, err := chainhash.NewHashFromStr(txid)
	if err != nil {
		return fmt.Errorf("txid: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if on {
		h.withheld[*hash] = true
	} else {
		delete(h.withheld, *hash)
	}
	return nil
}

// Withheld is the current withheld set.
func (h *Hooks) Withheld() map[chainhash.Hash]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[chainhash.Hash]bool, len(h.withheld))
	for k := range h.withheld {
		out[k] = true
	}
	return out
}

// Mine mines n blocks to address, or to the payer when address is empty, leaving
// out every withheld transaction and its descendants.
func (h *Hooks) Mine(ctx context.Context, n int, address string) ([]string, error) {
	if n < 1 || n > 1000 {
		return nil, errors.New("blocks must be between 1 and 1000")
	}
	if address == "" {
		var err error
		if address, err = h.Payer.NewAddress(ctx); err != nil {
			return nil, err
		}
	}
	hashes, err := h.Chain.Mine(n, address, h.Withheld())
	out := make([]string, len(hashes))
	for i, b := range hashes {
		out[i] = b.String()
	}
	return out, err
}

// Reorg reorganises the last depth blocks away and mines depth + 1 new ones to
// the payer, leaving out every withheld transaction (Chain.Reorg).
func (h *Hooks) Reorg(ctx context.Context, depth int) ([]string, error) {
	address, err := h.Payer.NewAddress(ctx)
	if err != nil {
		return nil, err
	}
	hashes, err := h.Chain.Reorg(depth, address, h.Withheld())
	out := make([]string, len(hashes))
	for i, b := range hashes {
		out[i] = b.String()
	}
	return out, err
}

// Tip is the chain's best block.
type Tip struct {
	Height int32  `json:"height"`
	Hash   string `json:"hash"`
}

// Tip reads the chain's best block.
func (h *Hooks) Tip() (Tip, error) {
	hash, height, err := h.Chain.rpc.GetBestBlock()
	if err != nil {
		return Tip{}, err
	}
	return Tip{Height: height, Hash: hash.String()}, nil
}

// Tx is a transaction as the chain node sees it.
type Tx struct {
	Txid  string `json:"txid"`
	Hex   string `json:"hex"`
	Depth int64  `json:"depth"` // 0 in the mempool
}

// Tx reads a transaction from the chain node's index.
func (h *Hooks) Tx(txid string) (Tx, error) {
	hash, err := chainhash.NewHashFromStr(txid)
	if err != nil {
		return Tx{}, fmt.Errorf("txid: %w", err)
	}
	res, err := h.Chain.rpc.GetRawTransactionVerbose(hash)
	if err != nil {
		return Tx{}, err
	}
	return Tx{Txid: res.Txid, Hex: res.Hex, Depth: int64(res.Confirmations)}, nil //nolint:gosec // confirmations are small
}

// LokiFromMillis converts an amount in millis to whole loki, refusing a fraction:
// an on-chain payment has no sub-loki part.
func LokiFromMillis(millis uint64) (int64, error) {
	if millis == 0 || millis%1000 != 0 {
		return 0, errors.New("amount_millis must be a positive whole number of loki (a multiple of 1000)")
	}
	return int64(millis / 1000), nil //nolint:gosec // bounded by the payer's balance
}

// PayAddress pays amountMillis on-chain from the payer to address and returns the txid.
func (h *Hooks) PayAddress(ctx context.Context, address string, amountMillis uint64) (string, error) {
	loki, err := LokiFromMillis(amountMillis)
	if err != nil {
		return "", err
	}
	resp, err := h.Payer.RPC.SendCoins(ctx, &lnrpc.SendCoinsRequest{Addr: address, Amount: loki, TargetConf: 1})
	if err != nil {
		return "", err
	}
	return resp.Txid, nil
}

// Payment is the outcome of a payer payment.
type Payment struct {
	Status        string `json:"status"` // SUCCEEDED, FAILED or IN_FLIGHT
	Preimage      string `json:"preimage,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// PayInvoice pays a BOLT11 invoice from the payer and waits for a final outcome or ctx.
func (h *Hooks) PayInvoice(ctx context.Context, invoice string) (Payment, error) {
	stream, err := h.Payer.RPC.SendPayment(ctx, &routerrpc.SendPaymentRequest{
		PaymentRequest: invoice,
		TimeoutSeconds: 60,
		FeeLimitMsat:   1_000_000_000,
	})
	if err != nil {
		return Payment{}, err
	}
	last := Payment{Status: "IN_FLIGHT"}
	for {
		p, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return last, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return last, nil
			}
			return last, err
		}
		last = Payment{Status: p.Status.String(), Preimage: p.PaymentPreimage}
		if p.Status == lnrpc.Payment_FAILED {
			last.FailureReason = p.FailureReason.String()
		}
		if p.Status == lnrpc.Payment_SUCCEEDED || p.Status == lnrpc.Payment_FAILED {
			return last, nil
		}
	}
}

// Invoice is an invoice made by the payer node.
type Invoice struct {
	PaymentRequest string `json:"payment_request"`
	PaymentHash    string `json:"payment_hash"`
}

// NewInvoice makes an invoice for amountMillis on the payer node (an external payee).
func (h *Hooks) NewInvoice(ctx context.Context, amountMillis uint64) (Invoice, error) {
	if amountMillis == 0 {
		return Invoice{}, errors.New("amount_millis must be positive")
	}
	resp, err := h.Payer.RPC.AddInvoice(ctx, &lnrpc.Invoice{ValueMsat: int64(amountMillis)}) //nolint:gosec // test amounts are small
	if err != nil {
		return Invoice{}, err
	}
	return Invoice{PaymentRequest: resp.PaymentRequest, PaymentHash: hex.EncodeToString(resp.RHash)}, nil
}

// InvoiceState is a payer invoice's state.
type InvoiceState struct {
	State   string `json:"state"` // OPEN, SETTLED, CANCELED or ACCEPTED
	Settled bool   `json:"settled"`
}

// InvoiceState reads a payer invoice by its payment hash.
func (h *Hooks) InvoiceState(ctx context.Context, paymentHash string) (InvoiceState, error) {
	hash, err := hex.DecodeString(paymentHash)
	if err != nil || len(hash) != 32 {
		return InvoiceState{}, errors.New("payment hash must be 64 hex characters")
	}
	inv, err := h.Payer.RPC.LookupInvoice(ctx, &lnrpc.PaymentHash{RHash: hash})
	if err != nil {
		return InvoiceState{}, err
	}
	return InvoiceState{State: inv.State.String(), Settled: inv.State == lnrpc.Invoice_SETTLED}, nil
}
