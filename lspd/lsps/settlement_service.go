package lsps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/chain"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/rs/zerolog/log"
)

// heldInvoiceSafetyNetPollInterval (H-10/H-11/H-12): CREATED orders'
// ACCEPTED transition is primarily observed live now, via a per-invoice
// SubscribeSingleInvoice watcher attached at order-creation time
// (WatchOrder/watchOrderInvoice) -- LND's base SubscribeInvoices RPC only
// ever delivers "newly added" and "settled" events, confirmed live against
// a real flnd node (2026-07-17), so it can't be used for this directly.
// This poll is now a safety net, not the primary detection path: it covers
// (a) a watcher whose stream errored and was deliberately not
// self-retried, and (b) any order that somehow never got WatchOrder called.
// 60s is still trivially fast relative to a hold invoice's real safety
// margin (LND's own hold-invoice-expiry watcher triggers on the order of
// tens of minutes).
const heldInvoiceSafetyNetPollInterval = 60 * time.Second

// Lsps1SettlementService monitors LND invoice settlements and processes LSPS1 orders
type Lsps1SettlementService struct {
	client       lightning.Client
	openingStore ports.OpeningStore
	historyStore history.Store
	// config is an atomic ref, not a raw pointer, so it can be hot-swapped
	// by the dashboard's config-update handler without racing settlement's
	// own reads here (C-3). Load() it once per logical operation.
	config              *config.NodeConfigRef
	feeEstimator        chain.FeeEstimator
	feeStrategy         chain.FeeStrategy
	notificationService NotificationService
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	// watchedOrders tracks orderId -> struct{} for orders with a live
	// watchOrderInvoice goroutine currently running (H-12). Makes
	// WatchOrder idempotent: the periodic safety-net poll re-discovers
	// every still-OPEN CREATED order on each tick and calls WatchOrder
	// again, which must be a no-op while a watcher for that order is
	// already active rather than spawning a duplicate LND stream. Cleared
	// when a watcher exits, so a later poll tick can re-attach one if an
	// earlier watcher's stream errored out (deliberately not retried
	// internally -- see watchOrderInvoice).
	watchedOrders sync.Map
	// pollRunning guards heldInvoiceSafetyNetPollInterval's ticker (H-10):
	// recoverSettledCreatedOrders now runs in its own goroutine per tick
	// (not inline in the shared event-dispatch select loop), and this
	// flag skips starting a new run if the previous one is still going,
	// rather than letting them pile up concurrently.
	pollRunning atomic.Bool
}

// NewLsps1SettlementService creates a new settlement monitoring service
func NewLsps1SettlementService(
	client lightning.Client,
	openingStore ports.OpeningStore,
	historyStore history.Store,
	config *config.NodeConfigRef,
	feeEstimator chain.FeeEstimator,
	feeStrategy chain.FeeStrategy,
	notificationService NotificationService,
) *Lsps1SettlementService {
	return &Lsps1SettlementService{
		client:              client,
		openingStore:        openingStore,
		historyStore:        historyStore,
		config:              config,
		feeEstimator:        feeEstimator,
		feeStrategy:         feeStrategy,
		notificationService: notificationService,
	}
}

// Start begins monitoring invoice settlements
func (s *Lsps1SettlementService) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	invoiceChan, errChan, err := s.client.SubscribeInvoices()
	if err != nil {
		log.Error().Err(err).Msg("LSPS1 Settlement: Failed to subscribe to invoices")
		return err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		log.Info().
			Str("component", string(logging.LogComponentSettlement)).
			Str("action", string(logging.LogActionSuccess)).
			Msg("LSPS1 Settlement: Service started, monitoring invoices")

		// Recover stuck orders in a separate goroutine to avoid blocking Start()
		go s.recoverStuckOrders()

		pollTicker := time.NewTicker(heldInvoiceSafetyNetPollInterval)
		defer pollTicker.Stop()

		for {

			select {
			case <-s.ctx.Done():
				log.Info().
					Str("component", string(logging.LogComponentSettlement)).
					Msg("LSPS1 Settlement: Service stopped")
				return

			case err := <-errChan:
				if err != nil {
					log.Error().Err(err).Msg("LSPS1 Settlement: Invoice subscription error")
					// TODO: Implement reconnection logic with exponential backoff
				}

			case update := <-invoiceChan:
				if update == nil {
					continue
				}
				switch update.State {
				case lightning.InvoiceStateSettled:
					s.processSettledInvoice(update)
					// InvoiceStateAccepted has no case here: confirmed live
					// against real flnd (2026-07-17) that LND's base
					// SubscribeInvoices RPC never delivers it (see
					// heldInvoiceSafetyNetPollInterval's doc comment).
					// watchOrderInvoice's per-invoice SubscribeSingleInvoice
					// stream is the live detection path now (H-12);
					// processAcceptedInvoice/processAcceptedOrder remain
					// directly unit-tested (settlement_service_test.go) and
					// stay reachable in production via watchOrderInvoice and
					// the safety-net poll below, not this switch.
				}

			case <-pollTicker.C:
				// H-10: run off this shared select loop, with an overlap
				// guard, so N pending orders' worth of poll work can never
				// delay a live invoice event for a different customer the
				// way it used to when this ran inline here. Tracked by
				// s.wg (like watchOrderInvoice) so Stop() still waits for
				// an in-flight poll to finish before returning.
				if s.pollRunning.CompareAndSwap(false, true) {
					s.wg.Add(1)
					go func() {
						defer s.wg.Done()
						defer s.pollRunning.Store(false)
						s.recoverSettledCreatedOrders()
					}()
				} else {
					log.Warn().Msg("LSPS1 Settlement: Safety-net poll still running from a previous tick, skipping this one")
				}
			}
		}
	}()

	return nil
}

// Stop gracefully shuts down the settlement service
func (s *Lsps1SettlementService) Stop() {
	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Msg("LSPS1 Settlement: Stopping service...")
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Msg("LSPS1 Settlement: Service stopped successfully")
}

