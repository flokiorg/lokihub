package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/jackc/pgtype"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

type PostgresInterceptStore struct {
	pool *pgxpool.Pool
}

func NewPostgresInterceptStore(pool *pgxpool.Pool) *PostgresInterceptStore {
	return &PostgresInterceptStore{pool: pool}
}

func (s *PostgresInterceptStore) PaymentInfo(htlcPaymentHash []byte) (string, string, *core.OpeningFeeParams, []byte, []byte, []byte, int64, int64, *wire.OutPoint, *string, error) {
	var (
		p, tag                                   *string
		paymentHash, paymentSecret, destination  []byte
		incomingAmountMloki, outgoingAmountMloki int64
		fundingTxID                              []byte
		fundingTxOutnum                          pgtype.Int4
	)
	err := s.pool.QueryRow(context.Background(),
		`SELECT payment_hash, payment_secret, destination, incoming_amount_mloki, outgoing_amount_mloki, funding_tx_id, funding_tx_outnum, opening_fee_params, tag
			FROM payments
			WHERE payment_hash=$1 OR sha256('probing-01:' || payment_hash)=$1`,
		htlcPaymentHash).Scan(&paymentHash, &paymentSecret, &destination, &incomingAmountMloki, &outgoingAmountMloki, &fundingTxID, &fundingTxOutnum, &p, &tag)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = nil
		}
		return "", "", nil, nil, nil, nil, 0, 0, nil, nil, err
	}

	var cp *wire.OutPoint
	if fundingTxID != nil {
		cp, err = lightning.NewOutPoint(fundingTxID, uint32(fundingTxOutnum.Int))
		if err != nil {
			log.Error().Msgf("invalid funding txid in database %x", fundingTxID)
		}
	}

	// opening_fee_params is nullable: a payment row with no fee params
	// attached (e.g. a plain forward, not a channel-open payment) has
	// p == nil. Returning here with zero-value token/orderId/params
	// mirrors the "not found" shape above rather than dereferencing a nil
	// extParams.
	if p == nil {
		return "", "", nil, paymentHash, paymentSecret, destination, incomingAmountMloki, outgoingAmountMloki, cp, tag, nil
	}

	var extParams *extendedParams
	err = json.Unmarshal([]byte(*p), &extParams)
	if err != nil {
		log.Error().Msgf("Failed to unmarshal OpeningFeeParams '%s': %v", *p, err)
		return "", "", nil, nil, nil, nil, 0, 0, nil, nil, err
	}
	return extParams.Token, extParams.OrderId, &extParams.Params, paymentHash, paymentSecret, destination, incomingAmountMloki, outgoingAmountMloki, cp, tag, nil
}

func (s *PostgresInterceptStore) SetFundingTx(paymentHash []byte, channelPoint *wire.OutPoint) error {
	commandTag, err := s.pool.Exec(context.Background(),
		`UPDATE payments
			SET funding_tx_id = $2, funding_tx_outnum = $3
			WHERE payment_hash=$1`,
		paymentHash, channelPoint.Hash[:], channelPoint.Index)
	log.Info().Msgf("setFundingTx(%x, %s, %d): %s err: %v", paymentHash, channelPoint.Hash.String(), channelPoint.Index, commandTag, err)
	return err
}

// UpdateLsps1OrderFunding and UpdateLsps1OrderState let the interceptor's
// LSPS1 JIT path mark an order funded + COMPLETED on the shared lsps1.orders
// table (FLSPD-0015). Both delegate to the canonical PostgresOpeningStore
// implementations over the same pool rather than re-copying their SQL, so the
// FLSPD-0021 first-write guard and the S-5 conflict handling stay in one place.
func (s *PostgresInterceptStore) UpdateLsps1OrderFunding(orderId string, fundingTxid string, fundingOutpoint int) (bool, error) {
	return NewPostgresOpeningStore(s.pool).UpdateLsps1OrderFunding(orderId, fundingTxid, fundingOutpoint)
}

func (s *PostgresInterceptStore) UpdateLsps1OrderState(orderId, newState, newPaymentState, expectedCurrentState string) (bool, error) {
	return NewPostgresOpeningStore(s.pool).UpdateLsps1OrderState(orderId, newState, newPaymentState, expectedCurrentState)
}

