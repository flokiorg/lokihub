package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AnnounceStateStore keeps the NIP-LSP announcement status per node in
// public.announce_state (T8-2).
type AnnounceStateStore struct {
	pool *pgxpool.Pool
}

var _ ports.AnnounceStateStore = (*AnnounceStateStore)(nil)

func NewAnnounceStateStore(pool *pgxpool.Pool) *AnnounceStateStore {
	return &AnnounceStateStore{pool: pool}
}

func (s *AnnounceStateStore) GetAnnounceState(ctx context.Context, nodeID []byte) (*core.AnnounceState, error) {
	if len(nodeID) == 0 {
		return nil, fmt.Errorf("node id required")
	}
	var status, block, fields string
	var updatedAt, createdAt int64
	var closedAt, publishedAt, offClosedAt *int64
	err := s.pool.QueryRow(ctx,
		`SELECT status, updated_at, closed_published_at, last_published_at, off_closed_published_at, self_check_block,
		        last_created_at, last_fields
		 FROM public.announce_state WHERE node_id = $1`,
		nodeID,
	).Scan(&status, &updatedAt, &closedAt, &publishedAt, &offClosedAt, &block, &createdAt, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return &core.AnnounceState{NodeID: nodeID, Status: core.AnnounceOpen}, nil
	}
	if err != nil {
		return nil, err
	}
	st := stateFromRow(nodeID, core.AnnounceStatus(status), updatedAt)
	st.ClosedPublishedAt = microTime(closedAt)
	st.LastPublishedAt = microTime(publishedAt)
	st.OffClosedPublishedAt = microTime(offClosedAt)
	st.SelfCheckBlock = block
	st.LastCreatedAt = createdAt
	if fields != "" {
		st.LastFields = []byte(fields)
	}
	return st, nil
}

func microTime(v *int64) time.Time {
	if v == nil {
		return time.Time{}
	}
	return time.UnixMicro(*v)
}

// upsertColumn sets one publisher column, creating the node's row as open
// first when it has none.
func (s *AnnounceStateStore) upsertColumn(ctx context.Context, nodeID []byte, column string, value any) error {
	if len(nodeID) == 0 {
		return fmt.Errorf("node id required")
	}
	// column is one of the fixed names below, never caller input.
	_, err := s.pool.Exec(ctx,
		`INSERT INTO public.announce_state (node_id, status, updated_at, `+column+`)
		 VALUES ($1, 'open', 0, $2)
		 ON CONFLICT (node_id) DO UPDATE SET `+column+` = EXCLUDED.`+column,
		nodeID, value,
	)
	return err
}

// MarkPublished sets last_published_at and last_fields together.
func (s *AnnounceStateStore) MarkPublished(ctx context.Context, nodeID []byte, at time.Time, fields []byte) error {
	if len(nodeID) == 0 {
		return fmt.Errorf("node id required")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO public.announce_state (node_id, status, updated_at, last_published_at, last_fields)
		 VALUES ($1, 'open', 0, $2, $3)
		 ON CONFLICT (node_id) DO UPDATE SET last_published_at = EXCLUDED.last_published_at, last_fields = EXCLUDED.last_fields`,
		nodeID, at.UnixMicro(), string(fields),
	)
	return err
}

func (s *AnnounceStateStore) SetLastCreatedAt(ctx context.Context, nodeID []byte, createdAt int64) error {
	return s.upsertColumn(ctx, nodeID, "last_created_at", createdAt)
}

func (s *AnnounceStateStore) MarkOffClosedPublished(ctx context.Context, nodeID []byte, at time.Time) error {
	return s.upsertColumn(ctx, nodeID, "off_closed_published_at", at.UnixMicro())
}

func (s *AnnounceStateStore) ClearOffClosed(ctx context.Context, nodeID []byte) error {
	return s.upsertColumn(ctx, nodeID, "off_closed_published_at", nil)
}

func (s *AnnounceStateStore) SetSelfCheckBlock(ctx context.Context, nodeID []byte, reason string) error {
	return s.upsertColumn(ctx, nodeID, "self_check_block", reason)
}

// ApplyAnnounceAction locks the node's row (creating it as open first, so
// there is a row to lock), applies core.NextAnnounceStatus and writes the
// result, so two concurrent actions are applied one after the other. An
// action that leaves the status unchanged keeps the row's updated_at (and
// closed_published_at); a change clears closed_published_at.
func (s *AnnounceStateStore) ApplyAnnounceAction(ctx context.Context, nodeID []byte, action core.AnnounceAction) (*core.AnnounceState, error) {
	if len(nodeID) == 0 {
		return nil, fmt.Errorf("node id required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// A placeholder row with updated_at 0 reads back as "never changed".
	if _, err := tx.Exec(ctx,
		`INSERT INTO public.announce_state (node_id, status, updated_at)
		 VALUES ($1, 'open', 0) ON CONFLICT (node_id) DO NOTHING`,
		nodeID,
	); err != nil {
		return nil, err
	}

	var current string
	var updatedAt int64
	var closedAt *int64
	if err := tx.QueryRow(ctx,
		`SELECT status, updated_at, closed_published_at FROM public.announce_state WHERE node_id = $1 FOR UPDATE`,
		nodeID,
	).Scan(&current, &updatedAt, &closedAt); err != nil {
		return nil, err
	}

	next, err := core.NextAnnounceStatus(core.AnnounceStatus(current), action)
	if err != nil {
		return nil, err
	}
	if next != core.AnnounceStatus(current) {
		updatedAt = time.Now().UnixMicro()
		closedAt = nil
		if _, err := tx.Exec(ctx,
			`UPDATE public.announce_state SET status = $2, updated_at = $3, closed_published_at = NULL WHERE node_id = $1`,
			nodeID, string(next), updatedAt,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	st := stateFromRow(nodeID, next, updatedAt)
	if closedAt != nil {
		st.ClosedPublishedAt = time.UnixMicro(*closedAt)
	}
	return st, nil
}

// MarkClosedPublished sets closed_published_at while the node is closed and
// not yet marked. A Resume racing the publish leaves the row unmarked.
func (s *AnnounceStateStore) MarkClosedPublished(ctx context.Context, nodeID []byte, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE public.announce_state SET closed_published_at = $2
		 WHERE node_id = $1 AND status = 'closed' AND closed_published_at IS NULL`,
		nodeID, at.UnixMicro(),
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func stateFromRow(nodeID []byte, status core.AnnounceStatus, updatedAt int64) *core.AnnounceState {
	st := &core.AnnounceState{NodeID: nodeID, Status: status}
	if updatedAt != 0 {
		st.UpdatedAt = time.UnixMicro(updatedAt)
	}
	return st
}