// processSettledInvoice handles a settled invoice event
func (s *Lsps1SettlementService) processSettledInvoice(update *lightning.InvoiceUpdate) {
	pr := update.PaymentRequest
	if len(pr) > 20 {
		pr = pr[:20] + "..."
	}
	log.Debug().
		Str("payment_request", pr).
		Uint64("amount_mloki", update.AmountMloki).
		Msg("LSPS1 Settlement: Processing settled invoice")

	// Lookup order by payment invoice
	order, err := s.openingStore.GetLsps1OrderByPaymentInvoice(update.PaymentRequest)
	if err != nil {
		// Not all invoices are LSPS1 orders, skip silently
		log.Trace().Err(err).Msg("LSPS1 Settlement: Invoice not found in LSPS1 orders (expected for non-LSPS1 invoices)")
		return
	}

	// Check if order is in CREATED state (prevent duplicate processing)
	if order.OrderState != string(Lsps1OrderStateCreated) {
		log.Warn().
			Str("order_id", order.OrderId).
			Str("current_state", order.OrderState).
			Msg("LSPS1 Settlement: Order not in CREATED state, skipping")
		return
	}

	// Update order state to PAID. This is a compare-and-set on the CREATED
	// state we just observed: if a concurrent writer (e.g. the startup
	// recovery sweep in recoverSettledCreatedOrders) already transitioned
	// this order, updated is false and we must not proceed — otherwise
	// both paths would call openChannelForOrder for the same order (F-1).
	updated, err := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStatePaid), string(Lsps1OrderStatePaid), string(Lsps1OrderStateCreated))
	if err != nil {
		log.Error().
			Err(err).
			Str("order_id", order.OrderId).
			Msg("LSPS1 Settlement: Failed to update order state")
		return
	}
	if !updated {
		log.Info().
			Str("order_id", order.OrderId).
			Msg("LSPS1 Settlement: Order already transitioned out of CREATED by a concurrent writer, skipping")
		return
	}

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("action", string(logging.LogActionSuccess)).
		Str("order_id", order.OrderId).
		Str("peer_id", order.PeerId).
		Int64("client_balance", order.ClientBalanceLoki).
		Msg("LSPS1 Settlement: Order marked as PAID")

	// Notify client of state change
	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStatePaid), "", "")
	}

	// Trigger automatic channel opening
	go s.openChannelForOrder(order)
}

// processAcceptedInvoice handles a hold invoice's HTLC being ACCEPTED (held,
// not yet settled) -- the live-event counterpart of processSettledInvoice
// for orders created with AddHoldInvoice.
func (s *Lsps1SettlementService) processAcceptedInvoice(update *lightning.InvoiceUpdate) {
	order, err := s.openingStore.GetLsps1OrderByPaymentInvoice(update.PaymentRequest)
	if err != nil {
		// Not all invoices are LSPS1 orders, skip silently
		log.Trace().Err(err).Msg("LSPS1 Settlement: Accepted invoice not found in LSPS1 orders (expected for non-LSPS1 invoices)")
		return
	}
	s.processAcceptedOrder(order)
}

// processAcceptedOrder is the CAS-then-dispatch shared by the live
// InvoiceStateAccepted event and recoverSettledCreatedOrders' startup
// replay (mirroring processSettledInvoice/processSettledOrder's split for
// the legacy flow). It moves the order straight from CREATED to the
// internal PAID state (F-1-style CAS, same mechanism processSettledInvoice
// uses) with payment_state HOLD rather than PAID: order_state=PAID here
// means "committed to attempting fulfillment", not "funds captured" --
// funds are only captured if openChannelForHeldOrder's attempt succeeds.
// This deliberately reuses order_state=PAID (rather than a new state) so
// openChannelForHeldOrder can reuse reconcileUnclaimedChannel/
// reconcileFundedOrder and GetAllPaidLsps1Orders unmodified.
func (s *Lsps1SettlementService) processAcceptedOrder(order *core.Lsps1Order) {
	if order.OrderState != string(Lsps1OrderStateCreated) {
		log.Debug().
			Str("order_id", order.OrderId).
			Str("current_state", order.OrderState).
			Msg("LSPS1 Settlement: Order not in CREATED state, skipping accepted-invoice handling")
		return
	}

	updated, err := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStatePaid), string(Lsps1PaymentStateHold), string(Lsps1OrderStateCreated))
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to update order state for accepted invoice")
		return
	}
	if !updated {
		log.Info().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Order already transitioned out of CREATED by a concurrent writer, skipping")
		return
	}

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("action", string(logging.LogActionSuccess)).
		Str("order_id", order.OrderId).
		Str("peer_id", order.PeerId).
		Msg("LSPS1 Settlement: Hold invoice ACCEPTED, order committed to attempt")

	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStatePaid), "", "")
	}

	go s.openChannelForHeldOrder(order)
}

// openChannelForHeldOrder is openChannelForOrder's counterpart for orders
// paid via a hold invoice (order.PaymentState == HOLD): the client's HTLC
// is held, not yet settled, so unlike openChannelForOrder this must reach a
// terminal decision -- settle (capture funds, order succeeds) or cancel
// (instant refund, no on-chain tx) -- rather than ever leaving the order
// pending. In particular a not-connected peer fails fast here instead of
// deferring: every second holding the HTLC is a real cost (risk of a
// force-close as it nears its CLTV expiry), unlike the legacy flow where
// the money is already safely captured and waiting costs nothing.
//
// Always called with order.FundingTxid == nil (a fresh attempt): a HOLD
// order that already has funding recorded (crash recovery found an earlier
// attempt's channel-open already broadcast) is routed to
// reconcileFundedHeldOrder instead -- see recoverStuckOrders (H-4).
func (s *Lsps1SettlementService) openChannelForHeldOrder(order *core.Lsps1Order) {
	cfg := s.config.Load()

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("action", string(logging.LogActionRequest)).
		Str("order_id", order.OrderId).
		Str("peer_id", order.PeerId).
		Msg("LSPS1 Settlement: Attempting channel open for held order")

	peerId, err := hex.DecodeString(order.PeerId)
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to decode peer ID")
		s.cancelHeldOrder(order, err)
		return
	}

	// A connectivity-check RPC error (e.g. a transient gRPC/LND hiccup) is
	// not the same as a confirmed not-connected result (H-6): the legacy
	// openChannelForOrder path can afford to just report FAILED and let a
	// later attempt retry, but a held order's HTLC is actively ticking down
	// to its CLTV deadline, so a few in-place retries are cheap insurance
	// against triggering a needless instant refund over a blip.
	connected, err := s.checkPeerConnectedWithRetry(peerId, order.OrderId)
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to check peer connectivity after retries")
		s.cancelHeldOrder(order, err)
		return
	}
	if !connected {
		log.Info().
			Str("component", string(logging.LogComponentSettlement)).
			Str("order_id", order.OrderId).
			Str("peer_id", order.PeerId).
			Msg("LSPS1 Settlement: Peer not connected, cancelling held order")
		s.cancelHeldOrder(order, fmt.Errorf("peer not connected"))
		return
	}

	req := s.buildOpenChannelRequest(order, cfg, peerId)

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("action", string(logging.LogActionRequest)).
		Str("order_id", order.OrderId).
		Uint64("capacity_loki", req.CapacityLoki).
		Uint64("push_loki", req.PushLoki).
		Msg("LSPS1 Settlement: Calling OpenChannel for held order")

	channelPoint, err := s.client.OpenChannel(req)

	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to open channel for held order")

		// Mirrors openChannelForOrder's F-8 handling: a retried attempt
		// (crash recovery) can land here after an earlier attempt's
		// OpenChannel already broadcast successfully. If so the LSP is
		// already committed -- record the funding and go through the same
		// settle-then-complete path as the happy path below (H-2: this
		// must NOT mark the order COMPLETED before settlement is verified
		// to have succeeded, unlike the old reconcileUnclaimedChannel-based
		// handling here, which wrote COMPLETED before settlement was even
		// attempted).
		point, ok := s.findUnclaimedChannelPoint(order, peerId)
		if !ok {
			s.cancelHeldOrder(order, err)
			return
		}
		s.recordHeldOrderFundingAndSettle(order, point)
		return
	}

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("action", string(logging.LogActionSuccess)).
		Str("order_id", order.OrderId).
		Str("channel_point", channelPoint.String()).
		Msg("LSPS1 Settlement: Channel opening initiated for held order, settling invoice")

	// The funding tx is broadcast and on-chain fees are already spent --
	// this is the point of no return. Record funding before attempting
	// settlement (not after, as this used to): if flspd crashes between
	// OpenChannel succeeding and settlement completing, recoverStuckOrders
	// must be able to find this order via FundingTxid and retry settlement
	// directly, rather than falling through to a second OpenChannel attempt.
	s.recordHeldOrderFundingAndSettle(order, channelPoint)
}

