package lsps

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	lspslogic "github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeCustomMessagesStream implements lnrpc.Lightning_SubscribeCustomMessagesClient
// with real logic only for Recv() (all subscribeAndListen calls) -- the
// embedded nil grpc.ClientStream satisfies the rest of the interface and
// would panic if any of those methods were ever called, which
// subscribeAndListen doesn't.
//
// Recv blocking on ctx.Done() (set via withContext, populated from the
// context subscribeAndListen actually passed to SubscribeCustomMessages --
// see the MockLndClient.On(...).Run(...) wiring below) matters, not just
// blocking forever: a real gRPC stream's Recv() returns once its context
// is canceled, which is exactly what subscribeAndListen's quit-channel
// watcher goroutine relies on to unblock a stream with nothing new to
// deliver. A fake that only ever plain-blocks would make any test
// depending on prompt shutdown pass or hang based on unrelated goroutine
// scheduling, not the behavior under test.
type fakeCustomMessagesStream struct {
	grpc.ClientStream
	messages []*lnrpc.CustomMessage
	recvErr  error
	idx      int
	ctx      context.Context
}

func (f *fakeCustomMessagesStream) withContext(ctx context.Context) *fakeCustomMessagesStream {
	f.ctx = ctx
	return f
}

func (f *fakeCustomMessagesStream) Recv() (*lnrpc.CustomMessage, error) {
	if f.idx < len(f.messages) {
		msg := f.messages[f.idx]
		f.idx++
		return msg, nil
	}
	if f.recvErr != nil {
		return nil, f.recvErr
	}
	if f.ctx != nil {
		<-f.ctx.Done()
		return nil, status.Error(codes.Canceled, "context canceled")
	}
	select {}
}

// mockMessageClient is a local MessageClient mock -- lsps.MockLndClient
// (the equivalent used by lsps' own tests) lives in a _test.go file and
// so isn't importable across the package boundary; this narrower mock
// only needs the two methods MessageClient itself declares.
type mockMessageClient struct {
	mock.Mock
}

func (m *mockMessageClient) SubscribeCustomMessages(ctx context.Context, req *lnrpc.SubscribeCustomMessagesRequest) (lnrpc.Lightning_SubscribeCustomMessagesClient, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(lnrpc.Lightning_SubscribeCustomMessagesClient), args.Error(1)
}

func (m *mockMessageClient) SendCustomMessage(ctx context.Context, msg *lnrpc.SendCustomMessageRequest) (*lnrpc.SendCustomMessageResponse, error) {
	args := m.Called(ctx, msg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lnrpc.SendCustomMessageResponse), args.Error(1)
}

func lspsMessage(peer []byte, payload interface{}) *lnrpc.CustomMessage {
	data, _ := json.Marshal(payload)
	return &lnrpc.CustomMessage{Peer: peer, Type: LspsMessageId, Data: data}
}

// fakeRouter is a minimal Router: these tests are about the transport
// loop's own behavior (message-type filtering, shutdown,
// marshal/unmarshal, and that handleMessage calls Route and sends back
// whatever it returns), not about the real LSPS0/1/2/5 dispatch logic
// (lsps.Handlers' own tests cover that). It always returns the same
// canned response, echoing the request's Id like a real handler would.
type fakeRouter struct {
	result interface{}
}

func (f *fakeRouter) Route(req *lspslogic.JsonRpcRequest, peer []byte) *lspslogic.JsonRpcResponse {
	return lspslogic.NewJsonRpcResponse(req.Id, f.result)
}

func newTestServer(messageClient MessageClient) *Server {
	return NewServer(messageClient, &fakeRouter{result: map[string]string{"ok": "true"}})
}

