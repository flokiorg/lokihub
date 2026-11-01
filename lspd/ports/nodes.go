package ports

import "github.com/flokiorg/lokihub/lspd/core"

type NodesService interface {
	GetNode(token string) (*core.Node, error)
	GetNodes() []*core.Node
	GetMinChannelCapacity() uint64
}
