package service

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/ohstr/nmilat/nipcash/transport"
)

// answeredRejection marks an item Decode refused with a code, so the
// dispatcher answers it rather than omitting it (NIP-CASH §The Private
// Transport: "an item over max_consolidate_sources with QUOTA_EXCEEDED").
// Decode keeps no id for a refused item, so unwrap reads it from the
// plaintext and wraps the refusal's error.
type answeredRejection struct {
	id, method string
	err        error
}

func (a *answeredRejection) Error() string { return a.err.Error() }
func (a *answeredRejection) Unwrap() error { return a.err }

// markAnsweredRejections wraps each coded rejection of envelope with its
// item's id and method, read from the plaintext Decode was given. An item whose
// id cannot be read stays omitted.
func markAnsweredRejections(plaintext []byte, envelope *transport.Envelope) {
	var wire struct {
		Items []json.RawMessage `json:"items"`
	}
	for i, r := range envelope.Rejected {
		if r.Code == "" {
			continue
		}
		if wire.Items == nil && json.Unmarshal(plaintext, &wire) != nil {
			return
		}
		if r.Index < 0 || r.Index >= len(wire.Items) {
			continue
		}
		var item struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(wire.Items[r.Index], &item) != nil || item.ID == "" {
			continue
		}
		envelope.Rejected[i].Err = &answeredRejection{id: item.ID, method: item.Method, err: r.Err}
	}
}

type indexedResult struct {
	index  int
	result transport.Result
}

// answeredRejections are the refusals the reply carries, in array order.
func answeredRejections(envelope *transport.Envelope) []indexedResult {
	var out []indexedResult
	for _, r := range envelope.Rejected {
		var a *answeredRejection
		if r.Code == "" || !errors.As(r.Err, &a) {
			continue
		}
		out = append(out, indexedResult{index: r.Index, result: transport.Result{
			ID: a.id, ResultType: a.method,
			Error: &transport.ResultError{Code: r.Code, Message: a.err.Error()},
		}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].index < out[j].index })
	return out
}

// itemWireIndices is the array index each of envelope.Items had on the wire:
// the indices no rejection took, in order.
func itemWireIndices(envelope *transport.Envelope) []int {
	taken := make(map[int]bool, len(envelope.Rejected))
	for _, r := range envelope.Rejected {
		taken[r.Index] = true
	}
	out := make([]int, 0, len(envelope.Items))
	for w := 0; len(out) < len(envelope.Items); w++ {
		if !taken[w] {
			out = append(out, w)
		}
	}
	return out
}