// recordHeldOrderFundingAndSettle persists a held order's funding outpoint
// and then attempts settlement, marking the order COMPLETED only if
// settlement actually succeeds (H-2). If settlement fails, the order is
// deliberately left at PAID/HOLD (not COMPLETED) with funding now recorded,
// so a later recoverStuckOrders pass -- routed by PaymentState==HOLD
// regardless of FundingTxid (H-4) -- retries settlement instead of the DB
// silently claiming success for an uncaptured payment.
//
// S-6: if the funding claim itself doesn't succeed (a transient DB error,
// or -- per S-5 -- another order already claimed this exact channel point),
// this does NOT proceed to settle/complete: the order stays PAID/HOLD with
// FundingTxid still nil in the DB, so the next recoverStuckOrders pass
// routes it back through openChannelForHeldOrder, whose OpenChannel retry
// will collide with the already-open channel and fall through to
// findUnclaimedChannelPoint, which finds and claims the orphaned channel
// properly -- reusing the existing self-healing mechanism instead of
// risking an order marked COMPLETED with no funding_txid ever recorded.
func (s *Lsps1SettlementService) recordHeldOrderFundingAndSettle(order *core.Lsps1Order, point *wire.OutPoint) {
	outpointIdx := int(point.Index)
	txid := point.Hash.String()

	claimed, err := s.openingStore.UpdateLsps1OrderFunding(order.OrderId, txid, outpointIdx)
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to update order funding details")
		return
	}
	if !claimed {
		log.Warn().Str("order_id", order.OrderId).Str("channel_point", fmt.Sprintf("%s:%d", txid, outpointIdx)).Msg("LSPS1 Settlement: Channel point already claimed by another order, leaving PAID/HOLD for a later recovery pass")
		return
	}
	order.FundingTxid = &txid
	order.FundingOutpoint = &outpointIdx

	s.settleAndCompleteHeldOrder(order)
}

// settleAndCompleteHeldOrder captures a held order's funds and marks it
// COMPLETED, but only if SettleInvoice actually succeeds (H-2). The caller
// must already have committed to this order (broadcast a channel-open, or
// verified one is already active) before calling this, since from that
// point on there's no "cancel" option left -- only "settled" or "needs a
// retry."
func (s *Lsps1SettlementService) settleAndCompleteHeldOrder(order *core.Lsps1Order) {
	if !s.settleHeldOrderInvoice(order) {
		log.Error().
			Str("order_id", order.OrderId).
			Msg("LSPS1 Settlement: Invoice not settled after retries, channel already committed -- leaving order PAID/HOLD for a later recovery pass to retry settlement")
		return
	}

	updated, err := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStateCompleted), string(Lsps1PaymentStatePaid), string(Lsps1OrderStatePaid))
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to update order state to COMPLETED")
		return
	}
	if !updated {
		log.Info().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Order already transitioned out of PAID by a concurrent writer, skipping COMPLETED notification")
		return
	}

	// S-10: both current callers (recordHeldOrderFundingAndSettle,
	// reconcileFundedHeldOrder via recoverStuckOrders' own FundingTxid
	// guard) always populate FundingTxid/FundingOutpoint before calling
	// this function, so this nil-check never actually fires today.
	// Deliberately left in rather than assumed away: it's cheap insurance
	// against a future third caller forgetting to populate them first,
	// which would otherwise silently send a notification with an empty
	// channel_point instead of failing loudly. Reviewed 2026-07-18 (S-10)
	// and kept as-is.
	channelPoint := ""
	if order.FundingTxid != nil && order.FundingOutpoint != nil {
		channelPoint = fmt.Sprintf("%s:%d", *order.FundingTxid, *order.FundingOutpoint)
	}
	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStateCompleted), channelPoint, "")
	}
}

// checkPeerConnectedWithRetry retries a connectivity check a few times
// before giving up, so a single transient IsConnected RPC error doesn't get
// treated the same as a confirmed not-connected result (H-6).
func (s *Lsps1SettlementService) checkPeerConnectedWithRetry(peerId []byte, orderId string) (bool, error) {
	const maxAttempts = 3
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var connected bool
		connected, err = s.client.IsConnected(peerId)
		if err == nil {
			return connected, nil
		}
		log.Warn().Err(err).Str("order_id", orderId).Int("attempt", attempt).Msg("LSPS1 Settlement: IsConnected check failed, retrying")
		if attempt < maxAttempts {
			time.Sleep(time.Second)
		}
	}
	return false, err
}

