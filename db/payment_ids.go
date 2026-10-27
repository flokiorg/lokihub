package db

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ParsePaymentIDs reads an unfinal_payment_ids column (nil: none).
func ParsePaymentIDs(raw *string) ([]uint, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	var ids []uint
	if err := json.Unmarshal([]byte(*raw), &ids); err != nil {
		return nil, fmt.Errorf("unfinal_payment_ids: %w", err)
	}
	return ids, nil
}

// MergePaymentIDs is the unfinal_payment_ids of a slice made from slices with
// the given columns: their union, sorted; nil when empty.
func MergePaymentIDs(raws ...*string) (*string, error) {
	seen := map[uint]bool{}
	var out []uint
	for _, r := range raws {
		ids, err := ParsePaymentIDs(r)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}
