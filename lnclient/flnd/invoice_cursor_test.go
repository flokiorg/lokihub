package flnd

import (
	"sync"
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/stretchr/testify/assert"
)

func TestInvoiceCursor_FirstSubscriptionReplaysNothing(t *testing.T) {
	var c invoiceCursor
	sub := c.subscription()
	assert.Zero(t, sub.AddIndex)
	assert.Zero(t, sub.SettleIndex)
	c.seen(nil)
	assert.Zero(t, c.subscription().AddIndex)
}

func TestInvoiceCursor_ResumesAfterTheHighestIndexesDelivered(t *testing.T) {
	var c invoiceCursor
	c.seen(&lnrpc.Invoice{AddIndex: 7, State: lnrpc.Invoice_OPEN})
	c.seen(&lnrpc.Invoice{AddIndex: 3, SettleIndex: 2, State: lnrpc.Invoice_SETTLED})
	// An older invoice delivered late never moves the cursor back.
	c.seen(&lnrpc.Invoice{AddIndex: 5, SettleIndex: 1, State: lnrpc.Invoice_SETTLED})
	sub := c.subscription()
	assert.EqualValues(t, 7, sub.AddIndex)
	assert.EqualValues(t, 2, sub.SettleIndex)
}

func TestInvoiceCursor_ConcurrentReadsAndWrites(t *testing.T) {
	var c invoiceCursor
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); c.seen(&lnrpc.Invoice{AddIndex: uint64(i), SettleIndex: uint64(i)}) }() //nolint:gosec // small
		go func() { defer wg.Done(); _ = c.subscription() }()
	}
	wg.Wait()
	assert.EqualValues(t, 49, c.subscription().SettleIndex)
}