// cancelHeldOrder is the instant-refund path a hold invoice makes
// possible: cancel the invoice (the payer's HTLC fails back through the
// route immediately, no on-chain transaction) and mark the order FAILED
// with payment_state REFUNDED. CancelInvoice's own failure is logged but
// not fatal to this flow -- LND's hold-invoice expiry watcher cancels the
// invoice on its own as the HTLC nears its CLTV deadline, so the payer's
// funds are never permanently stuck even if this particular call errors.
func (s *Lsps1SettlementService) cancelHeldOrder(order *core.Lsps1Order, reason error) {
	if hash, ok := s.paymentHashForOrder(order); ok {
		if cancelErr := s.client.CancelInvoice(context.Background(), hash); cancelErr != nil {
			log.Warn().Err(cancelErr).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: CancelInvoice failed, relying on LND's own hold-invoice expiry to release the HTLC")
		}
	}

	updated, err := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStateFailed), string(Lsps1PaymentStateRefunded), string(Lsps1OrderStatePaid))
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to update held order state to FAILED")
		return
	}
	if !updated {
		// H-5: a concurrent writer (e.g. a racing reconcile call) already
		// moved this order out of PAID -- most likely it already completed
		// successfully. Sending a FAILED notification now would be a
		// spurious, contradictory message with no DB state behind it.
		log.Info().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Order already transitioned out of PAID by a concurrent writer, skipping cancel notification")
		return
	}

	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStateFailed), "", fmt.Sprintf("held order cancelled: %v", reason))
	}
}

// settleHeldOrderInvoice reveals the preimage to actually capture a held
// order's funds. Called only once the LSP is committed (a channel-open
// attempt for this order has succeeded, or crash recovery found one
// already broadcast) -- from that point on there's no "cancel" option
// left, so failures are retried a few times and, if still failing, logged
// loudly for manual review rather than looped on forever or treated as a
// reason to undo an already-broadcast channel open. Returns whether
// settlement actually succeeded (H-2) so the caller can decide whether it's
// safe to mark the order COMPLETED.
func (s *Lsps1SettlementService) settleHeldOrderInvoice(order *core.Lsps1Order) bool {
	preimage, ok := s.decodeAndValidatePreimage(order, "settle invoice -- manual review required")
	if !ok {
		return false
	}

	const maxAttempts = 5
	var settleErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		settleErr = s.client.SettleInvoice(context.Background(), preimage)
		if settleErr == nil {
			log.Info().
				Str("component", string(logging.LogComponentSettlement)).
				Str("order_id", order.OrderId).
				Msg("LSPS1 Settlement: Hold invoice settled")
			return true
		}
		log.Warn().Err(settleErr).Str("order_id", order.OrderId).Int("attempt", attempt).Msg("LSPS1 Settlement: SettleInvoice failed, retrying")
		time.Sleep(time.Second)
	}
	log.Error().Err(settleErr).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: SettleInvoice failed after retries -- channel is committed but invoice may not be settled, manual review required")
	return false
}

// decodeAndValidatePreimage decodes order.PaymentPreimage and validates
// it's exactly 32 bytes (H-9), logging a descriptive error naming what
// operation needed it on any failure. Shared by cancelHeldOrder (via
// paymentHashForOrder), settleHeldOrderInvoice, and the H-12 watcher --
// all of which need to safely get from a stored preimage to usable bytes,
// and previously duplicated this decode-then-length-check logic.
func (s *Lsps1SettlementService) decodeAndValidatePreimage(order *core.Lsps1Order, operation string) ([]byte, bool) {
	if order.PaymentPreimage == nil {
		log.Error().Str("order_id", order.OrderId).Msgf("LSPS1 Settlement: Held order has no stored preimage, cannot %s", operation)
		return nil, false
	}
	preimage, err := hex.DecodeString(*order.PaymentPreimage)
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msgf("LSPS1 Settlement: Failed to decode stored preimage, cannot %s", operation)
		return nil, false
	}
	if len(preimage) != 32 {
		// A wrong-length preimage (data-integrity issue, not expected in
		// practice -- see handler_lsps1.go, which always generates 32
		// random bytes) would otherwise hash to the wrong payment hash.
		log.Error().Str("order_id", order.OrderId).Int("preimage_len", len(preimage)).Msgf("LSPS1 Settlement: Stored preimage has unexpected length, refusing to use it, cannot %s", operation)
		return nil, false
	}
	return preimage, true
}

// paymentHashForOrder derives a held order's invoice payment hash locally
// from its stored preimage (sha256), for callers that need the hash
// without settling/cancelling -- H-11's hash-based lookup and H-12's
// per-invoice watcher, exactly as cancelHeldOrder already did inline.
func (s *Lsps1SettlementService) paymentHashForOrder(order *core.Lsps1Order) ([]byte, bool) {
	preimage, ok := s.decodeAndValidatePreimage(order, "derive payment hash")
	if !ok {
		return nil, false
	}
	hash := sha256.Sum256(preimage)
	return hash[:], true
}

// WatchOrder attaches a live per-invoice watcher to a freshly-created
// hold-invoice order (H-12, satisfies the OrderWatcher interface in
// handlers.go): subscribes to LND's per-invoice stream so the order's
// ACCEPTED transition is observed near-instantly instead of waiting on
// the safety-net poll (heldInvoiceSafetyNetPollInterval). Safe to call
// even if the service isn't running (e.g. mid-shutdown) -- logs and
// relies on the safety-net poll in that case rather than starting a
// watcher with nothing to tear it down.
func (s *Lsps1SettlementService) WatchOrder(order *core.Lsps1Order) {
	if s.ctx == nil || s.ctx.Err() != nil {
		log.Warn().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Cannot attach live invoice watcher, service not running -- relying on the safety-net poll")
		return
	}
	if _, alreadyWatching := s.watchedOrders.LoadOrStore(order.OrderId, struct{}{}); alreadyWatching {
		// The safety-net poll re-discovers every still-OPEN order on each
		// tick and calls WatchOrder again -- a no-op while this order's
		// watcher is already running.
		return
	}
	hash, ok := s.paymentHashForOrder(order)
	if !ok {
		s.watchedOrders.Delete(order.OrderId) // never actually started a watcher; don't block a future retry
		return
	}
	s.wg.Add(1)
	go s.watchOrderInvoice(order, hash)
}

