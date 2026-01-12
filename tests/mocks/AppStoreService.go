package mocks

import (
	"github.com/flokiorg/lokihub/pkg/appstore"
	mock "github.com/stretchr/testify/mock"
)

type MockAppStoreService struct {
	mock.Mock
}

func (_m *MockAppStoreService) Start() {
	_m.Called()
}

func (_m *MockAppStoreService) ListApps() []appstore.App {
	ret := _m.Called()
	return ret.Get(0).([]appstore.App)
}

func (_m *MockAppStoreService) GetLogoPath(appId string) (string, error) {
	ret := _m.Called(appId)
	return ret.String(0), ret.Error(1)
}
