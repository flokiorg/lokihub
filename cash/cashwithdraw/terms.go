package cashwithdraw

import (
	"errors"
	"fmt"

	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// ScriptTypes are the outputs this Hub pays: version-0 key and script hashes
// and version-1 programs of 32 bytes, which this network's nodes relay and the
// payout engine builds. Derived, never an operator setting.
var ScriptTypes = [][2]int{{0, 20}, {0, 32}, {1, 32}}

// MinFinalDepth is the least final_depth a payout gets (NIP-CASH: at least 6).
const MinFinalDepth = 6

// TermsInput are the operator's inputs for one Cash Hub, and all of them.
type TermsInput struct {
	Enabled       bool   `json:"enabled"`
	FeeBaseMillis int64  `json:"fee_base_millis"`
	FeePpm        int64  `json:"fee_ppm"`
	MinMillis     *int64 `json:"min_millis,omitempty"`
	MaxMillis     *int64 `json:"max_millis,omitempty"`
}

// TermsView is what the operator reads back: the inputs, the derived values and
// whether withdrawals are offered on this hub right now.
type TermsView struct {
	TermsInput
	ScriptTypes [][2]int `json:"script_types"`
	FinalDepth  int      `json:"final_depth"`
	NodeEnabled bool     `json:"node_enabled"`
	Capable     bool     `json:"capable"`
	Offered     bool     `json:"offered"`
}

// NodeEnabled reports the node switch.
func (s *Service) NodeEnabled() bool {
	if s.Cfg == nil {
		return false
	}
	v, err := s.Cfg.Get(config.CashWithdrawEnabledKey, "")
	return err == nil && v == "true"
}

// SetNodeEnabled sets the node switch. Turning it on needs the on-chain
// capability; turning it off stops new quotes and calls only.
func (s *Service) SetNodeEnabled(on bool) error {
	if on && !s.Capable() {
		return fmt.Errorf("%w: this node cannot pay on-chain withdrawals", constants.ErrInvalidParams)
	}
	v := "false"
	if on {
		v = "true"
	}
	return s.Cfg.SetUpdate(config.CashWithdrawEnabledKey, v, "")
}

// Capable reports the node's on-chain capability and a payout engine.
func (s *Service) Capable() bool { return s.Funds != nil && s.payer() != nil }

// Offered reports whether hubAppID offers on-chain withdrawals now: the node
// switch, the hub's own enable, the capability and an engine past its startup
// reconcile. When false both methods are NOT_IMPLEMENTED and nothing is listed;
// committed payouts are still paid, looked up and cancelled.
func (s *Service) Offered(hubAppID uint) bool {
	if s == nil || !s.NodeEnabled() || !s.Capable() || !s.Funds.Ready() {
		return false
	}
	t, err := s.terms(s.DB, hubAppID)
	return err == nil && t != nil && t.Enabled
}

func (s *Service) terms(tx *gorm.DB, hubAppID uint) (*db.CashWithdrawTerms, error) {
	var t db.CashWithdrawTerms
	err := tx.Where("hub_app_id = ?", hubAppID).Limit(1).Find(&t).Error
	if err != nil {
		return nil, err
	}
	if t.HubAppID == 0 {
		return nil, nil
	}
	return &t, nil
}

// FinalDepth is the final_depth a payout committed now on hubAppID gets:
// max(6, the highest on-chain confirmation tier of the hub's order terms).
func (s *Service) FinalDepth(tx *gorm.DB, hubAppID uint) (int, error) {
	tier, err := s.orders().HighestOnchainTier(tx, hubAppID)
	if err != nil {
		return 0, err
	}
	return max(MinFinalDepth, tier), nil
}

// ErrNotCashHub: the app is not a Cash Hub.
var ErrNotCashHub = errors.New("not a Cash Hub")

func (s *Service) requireHub(hubAppID uint) error {
	var hub db.App
	if err := s.DB.Where("id = ? AND kind = ?", hubAppID, db.AppKindCashHub).Limit(1).Find(&hub).Error; err != nil {
		return err
	}
	if hub.ID == 0 {
		return ErrNotCashHub
	}
	return nil
}

// GetTerms is a hub's terms with the derived values.
func (s *Service) GetTerms(hubAppID uint) (*TermsView, error) {
	if err := s.requireHub(hubAppID); err != nil {
		return nil, err
	}
	t, err := s.terms(s.DB, hubAppID)
	if err != nil {
		return nil, err
	}
	fd, err := s.FinalDepth(s.DB, hubAppID)
	if err != nil {
		return nil, err
	}
	v := &TermsView{ScriptTypes: ScriptTypes, FinalDepth: fd, NodeEnabled: s.NodeEnabled(), Capable: s.Capable(),
		Offered: s.Offered(hubAppID)}
	if t != nil {
		v.TermsInput = TermsInput{Enabled: t.Enabled, FeeBaseMillis: t.FeeBaseMillis, FeePpm: int64(t.FeePpm),
			MinMillis: t.MinMillis, MaxMillis: t.MaxMillis}
	}
	return v, nil
}

// PutTerms replaces a hub's terms. It refuses, never clamps: a ppm above
// 1,000,000, a negative value, a minimum above the maximum, and enabling while
// the node switch is off or the node lacks the on-chain capability. A change
// applies to new quotes only: an accepted quote keeps its fee.
func (s *Service) PutTerms(hubAppID uint, in TermsInput) (*TermsView, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{constants.ErrInvalidParams}, a...)...)
	}
	switch {
	case in.FeePpm < 0 || in.FeePpm > 1_000_000:
		return nil, bad("fee_ppm must be 0 to 1000000")
	case in.FeeBaseMillis < 0:
		return nil, bad("fee_base_millis must not be negative")
	case in.MinMillis != nil && *in.MinMillis < 0, in.MaxMillis != nil && *in.MaxMillis < 0:
		return nil, bad("min_millis and max_millis must not be negative")
	case in.MinMillis != nil && in.MaxMillis != nil && *in.MinMillis > *in.MaxMillis:
		return nil, bad("min_millis exceeds max_millis")
	case in.Enabled && !s.NodeEnabled():
		return nil, bad("on-chain withdrawals are off for this node")
	case in.Enabled && !s.Capable():
		return nil, bad("this node cannot pay on-chain withdrawals")
	}
	if err := s.requireHub(hubAppID); err != nil {
		return nil, err
	}
	row := db.CashWithdrawTerms{HubAppID: hubAppID, Enabled: in.Enabled, FeeBaseMillis: in.FeeBaseMillis,
		FeePpm: int(in.FeePpm), MinMillis: in.MinMillis, MaxMillis: in.MaxMillis, UpdatedAt: s.now().Unix()}
	if err := s.DB.Save(&row).Error; err != nil {
		return nil, err
	}
	return s.GetTerms(hubAppID)
}