// watchOrderInvoice is WatchOrder's goroutine body. Deliberately does NOT
// retry on a stream error or exhausted subscription -- a dropped stream
// just means this one order falls back to safety-net-poll coverage for
// the rest of its lifetime, rather than the watcher silently retry-looping
// forever. Exits on: service shutdown (s.ctx.Done()), a stream error, or
// the order reaching a state where no further invoice events are useful
// (ACCEPTED -- openChannelForHeldOrder/cancelHeldOrder take over from
// there; SETTLED/CANCELED -- already terminal).
func (s *Lsps1SettlementService) watchOrderInvoice(order *core.Lsps1Order, hash []byte) {
	defer s.wg.Done()
	defer s.watchedOrders.Delete(order.OrderId)

	updateChan, errChan, err := s.client.SubscribeSingleInvoice(s.ctx, hash)
	if err != nil {
		log.Warn().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to subscribe to invoice updates, relying on the safety-net poll")
		return
	}

	for {
		select {
		case <-s.ctx.Done():
			return
		case _, ok := <-errChan:
			if !ok {
				return
			}
			// Logged inside the adapter's stream-wrapper goroutine
			// already; nothing more to do here except stop watching.
			return
		case update, ok := <-updateChan:
			if !ok {
				return
			}
			switch update.State {
			case lightning.InvoiceStateAccepted:
				s.processAcceptedOrder(order)
				return
			case lightning.InvoiceStateSettled:
				s.processSettledOrder(order)
				return
			case lightning.InvoiceStateCanceled:
				log.Info().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Watched invoice canceled (e.g. LND's own hold-invoice expiry), stopping watcher")
				return
			}
			// InvoiceStateOpen (the initial state SubscribeSingleInvoice
			// always sends immediately on subscribe) and any other state:
			// keep watching.
		}
	}
}

// buildOpenChannelRequest computes the fee estimate, capacity, and
// OpenChannelRequest shared by openChannelForOrder and
// openChannelForHeldOrder (H-13): these were previously duplicated
// byte-for-byte between the two, risking the held-order and legacy-order
// flows silently drifting apart on a future change to fee-estimation
// fallback or capacity calculation.
func (s *Lsps1SettlementService) buildOpenChannelRequest(order *core.Lsps1Order, cfg *config.NodeConfig, peerId []byte) *lightning.OpenChannelRequest {
	fee, err := s.feeEstimator.EstimateFeeRate(context.Background(), s.feeStrategy)
	if err != nil || fee == nil {
		log.Warn().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Fee estimation failed, using target conf from config")
		fee = &chain.FeeEstimation{
			TargetConf: &cfg.TargetConf,
		}
	}

	capacity := uint64(order.LspBalanceLoki+order.ClientBalanceLoki) + uint64(cfg.AdditionalChannelCapacity)
	if capacity < cfg.MinChannelCapacityLoki {
		capacity = cfg.MinChannelCapacityLoki
	}

	return &lightning.OpenChannelRequest{
		Destination:     peerId,
		CapacityLoki:    capacity,
		PushLoki:        uint64(order.ClientBalanceLoki),
		AnnounceChannel: order.AnnounceChannel,
		FeeLokiPerVByte: fee.LokiPerVByte,
		TargetConf:      fee.TargetConf,
		RoutingPolicy: &lightning.RoutingPolicy{
			Ppm:           uint32(cfg.FeeRate * 1_000_000),
			BaseMloki:     cfg.BaseFeeMloki,
			TimeLockDelta: cfg.TimeLockDelta,
		},
	}
}

func (s *Lsps1SettlementService) openChannelForOrder(order *core.Lsps1Order) {
	// Snapshot once so this channel-open decision is made against one
	// internally consistent config even if the dashboard updates it
	// concurrently mid-flight (C-3).
	cfg := s.config.Load()

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("action", string(logging.LogActionRequest)).
		Str("order_id", order.OrderId).
		Str("peer_id", order.PeerId).
		Msg("LSPS1 Settlement: Attempting automatic channel opening")

	peerId, err := hex.DecodeString(order.PeerId)
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to decode peer ID")
		// Ideally notify failure here too, but can't without peerId maybe?
		// We have peerId string in order.PeerId.
		if s.notificationService != nil {
			go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStateFailed), "", "failed to decode peer id")
		}
		return
	}

	// Check if peer is connected
	connected, err := s.client.IsConnected(peerId)
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to check peer connectivity")
		if s.notificationService != nil {
			go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStateFailed), "", fmt.Sprintf("failed to check connectivity: %v", err))
		}
		return
	}

	if !connected {
		log.Info().
			Str("component", string(logging.LogComponentSettlement)).
			Str("order_id", order.OrderId).
			Str("peer_id", order.PeerId).
			Msg("LSPS1 Settlement: Peer not connected, skipping automatic open (will be handled by JIT or manual)")
		// Not a failure per se, just deferred. Do not notify FAILED.
		return
	}

	// Open channel
	req := s.buildOpenChannelRequest(order, cfg, peerId)

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("action", string(logging.LogActionRequest)).
		Str("order_id", order.OrderId).
		Uint64("capacity_loki", req.CapacityLoki).
		Uint64("push_loki", req.PushLoki).
		Msg("LSPS1 Settlement: Calling OpenChannel")

	channelPoint, err := s.client.OpenChannel(req)

	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to open channel")

		// recoverStuckOrders calls openChannelForOrder for any PAID order
		// with no FundingTxid recorded yet -- which includes the case
		// where an *earlier* call to this same function already got past
		// the OpenChannel() call below (broadcasting a real funding tx
		// LND now holds as a pending channel) but crashed before
		// UpdateLsps1OrderFunding could persist that fact. Retrying
		// OpenChannel then collides with LND's own single-pending-channel-
		// per-peer limit and fails here -- reporting FAILED at this point
		// would silently orphan a channel the LSP already paid on-chain
		// fees to open. Check for it before giving up.
		if s.reconcileUnclaimedChannel(order, peerId) {
			return
		}

		// Update state to FAILED
		updated, dbErr := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStateFailed), string(Lsps1OrderStatePaid), string(Lsps1OrderStatePaid))
		if dbErr != nil {
			log.Error().Err(dbErr).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to update order state to FAILED")
			return
		}
		if !updated {
			// A concurrent writer already moved this order out of PAID
			// (most likely it already completed successfully) -- sending a
			// FAILED notification now would be spurious and contradict the
			// DB state.
			log.Info().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Order already transitioned out of PAID by a concurrent writer, skipping FAILED notification")
			return
		}

		if s.notificationService != nil {
			go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStateFailed), "", fmt.Sprintf("failed to open channel: %v", err))
		}
		return
	}

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("action", string(logging.LogActionSuccess)).
		Str("order_id", order.OrderId).
		Str("channel_point", channelPoint.String()).
		Msg("LSPS1 Settlement: Channel opening initiated")

	// Update order with funding details and set to COMPLETED
	// Wait a bit for the channel to be registered in LND if needed, but OpenChannel returns immediately with the point
	outpointIdx := int(channelPoint.Index)
	txid := channelPoint.Hash.String()

	// S-6: don't mark COMPLETED if the funding claim itself didn't
	// succeed (transient DB error, or -- per S-5 -- another order already
	// claimed this exact channel point). Leaving the order PAID with
	// FundingTxid still nil lets the next recoverStuckOrders pass retry
	// via openChannelForOrder -> reconcileUnclaimedChannel's existing
	// orphaned-channel recovery, instead of risking a COMPLETED order
	// with no funding_txid ever recorded.
	claimed, err := s.openingStore.UpdateLsps1OrderFunding(order.OrderId, txid, outpointIdx)
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to update order funding details")
		return
	}
	if !claimed {
		log.Warn().Str("order_id", order.OrderId).Str("channel_point", channelPoint.String()).Msg("LSPS1 Settlement: Channel point already claimed by another order, leaving PAID for a later recovery pass")
		return
	}

	updated, err := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStateCompleted), string(Lsps1OrderStatePaid), string(Lsps1OrderStatePaid))
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to update order state to COMPLETED")
		return
	}
	if !updated {
		log.Info().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Order already transitioned out of PAID by a concurrent writer, skipping COMPLETED notification")
		return
	}

	// Notify client of COMPLETED state with channel point
	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStateCompleted), channelPoint.String(), "")
	}
}

