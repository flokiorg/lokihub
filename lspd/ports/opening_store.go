package ports

import (
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
)

type OpeningStore interface {
	GetFeeParamsSettings(token string) ([]*core.OpeningFeeParamsSetting, error)
	SetFeeParams(token string, validity time.Duration, params core.OpeningFeeParams) error
	RegisterLsps2Buy(reg *core.Lsps2BuyRegistration) error
	GetLsps2BuyRegistration(scid uint64) (*core.Lsps2BuyRegistration, error)
	RegisterLsps1Order(order *core.Lsps1Order) error
	GetLsps1Order(orderId string) (*core.Lsps1Order, error)
	GetLsps1OrderByPaymentInvoice(paymentInvoice string) (*core.Lsps1Order, error)
	GetPaidLsps1Order(clientNodeId string) (*core.Lsps1Order, error)
	GetPaidLsps1Orders(clientNodeId string) ([]*core.Lsps1Order, error)
	GetAllPaidLsps1Orders() ([]*core.Lsps1Order, error)
	GetAllCreatedLsps1Orders() ([]*core.Lsps1Order, error)
	ListLsps1Orders(limit, offset int) ([]*core.Lsps1Order, error)
	// UpdateLsps1OrderState performs a compare-and-set: it only applies
	// newState/newPaymentState if the order's current order_state still
	// equals expectedCurrentState. Returns updated=false (with a nil
	// error) if another writer already moved the order out of
	// expectedCurrentState — callers MUST check this and not proceed with
	// side effects (like opening a channel) on a failed CAS, since it
	// means a concurrent writer already handled this transition.
	UpdateLsps1OrderState(orderId string, newState string, newPaymentState string, expectedCurrentState string) (updated bool, err error)
	// UpdateLsps1OrderFunding claims a channel point (funding_txid,
	// funding_outpoint) for an order. A DB-level unique constraint (S-5)
	// makes this atomic: claimed=false (nil error) means another writer
	// already claimed this exact channel point for a different order — a
	// normal, expected outcome under concurrency, not a failure. Callers
	// MUST check this and treat claimed=false the same as "no unclaimed
	// channel was actually available," not proceed as if the claim
	// succeeded.
	UpdateLsps1OrderFunding(orderId string, fundingTxid string, fundingOutpoint int) (claimed bool, err error)
	RegisterLsps5Webhook(webhook *core.Lsps5Webhook) error
	GetLsps5Webhooks(clientNodeId string) ([]*core.Lsps5Webhook, error)
	RemoveLsps5Webhook(clientNodeId, appName string) error
	CountLsps1Orders(clientNodeId string, state string, excludeExpiredBefore *time.Time) (int, error)
	// CountLsps2PendingRegistrations mirrors CountLsps1Orders for the
	// standing per-peer cap on LSPS2 buy registrations (Phase 7, F-17):
	// counts this peer's currently-live (ACTIVE or OPENING, not yet
	// expired) registrations.
	CountLsps2PendingRegistrations(peerId string) (int, error)
	CountLsps5Webhooks(clientNodeId string) (int, error)
	GetReservedLiquidity() (uint64, error)
	ExpireStaleOrders() (int64, error)
	// ExpireStaleLsps2Registrations flips ACTIVE registrations past
	// expires_at to EXPIRED (Phase 7, T7-6). Deliberately does NOT match
	// OPENING: a registration mid-channel-open still commits real,
	// about-to-be-spent capital and must not be swept out from under an
	// in-flight open (F-15's second-pass fix).
	ExpireStaleLsps2Registrations() (int64, error)

	// ReserveLsps1Order and ReserveLsps2Buy close F-20 (the liquidity
	// check-then-reserve TOCTOU race): each re-checks reserved liquidity
	// against availableWalletBalanceLoki and persists the row in ONE
	// transaction, guarded by a Postgres advisory lock, so two concurrent
	// callers can no longer both pass a check against the same
	// now-stale RESERVED-LIQUIDITY SUM before either row exists. The
	// caller supplies the wallet balance (a real LND RPC) so that slow
	// network call stays outside the lock -- only the DB
	// read-sum-compare-insert runs under it. Returns reserved=false (nil
	// error) if the re-check finds insufficient liquidity; callers must
	// not treat that as a generic failure (no compensating action needed
	// on the DB side) but SHOULD compensate any earlier side effect (e.g.
	// an LND hold invoice already created) the same way an outright
	// registration failure would.
	//
	// Scope note (second-pass finding, not eliminated by the above): the
	// wallet-balance figure itself is a snapshot taken BEFORE the lock is
	// acquired, not re-verified inside it -- a genuinely concurrent,
	// unrelated on-chain spend (a manual withdrawal, another channel's
	// funding broadcast outside this reservation system entirely) landing
	// in that window could still let a reservation through against a
	// now-stale, too-generous balance. This is a real, narrower residual
	// gap than the TOCTOU race above, deliberately left open: closing it
	// would mean re-fetching wallet balance (a slow RPC) INSIDE the
	// advisory lock, reintroducing the exact lock-held-across-a-slow-RPC
	// problem this design avoids. It's non-catastrophic in practice: the
	// actual channel-open that follows still goes through LND's own
	// real-time UTXO selection, which fails on its own if the real balance
	// genuinely isn't there -- caught by the normal open-channel-failure
	// path (compensate/release), not a silent double-spend.
	ReserveLsps1Order(order *core.Lsps1Order, availableWalletBalanceLoki uint64) (reserved bool, err error)
	ReserveLsps2Buy(reg *core.Lsps2BuyRegistration, availableWalletBalanceLoki uint64) (reserved bool, err error)
}
