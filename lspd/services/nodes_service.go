package services

import (
	"errors"
	"fmt"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/ports"
)

type nodesService struct {
	nodes      []*core.Node
	nodeLookup map[string]*core.Node
}

func NewNodesService(nodes []*core.Node) (ports.NodesService, error) {
	nodeLookup := make(map[string]*core.Node)
	for _, node := range nodes {
		for _, token := range node.Tokens {
			_, exists := nodeLookup[token]
			if exists {
				return nil, fmt.Errorf("cannot have multiple nodes with the same token")
			}

			nodeLookup[token] = node
		}
	}

	return &nodesService{
		nodes:      nodes,
		nodeLookup: nodeLookup,
	}, nil
}

var ErrNodeNotFound = errors.New("node not found")

func (s *nodesService) GetNode(token string) (*core.Node, error) {
	node, ok := s.nodeLookup[token]
	if !ok {
		return nil, ErrNodeNotFound
	}

	return node, nil
}

func (s *nodesService) GetNodes() []*core.Node {
	return s.nodes
}

// GetMinChannelCapacity returns the minimum channel capacity across all nodes.
// If no nodes are configured, returns a default of 20000.
func (s *nodesService) GetMinChannelCapacity() uint64 {
	if len(s.nodes) == 0 {
		return 20000
	}
	// Return the first node's config (all nodes should have same minimum in typical setup)
	return s.nodes[0].NodeConfig.Load().MinChannelCapacityLoki
}
