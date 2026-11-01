package ports

import (
	"context"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
)

// AnnounceStateStore keeps each node's NIP-LSP announcement status (open,
// paused, closed), set from the dashboard (T8-2) and read by the publisher
// (T8-3).
type AnnounceStateStore interface {
	// GetAnnounceState returns the node's state; a node never changed is
	// open with a zero UpdatedAt.
	GetAnnounceState(ctx context.Context, nodeID []byte) (*core.AnnounceState, error)
	// ApplyAnnounceAction applies action atomically (core.NextAnnounceStatus
	// under a row lock) and returns the new state.
	ApplyAnnounceAction(ctx context.Context, nodeID []byte, action core.AnnounceAction) (*core.AnnounceState, error)
	// MarkClosedPublished records that status closed was published, only
	// while the node is still closed; it reports whether it recorded.
	MarkClosedPublished(ctx context.Context, nodeID []byte, at time.Time) (bool, error)
	// MarkPublished records that a relay accepted an announcement, and the
	// values it announced (fields, opaque to the store).
	MarkPublished(ctx context.Context, nodeID []byte, at time.Time, fields []byte) error
	// SetLastCreatedAt records the created_at of the newest event signed
	// for the node, before it is sent.
	SetLastCreatedAt(ctx context.Context, nodeID []byte, createdAt int64) error
	// MarkOffClosedPublished records that closed was published because
	// announcing was switched off; ClearOffClosed forgets it when it is
	// switched on again.
	MarkOffClosedPublished(ctx context.Context, nodeID []byte, at time.Time) error
	ClearOffClosed(ctx context.Context, nodeID []byte) error
	// SetSelfCheckBlock stores why publishing is stopped; "" lifts it.
	SetSelfCheckBlock(ctx context.Context, nodeID []byte, reason string) error
}
