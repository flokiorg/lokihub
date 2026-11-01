package ports

import (
	"time"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/core"
)

type OpeningService interface {
	GetFeeParamsMenu(token string, privateKey *btcec.PrivateKey) ([]*core.OpeningFeeParams, error)
	ValidateOpeningFeeParams(params *core.OpeningFeeParams, publicKey *btcec.PublicKey) bool
	// IsCurrentChainFeeCheaper answers "is it still economically safe to
	// honor an expired opening_fee_params quote" (Phase 7, F-18): compares
	// the LIVE current on-chain feerate (nil if the estimator itself
	// failed -- treated conservatively as "not cheaper") against the
	// node's configured referenceFeeratePerVByte, the feerate its
	// business fee floor was calibrated to absorb. Before this fix, this
	// method never consulted the chain-fee estimator at all -- it compared
	// two entries from the same, rarely-changed config table, so it was
	// nearly always true regardless of real fee movement.
	IsCurrentChainFeeCheaper(currentFeeratePerVByte *float64, referenceFeeratePerVByte float64) bool
	GetLsps1Order(orderId string) (*core.Lsps1Order, error)
	RegisterLsps5Webhook(webhook *core.Lsps5Webhook) error
	GetLsps5Webhooks(clientNodeId string) ([]*core.Lsps5Webhook, error)
	RemoveLsps5Webhook(clientNodeId, appName string) error
	CountLsps1Orders(clientNodeId string, state string, excludeExpiredBefore *time.Time) (int, error)
	CountLsps2PendingRegistrations(peerId string) (int, error)
	CountLsps5Webhooks(clientNodeId string) (int, error)
}
