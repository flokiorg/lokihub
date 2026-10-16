package wrapper

import (
	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
)

// NewFLNDWrapperWithClients wraps already-connected lightning and router clients,
// so tests can stand fakes in for the node's gRPC services.
func NewFLNDWrapperWithClients(client lnrpc.LightningClient, router routerrpc.RouterClient) *FLNDWrapper {
	return &FLNDWrapper{client: client, routerClient: router}
}