// recoverStuckOrders finds and retries orders that were never fulfilled:
//  1. PAID orders: flspd crashed after marking PAID but before opening the channel.
//  2. CREATED orders whose LND invoice is already SETTLED: flspd crashed after the
//     invoice was settled in LND but before it could write PAID to the DB. The
//     SubscribeInvoices replay handles this eventually, but an explicit LND lookup
//     on startup makes recovery deterministic.
func (s *Lsps1SettlementService) recoverStuckOrders() {
	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Msg("LSPS1 Settlement: Checking for stuck PAID orders to recover...")

	paidOrders, err := s.openingStore.GetAllPaidLsps1Orders()
	if err != nil {
		log.Error().Err(err).Msg("LSPS1 Settlement: Failed to retrieve stuck PAID orders")
		return
	}

	if len(paidOrders) == 0 {
		log.Debug().Msg("LSPS1 Settlement: No stuck orders found")
	} else {
		log.Info().
			Str("component", string(logging.LogComponentSettlement)).
			Int("count", len(paidOrders)).
			Msgf("LSPS1 Settlement: Found %d stuck PAID orders, recovering...", len(paidOrders))

		for _, order := range paidOrders {
			// PaymentState HOLD means this order was paid via a hold
			// invoice: order_state=PAID here only means "committed to
			// attempting fulfillment," never "funds captured" (see
			// processAcceptedOrder's doc comment). This check must run
			// BEFORE the FundingTxid check below (H-4): a held order can
			// have FundingTxid already recorded (the channel-open half of
			// openChannelForHeldOrder succeeded, but flspd crashed before
			// settlement was confirmed) and still need the hold-aware path,
			// not the settlement-unaware reconcileFundedOrder, which would
			// mark it COMPLETED without ever verifying or retrying
			// SettleInvoice (H-2/H-3).
			if order.PaymentState == string(Lsps1PaymentStateHold) {
				if order.FundingTxid != nil && order.FundingOutpoint != nil {
					s.reconcileFundedHeldOrder(order)
				} else {
					s.openChannelForHeldOrder(order)
				}
				continue
			}
			// F-8: a PAID order that already has funding recorded means
			// openChannelForOrder's OpenChannel call already succeeded on
			// some earlier run — the only way it's still PAID (not
			// COMPLETED) is that the COMPLETED state write itself failed
			// or never ran (e.g. flspd crashed between the two writes).
			// Calling openChannelForOrder again here would try to open a
			// SECOND channel for the same order. Reconcile against actual
			// LND state instead of blindly retrying.
			if order.FundingTxid != nil && order.FundingOutpoint != nil {
				s.reconcileFundedOrder(order)
				continue
			}
			s.openChannelForOrder(order)
		}
	}

	// Also check CREATED orders: if the invoice was settled in LND while flspd
	// was down, the order never progressed. Look each one up directly in LND.
	s.recoverSettledCreatedOrders()
}

// verifyChannelActive checks whether order's recorded funding outpoint
// corresponds to a channel LND currently reports as active -- shared by
// reconcileFundedOrder (legacy flow) and reconcileFundedHeldOrder (H-3):
// both need the same "don't blindly trust a stale FundingTxid" check before
// treating a funded order as safe to finalize.
func (s *Lsps1SettlementService) verifyChannelActive(order *core.Lsps1Order) bool {
	if order.FundingTxid == nil || order.FundingOutpoint == nil {
		// Defensive only (S-8): both current callers only reach this
		// function after already checking FundingTxid/FundingOutpoint are
		// non-nil, so this should never fire in practice -- but this
		// function is documented as a shared, reusable helper, so it
		// shouldn't trust that invariant silently. Failing loudly here
		// beats a nil-pointer panic in NewOutPointFromString below if a
		// future caller (or a reordering of an existing one) forgets it.
		log.Error().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: verifyChannelActive called without funding recorded -- this is a caller bug")
		return false
	}

	peerId, err := hex.DecodeString(order.PeerId)
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to decode peer ID during funded-order reconciliation")
		return false
	}

	outpoint, err := lightning.NewOutPointFromString(fmt.Sprintf("%s:%d", *order.FundingTxid, *order.FundingOutpoint))
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Stored funding outpoint is invalid, cannot reconcile")
		return false
	}

	if _, err := s.client.GetChannel(peerId, *outpoint); err != nil {
		log.Warn().Err(err).
			Str("order_id", order.OrderId).
			Str("channel_point", fmt.Sprintf("%s:%d", *order.FundingTxid, *order.FundingOutpoint)).
			Msg("LSPS1 Settlement: Order has recorded funding but no matching active channel found in LND — leaving PAID for manual review, not retrying OpenChannel")
		return false
	}
	return true
}

