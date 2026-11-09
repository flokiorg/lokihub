package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// lsps2BuyRegistrationColumns is the canonical, shared column list for
// lsps2.buy_registrations -- reused by every INSERT/SELECT that reads or
// writes a full registration row (RegisterLsps2Buy, ReserveLsps2Buy,
// GetLsps2BuyRegistration here, plus GetLsps2Order/
// GetStuckLsps2Registrations in intercept_store.go), so a future schema
// change (add/rename/reorder a column) has one place to update instead of
// five that can silently drift out of sync with each other.
const lsps2BuyRegistrationColumns = `id, lsp_id, peer_id, scid, mode, payment_size_mloki,
	params_min_fee_mloki, params_proportional, params_valid_until,
	params_min_lifetime, params_max_client_to_self_delay, params_promise, token,
	created_at, expires_at, reserved_amount_loki, state, max_payment_size_mloki`

// scanLsps2BuyRegistration scans one row shaped like
// lsps2BuyRegistrationColumns (in that exact column order) into a
// *core.Lsps2BuyRegistration. Accepts pgx.Row (QueryRow) or pgx.Rows
// (Query, mid-iteration) -- both satisfy Scan(dest ...any) error.
func scanLsps2BuyRegistration(row pgx.Row) (*core.Lsps2BuyRegistration, error) {
	var reg core.Lsps2BuyRegistration
	var scidInt int64
	var state string
	err := row.Scan(
		&reg.Id, &reg.LspId, &reg.PeerId, &scidInt, &reg.Mode, &reg.PaymentSizeMloki,
		&reg.ParamsMinFeeMloki, &reg.ParamsProportional, &reg.ParamsValidUntil,
		&reg.ParamsMinLifetime, &reg.ParamsMaxClientToSelfDelay, &reg.ParamsPromise, &reg.Token,
		&reg.CreatedAt, &reg.ExpiresAt, &reg.ReservedAmountLoki, &state, &reg.MaxPaymentSizeMloki,
	)
	if err != nil {
		return nil, err
	}
	reg.Scid = uint64(scidInt)
	reg.State = core.Lsps2RegistrationState(state)
	return &reg, nil
}

// insertLsps2BuyRegistration runs the shared lsps2.buy_registrations
// INSERT against the given querier (either the pool directly, for
// RegisterLsps2Buy's own transaction-free write, or an in-flight pgx.Tx,
// for ReserveLsps2Buy's advisory-locked one) -- the one INSERT statement
// both call, instead of two copies that could drift.
func insertLsps2BuyRegistration(ctx context.Context, q interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, reg *core.Lsps2BuyRegistration) error {
	state := reg.State
	if state == "" {
		state = core.Lsps2RegistrationActive
	}
	_, err := q.Exec(ctx,
		`INSERT INTO lsps2.buy_registrations (`+lsps2BuyRegistrationColumns+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		reg.Id, reg.LspId, reg.PeerId, int64(reg.Scid), reg.Mode, reg.PaymentSizeMloki,
		reg.ParamsMinFeeMloki, reg.ParamsProportional, reg.ParamsValidUntil,
		reg.ParamsMinLifetime, reg.ParamsMaxClientToSelfDelay, reg.ParamsPromise, reg.Token,
		reg.CreatedAt, reg.ExpiresAt, reg.ReservedAmountLoki, string(state), reg.MaxPaymentSizeMloki)
	return err
}

type extendedParams struct {
	Token   string                `json:"token"`
	OrderId string                `json:"order_id,omitempty"`
	Params  core.OpeningFeeParams `json:"fees_params"`
}

type PostgresOpeningStore struct {
	pool *pgxpool.Pool
}

func NewPostgresOpeningStore(pool *pgxpool.Pool) *PostgresOpeningStore {
	return &PostgresOpeningStore{pool: pool}
}

