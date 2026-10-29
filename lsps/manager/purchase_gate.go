package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ohstr/nmilat/niplsp"

	"github.com/flokiorg/lokihub/lsps/persist"
)

// ErrLSPNotUsable refuses a message to, or a purchase from, an LSP the user has
// not trusted, or whose newest announcement failed a check, expired or closed;
// a purchase is also refused through a paused one, or when the live check
// finds an announced protocol missing (NIP-LSP §Client Behavior).
var ErrLSPNotUsable = errors.New("this LSP cannot be used")

// LiveCheck is the outcome of NIP-LSP's live check: connect to the LSP, then
// LSPS0 list_protocols (and the announced protocols' get_info).
type LiveCheck struct {
	// Usable: every announced protocol is listed.
	Usable bool
	// Worse: a live value is worse for the buyer than announced.
	Worse bool
	Error string
	// Fields are the compared values (niplsp.CheckLive).
	Fields []niplsp.FieldDiff
}

// LiveChecker runs, stores and returns the live check of the LSP with node key
// or node id key.
type LiveChecker func(ctx context.Context, key string) (*LiveCheck, error)

// SetLiveChecker sets the live check run before each purchase. Without one (a
// node without the LSP catalogue) no live check runs.
func (m *LiquidityManager) SetLiveChecker(f LiveChecker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.liveChecker = f
}

func (m *LiquidityManager) checker() LiveChecker {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.liveChecker
}

// storedCheckUsable is the stored live check's verdict: false only when one
// ran and found the LSP unusable.
func storedCheckUsable(l *persist.LSP) bool {
	if l.LastCheckResult == "" {
		return true
	}
	var r struct {
		Usable bool `json:"usable"`
	}
	if err := json.Unmarshal([]byte(l.LastCheckResult), &r); err != nil {
		return true
	}
	return r.Usable
}

// contactable is the catalogue row of an LSP this node may send LSPS messages
// to: trusted, its newest announcement usable, unexpired and not closed.
func (m *LiquidityManager) contactable(pubkey string) (*persist.LSP, error) {
	if pubkey == "" {
		return nil, fmt.Errorf("%w: no LSP named", ErrLSPNotUsable)
	}
	row, err := m.cfg.LSPManager.GetLSP(pubkey)
	if err != nil || row == nil {
		return nil, fmt.Errorf("%w: it is not in the LSP list", ErrLSPNotUsable)
	}
	switch {
	case !row.Trusted:
		return nil, fmt.Errorf("%w: it is not trusted", ErrLSPNotUsable)
	case !row.Usable:
		return nil, fmt.Errorf("%w: its newest announcement failed a check or expired", ErrLSPNotUsable)
	case row.Status == "closed":
		return nil, fmt.Errorf("%w: it announced that it closed", ErrLSPNotUsable)
	case row.ExpiresAt > 0 && time.Now().Unix() > row.ExpiresAt:
		// Expired since the catalogue's last sweep (NIP-LSP step 4).
		return nil, fmt.Errorf("%w: its newest announcement expired", ErrLSPNotUsable)
	}
	return row, nil
}

// CheckContact refuses an LSPS message to an LSP that is not contactable.
func (m *LiquidityManager) CheckContact(pubkey string) error {
	_, err := m.contactable(pubkey)
	return err
}

// CheckPurchase is what each purchase runs first (NIP-LSP §Client Behavior):
// the LSP is trusted, its newest announcement usable and open, and the live
// check, run now, finds every announced protocol. An automatic purchase also
// refuses a live value worse than announced, since no one can be asked again.
func (m *LiquidityManager) CheckPurchase(ctx context.Context, pubkey string, automatic bool) error {
	_, _, err := m.checkPurchase(ctx, pubkey, automatic)
	return err
}

// checkPurchase is CheckPurchase, returning the catalogue row and the live
// check run (nil without a live checker).
func (m *LiquidityManager) checkPurchase(ctx context.Context, pubkey string, automatic bool) (*persist.LSP, *LiveCheck, error) {
	row, err := m.contactable(pubkey)
	if err != nil {
		return nil, nil, err
	}
	if row.Status != "open" {
		return nil, nil, fmt.Errorf("%w: it is %s and offers nothing new", ErrLSPNotUsable, row.Status)
	}
	live := m.checker()
	if live == nil {
		return row, nil, nil
	}
	res, err := live(ctx, row.NodeKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the live check failed: %v", ErrLSPNotUsable, err)
	}
	if !res.Usable {
		reason := "an announced protocol is missing"
		if res.Error != "" {
			reason = res.Error
		}
		return nil, nil, fmt.Errorf("%w: the live check found it unusable: %s", ErrLSPNotUsable, reason)
	}
	if automatic && res.Worse {
		return nil, nil, fmt.Errorf("%w: its live terms are worse than announced", ErrLSPNotUsable)
	}
	return row, res, nil
}
