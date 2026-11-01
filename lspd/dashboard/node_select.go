package dashboard

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lsps"
)

// NodeView is one fleet node as list_nodes shows it.
type NodeView struct {
	NodeID string `json:"node_id"` // 66-hex node pubkey
	Name   string `json:"name"`
}

// nodeParam is the node selector shared by the per-node methods.
type nodeParam struct {
	Node string `json:"node"`
}

func (s *Handlers) handleListNodes(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	nodes := s.nodesService.GetNodes()
	out := make([]NodeView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, NodeView{NodeID: hex.EncodeToString(n.NodeId), Name: n.NodeConfig.Load().Name})
	}
	return lsps.NewJsonRpcResponse(req.Id, map[string]any{"nodes": out})
}

// selectNode returns the node req's "node" param names, and its index in
// the fleet. The param is required when the fleet has more than one node;
// with exactly one it may be left out. An unknown node id is refused.
func (s *Handlers) selectNode(req *lsps.JsonRpcRequest) (*core.Node, int, *lsps.JsonRpcResponse) {
	var p nodeParam
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, 0, lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params", err.Error())
		}
	}
	nodes := s.nodesService.GetNodes()
	if len(nodes) == 0 {
		return nil, 0, lsps.NewJsonRpcErrorResponse(req.Id, -32000, "No nodes configured", nil)
	}
	if p.Node == "" {
		if len(nodes) == 1 {
			return nodes[0], 0, nil
		}
		return nil, 0, lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params: node required (the fleet has more than one node)", nil)
	}
	for i, n := range nodes {
		if strings.EqualFold(p.Node, hex.EncodeToString(n.NodeId)) {
			return n, i, nil
		}
	}
	return nil, 0, lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params: unknown node", p.Node)
}
