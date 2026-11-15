package postgres

import (
	"context"
	"errors"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
)

// pgUniqueViolation is Postgres' SQLSTATE code for a unique-constraint
// violation (23505) -- shared by UpdateLsps1OrderFunding's S-5 conflict
// detection.
const pgUniqueViolation = "23505"

// GetLsps1OrderByPaymentInvoice retrieves an LSPS1 order by its payment invoice string
func (s *PostgresOpeningStore) GetLsps1OrderByPaymentInvoice(paymentInvoice string) (*core.Lsps1Order, error) {
	row := s.pool.QueryRow(context.Background(),
		`SELECT order_id, peer_id, lsp_balance_loki, client_balance_loki,
			funding_confirms_within_blocks, required_channel_confirmations,
			channel_expiry_blocks, token, announce_channel, created_at,
			order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
			COALESCE(refund_onchain_address, ''), COALESCE(order_total_loki, 0), COALESCE(fee_total_loki, 0)
		FROM lsps1.orders WHERE payment_invoice=$1`, paymentInvoice)

	var order core.Lsps1Order
	err := row.Scan(
		&order.OrderId, &order.PeerId, &order.LspBalanceLoki, &order.ClientBalanceLoki,
		&order.FundingConfirmsWithinBlocks, &order.RequiredChannelConfirmations,
		&order.ChannelExpiryBlocks, &order.Token, &order.AnnounceChannel, &order.CreatedAt,
		&order.OrderState, &order.PaymentState, &order.PaymentInvoice, &order.PaymentPreimage, &order.FundingTxid, &order.FundingOutpoint,
		&order.RefundOnchainAddress, &order.OrderTotalLoki, &order.FeeTotalLoki,
	)
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// UpdateLsps1OrderState performs a compare-and-set update of order_state
// and payment_state: the WHERE clause requires order_state to still equal
// expectedCurrentState, so two concurrent writers racing to transition the
// same order (e.g. a live settlement event vs. a startup recovery sweep,
// see F-1) can't both succeed. The loser gets updated=false and must not
// proceed with any side effect that assumes it won the transition.
func (s *PostgresOpeningStore) UpdateLsps1OrderState(orderId string, newState string, newPaymentState string, expectedCurrentState string) (bool, error) {
	result, err := s.pool.Exec(context.Background(),
		`UPDATE lsps1.orders
		 SET order_state = $2, payment_state = $3,
		     paid_at = COALESCE(paid_at, CASE WHEN $3 = 'PAID' THEN NOW() END),
		     completed_at = COALESCE(completed_at, CASE WHEN $2 = 'COMPLETED' THEN NOW() END)
		 WHERE order_id = $1 AND order_state = $4`,
		orderId, newState, newPaymentState, expectedCurrentState)

	if err != nil {
		log.Error().Err(err).Msgf("UpdateLsps1OrderState: failed to update order %s", orderId)
		return false, err
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		log.Warn().Msgf("UpdateLsps1OrderState: no rows updated for order %s (expected current state %s did not match, or order not found)", orderId, expectedCurrentState)
		return false, nil
	}

	return true, nil
}

// GetPaidLsps1Order finds the oldest PAID order for a client that hasn't
// been fulfilled yet. payment_state='PAID' excludes HOLD orders (H-1): see
// PostgresInterceptStore.GetPaidLsps1Order's doc comment for why -- this
// copy isn't on a live call path today but shares the same contract, so it
// gets the same filter to avoid becoming a landmine if it is wired up later.
func (s *PostgresOpeningStore) GetPaidLsps1Order(clientNodeId string) (*core.Lsps1Order, error) {
	// We want orders that are PAID but don't have a channel (FundingTxid is null/empty)
	// Ordered by created_at ASC to process oldest first (FIFO)
	row := s.pool.QueryRow(context.Background(),
		`SELECT order_id, peer_id, lsp_balance_loki, client_balance_loki,
			funding_confirms_within_blocks, required_channel_confirmations,
			channel_expiry_blocks, token, announce_channel, created_at,
			order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
			COALESCE(refund_onchain_address, ''), COALESCE(order_total_loki, 0), COALESCE(fee_total_loki, 0)
		 FROM lsps1.orders
		 WHERE peer_id=$1 AND order_state='PAID' AND payment_state='PAID' AND (funding_txid IS NULL OR funding_txid = '')
		 ORDER BY created_at ASC LIMIT 1`, clientNodeId)

	var order core.Lsps1Order
	err := row.Scan(
		&order.OrderId, &order.PeerId, &order.LspBalanceLoki, &order.ClientBalanceLoki,
		&order.FundingConfirmsWithinBlocks, &order.RequiredChannelConfirmations,
		&order.ChannelExpiryBlocks, &order.Token, &order.AnnounceChannel, &order.CreatedAt,
		&order.OrderState, &order.PaymentState, &order.PaymentInvoice, &order.PaymentPreimage, &order.FundingTxid, &order.FundingOutpoint,
		&order.RefundOnchainAddress, &order.OrderTotalLoki, &order.FeeTotalLoki,
	)
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// GetPaidLsps1Orders finds all PAID orders for a client that haven't been
// fulfilled yet, ordered by created_at ASC (FIFO). payment_state='PAID'
// excludes HOLD orders (H-1) -- see GetPaidLsps1Order's doc comment.
func (s *PostgresOpeningStore) GetPaidLsps1Orders(clientNodeId string) ([]*core.Lsps1Order, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT order_id, peer_id, lsp_balance_loki, client_balance_loki,
			funding_confirms_within_blocks, required_channel_confirmations,
			channel_expiry_blocks, token, announce_channel, created_at,
			order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
			COALESCE(refund_onchain_address, ''), COALESCE(order_total_loki, 0), COALESCE(fee_total_loki, 0)
		 FROM lsps1.orders
		 WHERE peer_id=$1 AND order_state='PAID' AND payment_state='PAID' AND (funding_txid IS NULL OR funding_txid = '')
		 ORDER BY created_at ASC`, clientNodeId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []*core.Lsps1Order
	for rows.Next() {
		var order core.Lsps1Order
		err := rows.Scan(
			&order.OrderId, &order.PeerId, &order.LspBalanceLoki, &order.ClientBalanceLoki,
			&order.FundingConfirmsWithinBlocks, &order.RequiredChannelConfirmations,
			&order.ChannelExpiryBlocks, &order.Token, &order.AnnounceChannel, &order.CreatedAt,
			&order.OrderState, &order.PaymentState, &order.PaymentInvoice, &order.PaymentPreimage, &order.FundingTxid, &order.FundingOutpoint,
			&order.RefundOnchainAddress, &order.OrderTotalLoki, &order.FeeTotalLoki,
		)
		if err != nil {
			return nil, err
		}
		orders = append(orders, &order)
	}
	return orders, rows.Err()
}

// GetAllPaidLsps1Orders finds all PAID orders across all peers that haven't
// reached a terminal state yet -- used for startup recovery
// (recoverStuckOrders) to retry orders that were stuck when flspd was down.
// Deliberately does NOT filter on funding_txid: recoverStuckOrders' own
// Go-level branching already distinguishes "PAID, no funding yet" (retry
// the whole open-channel attempt) from "PAID, funding already recorded"
// (reconcile against LND instead of risking a duplicate OpenChannel, F-8)
// from "PAID, payment_state=HOLD" (settle-or-retry, H-3/H-4) -- excluding
// funded rows here would starve that branching of the exact rows it exists
// to handle, making the funded-order recovery paths permanently
// unreachable regardless of how correct their own logic is.
func (s *PostgresOpeningStore) GetAllPaidLsps1Orders() ([]*core.Lsps1Order, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT order_id, peer_id, lsp_balance_loki, client_balance_loki,
			funding_confirms_within_blocks, required_channel_confirmations,
			channel_expiry_blocks, token, announce_channel, created_at,
			order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
			COALESCE(refund_onchain_address, ''), COALESCE(order_total_loki, 0), COALESCE(fee_total_loki, 0)
		 FROM lsps1.orders
		 WHERE order_state='PAID'
		 ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []*core.Lsps1Order
	for rows.Next() {
		var order core.Lsps1Order
		err := rows.Scan(
			&order.OrderId, &order.PeerId, &order.LspBalanceLoki, &order.ClientBalanceLoki,
			&order.FundingConfirmsWithinBlocks, &order.RequiredChannelConfirmations,
			&order.ChannelExpiryBlocks, &order.Token, &order.AnnounceChannel, &order.CreatedAt,
			&order.OrderState, &order.PaymentState, &order.PaymentInvoice, &order.PaymentPreimage, &order.FundingTxid, &order.FundingOutpoint,
			&order.RefundOnchainAddress, &order.OrderTotalLoki, &order.FeeTotalLoki,
		)
		if err != nil {
			return nil, err
		}
		orders = append(orders, &order)
	}
	return orders, rows.Err()
}

// GetAllCreatedLsps1Orders returns all orders still in CREATED state that have not expired.
// Used during startup to check whether any invoice was settled in LND while flspd was down.
func (s *PostgresOpeningStore) GetAllCreatedLsps1Orders() ([]*core.Lsps1Order, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT order_id, peer_id, lsp_balance_loki, client_balance_loki,
			funding_confirms_within_blocks, required_channel_confirmations,
			channel_expiry_blocks, token, announce_channel, created_at,
			order_state, payment_state, payment_invoice, payment_preimage, funding_txid, funding_outpoint,
			COALESCE(refund_onchain_address, ''), COALESCE(order_total_loki, 0), COALESCE(fee_total_loki, 0)
		 FROM lsps1.orders
		 WHERE order_state='CREATED'
		 ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []*core.Lsps1Order
	for rows.Next() {
		var order core.Lsps1Order
		err := rows.Scan(
			&order.OrderId, &order.PeerId, &order.LspBalanceLoki, &order.ClientBalanceLoki,
			&order.FundingConfirmsWithinBlocks, &order.RequiredChannelConfirmations,
			&order.ChannelExpiryBlocks, &order.Token, &order.AnnounceChannel, &order.CreatedAt,
			&order.OrderState, &order.PaymentState, &order.PaymentInvoice, &order.PaymentPreimage, &order.FundingTxid, &order.FundingOutpoint,
			&order.RefundOnchainAddress, &order.OrderTotalLoki, &order.FeeTotalLoki,
		)
		if err != nil {
			return nil, err
		}
		orders = append(orders, &order)
	}
	return orders, rows.Err()
}

// UpdateLsps1OrderFunding updates the funding_txid and funding_outpoint of
// an LSPS1 order, returning whether the claim actually succeeded. The
// partial unique index on (funding_txid, funding_outpoint) added for S-5
// makes this atomic: if a concurrent writer already recorded this exact
// channel point against a different order, this call returns
// (false, nil) -- a normal, expected outcome under concurrency (the same
// TOCTOU window findUnclaimedChannelPoint's read can't close on its own),
// not an error -- rather than silently overwriting or racing with it.
// Callers must treat claimed=false the same as "no unclaimed channel was
// actually available."
//
// FLSPD-0021: the WHERE clause also guards on the order's own funding still
// being null/empty, making this a one-shot first-write. The S-5 unique index
// only stops two *different* orders claiming the same point; it does nothing
// to stop a later caller re-writing *this* order's recorded funding to a
// *different* point and silently losing the real channel record. Guarding
// here means such a second write affects zero rows and returns
// (false, nil) -- the same "back off, the funding is already settled" signal
// every caller already handles -- instead of clobbering it.
func (s *PostgresOpeningStore) UpdateLsps1OrderFunding(orderId string, fundingTxid string, fundingOutpoint int) (bool, error) {
	result, err := s.pool.Exec(context.Background(),
		`UPDATE lsps1.orders
		 SET funding_txid = $2, funding_outpoint = $3, funded_at = COALESCE(funded_at, NOW())
		 WHERE order_id = $1 AND (funding_txid IS NULL OR funding_txid = '')`,
		orderId, fundingTxid, fundingOutpoint)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			log.Warn().Err(err).Msgf("UpdateLsps1OrderFunding: channel point %s:%d already claimed by another order, not %s", fundingTxid, fundingOutpoint, orderId)
			return false, nil
		}
		log.Error().Err(err).Msgf("UpdateLsps1OrderFunding: failed to update order %s", orderId)
		return false, err
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		log.Warn().Msgf("UpdateLsps1OrderFunding: no rows updated for order %s", orderId)
		return false, nil
	}

	return true, nil
}
