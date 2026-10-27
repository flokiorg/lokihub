package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/niplsp"

	"github.com/flokiorg/lokihub/lsps/persist"
)

// LSPSClient is the LSPS side of the live check, over this node's peer
// connection to the LSP.
type LSPSClient interface {
	// Connect connects to nodeID at addr (host:port).
	Connect(ctx context.Context, nodeID, addr string) error
	// ListProtocols is LSPS0 list_protocols.
	ListProtocols(ctx context.Context, nodeID string) ([]int, error)
	// LSPS1Info is LSPS1 get_info's result, as JSON in lokihub's dialect.
	LSPS1Info(ctx context.Context, nodeID string) (json.RawMessage, error)
	// LSPS2Menu is LSPS2 get_info's opening_fee_params_menu, as JSON.
	LSPS2Menu(ctx context.Context, nodeID string) (json.RawMessage, error)
}

// LiveResult is a stored live check (NIP-LSP §Client Behavior, Live check).
type LiveResult struct {
	At int64 `json:"at"`
	// NodeID is the node id the connection reached.
	NodeID string `json:"nodeId"`
	// Addr is the address connected through.
	Addr  string `json:"addr"`
	Error string `json:"error,omitempty"`
	// Usable: every announced protocol is listed by list_protocols.
	Usable bool `json:"usable"`
	// Worse: a live value is worse for the buyer than announced; the user is
	// asked again before buying.
	Worse           bool               `json:"worse"`
	MissingProtocol []int              `json:"missingProtocol,omitempty"`
	Changed         []niplsp.FieldDiff `json:"changed,omitempty"`
	Fields          []niplsp.FieldDiff `json:"fields,omitempty"`
}

// LiveCheck connects to a trusted LSP and compares its live LSPS answers with
// its announcement; the result is stored and returned. It is run when the user
// trusts an LSP and before each purchase.
func (s *Service) LiveCheck(ctx context.Context, key string) (*LiveResult, error) {
	if s.d.Live == nil {
		return nil, errors.New("the liquidity manager is not running")
	}
	var row persist.LSP
	if err := s.d.DB.First(&row, "node_key = ?", NodeKeyOf(key)).Error; err != nil {
		return nil, err
	}
	res := s.liveCheck(ctx, &row)
	raw, _ := json.Marshal(res)
	updates := map[string]any{"last_check_at": res.At, "last_check_result": string(raw)}
	if res.NodeID != "" {
		updates["pubkey"] = res.NodeID
	}
	if err := s.d.DB.Model(&persist.LSP{}).Where("node_key = ?", row.NodeKey).Updates(updates).Error; err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) liveCheck(ctx context.Context, row *persist.LSP) *LiveResult {
	res := &LiveResult{At: s.d.Now().Unix()}
	if !row.Usable || row.LastEventJSON == "" {
		res.Error = "no usable announcement"
		return res
	}
	var ev nip01.Event
	if err := json.Unmarshal([]byte(row.LastEventJSON), &ev); err != nil {
		res.Error = err.Error()
		return res
	}
	opts, err := s.checkOptions(ctx)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	ann, err := niplsp.ParseAnnouncement(&ev, opts)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	// The node id this node's graph knows, else both in turn: a connection to
	// the wrong one fails (NIP-LSP §Client Behavior, Connecting).
	candidates := []string{row.Pubkey}
	if row.Pubkey == "" {
		ids := ann.NodeIDs()
		candidates = ids[:]
	}
	var protocols []int
	var lastErr error
	for _, id := range candidates {
		for _, addr := range OrderedAddrs(ann.Addrs) {
			if err := s.d.Live.Connect(ctx, id, addr); err != nil {
				lastErr = err
				continue
			}
			p, err := s.d.Live.ListProtocols(ctx, id)
			if err != nil {
				lastErr = err
				continue
			}
			res.NodeID, res.Addr, protocols = id, addr, p
			break
		}
		if res.NodeID != "" {
			break
		}
	}
	if res.NodeID == "" {
		res.Error = fmt.Sprintf("could not reach the LSP: %v", lastErr)
		return res
	}
	live := niplsp.Live{Protocols: protocols}
	if ann.HasProtocol(niplsp.ProtocolLSPS1) && contains(protocols, niplsp.ProtocolLSPS1) {
		if raw, err := s.d.Live.LSPS1Info(ctx, res.NodeID); err == nil {
			if opts, err := niplsp.ParseLSPS1Info(raw); err == nil {
				live.LSPS1 = opts
			}
		}
	}
	if ann.HasProtocol(niplsp.ProtocolLSPS2) && contains(protocols, niplsp.ProtocolLSPS2) {
		if raw, err := s.d.Live.LSPS2Menu(ctx, res.NodeID); err == nil {
			if menu, err := niplsp.ParseLSPS2Menu(raw); err == nil {
				live.LSPS2Menu = menu
			}
		}
	}
	diff := niplsp.CheckLive(ann, live)
	res.Usable, res.Worse = diff.Usable(), diff.Worse()
	res.MissingProtocol, res.Changed, res.Fields = diff.MissingProtocol, diff.Changed(), diff.Fields
	return res
}

func contains(l []int, v int) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}