func (s *PostgresInterceptStore) RegisterPayment(token string, orderId string, params *core.OpeningFeeParams, destination, paymentHash, paymentSecret []byte, incomingAmountMloki, outgoingAmountMloki int64, tag string) error {
	var t *string
	if tag != "" {
		t = &tag
	}

	p := []byte{}
	if params != nil {
		var err error
		p, err = json.Marshal(extendedParams{Token: token, OrderId: orderId, Params: *params})
		if err != nil {
			log.Error().Msgf("Failed to marshal OpeningFeeParams: %v", err)
			return err
		}
	}

	commandTag, err := s.pool.Exec(context.Background(),
		`INSERT INTO
		payments (destination, payment_hash, payment_secret, incoming_amount_mloki, outgoing_amount_mloki, tag, opening_fee_params)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING`,
		destination, paymentHash, paymentSecret, incomingAmountMloki, outgoingAmountMloki, t, p)
	// The payment secret is never logged nor put in the error.
	log.Info().Msgf("registerPayment(%x, %x, %v, %v, %v, %s) rows: %v err: %v",
		destination, paymentHash, incomingAmountMloki, outgoingAmountMloki, tag, p, commandTag.RowsAffected(), err)
	if err != nil {
		return fmt.Errorf("registerPayment(%x, %x, %v, %v, %v, %s) error: %w",
			destination, paymentHash, incomingAmountMloki, outgoingAmountMloki, tag, p, err)
	}
	return nil
}

