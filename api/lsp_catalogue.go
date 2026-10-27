package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ohstr/nmilat/niplsp"

	"github.com/flokiorg/lokihub/lsps/discovery"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/lsps/persist"
)

// LSPCatalogueAPI is the LSP catalogue built from NIP-LSP announcements
// (WP-26), mirrored by Wails. The list changes only through the trust API.
type LSPCatalogueAPI interface {
	// HandleListLSPs is the catalogue: every LSP announced for this node's chain
	// and network, unranked (LP-1).
	HandleListLSPs(ctx context.Context) ([]LSPCatalogueEntry, error)
	// HandleAddLSP looks up one node's announcement (LP-4); refused when none.
	HandleAddLSP(ctx context.Context, req *AddLSPRequest) (*LSPCatalogueEntry, error)
	// HandleTrustLSP records the user's trust; trusting runs the live check.
	HandleTrustLSP(ctx context.Context, key string, trusted bool) (*LSPCatalogueEntry, error)
	// HandleLSPLiveCheck runs the live check again (before a purchase).
	HandleLSPLiveCheck(ctx context.Context, key string) (*LSPCatalogueEntry, error)
	// ListLSPs are the trusted LSPs.
	ListLSPs() ([]manager.SettingsLSP, error)
}

// AddLSPRequest adds an LSP by node key or node id; its announcement supplies
// everything else (no pubkey@host input).
type AddLSPRequest struct {
	Pubkey string `json:"pubkey"`
}

// TrustLSPRequest is PUT /api/lsps/{pubkey}/trust.
type TrustLSPRequest struct {
	Trusted bool `json:"trusted"`
}

// LSPCatalogueEntry is one LSP as Settings > Services shows it. Title, content
// and website are the LSP's claims; graph facts are this node's own.
type LSPCatalogueEntry struct {
	persist.LSP
	Addrs     []string              `json:"addrs"`
	Protocols []int                 `json:"protocols"`
	LSPS1     []niplsp.Option       `json:"lsps1"`
	LSPS2Fees []niplsp.FeeRow       `json:"lsps2Fees"`
	LSPS5     []niplsp.Option       `json:"lsps5"`
	Languages []string              `json:"languages"`
	Relays    []string              `json:"relays"`
	LastCheck *discovery.LiveResult `json:"lastCheck,omitempty"`
}

// ErrLSPCatalogueNotRunning is a call before the node runs (503).
var ErrLSPCatalogueNotRunning = errors.New("the LSP catalogue is not running")

func (api *api) lspCatalogue() (*discovery.Service, error) {
	if api.svc == nil {
		return nil, ErrLSPCatalogueNotRunning
	}
	c := api.svc.GetLSPCatalogue()
	if c == nil {
		return nil, ErrLSPCatalogueNotRunning
	}
	return c, nil
}

func catalogueEntry(l persist.LSP) LSPCatalogueEntry {
	e := LSPCatalogueEntry{LSP: l}
	_ = json.Unmarshal([]byte(l.Addrs), &e.Addrs)
	_ = json.Unmarshal([]byte(l.Protocols), &e.Protocols)
	_ = json.Unmarshal([]byte(l.LSPS1), &e.LSPS1)
	_ = json.Unmarshal([]byte(l.LSPS2Fees), &e.LSPS2Fees)
	_ = json.Unmarshal([]byte(l.LSPS5), &e.LSPS5)
	_ = json.Unmarshal([]byte(l.Languages), &e.Languages)
	_ = json.Unmarshal([]byte(l.Relays), &e.Relays)
	if l.LastCheckResult != "" {
		var r discovery.LiveResult
		if json.Unmarshal([]byte(l.LastCheckResult), &r) == nil {
			e.LastCheck = &r
		}
	}
	return e
}

func (api *api) HandleListLSPs(ctx context.Context) ([]LSPCatalogueEntry, error) {
	rows, err := api.lspManager.ListLSPs()
	if err != nil {
		return nil, err
	}
	out := make([]LSPCatalogueEntry, 0, len(rows))
	for _, l := range rows {
		out = append(out, catalogueEntry(l))
	}
	return out, nil
}

func (api *api) HandleAddLSP(ctx context.Context, req *AddLSPRequest) (*LSPCatalogueEntry, error) {
	if req == nil || strings.Contains(req.Pubkey, "@") {
		return nil, fmt.Errorf("%w: add an LSP by its node key; its announcement gives its addresses", discovery.ErrRefused)
	}
	c, err := api.lspCatalogue()
	if err != nil {
		return nil, err
	}
	row, err := c.Add(ctx, req.Pubkey)
	if err != nil {
		return nil, err
	}
	e := catalogueEntry(*row)
	return &e, nil
}

func (api *api) HandleTrustLSP(ctx context.Context, key string, trusted bool) (*LSPCatalogueEntry, error) {
	row, err := api.lspManager.GetLSP(key)
	if err != nil {
		return nil, fmt.Errorf("%w: no such LSP", discovery.ErrRefused)
	}
	if !trusted {
		// Untrusting with open channels keeps the existing refusal.
		if row.Pubkey != "" {
			if err := api.checkHasChannels(ctx, row.Pubkey); err != nil {
				return nil, fmt.Errorf("%w: %v", discovery.ErrRefused, err)
			}
		}
		if err := api.lspManager.SetTrusted(row.NodeKey, false); err != nil {
			return nil, err
		}
		if row.Pubkey != "" {
			_ = api.disconnectLSP(ctx, row.Pubkey)
		}
		return api.lspEntry(row.NodeKey)
	}
	if !row.Usable || row.Status == string(niplsp.StatusClosed) {
		return nil, fmt.Errorf("%w: this LSP has no usable open announcement", discovery.ErrRefused)
	}
	if err := api.lspManager.SetTrusted(row.NodeKey, true); err != nil {
		return nil, err
	}
	// Trusting runs the live check (NIP-LSP §Client Behavior); its result is
	// stored and shown, and the trust stays whatever it says.
	if c, err := api.lspCatalogue(); err == nil {
		_, _ = c.LiveCheck(ctx, row.NodeKey)
	}
	return api.lspEntry(row.NodeKey)
}

func (api *api) HandleLSPLiveCheck(ctx context.Context, key string) (*LSPCatalogueEntry, error) {
	row, err := api.lspManager.GetLSP(key)
	if err != nil {
		return nil, fmt.Errorf("%w: no such LSP", discovery.ErrRefused)
	}
	if !row.Trusted {
		return nil, fmt.Errorf("%w: only a trusted LSP is contacted", discovery.ErrRefused)
	}
	c, err := api.lspCatalogue()
	if err != nil {
		return nil, err
	}
	if _, err := c.LiveCheck(ctx, row.NodeKey); err != nil {
		return nil, err
	}
	return api.lspEntry(row.NodeKey)
}

func (api *api) lspEntry(key string) (*LSPCatalogueEntry, error) {
	row, err := api.lspManager.GetLSP(key)
	if err != nil {
		return nil, err
	}
	e := catalogueEntry(*row)
	return &e, nil
}
