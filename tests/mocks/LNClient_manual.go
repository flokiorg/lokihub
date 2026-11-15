package mocks

import (
	"context"

	"github.com/stretchr/testify/mock"
)

// SetNodeAlias provides a mock function for the type MockLNClient
func (_mock *MockLNClient) SetNodeAlias(ctx context.Context, alias string) error {
	ret := _mock.Called(ctx, alias)

	if len(ret) == 0 {
		panic("no return value specified for SetNodeAlias")
	}

	var r0 error
	if returnFunc, ok := ret.Get(0).(func(context.Context, string) error); ok {
		r0 = returnFunc(ctx, alias)
	} else {
		r0 = ret.Error(0)
	}
	return r0
}

// MockLNClient_SetNodeAlias_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'SetNodeAlias'
type MockLNClient_SetNodeAlias_Call struct {
	*mock.Call
}

// SetNodeAlias is a helper method to define mock.On call
//   - ctx
//   - alias
func (_e *MockLNClient_Expecter) SetNodeAlias(ctx interface{}, alias interface{}) *MockLNClient_SetNodeAlias_Call {
	return &MockLNClient_SetNodeAlias_Call{Call: _e.mock.On("SetNodeAlias", ctx, alias)}
}

func (_c *MockLNClient_SetNodeAlias_Call) Run(run func(ctx context.Context, alias string)) *MockLNClient_SetNodeAlias_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context), args[1].(string))
	})
	return _c
}

func (_c *MockLNClient_SetNodeAlias_Call) Return(err error) *MockLNClient_SetNodeAlias_Call {
	_c.Call.Return(err)
	return _c
}

func (_c *MockLNClient_SetNodeAlias_Call) RunAndReturn(run func(ctx context.Context, alias string) error) *MockLNClient_SetNodeAlias_Call {
	_c.Call.Return(run)
	return _c
}