func (s *PostgresOpeningStore) GetFeeParamsSettings(token string) ([]*core.OpeningFeeParamsSetting, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT validity, params FROM new_channel_params WHERE (token=$1 OR token IS NULL) AND validity>0`, token)
	if err != nil {
		log.Error().Msgf("GetFeeParamsSettings(%v) error: %v", token, err)
		return nil, err
	}
	defer rows.Close()

	var settings []*core.OpeningFeeParamsSetting
	for rows.Next() {
		var validity int64
		var param string
		err = rows.Scan(&validity, &param)
		if err != nil {
			return nil, err
		}

		var params *core.OpeningFeeParams
		err := json.Unmarshal([]byte(param), &params)
		if err != nil {
			log.Error().Msgf("Failed to unmarshal fee param '%v': %v", param, err)
			return nil, err
		}

		duration := time.Second * time.Duration(validity)
		settings = append(settings, &core.OpeningFeeParamsSetting{
			Validity: duration,
			Params:   params,
		})
	}

	return settings, nil
}

func (s *PostgresOpeningStore) SetFeeParams(token string, validity time.Duration, params core.OpeningFeeParams) error {
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return err
	}
	validitySec := int64(validity.Seconds())
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `DELETE FROM new_channel_params WHERE token=$1 AND validity=$2`, token, validitySec)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `INSERT INTO new_channel_params (token, validity, params) VALUES ($1, $2, $3)`, token, validitySec, paramsBytes)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *PostgresOpeningStore) RegisterLsps2Buy(reg *core.Lsps2BuyRegistration) error {
	// DEBUG LOGGING
	log.Trace().Msgf("RegisterLsps2Buy inserting int64 SCID: %d (uint64: %d)", int64(reg.Scid), reg.Scid)
	if err := insertLsps2BuyRegistration(context.Background(), s.pool, reg); err != nil {
		log.Error().Msgf("RegisterLsps2Buy error: %v", err)
		return err
	}
	return nil
}

func (s *PostgresOpeningStore) GetLsps2BuyRegistration(scid uint64) (*core.Lsps2BuyRegistration, error) {
	row := s.pool.QueryRow(context.Background(),
		`SELECT `+lsps2BuyRegistrationColumns+`
		FROM lsps2.buy_registrations WHERE scid=$1`, int64(scid))
	return scanLsps2BuyRegistration(row)
}

func (s *PostgresOpeningStore) RegisterLsps1Order(order *core.Lsps1Order) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO lsps1.orders (
			order_id, peer_id, lsp_balance_loki, client_balance_loki,
			funding_confirms_within_blocks, required_channel_confirmations,
			channel_expiry_blocks, token, announce_channel, created_at,
			order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
			refund_onchain_address, order_total_loki, fee_total_loki,
			expires_at, reserved_amount_loki
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`,
		order.OrderId, order.PeerId, order.LspBalanceLoki, order.ClientBalanceLoki,
		order.FundingConfirmsWithinBlocks, order.RequiredChannelConfirmations,
		order.ChannelExpiryBlocks, order.Token, order.AnnounceChannel, order.CreatedAt,
		order.OrderState, order.PaymentState, order.PaymentInvoice, order.PaymentPreimage, order.FundingTxid, order.FundingOutpoint,
		order.RefundOnchainAddress, order.OrderTotalLoki, order.FeeTotalLoki,
		order.ExpiresAt, order.ReservedAmountLoki)
	if err != nil {
		log.Error().Msgf("RegisterLsps1Order error: %v", err)
		return err
	}
	return nil
}

func (s *PostgresOpeningStore) GetLsps1Order(orderId string) (*core.Lsps1Order, error) {
	row := s.pool.QueryRow(context.Background(),
		`SELECT order_id, peer_id, lsp_balance_loki, client_balance_loki,
			funding_confirms_within_blocks, required_channel_confirmations,
			channel_expiry_blocks, token, announce_channel, created_at,
			order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
			COALESCE(refund_onchain_address, ''), COALESCE(order_total_loki, 0), COALESCE(fee_total_loki, 0),
			paid_at, funded_at, completed_at
		FROM lsps1.orders WHERE order_id=$1`, orderId)

	var order core.Lsps1Order
	err := row.Scan(
		&order.OrderId, &order.PeerId, &order.LspBalanceLoki, &order.ClientBalanceLoki,
		&order.FundingConfirmsWithinBlocks, &order.RequiredChannelConfirmations,
		&order.ChannelExpiryBlocks, &order.Token, &order.AnnounceChannel, &order.CreatedAt,
		&order.OrderState, &order.PaymentState, &order.PaymentInvoice, &order.PaymentPreimage, &order.FundingTxid, &order.FundingOutpoint,
		&order.RefundOnchainAddress, &order.OrderTotalLoki, &order.FeeTotalLoki,
		&order.PaidAt, &order.FundedAt, &order.CompletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, core.ErrOrderNotFound
		}
		return nil, err
	}
	return &order, nil
}

