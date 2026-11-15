package interceptor

import (
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
)

type InterceptStore interface {
	PaymentInfo(htlcPaymentHash []byte) (string, string, *core.OpeningFeeParams, []byte, []byte, []byte, int64, int64, *wire.OutPoint, *string, error)
	SetFundingTx(paymentHash []byte, channelPoint *wire.OutPoint) error
	RegisterPayment(token string, orderId string, params *core.OpeningFeeParams, destination, paymentHash, paymentSecret []byte, incomingAmountMloki, outgoingAmountMloki int64, tag string) error
	GetLsps2Order(scid uint64) (*core.Lsps2BuyRegistration, error)
	GetPaidLsps1Order(clientNodeId string) (*core.Lsps1Order, error)

	// UpdateLsps1OrderFunding records a channel point as an LSPS1 order's
	// funding and UpdateLsps1OrderState compare-and-set transitions its
	// state. FLSPD-0015: after the interceptor opens a JIT channel for a PAID
	// LSPS1 order (the GetPaidLsps1Order reconnect-fallback path), it must
	// mark the lsps1.orders row funded + COMPLETED -- the LSPS1 analogue of
	// the LSPS2 ClaimLsps2Registration/ConsumeLsps2Registration gate -- so
	// GetPaidLsps1Order stops returning it and a later HTLC or recovery pass
	// cannot open a second channel for the same paid order. UpdateLsps1Order-
	// Funding is a guarded first-write (FLSPD-0021): claimed=false means the
	// order was already funded or the point is claimed by another order.
	UpdateLsps1OrderFunding(orderId string, fundingTxid string, fundingOutpoint int) (bool, error)
	UpdateLsps1OrderState(orderId, newState, newPaymentState, expectedCurrentState string) (bool, error)

	// The four methods below implement Phase 7's fix for F-15/F-22 (a
	// single LSPS2 buy registration could otherwise JIT-open more than one
	// real channel): a 3-state lifecycle (ACTIVE->OPENING->CONSUMED),
	// claimed *before* openChannel is called (an actual concurrency gate),
	// not a post-hoc bookkeeping entry after the fact.

	// ClaimLsps2Registration atomically transitions ACTIVE->OPENING,
	// returning claimed=false (nil error) if the registration is not
	// currently ACTIVE (already OPENING/CONSUMED/EXPIRED, or gone) or has
	// already passed its expires_at -- callers MUST NOT call openChannel
	// unless claimed=true.
	ClaimLsps2Registration(scid uint64) (bool, error)
	// ReleaseLsps2Registration reverts OPENING->ACTIVE (best-effort) after
	// openChannel failed, so a later HTLC can retry the same registration.
	ReleaseLsps2Registration(scid uint64) error
	// ConsumeLsps2Registration atomically transitions OPENING->CONSUMED
	// once a channel is confirmed open, and records the redemption in
	// lsps2.bought_channels (finally using the table -- present in the
	// schema since the original migration, never written to before this).
	// Returns consumed=false (nil error) if the registration was not
	// OPENING (e.g. a concurrent caller already consumed or it expired).
	ConsumeLsps2Registration(scid uint64, channelPoint *wire.OutPoint, paymentSizeMloki, feeMloki uint64) (bool, error)
	// GetStuckLsps2Registrations returns registrations left at OPENING --
	// e.g. by a crash between openChannel succeeding and the
	// ConsumeLsps2Registration commit (T7-13, the LSPS2 analogue of
	// settlement_service.go's recoverStuckOrders/reconcileUnclaimedChannel).
	GetStuckLsps2Registrations() ([]*core.Lsps2BuyRegistration, error)
	// GetClaimedLsps2ChannelPoints returns the set of channel points (as
	// wire.OutPoint.String()'s "hash:index" form) already recorded in
	// lsps2.bought_channels -- i.e. already attributed to some completed
	// LSPS2 purchase. RecoverStuckLsps2Registrations uses this to exclude
	// them when looking for a peer's unclaimed channel, mirroring LSPS1's
	// own claimedChannelPoints/findUnclaimedChannelPoint: without it, a
	// peer with any pre-existing channel (from an earlier order, a manual
	// open, or a prior JIT purchase) could have that unrelated channel
	// wrongly attributed to a stuck registration.
	GetClaimedLsps2ChannelPoints() (map[string]bool, error)
}
