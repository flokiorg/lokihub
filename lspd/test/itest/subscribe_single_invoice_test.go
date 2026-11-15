package itest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubscribeSingleInvoiceDeliversAccepted is the H-12 devnet
// verification test: proves invoicesrpc.InvoicesClient.SubscribeSingleInvoice
// actually delivers an ACCEPTED state transition against real flnd, not
// just trusted from the proto definition. This fork has diverged from
// upstream LND invoice-subscription behavior before (the base
// SubscribeInvoices RPC never delivers ACCEPTED at all -- see
// heldInvoiceSafetyNetPollInterval's doc comment in
// lsps/settlement_service.go) -- the same caution applies here. Drives
// invoicesrpc directly, independent of flspd's own SubscribeSingleInvoice
// wrapper, so a bug in flspd's wrapper couldn't mask a real RPC-level
// problem or vice versa.
func TestSubscribeSingleInvoiceDeliversAccepted(t *testing.T) {
	d := StartDevnet(t)
	ctx := t.Context()

	lspClient, err := d.LightningClient("lsp")
	require.NoError(t, err)
	lspInfo, err := lspClient.GetInfo()
	require.NoError(t, err)

	payerLightning, err := d.LightningClient("payer")
	require.NoError(t, err)
	payerRaw, err := d.RawLightningClient("payer")
	require.NoError(t, err)
	payerRouter, err := d.RawRouterClient("payer")
	require.NoError(t, err)
	lspInvoices, err := d.RawInvoicesClient("lsp")
	require.NoError(t, err)

	require.NoError(t, ConnectPeer(ctx, payerRaw, lspInfo.Pubkey, "flnd-lsp:9735"))

	// Reuse whatever channel the payer already has to the lsp from earlier
	// test/devnet setup -- this test only needs a route to exist, not to
	// open a fresh channel.
	payerChannels, err := payerLightning.ListChannels()
	require.NoError(t, err)
	var payerToLspChanId uint64
	for _, ch := range payerChannels {
		if hex.EncodeToString(ch.PeerId) == lspInfo.Pubkey && ch.ConfirmedScid != nil {
			payerToLspChanId = uint64(*ch.ConfirmedScid)
			break
		}
	}
	require.NotZero(t, payerToLspChanId, "payer must already have a confirmed channel to the lsp from devnet setup")

	var preimage [32]byte
	_, err = rand.Read(preimage[:])
	require.NoError(t, err)
	hash := sha256.Sum256(preimage[:])

	addResp, err := lspInvoices.AddHoldInvoice(ctx, &invoicesrpc.AddHoldInvoiceRequest{
		Memo:   "H-12 devnet verification",
		Hash:   hash[:],
		Value:  1000,
		Expiry: 300,
	})
	require.NoError(t, err)

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	stream, err := lspInvoices.SubscribeSingleInvoice(subCtx, &invoicesrpc.SubscribeSingleInvoiceRequest{RHash: hash[:]})
	require.NoError(t, err)

	// The proto doc promises the current state is always sent immediately
	// on subscribe -- verify that promise, not just the ACCEPTED one below.
	first, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, lnrpc.Invoice_OPEN, first.State, "SubscribeSingleInvoice must send the current (OPEN) state immediately on subscribe")

	paymentDone := make(chan error, 1)
	go func() {
		_, payErr := PayInvoice(ctx, payerRouter, addResp.PaymentRequest, payerToLspChanId, 60*time.Second)
		paymentDone <- payErr
	}()

	second, err := stream.Recv()
	require.NoError(t, err, "SubscribeSingleInvoice must deliver a second update once the payer's HTLC locks in")
	assert.Equal(t, lnrpc.Invoice_ACCEPTED, second.State, "the held HTLC must be reported as ACCEPTED, not silently missing like the base SubscribeInvoices RPC")

	// Release the payer's HTLC now that the assertion above is proven --
	// otherwise it sits ACCEPTED on shared devnet state until LND's own
	// hold-invoice-expiry watcher eventually clears it.
	_, err = lspInvoices.CancelInvoice(ctx, &invoicesrpc.CancelInvoiceMsg{PaymentHash: hash[:]})
	require.NoError(t, err)

	select {
	case payErr := <-paymentDone:
		assert.Error(t, payErr, "the payment must fail back once the invoice is canceled")
	case <-time.After(30 * time.Second):
		t.Fatal("payment did not resolve after CancelInvoice")
	}
}