func (s *PostgresInterceptStore) GetLsps2Order(scid uint64) (*core.Lsps2BuyRegistration, error) {
	// DEBUG LOGGING
	log.Trace().Msgf("GetLsps2Order querying int64 SCID: %d (uint64: %d)", int64(scid), scid)
	row := s.pool.QueryRow(context.Background(),
		`SELECT `+lsps2BuyRegistrationColumns+`
		FROM lsps2.buy_registrations WHERE scid=$1`, int64(scid))

	reg, err := scanLsps2BuyRegistration(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return reg, nil
}

// execLsps2RegistrationCAS runs sql -- a full, self-contained
// compare-and-set UPDATE targeting exactly one row by scid -- and reports
// whether it actually matched a row. The shared boilerplate behind
// ClaimLsps2Registration and ReleaseLsps2Registration: both are a
// single-column CAS on lsps2.buy_registrations.state (the same shape
// UpdateLsps1OrderState already generalized for lsps1.orders' two-column
// case), differing only in which transition and extra condition each
// enforces -- kept as two full, independently-readable/greppable SQL
// statements at their call sites rather than a further-generalized,
// string-built query, so the actual UPDATE each runs stays visible in one
// place instead of assembled from parts.
func (s *PostgresInterceptStore) execLsps2RegistrationCAS(sql string, scid uint64) (bool, error) {
	result, err := s.pool.Exec(context.Background(), sql, int64(scid))
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

// ClaimLsps2Registration atomically transitions a registration
// ACTIVE->OPENING (Phase 7, F-15/F-22/T7-2): the actual concurrency gate a
// real channel-open must pass *before* openChannel is called, not a
// post-hoc bookkeeping entry after. Matches expires_at > now() too, so an
// expired-but-not-yet-swept registration can't be claimed either.
func (s *PostgresInterceptStore) ClaimLsps2Registration(scid uint64) (bool, error) {
	return s.execLsps2RegistrationCAS(
		`UPDATE lsps2.buy_registrations
		 SET state = 'OPENING'
		 WHERE scid = $1 AND state = 'ACTIVE' AND expires_at > now()`,
		scid)
}

// ReleaseLsps2Registration reverts OPENING->ACTIVE (best-effort) after
// openChannel failed, so a later HTLC can retry the same registration.
func (s *PostgresInterceptStore) ReleaseLsps2Registration(scid uint64) error {
	_, err := s.execLsps2RegistrationCAS(
		`UPDATE lsps2.buy_registrations SET state = 'ACTIVE' WHERE scid = $1 AND state = 'OPENING'`,
		scid)
	return err
}

// ConsumeLsps2Registration atomically transitions OPENING->CONSUMED and
// records the redemption in lsps2.bought_channels -- present in the schema
// since the original migration, never written to before Phase 7 (F-15,
// F-21). Both writes happen in one transaction so a registration can never
// be marked CONSUMED without a matching bought_channels row, or vice versa.
func (s *PostgresInterceptStore) ConsumeLsps2Registration(scid uint64, channelPoint *wire.OutPoint, paymentSizeMloki, feeMloki uint64) (bool, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var registrationId string
	err = tx.QueryRow(ctx,
		`UPDATE lsps2.buy_registrations SET state = 'CONSUMED'
		 WHERE scid = $1 AND state = 'OPENING'
		 RETURNING id`,
		int64(scid)).Scan(&registrationId)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Someone else already consumed, released, or expired this
			// registration -- not this caller's channel to record.
			return false, nil
		}
		return false, err
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO lsps2.bought_channels
			(id, registration_id, funding_tx_id, funding_tx_outnum, fee_mloki, payment_size_mloki, is_completed, completed_at)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, true, now())`,
		registrationId, channelPoint.Hash[:], channelPoint.Index, feeMloki, paymentSizeMloki)
	if err != nil {
		return false, fmt.Errorf("ConsumeLsps2Registration: failed to record bought_channels for scid=%d: %w", scid, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// GetStuckLsps2Registrations returns registrations left at OPENING -- e.g.
// by a crash between openChannel succeeding and the
// ConsumeLsps2Registration commit (T7-13, the LSPS2 analogue of
// settlement_service.go's recoverStuckOrders/reconcileUnclaimedChannel).
func (s *PostgresInterceptStore) GetStuckLsps2Registrations() ([]*core.Lsps2BuyRegistration, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT `+lsps2BuyRegistrationColumns+`
		FROM lsps2.buy_registrations WHERE state = 'OPENING'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var regs []*core.Lsps2BuyRegistration
	for rows.Next() {
		reg, err := scanLsps2BuyRegistration(rows)
		if err != nil {
			return nil, err
		}
		regs = append(regs, reg)
	}
	return regs, rows.Err()
}

// GetClaimedLsps2ChannelPoints returns the set of channel points already
// recorded in lsps2.bought_channels, keyed by wire.OutPoint.String()'s
// "hash:index" form -- the same key shape RecoverStuckLsps2Registrations
// compares against lightning.Channel.ChannelPoint.String(), and the same
// convention Lsps1SettlementService.claimedChannelPoints uses for LSPS1's
// funding_txid/funding_outpoint. A small, one-shot full-table scan: this
// only ever runs once at startup (and optionally on a periodic tick),
// never on the HTLC hot path.
func (s *PostgresInterceptStore) GetClaimedLsps2ChannelPoints() (map[string]bool, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT funding_tx_id, funding_tx_outnum FROM lsps2.bought_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	claimed := make(map[string]bool)
	for rows.Next() {
		var fundingTxID []byte
		var fundingTxOutnum int64
		if err := rows.Scan(&fundingTxID, &fundingTxOutnum); err != nil {
			return nil, err
		}
		point, err := lightning.NewOutPoint(fundingTxID, uint32(fundingTxOutnum))
		if err != nil {
			log.Error().Err(err).Msgf("GetClaimedLsps2ChannelPoints: invalid funding txid in database %x", fundingTxID)
			continue
		}
		claimed[point.String()] = true
	}
	return claimed, rows.Err()
}

// GetPaidLsps1Order finds the oldest PAID order for a client that hasn't
// been fulfilled yet. payment_state='PAID' excludes HOLD orders (H-1): a
// held order's order_state is already 'PAID' the moment its HTLC is merely
// ACCEPTED (see settlement_service.go's processAcceptedOrder doc comment),
// but its funds aren't captured yet -- this JIT fallback must not treat
// that as safe to open a channel for, or it races settlement_service's own
// openChannelForHeldOrder into a duplicate channel open for the same order.
func (s *PostgresInterceptStore) GetPaidLsps1Order(clientNodeId string) (*core.Lsps1Order, error) {
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
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &order, nil
}
