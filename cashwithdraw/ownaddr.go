package cashwithdraw

import (
	"context"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// ownCacheTTL is how long the node wallet's script set is reused.
const ownCacheTTL = 60 * time.Second

type ownCacheEntry struct {
	at      time.Time
	scripts map[string]bool
}

// ChangeScripts is implemented by a payout engine that knows the change
// outputs of its own versions.
type ChangeScripts interface {
	IsChangeScriptTx(tx *gorm.DB, script []byte) (bool, error)
}

// IsOwnScript reports whether script pays this Hub (own_address): a NIP-CO
// order address in any state, burnt included; an address of the node wallet,
// every account and change included (cached for a minute, and refreshed once on
// a miss); or the change output of one of the payout engine's versions. A quote
// never asks; only the call does, last.
func (s *Service) IsOwnScript(ctx context.Context, script []byte) (bool, error) {
	if own, err := s.orders().IsOrderScript(s.DB, script); err != nil || own {
		return own, err
	}
	if cs, ok := s.Funds.(ChangeScripts); ok {
		if own, err := cs.IsChangeScriptTx(s.DB, script); err != nil || own {
			return own, err
		}
	}
	key := hex.EncodeToString(script)
	s.ownMu.Lock()
	c := s.ownCache
	s.ownMu.Unlock()
	fresh := !c.at.IsZero() && s.now().Sub(c.at) < ownCacheTTL
	if fresh && c.scripts[key] {
		return true, nil
	}
	// Stale, or a miss: read the wallet again.
	p := s.payer()
	if p == nil {
		return false, nil
	}
	list, err := p.ListWalletScripts(ctx)
	if err != nil {
		return false, err
	}
	set := make(map[string]bool, len(list))
	for _, sc := range list {
		set[hex.EncodeToString(sc)] = true
	}
	s.ownMu.Lock()
	s.ownCache = ownCacheEntry{at: s.now(), scripts: set}
	s.ownMu.Unlock()
	return set[key], nil
}
