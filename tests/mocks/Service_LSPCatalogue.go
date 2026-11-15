package mocks

import (
	"github.com/flokiorg/lokihub/lsps/discovery"
)

func (_mock *MockService) GetLSPCatalogue() *discovery.Service {
	args := _mock.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*discovery.Service)
}
