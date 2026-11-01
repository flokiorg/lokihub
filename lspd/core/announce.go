package core

import (
	"fmt"
	"time"
)

// AnnounceStatus is a node's NIP-LSP announcement `status` (T8-2): runtime
// state set from the dashboard and kept in the database, not in nodes.yaml.
type AnnounceStatus string

const (
	// AnnounceOpen: the LSP takes orders. The state of a node never changed.
	AnnounceOpen AnnounceStatus = "open"
	// AnnouncePaused: no new orders for now (dashboard Pause). Like closed,
	// it is what the announcement says; flspd does not refuse orders on it.
	AnnouncePaused AnnounceStatus = "paused"
	// AnnounceClosed: the LSP has stopped selling (dashboard Unlist). The
	// publisher announces it once, then stops publishing.
	AnnounceClosed AnnounceStatus = "closed"
)

// Valid reports whether s is one of the three NIP-LSP statuses.
func (s AnnounceStatus) Valid() bool {
	return s == AnnounceOpen || s == AnnouncePaused || s == AnnounceClosed
}

// AnnounceAction is a dashboard action on the announcement status.
type AnnounceAction string

const (
	AnnouncePause  AnnounceAction = "pause"
	AnnounceResume AnnounceAction = "resume"
	AnnounceUnlist AnnounceAction = "unlist"
)

// ErrAnnounceTransition is returned for an action the current status does
// not allow.
var ErrAnnounceTransition = fmt.Errorf("announce status transition not allowed")

// NextAnnounceStatus applies action to current:
//
//   - pause:  open → paused (paused stays paused); refused while closed,
//     since an unlisted LSP is listed again with resume, not pause.
//   - resume: paused or closed → open (open stays open). Resuming a closed
//     node lists it again.
//   - unlist: any → closed.
func NextAnnounceStatus(current AnnounceStatus, action AnnounceAction) (AnnounceStatus, error) {
	if !current.Valid() {
		return "", fmt.Errorf("unknown announce status %q", current)
	}
	switch action {
	case AnnouncePause:
		if current == AnnounceClosed {
			return "", fmt.Errorf("%w: the node is unlisted; resume lists it again", ErrAnnounceTransition)
		}
		return AnnouncePaused, nil
	case AnnounceResume:
		return AnnounceOpen, nil
	case AnnounceUnlist:
		return AnnounceClosed, nil
	default:
		return "", fmt.Errorf("unknown announce action %q (want pause, resume or unlist)", action)
	}
}

// AnnounceState is a node's stored announcement status.
type AnnounceState struct {
	NodeID []byte
	Status AnnounceStatus
	// UpdatedAt is when Status last changed; zero for a node never changed.
	UpdatedAt time.Time
	// ClosedPublishedAt is when status closed was published for the current
	// Unlist; zero until then, and reset by any later status change.
	ClosedPublishedAt time.Time
	// LastPublishedAt is when a relay last accepted an announcement of the
	// node; zero if it never published (and so has nothing to withdraw).
	LastPublishedAt time.Time
	// OffClosedPublishedAt is when status closed was published because
	// announcing was switched off; zero while on, or not yet published.
	OffClosedPublishedAt time.Time
	// SelfCheckBlock is why a failed self-check paused the announcement,
	// or "". It survives restarts; only a Recheck clears it.
	SelfCheckBlock string
	// LastCreatedAt is the created_at (unix seconds) of the newest event
	// signed for the node, 0 if none; the next one is signed after it.
	LastCreatedAt int64
	// LastFields are the announced values of the last event a relay
	// accepted: the signed event as JSON (rows written before it was kept
	// hold niplsp Fields as JSON); nil if none. Closed is built from them when the config no longer can.
	LastFields []byte
}

// AnnounceRelayResult is one relay's answer to a published announcement.
type AnnounceRelayResult struct {
	Relay    string
	Accepted bool
	Error    string
	// RetryAt is when a relay that refused is tried again (exponential
	// backoff); zero once it accepted.
	RetryAt time.Time
}

// AnnouncePublication is the last announcement the publisher sent (T8-3).
type AnnouncePublication struct {
	EventID    string
	CreatedAt  time.Time
	Expiration time.Time
	Status     AnnounceStatus
	Relays     []AnnounceRelayResult
}

// AnnounceSelfCheck is the result of checking a published announcement
// against the live LSPS answers (T8-4).
type AnnounceSelfCheck struct {
	CheckedAt time.Time
	OK        bool
	// Problems lists each difference or failed check, empty when OK.
	Problems []string
}

// AnnouncePublisherStatus is what the dashboard shows of a node's publisher.
type AnnouncePublisherStatus struct {
	Last      *AnnouncePublication
	SelfCheck *AnnounceSelfCheck
	// Blocked is why the announcement is paused (a failed self-check), or "".
	Blocked string
	// LastError is the last error building, signing or publishing, or "".
	LastError string
}
