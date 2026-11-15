package mocks

import (
	"github.com/flokiorg/lokihub/lsps/manager"
)

func (_mock *MockService) GetLiquidityManager() *manager.LiquidityManager {
	args := _mock.Called()
	return args.Get(0).(*manager.LiquidityManager)
}
