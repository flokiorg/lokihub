package wrapper

import (
	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/chainrpc"
	"github.com/flokiorg/flnd/lnrpc/walletrpc"
)

// WalletKit is flnd's wallet-kit subserver, for on-chain withdrawals.
func (wrapper *FLNDWrapper) WalletKit() walletrpc.WalletKitClient { return wrapper.walletKitClient }

// Lightning is the main lnrpc client.
func (wrapper *FLNDWrapper) Lightning() lnrpc.LightningClient { return wrapper.client }

// ChainKit is flnd's chain-kit subserver on the same connection.
func (wrapper *FLNDWrapper) ChainKit() chainrpc.ChainKitClient {
	return chainrpc.NewChainKitClient(wrapper.conn)
}

// ChainNotifier is flnd's chain notifier on the same connection.
func (wrapper *FLNDWrapper) ChainNotifier() chainrpc.ChainNotifierClient {
	return chainrpc.NewChainNotifierClient(wrapper.conn)
}
