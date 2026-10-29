// Package circlelife is a Circle Wallet's lifecycle (NIP-CW §Lifecycle and
// Deletion): its states, the only edges between them, its grace period, and
// its deletion. Every transition runs inside the caller's transaction.
package circlelife

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// The states a Circle Wallet row can be in. Deleted has no row.
const (
	StateActive    = "active"
	StateSuspended = "suspended"
	StateExpired   = "expired"
	StateClosing   = "closing"
)

// Event is what moves a wallet between states.
type Event int

// The events of NIP-CW §Lifecycle and Deletion.
const (
	// EventSuspend: a successful resolution no longer names the member.
	EventSuspend Event = iota + 1
	// EventResume: a resolution names the member again within its grace.
	EventResume
	// EventExpire: now is past the wallet's expires_at.
	EventExpire
	// EventHostDelete: the host deleted the wallet.
	EventHostDelete
	// EventTeardown: the host tore the whole Hub down.
	EventTeardown
	// EventGraceEnd: a Suspended wallet's grace period ended.
	EventGraceEnd
)

func (e Event) String() string {
	switch e {
	case EventSuspend:
		return "suspend"
	case EventResume:
		return "resume"
	case EventExpire:
		return "expire"
	case EventHostDelete:
		return "host_delete"
	case EventTeardown:
		return "teardown"
	case EventGraceEnd:
		return "grace_end"
	}
	return fmt.Sprintf("event(%d)", int(e))
}

// ErrForbiddenEdge is an event the wallet's state has no edge for: nothing
// returns to Active from Expired or Closing, and there is no edge between
// Expired and Closing.
var ErrForbiddenEdge = errors.New("circlelife: no such edge from this state")

// State is a wallet's stored state; an empty one is Active.
func State(w *db.App) string {
	if w.CircleState == "" {
		return StateActive
	}
	return w.CircleState
}

// EffectiveState is the state a wallet is in at now: an Active or Suspended
// wallet whose expires_at has passed (now later than it) is Expired, whether or
// not the job has recorded it yet (NIP-CW §Lifecycle and Deletion).
func EffectiveState(w *db.App, now time.Time) string {
	state := State(w)
	if (state == StateActive || state == StateSuspended) && w.ExpiresAt != nil && now.After(*w.ExpiresAt) {
		return StateExpired
	}
	return state
}

// IsActive reports whether a wallet is Active at now.
func IsActive(w *db.App, now time.Time) bool { return EffectiveState(w, now) == StateActive }

// Next is the state event moves from to, or ErrForbiddenEdge.
func Next(from string, event Event) (string, error) {
	switch {
	case from == StateActive && event == EventSuspend:
		return StateSuspended, nil
	case from == StateSuspended && event == EventResume:
		return StateActive, nil
	case (from == StateActive || from == StateSuspended) && event == EventExpire:
		return StateExpired, nil
	case (from == StateActive || from == StateSuspended) && (event == EventHostDelete || event == EventTeardown):
		return StateClosing, nil
	case from == StateSuspended && event == EventGraceEnd:
		return StateClosing, nil
	}
	return "", fmt.Errorf("%w: %s on %s", ErrForbiddenEdge, event, from)
}

// Transition applies event to wallet at now inside tx, with graceSecs the Hub's
// grace period: the state, the grace period (started when the wallet first
// leaves Active: at its suspension, at expires_at for an expiry, now for a
// host delete; kept, never restarted, after that; cleared on return to
// Active), and on reaching Expired or Closing the membership row, which frees
// the member to join again. A Suspended wallet returns to Active only while
// now is within its grace. wallet is updated in place.
func Transition(tx *gorm.DB, wallet *db.App, event Event, now time.Time, graceSecs int64) error {
	from := State(wallet)
	if event != EventExpire && EffectiveState(wallet, now) != from {
		// Expired live but not yet recorded: record that first.
		if err := Transition(tx, wallet, EventExpire, now, graceSecs); err != nil {
			return err
		}
		from = StateExpired
	}
	to, err := Next(from, event)
	if err != nil {
		return err
	}
	if event == EventResume && (wallet.CircleGraceEndsAt == nil || now.Unix() >= *wallet.CircleGraceEndsAt) {
		return fmt.Errorf("%w: the grace period is over", ErrForbiddenEdge)
	}
	updates := map[string]interface{}{"circle_state": to, "circle_state_at": now.Unix()}
	switch {
	case to == StateActive:
		updates["circle_grace_started_at"] = nil
		updates["circle_grace_ends_at"] = nil
		wallet.CircleGraceStartedAt, wallet.CircleGraceEndsAt = nil, nil
	case wallet.CircleGraceStartedAt == nil:
		start := now.Unix()
		if event == EventExpire && wallet.ExpiresAt != nil {
			start = wallet.ExpiresAt.Unix()
		}
		end := start + graceSecs
		updates["circle_grace_started_at"] = start
		updates["circle_grace_ends_at"] = end
		wallet.CircleGraceStartedAt, wallet.CircleGraceEndsAt = &start, &end
	}
	if err := tx.Model(&db.App{}).Where("id = ?", wallet.ID).Updates(updates).Error; err != nil {
		return err
	}
	at := now.Unix()
	wallet.CircleState, wallet.CircleStateAt = to, &at
	if to == StateExpired || to == StateClosing {
		// Neither counts for the one-wallet-per-member rule any more.
		if err := tx.Where("wallet_app_id = ?", wallet.ID).Delete(&db.CircleWalletMembership{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// Dust is a wallet's dust threshold.
func Dust(w *db.App) int64 {
	if w.CircleDustThresholdMloki == nil {
		return 0
	}
	return *w.CircleDustThresholdMloki
}

// InFlight reports whether a payment to or from the wallet is in flight: an
// outgoing payment pending, or a held payment the Hub accepted.
func InFlight(tx *gorm.DB, walletID uint) (bool, error) {
	var n int64
	err := tx.Model(&db.Transaction{}).
		Where("app_id = ? AND ((type = ? AND state = ?) OR (type = ? AND state = ?))", walletID,
			constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_PENDING,
			constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_ACCEPTED).
		Count(&n).Error
	return n > 0, err
}
