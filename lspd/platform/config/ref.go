package config

import "sync/atomic"

// NodeConfigRef holds a NodeConfig behind an atomic pointer so that
// concurrent readers (e.g. the interceptor's HTLC hot path) and a
// concurrent writer (e.g. the dashboard's config-update handler) never
// race on the struct's fields (C-3). Every holder of the same
// *NodeConfigRef sees updates immediately via Load(); Store() atomically
// swaps in an entirely new NodeConfig rather than mutating one in place,
// so a reader that calls Load() once at the start of an operation always
// sees a single, internally consistent snapshot for that operation.
type NodeConfigRef struct {
	ptr atomic.Pointer[NodeConfig]
}

func NewNodeConfigRef(cfg *NodeConfig) *NodeConfigRef {
	r := &NodeConfigRef{}
	r.ptr.Store(cfg)
	return r
}

func (r *NodeConfigRef) Load() *NodeConfig {
	return r.ptr.Load()
}

func (r *NodeConfigRef) Store(cfg *NodeConfig) {
	r.ptr.Store(cfg)
}