// StatusTerms is cash_status' withdraw object for hubAppID, or nil when the hub
// does not offer withdrawals.
func (s *Service) StatusTerms(hubAppID uint) *nipcash.WithdrawTerms {
	if !s.Offered(hubAppID) {
		return nil
	}
	t, err := s.terms(s.DB, hubAppID)
	if err != nil || t == nil {
		return nil
	}
	fd, err := s.FinalDepth(s.DB, hubAppID)
	if err != nil {
		return nil
	}
	w := &nipcash.WithdrawTerms{FeeBase: uint64(max(t.FeeBaseMillis, 0)), FeePpm: uint64(max(t.FeePpm, 0)), //nolint:gosec // non-negative
		ScriptTypes: ScriptTypes, FinalDepth: fd}
	if t.MinMillis != nil {
		v := uint64(*t.MinMillis) //nolint:gosec // validated non-negative
		w.MinMillis = &v
	}
	if t.MaxMillis != nil {
		v := uint64(*t.MaxMillis) //nolint:gosec // validated non-negative
		w.MaxMillis = &v
	}
	return w
}

func pays() []nipcash.ScriptType {
	out := make([]nipcash.ScriptType, len(ScriptTypes))
	for i, p := range ScriptTypes {
		out[i] = nipcash.ScriptType{Version: uint8(p[0]), ProgramLen: uint8(p[1])} //nolint:gosec // small constants
	}
	return out
}