func (s *PostgresOpeningStore) ListLsps1Orders(limit, offset int) ([]*core.Lsps1Order, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT order_id, peer_id, lsp_balance_loki, client_balance_loki,
			funding_confirms_within_blocks, required_channel_confirmations,
			channel_expiry_blocks, token, announce_channel, created_at,
			order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
			COALESCE(refund_onchain_address, ''), COALESCE(order_total_loki, 0), COALESCE(fee_total_loki, 0),
			paid_at, funded_at, completed_at
		FROM lsps1.orders
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []*core.Lsps1Order
	for rows.Next() {
		var o core.Lsps1Order
		err := rows.Scan(
			&o.OrderId, &o.PeerId, &o.LspBalanceLoki, &o.ClientBalanceLoki,
			&o.FundingConfirmsWithinBlocks, &o.RequiredChannelConfirmations,
			&o.ChannelExpiryBlocks, &o.Token, &o.AnnounceChannel, &o.CreatedAt,
			&o.OrderState, &o.PaymentState, &o.PaymentInvoice, &o.PaymentPreimage, &o.FundingTxid, &o.FundingOutpoint,
			&o.RefundOnchainAddress, &o.OrderTotalLoki, &o.FeeTotalLoki,
			&o.PaidAt, &o.FundedAt, &o.CompletedAt,
		)
		if err != nil {
			return nil, err
		}
		orders = append(orders, &o)
	}
	return orders, nil
}

func (s *PostgresOpeningStore) RegisterLsps5Webhook(webhook *core.Lsps5Webhook) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO lsps5.webhooks (client_node_id, app_name, url, transport, created_at) 
		VALUES ($1, $2, $3, $4, $5) 
		ON CONFLICT (client_node_id, app_name) DO UPDATE SET url=$3, transport=$4, created_at=$5`,
		webhook.ClientNodeId, webhook.AppName, webhook.Url, webhook.Transport, webhook.CreatedAt)
	if err != nil {
		log.Error().Msgf("RegisterLsps5Webhook error: %v", err)
		return err
	}
	return nil
}

func (s *PostgresOpeningStore) GetLsps5Webhooks(clientNodeId string) ([]*core.Lsps5Webhook, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT client_node_id, app_name, url, transport, created_at FROM lsps5.webhooks WHERE client_node_id=$1`,
		clientNodeId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var webhooks []*core.Lsps5Webhook
	for rows.Next() {
		var w core.Lsps5Webhook
		err := rows.Scan(&w.ClientNodeId, &w.AppName, &w.Url, &w.Transport, &w.CreatedAt)
		if err != nil {
			return nil, err
		}
		webhooks = append(webhooks, &w)
	}
	return webhooks, nil
}

func (s *PostgresOpeningStore) RemoveLsps5Webhook(clientNodeId, appName string) error {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM lsps5.webhooks WHERE client_node_id=$1 AND app_name=$2`,
		clientNodeId, appName)
	return err
}

func (s *PostgresOpeningStore) CountLsps1Orders(clientNodeId string, state string, excludeExpiredBefore *time.Time) (int, error) {
	var count int
	var err error

	if excludeExpiredBefore != nil {
		err = s.pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM lsps1.orders WHERE peer_id=$1 AND order_state=$2 AND created_at >= $3`,
			clientNodeId, state, *excludeExpiredBefore).Scan(&count)
	} else {
		err = s.pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM lsps1.orders WHERE peer_id=$1 AND order_state=$2`,
			clientNodeId, state).Scan(&count)
	}

	return count, err
}

// CountLsps2PendingRegistrations mirrors CountLsps1Orders for LSPS2
// (Phase 7, F-17): served by buy_reg_peer_active_idx (peer_id, expires_at)
// WHERE state IN ('ACTIVE','OPENING') -- the same partial index T7-7's
// design called for, added in the Phase 7 migration.
func (s *PostgresOpeningStore) CountLsps2PendingRegistrations(peerId string) (int, error) {
	var count int
	err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM lsps2.buy_registrations
		 WHERE peer_id=$1 AND state IN ('ACTIVE', 'OPENING') AND expires_at > now()`,
		peerId).Scan(&count)
	return count, err
}

