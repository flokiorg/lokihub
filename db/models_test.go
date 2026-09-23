package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPublishesNip47InfoEvent pins the rule that keeps a spent cash bill
// unfindable: a cash_wallet must never advertise itself with a kind-13194 info
// event, because that event is authored by the bill's own wallet pubkey and so
// would answer "did this hub ever serve this pubkey" long after the bill was
// hard-deleted — the exact question the deletion exists to make unanswerable.
//
// Every kind is listed explicitly rather than testing only the cash case, so
// that adding a new kind forces a deliberate decision here instead of silently
// inheriting the default.
func TestPublishesNip47InfoEvent(t *testing.T) {
	tests := []struct {
		kind string
		want bool
		why  string
	}{
		{AppKindStandard, true, "an ordinary NWC connection is discovered by relay query"},
		{AppKindIsolated, true, "a sub-wallet is a normal connection its owner looks up"},
		{AppKindCashHub, true, "a hub is a public service; advertising mint_cash is the point"},
		{AppKindCircleHub, true, "a circle hub is likewise a service members connect to"},
		{AppKindCircleWallet, true, "a circle member's wallet is long-lived and owner-known"},
		{AppKindCashWallet, false, "a cash bill is a bearer instrument and must advertise nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			assert.Equal(t, tt.want, PublishesNip47InfoEvent(tt.kind), tt.why)
		})
	}
}

// TestPublishesNip47InfoEvent_UnknownKindAdvertises documents the default for a
// kind this function has never heard of: it advertises. That is the safe
// direction for functionality (a new connection kind keeps working) and the
// unsafe one for privacy, which is why the table above is exhaustive.
func TestPublishesNip47InfoEvent_UnknownKindAdvertises(t *testing.T) {
	assert.True(t, PublishesNip47InfoEvent("some_future_kind"))
}