// TestHandleMessage_ValidRequest_RoutesAndReplies covers handleMessage's
// full round trip: unmarshal -> route -> marshal -> SendCustomMessage.
func TestHandleMessage_ValidRequest_RoutesAndReplies(t *testing.T) {
	mockLnd := new(mockMessageClient)
	var sentData []byte
	mockLnd.On("SendCustomMessage", mock.Anything, mock.MatchedBy(func(req *lnrpc.SendCustomMessageRequest) bool {
		sentData = req.Data
		return req.Type == LspsMessageId
	})).Return(&lnrpc.SendCustomMessageResponse{}, nil)

	server := newTestServer(mockLnd)
	msg := lspsMessage([]byte{0x01}, lspslogic.JsonRpcRequest{JsonRpc: "2.0", Method: "lsps0.list_protocols", Id: "req-1"})

	server.handleMessage(msg)

	require.NotEmpty(t, sentData)
	var resp lspslogic.JsonRpcResponse
	require.NoError(t, json.Unmarshal(sentData, &resp))
	assert.Nil(t, resp.Error)
	mockLnd.AssertCalled(t, "SendCustomMessage", mock.Anything, mock.Anything)
}

// TestHandleMessage_UnparsableJSON_DoesNotReply covers the malformed-input
// guard: handleMessage must give up quietly (no reply sent) rather than
// try to construct a response for a request it couldn't even parse an ID
// out of.
func TestHandleMessage_UnparsableJSON_DoesNotReply(t *testing.T) {
	mockLnd := new(mockMessageClient)
	server := newTestServer(mockLnd)
	msg := &lnrpc.CustomMessage{Peer: []byte{0x01}, Type: LspsMessageId, Data: []byte(`not-json`)}

	server.handleMessage(msg)

	mockLnd.AssertNotCalled(t, "SendCustomMessage", mock.Anything, mock.Anything)
}

// TestSubscribeAndListen_DispatchesLspsMessagesOnly covers the message-type
// filter (only LspsMessageId-typed custom messages are handled) and that
// a normal stream-end (context canceled) is treated as a clean shutdown,
// not an error to retry on.
func TestSubscribeAndListen_DispatchesLspsMessagesOnly(t *testing.T) {
	mockLnd := new(mockMessageClient)
	var handledCount int32
	var mu sync.Mutex
	mockLnd.On("SendCustomMessage", mock.Anything, mock.Anything).Run(func(mock.Arguments) {
		mu.Lock()
		handledCount++
		mu.Unlock()
	}).Return(&lnrpc.SendCustomMessageResponse{}, nil)

	stream := &fakeCustomMessagesStream{
		messages: []*lnrpc.CustomMessage{
			lspsMessage([]byte{0x01}, lspslogic.JsonRpcRequest{JsonRpc: "2.0", Method: "lsps0.list_protocols", Id: "1"}),
			{Peer: []byte{0x01}, Type: 99999, Data: []byte(`{}`)}, // not an LSPS message, must be ignored
		},
		recvErr: status.Error(codes.Canceled, "context canceled"),
	}
	mockLnd.On("SubscribeCustomMessages", mock.Anything, mock.Anything).Return(stream, nil)

	server := newTestServer(mockLnd)
	server.quit = make(chan struct{})

	err := server.subscribeAndListen()
	require.NoError(t, err, "a Canceled recv error must be treated as a clean shutdown, not propagated as an error to retry on")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return handledCount == 1
	}, time.Second, 10*time.Millisecond, "exactly the one LspsMessageId-typed message must be routed; the other type must be silently ignored")
}

// TestSubscribeAndListen_RecvErrorPropagates covers the retry-worthy error
// path: a non-Canceled Recv error must be returned so messageLoop's
// caller knows to back off and retry, not swallowed like a clean shutdown.
func TestSubscribeAndListen_RecvErrorPropagates(t *testing.T) {
	mockLnd := new(mockMessageClient)
	stream := &fakeCustomMessagesStream{recvErr: errors.New("connection reset")}
	mockLnd.On("SubscribeCustomMessages", mock.Anything, mock.Anything).Return(stream, nil)

	server := newTestServer(mockLnd)
	server.quit = make(chan struct{})

	err := server.subscribeAndListen()
	assert.Error(t, err)
}

// TestSubscribeAndListen_SubscribeErrorPropagates covers the case where
// the initial SubscribeCustomMessages call itself fails.
func TestSubscribeAndListen_SubscribeErrorPropagates(t *testing.T) {
	mockLnd := new(mockMessageClient)
	mockLnd.On("SubscribeCustomMessages", mock.Anything, mock.Anything).Return(nil, assert.AnError)

	server := newTestServer(mockLnd)
	server.quit = make(chan struct{})

	err := server.subscribeAndListen()
	assert.Error(t, err)
}