// reconcileFundedOrder handles a PAID order that already has funding details
// recorded (F-8): rather than risk opening a duplicate channel, it verifies
// the previously-opened channel is actually active in LND and, if so,
// transitions the order straight to COMPLETED. If the channel can't be
// found (e.g. force-closed before recovery ran), this deliberately does
// NOT fall back to retrying OpenChannel — that ambiguous case (was the
// channel ever really there?) needs a human to look, not a guess that
// risks a duplicate open.
//
// Only ever called for legacy (non-hold) orders: recoverStuckOrders routes
// HOLD orders to reconcileFundedHeldOrder instead (H-3/H-4), since this
// function never checks or retries settlement.
func (s *Lsps1SettlementService) reconcileFundedOrder(order *core.Lsps1Order) {
	if !s.verifyChannelActive(order) {
		return
	}

	channelPoint := fmt.Sprintf("%s:%d", *order.FundingTxid, *order.FundingOutpoint)
	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("order_id", order.OrderId).
		Str("channel_point", channelPoint).
		Msg("LSPS1 Settlement: Recorded funding verified active in LND — reconciling order to COMPLETED")

	updated, err := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStateCompleted), string(Lsps1OrderStatePaid), string(Lsps1OrderStatePaid))
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to reconcile order state to COMPLETED")
		return
	}
	if !updated {
		// H-5: a concurrent writer already moved this order out of PAID.
		log.Info().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Order already transitioned out of PAID by a concurrent writer, skipping reconciliation notification")
		return
	}

	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStateCompleted), channelPoint, "")
	}
}

// reconcileFundedHeldOrder handles a HOLD order that already has funding
// details recorded (H-3/H-4): the channel-open half of an earlier
// openChannelForHeldOrder attempt already succeeded, but flspd crashed (or
// SettleInvoice failed all its retries) before the order could be marked
// COMPLETED. Verifies the channel is still active (mirroring
// reconcileFundedOrder) before retrying settlement -- unlike
// reconcileFundedOrder, this does NOT assume "funded" means "done": it
// still needs settleAndCompleteHeldOrder to actually confirm/retry
// SettleInvoice before the order can be marked COMPLETED.
func (s *Lsps1SettlementService) reconcileFundedHeldOrder(order *core.Lsps1Order) {
	if !s.verifyChannelActive(order) {
		return
	}
	s.settleAndCompleteHeldOrder(order)
}

// findUnclaimedChannelPoint looks for exactly one channel (LND's
// ListChannels already merges in still-pending, not-yet-confirmed opens --
// see LndClient.ListChannels) to order's peer that isn't already recorded
// as the funding for some other order, and if found, treats it as the
// result of an earlier, crashed channel-open attempt: OpenChannel broadcast
// the funding transaction successfully, but the crash landed before
// UpdateLsps1OrderFunding could persist that. Reconciling here instead of
// retrying OpenChannel avoids both a collision with LND's own
// pending-channel limit and, worse, silently reporting FAILED/refunding an
// order whose channel the LSP already paid on-chain fees to open.
//
// Deliberately conservative: zero or more than one unclaimed candidate is
// ambiguous, so this returns false and lets the caller fall through to its
// normal failure handling rather than guessing which channel (if any)
// belongs to this order -- the same philosophy reconcileFundedOrder already
// applies to the has-FundingTxid case.
func (s *Lsps1SettlementService) findUnclaimedChannelPoint(order *core.Lsps1Order, peerId []byte) (*wire.OutPoint, bool) {
	channels, err := s.client.ListChannels()
	if err != nil {
		log.Warn().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to list channels while checking for an unclaimed prior attempt")
		return nil, false
	}

	var peerChannels []*wire.OutPoint
	for _, ch := range channels {
		if ch.ChannelPoint == nil || !bytes.Equal(ch.PeerId, peerId) {
			continue
		}
		peerChannels = append(peerChannels, ch.ChannelPoint)
	}
	if len(peerChannels) == 0 {
		return nil, false
	}

	// Only worth the extra DB round-trip once there's at least one
	// channel to this peer that might need attributing.
	claimed, err := s.claimedChannelPoints()
	if err != nil {
		log.Warn().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to list existing order funding records while checking for an unclaimed prior attempt")
		return nil, false
	}

	var candidates []*wire.OutPoint
	for _, point := range peerChannels {
		if !claimed[point.String()] {
			candidates = append(candidates, point)
		}
	}

	if len(candidates) != 1 {
		if len(candidates) > 1 {
			log.Warn().
				Str("order_id", order.OrderId).
				Int("candidate_count", len(candidates)).
				Msg("LSPS1 Settlement: Multiple unclaimed channels found for this peer, cannot disambiguate which (if any) belongs to this order — leaving for manual review")
		}
		return nil, false
	}

	point := candidates[0]
	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("order_id", order.OrderId).
		Str("channel_point", point.String()).
		Msg("LSPS1 Settlement: Found an unclaimed channel to this peer — treating as an earlier attempt's crash-orphaned open")
	return point, true
}

// reconcileUnclaimedChannel is openChannelForOrder's (legacy flow) use of
// findUnclaimedChannelPoint: records the found channel as this order's
// funding and marks it COMPLETED directly, since the legacy flow's funds
// are already captured by the time order_state reaches PAID -- there's no
// settlement step left to verify. openChannelForHeldOrder does NOT use this
// function (see recordHeldOrderFundingAndSettle): a held order's funds
// aren't captured yet, so it must settle-then-check before it's safe to
// call the order COMPLETED (H-2).
func (s *Lsps1SettlementService) reconcileUnclaimedChannel(order *core.Lsps1Order, peerId []byte) bool {
	point, ok := s.findUnclaimedChannelPoint(order, peerId)
	if !ok {
		return false
	}

	// S-5: findUnclaimedChannelPoint's read (ListChannels + a DB scan of
	// already-claimed points) isn't atomic with this write -- two orders
	// racing to reconcile the same peer's channel can both see it as
	// unclaimed. The partial unique index on (funding_txid,
	// funding_outpoint) makes the write itself atomic: claimed=false here
	// means another order's write already won, so this one must back off
	// exactly as if no unclaimed channel had been found, not proceed to
	// mark itself COMPLETED against a channel that isn't actually its own.
	claimed, err := s.openingStore.UpdateLsps1OrderFunding(order.OrderId, point.Hash.String(), int(point.Index))
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to record reconciled funding details")
		return false
	}
	if !claimed {
		log.Warn().Str("order_id", order.OrderId).Str("channel_point", point.String()).Msg("LSPS1 Settlement: Channel point already claimed by another order while reconciling, cannot use it for this order")
		return false
	}
	updated, err := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStateCompleted), string(Lsps1OrderStatePaid), string(Lsps1OrderStatePaid))
	if err != nil {
		log.Error().Err(err).Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Failed to update reconciled order state to COMPLETED")
		return false
	}
	if !updated {
		// H-5: a concurrent writer already moved this order out of PAID --
		// funding is still recorded above (harmless/idempotent), but don't
		// send a COMPLETED notification for a transition that didn't happen.
		log.Info().Str("order_id", order.OrderId).Msg("LSPS1 Settlement: Order already transitioned out of PAID by a concurrent writer, skipping reconciliation notification")
		return true
	}

	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStateCompleted), point.String(), "")
	}
	return true
}

