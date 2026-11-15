package flnd

import (
	"sync"

	"github.com/flokiorg/flnd/lnrpc"
)

// invoiceCursor is the highest add_index and settle_index the invoice stream
// delivered. A subscription that names them replays every invoice added or
// settled since (lnrpc.InvoiceSubscription), so a settlement made while the
// stream was down is still delivered once it is back. Both start at 0, which
// replays nothing: what settled before the process started is read by the
// startup sweeps.
type invoiceCursor struct {
	mu     sync.Mutex
	add    uint64
	settle uint64
}

// seen records inv as delivered.
func (c *invoiceCursor) seen(inv *lnrpc.Invoice) {
	if inv == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.add = max(c.add, inv.GetAddIndex())
	c.settle = max(c.settle, inv.GetSettleIndex())
}

// subscription resumes the stream after the last invoice delivered.
func (c *invoiceCursor) subscription() *lnrpc.InvoiceSubscription {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &lnrpc.InvoiceSubscription{AddIndex: c.add, SettleIndex: c.settle}
}