func (s *PostgresOpeningStore) CountLsps5Webhooks(clientNodeId string) (int, error) {
	var count int
	err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM lsps5.webhooks WHERE client_node_id=$1`,
		clientNodeId).Scan(&count)
	return count, err
}

// GetReservedLiquidity sums live reservations across BOTH protocols
// (Phase 7, F-16): before this, only lsps1.orders counted, so every LSPS2
// buy registration was checked against a balance nothing about LSPS2 ever
// reduced. Reads each row's own persisted expires_at (F-19) rather than a
// hardcoded interval, and includes LSPS2 registrations in OPENING as well
// as ACTIVE -- a registration mid-channel-open still commits real,
// about-to-be-spent capital. Both branches are covered by a partial index
// on exactly this predicate (orders_active_expiry_idx /
// buy_reg_active_expiry_idx), so this stays a cheap indexed sum, not a
// table scan, regardless of how large either table's full history grows.
func (s *PostgresOpeningStore) GetReservedLiquidity() (uint64, error) {
	var reserved uint64
	err := s.pool.QueryRow(context.Background(), reservedLiquiditySQL).Scan(&reserved)
	if err != nil {
		return 0, err
	}
	return reserved, nil
}

// ExpireStaleOrders marks CREATED orders past their own persisted
// expires_at as EXPIRED (Phase 7, F-19: previously hardcoded
// `INTERVAL '24 hours'`, disagreeing with the API-level isOrderExpired
// check the moment Lsps1OrderExpirySeconds is changed from its default).
func (s *PostgresOpeningStore) ExpireStaleOrders() (int64, error) {
	result, err := s.pool.Exec(context.Background(),
		`UPDATE lsps1.orders
		 SET order_state = 'EXPIRED'
		 WHERE order_state = 'CREATED'
		 AND expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// ExpireStaleLsps2Registrations marks ACTIVE registrations past their own
// expires_at as EXPIRED (Phase 7, T7-6/F-21). Deliberately does not match
// OPENING -- see the ports.OpeningStore doc comment for why.
func (s *PostgresOpeningStore) ExpireStaleLsps2Registrations() (int64, error) {
	result, err := s.pool.Exec(context.Background(),
		`UPDATE lsps2.buy_registrations
		 SET state = 'EXPIRED'
		 WHERE state = 'ACTIVE'
		 AND expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// reservedLiquiditySQL is the same union-across-both-protocols query
// GetReservedLiquidity uses, shared here so the transaction-scoped
// re-check inside ReserveLsps1Order/ReserveLsps2Buy can never silently
// drift from the read-only version.
//
// LSPS1's CREATED branch keeps its own expires_at filter -- CREATED means
// "still waiting on the client's payment," and once that window closes
// with no payment, the capital was never actually committed. PAID orders
// are different: real capital IS captured/committed the instant an order
// reaches PAID (payment_state PAID or a settled/ACCEPTED hold invoice --
// see Lsps1OrderStatePaid), and the channel-open that follows can be
// deferred indefinitely if the peer isn't connected (settlement_service's
// openChannelForOrder explicitly leaves it at PAID with no timeout in
// that case: "will be handled by JIT or manual"). expires_at was only
// ever about the payment window, not how long committed capital stays
// reserved, so it must NOT gate the PAID branch -- otherwise a PAID
// order's reserved_amount_loki silently drops out of this sum the moment
// its original payment deadline passes, even though the channel it's
// paying for hasn't opened yet.
const reservedLiquiditySQL = `
	SELECT COALESCE(SUM(reserved_amount_loki), 0) FROM (
		SELECT reserved_amount_loki FROM lsps1.orders
			WHERE (order_state = 'CREATED' AND expires_at > now())
				OR order_state = 'PAID'
		UNION ALL
		SELECT reserved_amount_loki FROM lsps2.buy_registrations
			WHERE state IN ('ACTIVE', 'OPENING') AND expires_at > now()
	) live_reservations`

// liquidityReservationLockKey is a single well-known constant, not one
// derived per node (Phase 7, F-20/T7-5): this codebase's liquidity
// accounting (reservedLiquiditySQL above) has never been scoped per
// node/lsp_id -- it sums across the whole database -- so a lock scoped
// the same way correctly matches what it's actually guarding. If this
// deployment model ever changes to multiple LSP nodes sharing one
// Postgres database with INDEPENDENTLY tracked liquidity, lsp_id needs to
// be added to lsps1.orders (it already exists on lsps2.buy_registrations)
// and threaded through both this lock key and reservedLiquiditySQL's
// WHERE clauses -- changing only one of the two would silently reopen
// the exact race this lock exists to close.
//
// Verified (second-pass review question) that cmd/flspd/run.go's
// per-node loop -- which does construct one LiquidityManager/Handlers per
// configured node -- doesn't actually contradict this: those per-node
// instances all share the SAME nodesService built from the full node
// list, and this codebase's own business-config logic already treats the
// fleet as sharing one config, not each node independently -- e.g.
// nodesService.GetMinChannelCapacity() and
// lsps.Handlers.getLsps1ConfigValues() both explicitly read nodes[0] for
// every request regardless of which node's own client/interceptor
// happens to be handling it. The per-node loop exists because each
// node needs its own LND gRPC connection and HTLC-interceptor
// subscription, not because business config or liquidity is meant to be
// independently scoped per node. A single, fleet-wide liquidity lock key
// is therefore consistent with, not a regression from, this codebase's
// pre-existing single-config-fleet assumption.
const liquidityReservationLockKey = "flspd:liquidity_reservation"

// reserveIfAvailable is the shared skeleton behind ReserveLsps1Order and
// ReserveLsps2Buy (Phase 7, F-20): begin a transaction, take the
// cross-protocol advisory lock, re-check reservedLiquiditySQL against
// availableWalletBalanceLoki, and -- only if requestedAmountLoki fits --
// run insert (the entity-specific INSERT) and commit. Returns
// reserved=false (nil error) without calling insert at all if there's
// insufficient liquidity. The two callers previously duplicated this
// entire begin/lock/check/insert/commit shape; extracted once so a future
// change to the locking or re-check logic can't be applied to only one of
// them.
func (s *PostgresOpeningStore) reserveIfAvailable(ctx context.Context, availableWalletBalanceLoki, requestedAmountLoki uint64, insert func(ctx context.Context, tx pgx.Tx) error) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, liquidityReservationLockKey); err != nil {
		return false, err
	}

	var reserved uint64
	if err := tx.QueryRow(ctx, reservedLiquiditySQL).Scan(&reserved); err != nil {
		return false, err
	}
	var available uint64
	if reserved < availableWalletBalanceLoki {
		available = availableWalletBalanceLoki - reserved
	}
	if requestedAmountLoki > available {
		return false, nil
	}

	if err := insert(ctx, tx); err != nil {
		return false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// ReserveLsps1Order re-checks reserved liquidity against
// availableWalletBalanceLoki and inserts the order in ONE transaction
// under liquidityReservationLockKey (Phase 7, F-20): closes the TOCTOU
// race where two concurrent create_order/buy calls could both pass a
// liquidity check against the same stale "available" figure before either
// row existed. The (slow, network) wallet-balance read happens in the
// caller, before this is called -- only the cheap read-sum-compare-insert
// runs under the lock.
func (s *PostgresOpeningStore) ReserveLsps1Order(order *core.Lsps1Order, availableWalletBalanceLoki uint64) (bool, error) {
	return s.reserveIfAvailable(context.Background(), availableWalletBalanceLoki, order.ReservedAmountLoki, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO lsps1.orders (
				order_id, peer_id, lsp_balance_loki, client_balance_loki,
				funding_confirms_within_blocks, required_channel_confirmations,
				channel_expiry_blocks, token, announce_channel, created_at,
				order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
				refund_onchain_address, order_total_loki, fee_total_loki,
				expires_at, reserved_amount_loki
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`,
			order.OrderId, order.PeerId, order.LspBalanceLoki, order.ClientBalanceLoki,
			order.FundingConfirmsWithinBlocks, order.RequiredChannelConfirmations,
			order.ChannelExpiryBlocks, order.Token, order.AnnounceChannel, order.CreatedAt,
			order.OrderState, order.PaymentState, order.PaymentInvoice, order.PaymentPreimage, order.FundingTxid, order.FundingOutpoint,
			order.RefundOnchainAddress, order.OrderTotalLoki, order.FeeTotalLoki,
			order.ExpiresAt, order.ReservedAmountLoki)
		return err
	})
}

// ReserveLsps2Buy is ReserveLsps1Order's LSPS2 counterpart -- same
// advisory-lock-guarded check-and-insert, same rationale.
func (s *PostgresOpeningStore) ReserveLsps2Buy(reg *core.Lsps2BuyRegistration, availableWalletBalanceLoki uint64) (bool, error) {
	return s.reserveIfAvailable(context.Background(), availableWalletBalanceLoki, reg.ReservedAmountLoki, func(ctx context.Context, tx pgx.Tx) error {
		return insertLsps2BuyRegistration(ctx, tx, reg)
	})
}
