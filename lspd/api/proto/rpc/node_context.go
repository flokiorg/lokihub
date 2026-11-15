package rpc

import (
	context "context"

	"github.com/flokiorg/lokihub/lspd/core"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

type contextKey string
type nodeContext struct {
	token string
	node  *core.Node
}

func GetNode(ctx context.Context) (*core.Node, string, error) {
	nd := ctx.Value(contextKey("node"))
	if nd == nil {
		return nil, "", status.Errorf(codes.PermissionDenied, "Not authorized")
	}

	nodeContext, ok := nd.(*nodeContext)
	if !ok {
		return nil, "", status.Errorf(codes.PermissionDenied, "Not authorized")
	}

	return nodeContext.node, nodeContext.token, nil
}

func WithNode(ctx context.Context, node *core.Node, token string) context.Context {
	return context.WithValue(ctx, contextKey("node"), &nodeContext{
		token: token,
		node:  node,
	})
}
