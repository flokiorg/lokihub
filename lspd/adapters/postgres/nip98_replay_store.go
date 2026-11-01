package postgres

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
)

// replayRetentionSeconds is how long a seen NIP-98 event id is remembered. It
// must exceed the auth freshness window (lspd auth: ±60s) so an event cannot
// expire from the store while its timestamp would still be accepted; it
// mirrors auth's own in-memory replayCacheTTLSeconds (3 × 60 = 180s).
const replayRetentionSeconds = 180

// replayPruneIntervalSeconds bounds how often CheckAndRecord deletes expired
// rows, so pruning doesn't run on every auth call.
const replayPruneIntervalSeconds = 60

// Nip98ReplayStore is a durable, shared NIP-98 replay cache backed by
// Postgres. It satisfies auth.ReplayStore so the operator dashboard rejects a
// captured in-window auth event even across a process restart or a second
// replica -- which the in-process cache could not (FLSPD-0016 /
// R-LSP-NIP98REPLAY). It mirrors the NIP-CASH served-nonce store: fail-closed
// (a backend error rejects), pruned by expiry.
type Nip98ReplayStore struct {
	pool      *pgxpool.Pool
	lastPrune atomic.Int64
}

func NewNip98ReplayStore(pool *pgxpool.Pool) *Nip98ReplayStore {
	return &Nip98ReplayStore{pool: pool}
}

// CheckAndRecord records eventID keyed by its canonical id and returns an
// error if it was already recorded (a replay) or could not be recorded (a DB
// failure -- fail-closed). The primary key resolves a race between two
// concurrent deliveries of the same event: exactly one INSERT affects a row,
// any other sees the conflict and is rejected.
func (s *Nip98ReplayStore) CheckAndRecord(ctx context.Context, eventID string, nowUnix int64) error {
	s.pruneExpired(ctx, nowUnix)

	tag, err := s.pool.Exec(
		ctx,
		`INSERT INTO public.nip98_seen_events (event_id, expires_at)
		 VALUES ($1, $2)
		 ON CONFLICT (event_id) DO NOTHING`,
		eventID,
		nowUnix+replayRetentionSeconds,
	)
	if err != nil {
		return fmt.Errorf("nip98 replay store unavailable: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("event already used (replay)")
	}
	return nil
}

// pruneExpired deletes rows past their expiry, at most once per
// replayPruneIntervalSeconds. Best-effort cleanup: correctness of replay
// rejection rests on the row existing, not on timely pruning.
func (s *Nip98ReplayStore) pruneExpired(ctx context.Context, nowUnix int64) {
	last := s.lastPrune.Load()
	if nowUnix-last < replayPruneIntervalSeconds {
		return
	}
	if !s.lastPrune.CompareAndSwap(last, nowUnix) {
		return
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM public.nip98_seen_events WHERE expires_at < $1`, nowUnix)
}
