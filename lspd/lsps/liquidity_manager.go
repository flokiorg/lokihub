package lsps

import (
	"context"
	"fmt"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/rs/zerolog/log"
)

// LiquiditySnapshot is a single wallet-balance + reserved-liquidity read.
// Callers that need more than one liquidity-related check within the same
// request (e.g. the fleet-wide reservation ceiling AND a plain
// availability check) should fetch this once and derive both checks from
// it, rather than each independently re-querying the same
// reserved-liquidity sum and re-fetching the same wallet balance.
type LiquiditySnapshot struct {
	ConfirmedBalanceLoki uint64
	ReservedLoki         uint64
}

// AvailableLoki is the confirmed balance minus what's already reserved,
// floored at zero (a reserved sum that has somehow overtaken the real
// balance means nothing is available, not a negative/wrapped uint64).
func (s LiquiditySnapshot) AvailableLoki() uint64 {
	if s.ReservedLoki > s.ConfirmedBalanceLoki {
		return 0
	}
	return s.ConfirmedBalanceLoki - s.ReservedLoki
}

// LiquidityManager defines the interface for managing on-chain liquidity
type LiquidityManager interface {
	// GetAvailableOnChainLiquidity returns the confirmed wallet balance minus reserved liquidity
	GetAvailableOnChainLiquidity(ctx context.Context) (uint64, error)

	// GetReservedOnChainLiquidity returns the total liquidity reserved for pending orders
	GetReservedOnChainLiquidity(ctx context.Context) (uint64, error)

	// GetLiquiditySnapshot fetches wallet balance and reserved liquidity
	// together, once -- the shared read behind GetAvailableOnChainLiquidity,
	// CheckOnChainLiquidity, and any caller (e.g. the LSPS1/2 handlers'
	// fleet-ceiling check) that needs either figure without triggering its
	// own separate reserved-liquidity query for the same request.
	GetLiquiditySnapshot(ctx context.Context) (LiquiditySnapshot, error)

	// CheckOnChainLiquidity checks if the requested amount is available
	CheckOnChainLiquidity(ctx context.Context, amountSat uint64) error

	// ReserveLsps1Order and ReserveLsps2Buy close F-20 (the liquidity
	// check-then-reserve TOCTOU race): unlike CheckOnChainLiquidity (a
	// plain read, safe to call early for a fast, cheap rejection before
	// doing expensive work like creating a hold invoice), these
	// atomically re-check liquidity and persist the row in one
	// transaction, immediately before really committing to the order/
	// registration. Returns reserved=false (nil error) if the real-time
	// re-check finds insufficient liquidity -- callers must treat this
	// exactly like CheckOnChainLiquidity's error case (reject the
	// request, compensate any earlier side effect), not as an unexpected
	// failure.
	ReserveLsps1Order(ctx context.Context, order *core.Lsps1Order) (reserved bool, err error)
	ReserveLsps2Buy(ctx context.Context, reg *core.Lsps2BuyRegistration) (reserved bool, err error)
}

type liquidityManager struct {
	lndClient lightning.Client
	store     ports.OpeningStore
}

func NewLiquidityManager(lndClient lightning.Client, store ports.OpeningStore) LiquidityManager {
	return &liquidityManager{
		lndClient: lndClient,
		store:     store,
	}
}

func (m *liquidityManager) GetReservedOnChainLiquidity(ctx context.Context) (uint64, error) {
	// Query the store for the sum of LSP balances in CREATED orders
	reserved, err := m.store.GetReservedLiquidity()
	if err != nil {
		return 0, fmt.Errorf("failed to get reserved liquidity: %w", err)
	}
	return reserved, nil
}

func (m *liquidityManager) GetLiquiditySnapshot(ctx context.Context) (LiquiditySnapshot, error) {
	balance, err := m.lndClient.WalletBalance(ctx)
	if err != nil {
		return LiquiditySnapshot{}, fmt.Errorf("failed to get wallet balance: %w", err)
	}
	reserved, err := m.GetReservedOnChainLiquidity(ctx)
	if err != nil {
		return LiquiditySnapshot{}, err
	}
	return LiquiditySnapshot{
		ConfirmedBalanceLoki: uint64(balance.ConfirmedBalance),
		ReservedLoki:         reserved,
	}, nil
}

func (m *liquidityManager) GetAvailableOnChainLiquidity(ctx context.Context) (uint64, error) {
	snap, err := m.GetLiquiditySnapshot(ctx)
	if err != nil {
		return 0, err
	}
	if snap.ReservedLoki > snap.ConfirmedBalanceLoki {
		log.Warn().
			Uint64("confirmed_balance", snap.ConfirmedBalanceLoki).
			Uint64("reserved_liquidity", snap.ReservedLoki).
			Msg("LiquidityManager: Reserved liquidity exceeds confirmed balance!")
		return 0, nil
	}
	return snap.AvailableLoki(), nil
}

func (m *liquidityManager) CheckOnChainLiquidity(ctx context.Context, amountSat uint64) error {
	available, err := m.GetAvailableOnChainLiquidity(ctx)
	if err != nil {
		return err
	}

	if amountSat > available {
		return fmt.Errorf("insufficient liquidity: requested %d loki, available %d loki", amountSat, available)
	}

	return nil
}

func (m *liquidityManager) ReserveLsps1Order(ctx context.Context, order *core.Lsps1Order) (bool, error) {
	balance, err := m.lndClient.WalletBalance(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get wallet balance: %w", err)
	}
	return m.store.ReserveLsps1Order(order, uint64(balance.ConfirmedBalance))
}

func (m *liquidityManager) ReserveLsps2Buy(ctx context.Context, reg *core.Lsps2BuyRegistration) (bool, error) {
	balance, err := m.lndClient.WalletBalance(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get wallet balance: %w", err)
	}
	return m.store.ReserveLsps2Buy(reg, uint64(balance.ConfirmedBalance))
}
