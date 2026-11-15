package cashorder

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// NewOrderAddress is a fresh address for an on-chain order of hubID (NIP-CO
// §On-chain: never given out before, never again). It is recorded in its own
// transaction before it is returned, so a crash cannot hand it out twice; an
// address the node already gave out elsewhere is refused.
//
// lnclient offers no dedicated account for order addresses, so they come from
// the node's default wallet account; this table is what keeps them apart from
// the node's other addresses.
func NewOrderAddress(ctx context.Context, deps Deps, hubID uint, now time.Time) (string, error) {
	for range 3 {
		address, err := deps.LN.GetNewOnchainAddress(ctx)
		if err != nil {
			return "", fmt.Errorf("get an order address: %w", err)
		}
		if address == "" {
			return "", fmt.Errorf("the node returned no address")
		}
		err = deps.DB.Create(&db.CashOrderAddress{Address: address, HubAppID: hubID, IssuedAt: now.Unix()}).Error
		if err == nil {
			return address, nil
		}
		if !db.IsDuplicateKeyError(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("the node keeps returning addresses already given out")
}

// BurnAddress marks an address never to be bound: its placement failed. A
// payment that still reaches it is kept by the Hub.
func BurnAddress(gormDB *gorm.DB, address string) {
	if err := gormDB.Model(&db.CashOrderAddress{}).Where("address = ?", address).Update("burnt", true).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to burn an order address")
	}
}
