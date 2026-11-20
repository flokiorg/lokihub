package mcp

import (
	"encoding/hex"
	"encoding/json"
)

// ExecutionStore persists completed writes by (connection, tool, key) so a
// retry after a restart never runs them again (critic O1).
type ExecutionStore interface {
	Get(appID uint, tool, key string) (argsHash string, result []byte, ok bool, err error)
	Put(appID uint, tool, key, argsHash string, result []byte) error
}

// ActivityStore persists the owner's activity log.
type ActivityStore interface {
	Save(e ActivityEntry) error
	Recent(appID uint, limit int) ([]ActivityEntry, error)
}

// SetPersistence wires durable stores (nil leaves the in-memory behaviour).
func (s *Server) SetPersistence(exec ExecutionStore, act ActivityStore) {
	s.idem.persist = exec
	s.activity.store = act
}

// persisted is how a completed result is stored and restored.
type persisted struct {
	OwnerQueued string          `json:"owner_queued,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

func encodeResult(res any) []byte {
	var p persisted
	if q, ok := res.(ownerQueued); ok {
		p.OwnerQueued = q.id
	} else {
		b, err := json.Marshal(res)
		if err != nil {
			return nil
		}
		p.Result = b
	}
	b, _ := json.Marshal(p)
	return b
}

// restoredResult is a result loaded from the store; helpers decode it into
// their typed output.
type restoredResult struct{ raw json.RawMessage }

func decodeResult(b []byte) any {
	var p persisted
	if json.Unmarshal(b, &p) != nil {
		return restoredResult{}
	}
	if p.OwnerQueued != "" {
		return ownerQueued{id: p.OwnerQueued}
	}
	return restoredResult{raw: p.Result}
}

// asOut converts an idempotency result to a tool's output type, decoding a
// result restored from the store.
func asOut[Out any](res any) Out {
	var out Out
	switch r := res.(type) {
	case Out:
		return r
	case restoredResult:
		_ = json.Unmarshal(r.raw, &out)
	}
	return out
}

func hashHex(h [32]byte) string { return hex.EncodeToString(h[:]) }
