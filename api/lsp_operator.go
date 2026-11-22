package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/flokiorg/lokihub/lsps/daemonclient"
)

// ErrLSPServiceNotConfigured: this hub is not attached to an lspd daemon (no
// LSP service endpoint is configured), so the LSP-operator back-office is off.
var ErrLSPServiceNotConfigured = errors.New("the LSP service (lspd) is not configured on this hub")

// LSPOperatorAPI is the LSP-operator back-office (decision D10): the lspd
// dashboard methods the Hub proxies over NIP-98 via daemonclient. The HTTP
// /api/lsp/* routes and the MCP tools reach the same daemonclient.Client. Each
// result is lspd's JSON verbatim; callers that show it to someone else (MCP)
// map it to their own views.
type LSPOperatorAPI interface {
	LSPOperatorStatus(ctx context.Context) (*LSPServiceInfo, error)
	LSPOperatorSummary(ctx context.Context) (json.RawMessage, error)
	LSPOperatorOrders(ctx context.Context) (json.RawMessage, error)
	LSPOperatorOrder(ctx context.Context, orderID string) (json.RawMessage, error)
	LSPOperatorPeers(ctx context.Context) (json.RawMessage, error)
	LSPOperatorJitChannels(ctx context.Context) (json.RawMessage, error)
	LSPOperatorNodes(ctx context.Context) (json.RawMessage, error)
	LSPOperatorAnalytics(ctx context.Context) (json.RawMessage, error)
	LSPOperatorConfig(ctx context.Context, node string) (json.RawMessage, error)
	// LSPOperatorUpdateConfig replaces a node's config with cfg (the full
	// non-secret config, as LSPOperatorConfig returns it); lspd keeps the
	// node's secrets. node may be empty for a single-node fleet.
	LSPOperatorUpdateConfig(ctx context.Context, node string, cfg json.RawMessage) (json.RawMessage, error)
	LSPOperatorAnnounceState(ctx context.Context, node string) (json.RawMessage, error)
	// LSPOperatorSetAnnounceState applies "pause", "resume" or "unlist".
	LSPOperatorSetAnnounceState(ctx context.Context, action, node string) (json.RawMessage, error)
	LSPOperatorRecheckAnnouncement(ctx context.Context, node string) (json.RawMessage, error)
}

var _ LSPOperatorAPI = (*api)(nil)

// SetLSPOperatorClient attaches the Hub's lspd client (built by the HTTP
// service from the operator env) so the API shares it — one signer, one
// status cache. A nil client leaves the back-office off.
func (api *api) SetLSPOperatorClient(c *daemonclient.Client) {
	api.lspOperatorMu.Lock()
	api.lspOperator = c
	api.lspOperatorMu.Unlock()
}

func (api *api) lspOperatorClient() (*daemonclient.Client, error) {
	api.lspOperatorMu.RLock()
	c := api.lspOperator
	api.lspOperatorMu.RUnlock()
	if c == nil {
		return nil, ErrLSPServiceNotConfigured
	}
	return c, nil
}

// LSPServiceStatusInfo maps a daemonclient status onto the /api/info block.
func LSPServiceStatusInfo(s daemonclient.Status) *LSPServiceInfo {
	info := &LSPServiceInfo{
		Configured: s.Configured,
		Reachable:  s.Reachable,
		Attached:   s.Attached,
		Endpoint:   s.Endpoint,
		Pubkey:     s.Pubkey,
		Version:    s.Version,
		Alias:      s.Alias,
		Error:      s.Error,
	}
	if !s.CheckedAt.IsZero() {
		checkedAt := s.CheckedAt
		info.CheckedAt = &checkedAt
	}
	return info
}

// LSPOperatorStatus is the attach status; never an error when unconfigured
// (Configured is false).
func (api *api) LSPOperatorStatus(ctx context.Context) (*LSPServiceInfo, error) {
	c, err := api.lspOperatorClient()
	if err != nil {
		return &LSPServiceInfo{Configured: false}, nil
	}
	return LSPServiceStatusInfo(c.Status(ctx)), nil
}

func (api *api) lspOperatorCall(fn func(*daemonclient.Client) (json.RawMessage, error)) (json.RawMessage, error) {
	c, err := api.lspOperatorClient()
	if err != nil {
		return nil, err
	}
	return fn(c)
}

func (api *api) LSPOperatorSummary(ctx context.Context) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) { return c.GetSummary(ctx) })
}

func (api *api) LSPOperatorOrders(ctx context.Context) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) { return c.ListOrders(ctx) })
}

func (api *api) LSPOperatorOrder(ctx context.Context, orderID string) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) {
		return c.GetOrderDetail(ctx, orderID)
	})
}

func (api *api) LSPOperatorPeers(ctx context.Context) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) { return c.ListPeers(ctx) })
}

func (api *api) LSPOperatorJitChannels(ctx context.Context) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) { return c.ListJitChannels(ctx) })
}

func (api *api) LSPOperatorNodes(ctx context.Context) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) { return c.ListNodes(ctx) })
}

func (api *api) LSPOperatorAnalytics(ctx context.Context) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) { return c.GetAnalytics(ctx) })
}

func (api *api) LSPOperatorConfig(ctx context.Context, node string) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) { return c.GetConfig(ctx, node) })
}

func (api *api) LSPOperatorUpdateConfig(ctx context.Context, node string, cfg json.RawMessage) (json.RawMessage, error) {
	params := map[string]json.RawMessage{"config": cfg}
	if node != "" {
		n, err := json.Marshal(node)
		if err != nil {
			return nil, err
		}
		params["node"] = n
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) {
		return c.UpdateConfig(ctx, body)
	})
}

func (api *api) LSPOperatorAnnounceState(ctx context.Context, node string) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) {
		return c.GetAnnounceState(ctx, node)
	})
}

func (api *api) LSPOperatorSetAnnounceState(ctx context.Context, action, node string) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) {
		return c.SetAnnounceState(ctx, action, node)
	})
}

func (api *api) LSPOperatorRecheckAnnouncement(ctx context.Context, node string) (json.RawMessage, error) {
	return api.lspOperatorCall(func(c *daemonclient.Client) (json.RawMessage, error) {
		return c.RecheckAnnouncement(ctx, node)
	})
}
