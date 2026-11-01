package postgres

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/notifications"
	"github.com/jackc/pgx/v5/pgxpool"
)

type NotificationsStore struct {
	pool *pgxpool.Pool
}

func NewNotificationsStore(pool *pgxpool.Pool) *NotificationsStore {
	return &NotificationsStore{pool: pool}
}

func (s *NotificationsStore) Register(
	ctx context.Context,
	pubkey string,
	url string,
	transport string,
) error {
	pk, err := hex.DecodeString(pubkey)
	if err != nil {
		return err
	}

	now := time.Now().UnixMicro()
	_, err = s.pool.Exec(
		ctx,
		`INSERT INTO public.notification_subscriptions (pubkey, url, transport, created_at, refreshed_at)
		 values ($1, $2, $3, $4, $5)
		 ON CONFLICT (pubkey, url) DO UPDATE SET transport = $3, refreshed_at = $5`,
		pk,
		url,
		transport,
		now,
		now,
	)

	return err
}

func (s *NotificationsStore) GetRegistrations(
	ctx context.Context,
	pubkey string,
) ([]core.NotificationRegistration, error) {
	pk, err := hex.DecodeString(pubkey)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(
		ctx,
		`SELECT url, transport 
		 FROM public.notification_subscriptions
		 WHERE pubkey = $1`,
		pk,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []core.NotificationRegistration
	for rows.Next() {
		var reg core.NotificationRegistration
		err = rows.Scan(&reg.Url, &reg.Transport)
		if err != nil {
			return nil, err
		}

		result = append(result, reg)
	}

	return result, nil
}

func (s *NotificationsStore) Unsubscribe(
	ctx context.Context,
	pubkey string,
	url string,
) error {
	pk, err := hex.DecodeString(pubkey)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(
		ctx,
		`DELETE FROM public.notification_subscriptions
		 WHERE pubkey = $1 AND url = $2`,
		pk,
		url,
	)

	return err
}

func (s *NotificationsStore) RemoveExpired(
	ctx context.Context,
	before time.Time,
) error {
	_, err := s.pool.Exec(
		ctx,
		`DELETE FROM public.notification_subscriptions
		 WHERE refreshed_at < $1`,
		before.UnixMicro())

	return err
}

func (s *NotificationsStore) Enqueue(
	ctx context.Context,
	pubkey string,
	url string,
	transport string,
	method string,
	params interface{},
) (int64, error) {
	pk, err := hex.DecodeString(pubkey)
	if err != nil {
		return 0, err
	}

	var id int64
	err = s.pool.QueryRow(
		ctx,
		`INSERT INTO public.notification_queue (pubkey, target_url, transport, method, params, status)
		 VALUES ($1, $2, $3, $4, $5, 'pending')
		 RETURNING id`,
		pk, url, transport, method, params,
	).Scan(&id)

	return id, err
}

func (s *NotificationsStore) GetPending(
	ctx context.Context,
	limit int,
) ([]notifications.NotificationQueueItem, error) {
	rows, err := s.pool.Query(
		ctx,
		`SELECT id, pubkey, target_url, transport, method, params, attempts
		 FROM public.notification_queue
		 WHERE status IN ('pending', 'failed') AND next_retry_at <= NOW()
		 ORDER BY next_retry_at ASC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []notifications.NotificationQueueItem
	for rows.Next() {
		var item notifications.NotificationQueueItem
		var pk []byte
		err := rows.Scan(
			&item.ID,
			&pk,
			&item.TargetURL,
			&item.Transport,
			&item.Method,
			&item.Params,
			&item.Attempts,
		)
		if err != nil {
			return nil, err
		}
		item.Pubkey = hex.EncodeToString(pk)
		result = append(result, item)
	}

	return result, nil
}

func (s *NotificationsStore) UpdateStatus(
	ctx context.Context,
	id int64,
	status string,
	nextRetry time.Time,
) error {
	_, err := s.pool.Exec(
		ctx,
		`UPDATE public.notification_queue
		 SET status = $2, next_retry_at = $3, attempts = attempts + 1, updated_at = NOW()
		 WHERE id = $1`,
		id, status, nextRetry,
	)
	return err
}
