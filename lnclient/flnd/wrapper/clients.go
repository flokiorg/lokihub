package wrapper

import (
	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
	"github.com/flokiorg/flnd/lnrpc/signrpc"
)

// NewFLNDWrapperWithClients wraps already-connected lightning and router clients,
// so tests can stand fakes in for the node's gRPC services.
func NewFLNDWrapperWithClients(client lnrpc.LightningClient, router routerrpc.RouterClient) *FLNDWrapper {
	return &FLNDWrapper{client: client, routerClient: router}
}

// WithSigner sets the signer client, so tests can stand a fake in for signrpc.
func (wrapper *FLNDWrapper) WithSigner(signer signrpc.SignerClient) *FLNDWrapper {
	wrapper.signerClient = signer
	return wrapper
}
