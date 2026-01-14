package wrapper

import (
	"context"

	"github.com/flokiorg/flnd/lnrpc"
	"google.golang.org/grpc"
)

// SendCustomMessage sends a custom peer message.
func (wrapper *LNDWrapper) SendCustomMessage(ctx context.Context, req *lnrpc.SendCustomMessageRequest, options ...grpc.CallOption) (*lnrpc.SendCustomMessageResponse, error) {
	return wrapper.client.SendCustomMessage(ctx, req, options...)
}

// SubscribeCustomMessages subscribes to custom peer messages.
func (wrapper *LNDWrapper) SubscribeCustomMessages(ctx context.Context, req *lnrpc.SubscribeCustomMessagesRequest, options ...grpc.CallOption) (lnrpc.Lightning_SubscribeCustomMessagesClient, error) {
	return wrapper.client.SubscribeCustomMessages(ctx, req, options...)
}