// claimedChannelPoints returns the set of channel points (as
// wire.OutPoint.String()'s "hash:index" form) already recorded as funding
// for some Lsps1Order, so reconcileUnclaimedChannel never mis-attributes a
// channel that's already accounted for.
func (s *Lsps1SettlementService) claimedChannelPoints() (map[string]bool, error) {
	claimed := make(map[string]bool)
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		orders, err := s.openingStore.ListLsps1Orders(pageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, o := range orders {
			if o.FundingTxid != nil && o.FundingOutpoint != nil {
				claimed[fmt.Sprintf("%s:%d", *o.FundingTxid, *o.FundingOutpoint)] = true
			}
		}
		if len(orders) < pageSize {
			break
		}
	}
	return claimed, nil
}

// recoverSettledCreatedOrders queries LND for any CREATED order whose invoice
// is already SETTLED, then processes it as if the settlement event just arrived.
func (s *Lsps1SettlementService) recoverSettledCreatedOrders() {
	createdOrders, err := s.openingStore.GetAllCreatedLsps1Orders()
	if err != nil {
		log.Error().Err(err).Msg("LSPS1 Settlement: Failed to retrieve CREATED orders for LND check")
		return
	}
	if len(createdOrders) == 0 {
		return
	}

	log.Debug().
		Str("component", string(logging.LogComponentSettlement)).
		Int("count", len(createdOrders)).
		Msg("LSPS1 Settlement: Checking CREATED orders against LND for missed settlements")

	for _, order := range createdOrders {
		if order.PaymentInvoice == "" {
			continue
		}

		// H-11: a hold-invoice order's payment hash is already derivable
		// locally from its stored preimage -- skip the DecodePayReq
		// round-trip LookupInvoiceState needs for a legacy order with no
		// stored preimage. The nil check comes first (silent, cheap) so a
		// legacy order without a preimage -- the normal, expected case for
		// that flow -- doesn't trigger paymentHashForOrder's error-level
		// logging on every poll cycle.
		var state lightning.InvoiceState
		var err error
		if order.PaymentPreimage != nil {
			var hash []byte
			var ok bool
			hash, ok = s.paymentHashForOrder(order)
			if !ok {
				continue
			}
			state, err = s.client.LookupInvoiceStateByHash(s.ctx, hash)
		} else {
			state, err = s.client.LookupInvoiceState(s.ctx, order.PaymentInvoice)
		}
		if err != nil {
			log.Warn().Err(err).
				Str("order_id", order.OrderId).
				Msg("LSPS1 Settlement: Could not look up invoice state in LND, skipping")
			continue
		}

		stateStr := "OPEN"
		switch state {
		case lightning.InvoiceStateSettled:
			stateStr = "SETTLED"
		case lightning.InvoiceStateCanceled:
			stateStr = "CANCELED"
		case lightning.InvoiceStateAccepted:
			stateStr = "ACCEPTED"
		}

		log.Info().
			Str("component", string(logging.LogComponentSettlement)).
			Str("order_id", order.OrderId).
			Str("invoice_state", stateStr).
			Int64("order_total_loki", order.OrderTotalLoki).
			Msg("LSPS1 Settlement: CREATED order invoice state")

		switch state {
		case lightning.InvoiceStateSettled:
			log.Info().
				Str("component", string(logging.LogComponentSettlement)).
				Str("order_id", order.OrderId).
				Str("peer_id", order.PeerId).
				Msg("LSPS1 Settlement: Found CREATED order with settled invoice — recovering")
			s.processSettledOrder(order)
		case lightning.InvoiceStateAccepted:
			log.Info().
				Str("component", string(logging.LogComponentSettlement)).
				Str("order_id", order.OrderId).
				Str("peer_id", order.PeerId).
				Msg("LSPS1 Settlement: Found CREATED order with accepted (held) invoice — recovering")
			s.processAcceptedOrder(order)
		case lightning.InvoiceStateCanceled:
			log.Warn().
				Str("component", string(logging.LogComponentSettlement)).
				Str("order_id", order.OrderId).
				Msg("LSPS1 Settlement: Invoice is CANCELED — order cannot be fulfilled")
		default:
			// Still OPEN: this order predates the current process (no
			// watcher is running for it yet, since WatchOrder is only
			// called at order-creation time in handleLsps1CreateOrder).
			// Attach one now so it gets live ACCEPTED coverage going
			// forward instead of depending on the safety-net poll for its
			// whole remaining lifetime (H-12).
			s.WatchOrder(order)
		}
	}
}

// processSettledOrder is the DB-update + channel-open logic factored out of
// processSettledInvoice so that recoverSettledCreatedOrders can reuse it
// without needing the full InvoiceUpdate.
func (s *Lsps1SettlementService) processSettledOrder(order *core.Lsps1Order) {
	// Compare-and-set on CREATED, mirroring processSettledInvoice: this is
	// the other side of the F-1 race, where the live SubscribeInvoices
	// event and this startup recovery sweep can both observe the same
	// order as CREATED. Only the winner proceeds to open a channel.
	updated, err := s.openingStore.UpdateLsps1OrderState(order.OrderId, string(Lsps1OrderStatePaid), string(Lsps1OrderStatePaid), string(Lsps1OrderStateCreated))
	if err != nil {
		log.Error().Err(err).
			Str("order_id", order.OrderId).
			Msg("LSPS1 Settlement: Failed to update order state to PAID during recovery")
		return
	}
	if !updated {
		log.Info().
			Str("order_id", order.OrderId).
			Msg("LSPS1 Settlement: Order already transitioned out of CREATED by a concurrent writer, skipping recovery")
		return
	}

	log.Info().
		Str("component", string(logging.LogComponentSettlement)).
		Str("order_id", order.OrderId).
		Str("peer_id", order.PeerId).
		Msg("LSPS1 Settlement: Order marked as PAID (recovery)")

	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(order.PeerId, order.OrderId, string(Lsps1OrderStatePaid), "", "")
	}

	s.openChannelForOrder(order)
}