// TestStartStop_ReturnsPromptly covers Start/Stop/messageLoop's lifecycle:
// Stop must unblock messageLoop (and, transitively, subscribeAndListen's
// own quit-triggered context cancellation) rather than hang waiting for a
// stream that will never produce a normal Recv() error on its own.
func TestStartStop_ReturnsPromptly(t *testing.T) {
	mockLnd := new(mockMessageClient)
	stream := &fakeCustomMessagesStream{} // blocks in Recv() until its context is canceled
	mockLnd.On("SubscribeCustomMessages", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		stream.withContext(args.Get(0).(context.Context))
	}).Return(stream, nil)

	server := newTestServer(mockLnd)
	server.Start()

	done := make(chan struct{})
	go func() {
		server.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return promptly")
	}
}

// TestStop_IsIdempotent covers quitOnce: calling Stop twice must not panic
// on a double-close of the quit channel.
func TestStop_IsIdempotent(t *testing.T) {
	mockLnd := new(mockMessageClient)
	stream := &fakeCustomMessagesStream{}
	mockLnd.On("SubscribeCustomMessages", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		stream.withContext(args.Get(0).(context.Context))
	}).Return(stream, nil)

	server := newTestServer(mockLnd)
	server.Start()

	done := make(chan struct{})
	go func() {
		server.Stop()
		server.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("calling Stop twice must not hang or panic")
	}
}

// blockingRouter blocks in Route until release is closed, recording how many
// handlers are in flight at once so a test can prove the server caps them.
type blockingRouter struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	entered  int
	release  chan struct{}
}

func (r *blockingRouter) Route(req *lspslogic.JsonRpcRequest, _ []byte) *lspslogic.JsonRpcResponse {
	r.mu.Lock()
	r.inFlight++
	r.entered++
	if r.inFlight > r.maxSeen {
		r.maxSeen = r.inFlight
	}
	r.mu.Unlock()
	<-r.release
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
	return lspslogic.NewJsonRpcResponse(req.Id, map[string]string{"ok": "true"})
}

// TestServer_BoundsConcurrentHandlers (R-LSP-5) pins the in-flight cap: a flood
// of custom messages arriving faster than they are handled spawns at most
// defaultMaxInFlight handler goroutines at once; the rest are dropped, not
// queued into unbounded goroutines.
func TestServer_BoundsConcurrentHandlers(t *testing.T) {
	const extra = 16
	mockLnd := new(mockMessageClient)
	mockLnd.On("SendCustomMessage", mock.Anything, mock.Anything).Return(&lnrpc.SendCustomMessageResponse{}, nil).Maybe()

	msgs := make([]*lnrpc.CustomMessage, 0, defaultMaxInFlight+extra)
	for i := 0; i < defaultMaxInFlight+extra; i++ {
		msgs = append(msgs, lspsMessage([]byte{0x01}, lspslogic.JsonRpcRequest{JsonRpc: "2.0", Method: "lsps2.get_info", Id: "req"}))
	}
	stream := &fakeCustomMessagesStream{messages: msgs, recvErr: status.Error(codes.Canceled, "context canceled")}
	mockLnd.On("SubscribeCustomMessages", mock.Anything, mock.Anything).Return(stream, nil)

	router := &blockingRouter{release: make(chan struct{})}
	server := NewServer(mockLnd, router)
	server.quit = make(chan struct{})

	require.NoError(t, server.subscribeAndListen())

	require.Eventually(t, func() bool {
		router.mu.Lock()
		defer router.mu.Unlock()
		return router.inFlight == defaultMaxInFlight
	}, 2*time.Second, 5*time.Millisecond, "the in-flight cap's worth of handlers should be running at once")

	// Give any erroneously-spawned extra handler a chance to appear.
	time.Sleep(50 * time.Millisecond)
	router.mu.Lock()
	assert.LessOrEqual(t, router.maxSeen, defaultMaxInFlight, "no more than the in-flight cap may run concurrently")
	assert.Equal(t, defaultMaxInFlight, router.entered, "messages past the cap are dropped, not spawned")
	router.mu.Unlock()

	close(router.release) // let the blocked handlers finish
}
