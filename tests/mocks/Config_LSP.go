package mocks

import (
	mock "github.com/stretchr/testify/mock"
)

// GetLSP provides a mock function for the type MockConfig
func (_mock *MockConfig) GetLSP() string {
	ret := _mock.Called()

	var r0 string
	if returnFunc, ok := ret.Get(0).(func() string); ok {
		r0 = returnFunc()
	} else {
		r0 = ret.Get(0).(string)
	}
	return r0
}

// MockConfig_GetLSP_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'GetLSP'
type MockConfig_GetLSP_Call struct {
	*mock.Call
}

// GetLSP is a helper method to define mock.On call
func (_e *MockConfig_Expecter) GetLSP() *MockConfig_GetLSP_Call {
	return &MockConfig_GetLSP_Call{Call: _e.mock.On("GetLSP")}
}

func (_c *MockConfig_GetLSP_Call) Run(run func()) *MockConfig_GetLSP_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run()
	})
	return _c
}

func (_c *MockConfig_GetLSP_Call) Return(s string) *MockConfig_GetLSP_Call {
	_c.Call.Return(s)
	return _c
}

func (_c *MockConfig_GetLSP_Call) RunAndReturn(run func() string) *MockConfig_GetLSP_Call {
	_c.Call.Return(run)
	return _c
}

// SetLSP provides a mock function for the type MockConfig
func (_mock *MockConfig) SetLSP(value string) error {
	ret := _mock.Called(value)

	var r0 error
	if returnFunc, ok := ret.Get(0).(func(string) error); ok {
		r0 = returnFunc(value)
	} else {
		r0 = ret.Error(0)
	}
	return r0
}

// MockConfig_SetLSP_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'SetLSP'
type MockConfig_SetLSP_Call struct {
	*mock.Call
}

// SetLSP is a helper method to define mock.On call
//   - value
func (_e *MockConfig_Expecter) SetLSP(value interface{}) *MockConfig_SetLSP_Call {
	return &MockConfig_SetLSP_Call{Call: _e.mock.On("SetLSP", value)}
}

func (_c *MockConfig_SetLSP_Call) Run(run func(value string)) *MockConfig_SetLSP_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(string))
	})
	return _c
}

func (_c *MockConfig_SetLSP_Call) Return(err error) *MockConfig_SetLSP_Call {
	_c.Call.Return(err)
	return _c
}

func (_c *MockConfig_SetLSP_Call) RunAndReturn(run func(value string) error) *MockConfig_SetLSP_Call {
	_c.Call.Return(run)
	return _c
}
