package mocks

import (
	"context"

	"github.com/flokiorg/lokihub/lnclient"
	mock "github.com/stretchr/testify/mock"
)

// SendCustomMessage provides a mock function for the type MockLNClient
func (_mock *MockLNClient) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	ret := _mock.Called(ctx, peerPubkey, msgType, data)

	if len(ret) == 0 {
		panic("no return value specified for SendCustomMessage")
	}

	var r0 error
	if returnFunc, ok := ret.Get(0).(func(context.Context, string, uint32, []byte) error); ok {
		r0 = returnFunc(ctx, peerPubkey, msgType, data)
	} else {
		r0 = ret.Error(0)
	}
	return r0
}

// MockLNClient_SendCustomMessage_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'SendCustomMessage'
type MockLNClient_SendCustomMessage_Call struct {
	*mock.Call
}

// SendCustomMessage is a helper method to define mock.On call
//   - ctx
//   - peerPubkey
//   - msgType
//   - data
func (_e *MockLNClient_Expecter) SendCustomMessage(ctx interface{}, peerPubkey interface{}, msgType interface{}, data interface{}) *MockLNClient_SendCustomMessage_Call {
	return &MockLNClient_SendCustomMessage_Call{Call: _e.mock.On("SendCustomMessage", ctx, peerPubkey, msgType, data)}
}

func (_c *MockLNClient_SendCustomMessage_Call) Run(run func(ctx context.Context, peerPubkey string, msgType uint32, data []byte)) *MockLNClient_SendCustomMessage_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context), args[1].(string), args[2].(uint32), args[3].([]byte))
	})
	return _c
}

func (_c *MockLNClient_SendCustomMessage_Call) Return(err error) *MockLNClient_SendCustomMessage_Call {
	_c.Call.Return(err)
	return _c
}

func (_c *MockLNClient_SendCustomMessage_Call) RunAndReturn(run func(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error) *MockLNClient_SendCustomMessage_Call {
	_c.Call.Return(run)
	return _c
}

// SubscribeCustomMessages provides a mock function for the type MockLNClient
func (_mock *MockLNClient) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	ret := _mock.Called(ctx)

	if len(ret) == 0 {
		panic("no return value specified for SubscribeCustomMessages")
	}

	var r0 <-chan lnclient.CustomMessage
	var r1 <-chan error
	var r2 error
	if returnFunc, ok := ret.Get(0).(func(context.Context) (<-chan lnclient.CustomMessage, <-chan error, error)); ok {
		return returnFunc(ctx)
	}
	if returnFunc, ok := ret.Get(0).(func(context.Context) <-chan lnclient.CustomMessage); ok {
		r0 = returnFunc(ctx)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(<-chan lnclient.CustomMessage)
		}
	}
	if returnFunc, ok := ret.Get(1).(func(context.Context) <-chan error); ok {
		r1 = returnFunc(ctx)
	} else {
		if ret.Get(1) != nil {
			r1 = ret.Get(1).(<-chan error)
		}
	}
	if returnFunc, ok := ret.Get(2).(func(context.Context) error); ok {
		r2 = returnFunc(ctx)
	} else {
		r2 = ret.Error(2)
	}
	return r0, r1, r2
}

// MockLNClient_SubscribeCustomMessages_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'SubscribeCustomMessages'
type MockLNClient_SubscribeCustomMessages_Call struct {
	*mock.Call
}

// SubscribeCustomMessages is a helper method to define mock.On call
//   - ctx
func (_e *MockLNClient_Expecter) SubscribeCustomMessages(ctx interface{}) *MockLNClient_SubscribeCustomMessages_Call {
	return &MockLNClient_SubscribeCustomMessages_Call{Call: _e.mock.On("SubscribeCustomMessages", ctx)}
}

func (_c *MockLNClient_SubscribeCustomMessages_Call) Run(run func(ctx context.Context)) *MockLNClient_SubscribeCustomMessages_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(context.Context))
	})
	return _c
}

func (_c *MockLNClient_SubscribeCustomMessages_Call) Return(_a0 <-chan lnclient.CustomMessage, _a1 <-chan error, _a2 error) *MockLNClient_SubscribeCustomMessages_Call {
	_c.Call.Return(_a0, _a1, _a2)
	return _c
}

func (_c *MockLNClient_SubscribeCustomMessages_Call) RunAndReturn(run func(context.Context) (<-chan lnclient.CustomMessage, <-chan error, error)) *MockLNClient_SubscribeCustomMessages_Call {
	_c.Call.Return(run)
	return _c
}
