package service

import (
	"encoding/json"
	"fmt"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// privateServableMethods is the set of methods reachable over the private
// transport — an explicit allowlist, not a reuse of the standard path's dispatch.
//
// This is a security decision rather than a convenience. The standard path's
// switch routes every NIP-47 method there is, including get_balance, pay_invoice
// and create_connection. Reusing it would make all of them reachable on the bill
// transport, where the caller authenticates with a bill's connection key. A bill
// is a bearer instrument: whoever holds the string holds it. Letting that key
// reach the hub's own wallet methods would be a privilege escalation, and it would
// arrive silently, as a working feature nobody asked for.
//
// So the private transport serves exactly what a bill is for, plus the circle join
// the user scoped in. Anything absent is omitted from the response rather than
// answered with an error, so the allowlist leaks nothing about what a hub supports.
//
// mint_cash is deliberately NOT here. It is gated by CASH_HUB_SCOPE and belongs to
// a Cash Hub app connection, which stays on kind 23194 — and it is also the one
// method with no retry idempotency, which is what makes the in-memory replay set
// safe. Adding it here would quietly invalidate that reasoning.
var privateServableMethods = map[string]struct{}{
	constants.NIP47MethodCashStatus:         {},
	constants.NIP47MethodCashRedeem:         {},
	constants.NIP47MethodCashTransfer:       {},
	constants.NIP47MethodCashConsolidate:    {},
	constants.NIP47MethodCreateCircleWallet: {},
}

// isPrivateServableMethod reports whether a method may be served over the private
// transport.
func isPrivateServableMethod(method string) bool {
	_, ok := privateServableMethods[method]
	return ok
}

// itemResponseCollector adapts one controller call into a collected result.
//
// Every cash controller is written as one request producing one published
// response: it takes a publishFunc and calls it. A batch needs the opposite —
// run several and gather the outcomes into a single envelope. Rather than give
// the private transport its own copy of each handler, this captures what the
// controller would have published and leaves the handler untouched.
//
// That reuse is the point. cash_redeem, cash_transfer and cash_consolidate carry
// the money, and their guarantees — atomic claims, no double spend, the
// concurrency behaviour covered by the existing race tests — hold precisely
// because there is one implementation. A second one for the private path would be
// a second thing to get right.
type itemResponseCollector struct {
	response *models.Response
	// published counts calls, so a handler that answers twice is caught rather
	// than silently having its first answer overwritten.
	published int
}

// publish matches the controllers' publishFunc signature. Tags are discarded: on
// the standard path they route a reply event, but a batch's reply is addressed
// once for the whole envelope by its reply_to.
func (c *itemResponseCollector) publish(response *models.Response, _ nostr.Tags) {
	c.published++
	c.response = response
}

// result converts what the controller produced into an envelope result.
//
// A controller that published nothing yields no result at all, and the caller
// omits the item. That is not a defensive shrug: omission is how this transport
// avoids becoming an existence oracle, so "no answer" has to stay representable
// all the way through rather than being turned into an error here.
func (c *itemResponseCollector) result(itemID string) (transport.Result, bool, error) {
	if c.published == 0 || c.response == nil {
		return transport.Result{}, false, nil
	}
	if c.published > 1 {
		// A handler answering twice means one of its answers is being discarded.
		// Refuse rather than pick, since picking would hide the bug.
		return transport.Result{}, false, fmt.Errorf(
			"handler published %d responses for item %q; exactly one is expected", c.published, itemID)
	}

	out := transport.Result{ID: itemID, ResultType: c.response.ResultType}

	if c.response.Error != nil {
		out.Error = &transport.ResultError{
			Code:    c.response.Error.Code,
			Message: c.response.Error.Message,
		}
		// An error and a result together would be ambiguous, and the response
		// codec rejects it — so never carry both, even if a handler set both.
		return out, true, nil
	}

	if c.response.Result != nil {
		encoded, err := json.Marshal(c.response.Result)
		if err != nil {
			return transport.Result{}, false, fmt.Errorf("encode result for item %q: %w", itemID, err)
		}
		out.Result = encoded
	}
	return out, true, nil
}
