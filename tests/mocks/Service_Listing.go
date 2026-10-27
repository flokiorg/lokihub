package mocks

import (
	"github.com/flokiorg/lokihub/listing"
)

func (_mock *MockService) GetListingService() *listing.Service {
	args := _mock.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*listing.Service)
}
