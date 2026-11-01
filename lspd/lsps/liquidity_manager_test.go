package lsps

import (
	"context"
	"fmt"
	"testing"

	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Define local mocks to avoid circular dependencies or import cycle issues
// if we tried to use the ones from server_test.go (which are in same package so it's fine,
// but for clarity/isolation in this new file we can reuse or define minimal ones).
// Since we are in package `lsps`, we can reuse mocks from `server_test.go` if they are exported or in same package.
// `MockLndClient`, `MockOpeningStore` are in `server_test.go` (package `lsps`).

func TestLiquidityManagerMatrix(t *testing.T) {
	tests := []struct {
		name              string
		walletConfirmed   int64
		walletUnconfirmed int64
		reservedLiquidity uint64
		requestedAmount   uint64
		expectedError     bool
		errorContains     string
	}{
		// 1. Sufficient Liquidity
		{
			name:              "Sufficient Liquidity - High Balance",
			walletConfirmed:   10_000_000,
			reservedLiquidity: 0,
			requestedAmount:   1_000_000,
			expectedError:     false,
		},
		{
			name:              "Sufficient Liquidity - Exact Match",
			walletConfirmed:   1_000_000,
			reservedLiquidity: 0,
			requestedAmount:   1_000_000,
			expectedError:     false,
		},
		{
			name:              "Sufficient Liquidity - With Reserved",
			walletConfirmed:   10_000_000,
			reservedLiquidity: 5_000_000,
			requestedAmount:   4_000_000,
			expectedError:     false, // 10M - 5M = 5M >= 4M
		},

		// 2. Insufficient Liquidity
		{
			name:              "Insufficient Liquidity - Zero Balance",
			walletConfirmed:   0,
			reservedLiquidity: 0,
			requestedAmount:   100_000,
			expectedError:     true,
			errorContains:     "insufficient liquidity",
		},
		{
			name:              "Insufficient Liquidity - Over Requested",
			walletConfirmed:   1_000_000,
			reservedLiquidity: 0,
			requestedAmount:   1_000_001,
			expectedError:     true,
			errorContains:     "insufficient liquidity",
		},
		{
			name:              "Insufficient Liquidity - High Reserved",
			walletConfirmed:   10_000_000,
			reservedLiquidity: 9_000_000,
			requestedAmount:   2_000_000,
			expectedError:     true, // 10M - 9M = 1M < 2M
			errorContains:     "insufficient liquidity",
		},

		// 3. Edge Cases
		{
			name:              "Edge Case - Reserved > Balance (Should not happen but handle gracefully)",
			walletConfirmed:   5_000_000,
			reservedLiquidity: 6_000_000,
			requestedAmount:   1_000,
			expectedError:     true, // Available = 0 effectively
			errorContains:     "insufficient liquidity",
		},
		{
			name:              "Edge Case - Zero Request",
			walletConfirmed:   1_000_000,
			reservedLiquidity: 0,
			requestedAmount:   0,
			expectedError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLnd := new(MockLightningClient)
			mockStore := new(MockOpeningStore)

			// Setup LND Mock
			mockLnd.On("WalletBalance", mock.Anything).Return(&lightning.WalletBalanceResult{
				ConfirmedBalance:   tt.walletConfirmed,
				TotalBalance:       tt.walletConfirmed + tt.walletUnconfirmed,
				UnconfirmedBalance: tt.walletUnconfirmed,
			}, nil)

			// Setup Store Mock
			mockStore.On("GetReservedLiquidity").Return(tt.reservedLiquidity, nil)

			lm := NewLiquidityManager(mockLnd, mockStore)

			err := lm.CheckOnChainLiquidity(context.Background(), tt.requestedAmount)

			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			mockLnd.AssertExpectations(t)
			mockStore.AssertExpectations(t)
		})
	}
}

// TestGetLiquiditySnapshot_FetchesBalanceAndReservedTogether is the
// regression test for the shared-fetch refactor (closes the "fleet
// ceiling and CheckOnChainLiquidity each independently re-query reserved
// liquidity" finding): GetLiquiditySnapshot must return exactly one
// wallet-balance read and one reserved-liquidity read, correctly
// combined, callable once per request by anything needing either figure.
func TestGetLiquiditySnapshot_FetchesBalanceAndReservedTogether(t *testing.T) {
	mockLnd := new(MockLightningClient)
	mockStore := new(MockOpeningStore)

	mockLnd.On("WalletBalance", mock.Anything).Return(&lightning.WalletBalanceResult{ConfirmedBalance: 10_000_000}, nil)
	mockStore.On("GetReservedLiquidity").Return(uint64(3_000_000), nil)

	lm := NewLiquidityManager(mockLnd, mockStore)

	snap, err := lm.GetLiquiditySnapshot(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, uint64(10_000_000), snap.ConfirmedBalanceLoki)
	assert.Equal(t, uint64(3_000_000), snap.ReservedLoki)
	assert.Equal(t, uint64(7_000_000), snap.AvailableLoki())

	mockLnd.AssertNumberOfCalls(t, "WalletBalance", 1)
	mockStore.AssertNumberOfCalls(t, "GetReservedLiquidity", 1)
}

func TestLiquiditySnapshot_AvailableLoki_FloorsAtZeroWhenReservedExceedsBalance(t *testing.T) {
	snap := LiquiditySnapshot{ConfirmedBalanceLoki: 5_000, ReservedLoki: 6_000}
	assert.Equal(t, uint64(0), snap.AvailableLoki(), "reserved exceeding balance must floor at zero, not wrap/underflow")
}

func TestLiquidityManager_LndError(t *testing.T) {
	mockLnd := new(MockLightningClient)
	mockStore := new(MockOpeningStore)

	mockLnd.On("WalletBalance", mock.Anything).Return(nil, fmt.Errorf("connection error"))

	lm := NewLiquidityManager(mockLnd, mockStore)

	err := lm.CheckOnChainLiquidity(context.Background(), 1000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get wallet balance")
}

func TestLiquidityManager_StoreError(t *testing.T) {
	mockLnd := new(MockLightningClient)
	mockStore := new(MockOpeningStore)

	mockLnd.On("WalletBalance", mock.Anything).Return(&lightning.WalletBalanceResult{ConfirmedBalance: 1000}, nil)
	mockStore.On("GetReservedLiquidity").Return(uint64(0), fmt.Errorf("db error"))

	lm := NewLiquidityManager(mockLnd, mockStore)

	err := lm.CheckOnChainLiquidity(context.Background(), 1000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get reserved liquidity")
}
