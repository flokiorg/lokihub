package interceptor

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/chain"
	"github.com/flokiorg/lokihub/lspd/common"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/notifications"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
)

// lsps1JitTag/lsps2JitTag mark a payments row as belonging to a
// JIT-triggered channel open, persisted via RegisterPayment's tag column
// and read back by PaymentInfo. resolvePayment uses whichever of these
// comes back to re-derive isJit/isLsps2Registration on EVERY call, not
// only the one that first registered the payment -- see the comment
// where they're consumed, below.
const (
	lsps1JitTag = "lsps1.v1"
	lsps2JitTag = "lsps2.jit"
)

type Interceptor struct {
	nodeid []byte
	client lightning.Client
	// config is an atomic ref, not a raw pointer, so it can be hot-swapped
	// by the dashboard's config-update handler without racing the HTLC
	// hot path reading it here (C-3). Callers should Load() it once per
	// logical operation for an internally consistent snapshot.
	config              *config.NodeConfigRef
	store               InterceptStore
	historyStore        history.Store
	openingService      ports.OpeningService
	feeEstimator        chain.FeeEstimator
	feeStrategy         chain.FeeStrategy
	payHashGroup        singleflight.Group
	notificationService notifications.Notifier
	nodePrivKey         interface{} // *btcec.PrivateKey for LSPS5 signing
}

func NewInterceptHandler(
	client lightning.Client,
	config *config.NodeConfigRef,
	store InterceptStore,
	historyStore history.Store,
	openingService ports.OpeningService,
	feeEstimator chain.FeeEstimator,
	feeStrategy chain.FeeStrategy,
	notificationService notifications.Notifier,
	nodePrivKey interface{},
) *Interceptor {
	nodeid, _ := hex.DecodeString(config.Load().NodePubkey)
	return &Interceptor{
		nodeid:              nodeid,
		client:              client,
		config:              config,
		store:               store,
		historyStore:        historyStore,
		openingService:      openingService,
		feeEstimator:        feeEstimator,
		feeStrategy:         feeStrategy,
		notificationService: notificationService,
		nodePrivKey:         nodePrivKey,
	}
}

// interceptOutcome carries a singleflight-shared InterceptResult out of
// Intercept's payHashGroup.Do closure, alongside the two payment-level
// totals (outgoingAmountMloki/incomingAmountMloki) needed to compute a
// PER-CALLER forward amount afterward (Phase 7, F-23). Before this existed,
// the closure computed one `amt` from whichever HTLC's own req happened to
// win the singleflight race, then singleflight.Do returned that SAME
// InterceptResult (including AmountMloki) to every other concurrent HTLC
// sharing the payment hash -- correct for the winner, silently wrong for
// every other genuinely-concurrent MPP part or replay, which would be told
// to forward the winner's amount instead of its own.
//
// Residual, intentionally out-of-scope limitation: incomingAmountMloki
// here is still whichever HTLC's req happened to first resolve the
// payment (resolvePayment itself stays inside the shared closure, since
// most of what it does -- CLTV/expiry checks, connectivity, the DB claim
// itself -- is correctly a once-per-payment decision, not a once-per-HTLC
// one). True proportional multi-part splitting would need a real
// total-payment-amount field plumbed from LND's own HTLC intercept event
// through core.InterceptRequest, which doesn't exist today -- this fix
// closes the "every caller gets an identical, wrong, copied amount" defect
// without attempting that larger, separate change.
type interceptOutcome struct {
	result              core.InterceptResult
	outgoingAmountMloki int64
	incomingAmountMloki int64
}

func (i *Interceptor) Intercept(req core.InterceptRequest) core.InterceptResult {
	reqPaymentHashStr := hex.EncodeToString(req.PaymentHash)
	log.Debug().Msgf("Intercept: scid=%s paymentHash=%s outgoingAmount=%v outgoingExpiry=%v incomingExpiry=%v", req.Scid.ToString(), reqPaymentHashStr, req.OutgoingAmountMloki, req.OutgoingExpiry, req.IncomingExpiry)
	log.Trace().Msgf("Checking singleflight for %s", reqPaymentHashStr)
	resp, _, _ := i.payHashGroup.Do(reqPaymentHashStr, func() (interface{}, error) {
		// Snapshot once so this payment's processing sees one internally
		// consistent config even if the dashboard updates it concurrently
		// mid-flight (C-3).
		cfg := i.config.Load()
		data, err := i.resolvePayment(req)
		if err != nil {
			log.Error().Msgf("resolvePayment(%x) error: %v", req.PaymentHash, err)
			return interceptOutcome{result: core.InterceptResult{
				Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
				FailureCode: core.FAILURE_TEMPORARY_NODE_FAILURE,
			}}, nil
		}

		params := data.params
		paymentHash := data.paymentHash
		paymentSecret := data.paymentSecret
		destination := data.destination
		incomingAmountMloki := data.incomingAmountMloki
		outgoingAmountMloki := data.outgoingAmountMloki
		channelPoint := data.channelPoint
		tag := data.tag

		isRegistered := paymentSecret != nil
		// Sanity check. If the payment is registered, the destination is always set.
		if isRegistered && (destination == nil || len(destination) != 33) {
			log.Error().Msgf("ERROR: Payment was registered without destination. paymentHash: %s", reqPaymentHashStr)
		}

		isProbe := isRegistered && !bytes.Equal(paymentHash, req.PaymentHash)
		nextHop, err := i.client.GetPeerId(&req.Scid)
		if err != nil {
			log.Error().Msgf("GetPeerId(%s) error: %v", req.Scid.ToString(), err)
			return interceptOutcome{result: core.InterceptResult{
				Action: core.INTERCEPT_RESUME,
			}}, nil
		}

		// If the payment was registered, but the next hop is not the destination
		// that means we are not the last hop of the payment, so we'll just forward.
		if isRegistered && nextHop != nil && !bytes.Equal(nextHop, destination) {
			log.Debug().Msgf("Intercept: paymentHash=%s routing to nextHop=%s (destination=%s)", reqPaymentHashStr, hex.EncodeToString(nextHop), hex.EncodeToString(destination))
			return interceptOutcome{result: core.InterceptResult{
				Action: core.INTERCEPT_RESUME,
			}}, nil
		}

		// nextHop is set if the sender's scid corresponds to a known channel.
		// destination is set if the payment was registered for a channel open.
		// The 'actual' next hop will be either of those.
		if nextHop == nil {

			// If the next hop cannot be deduced from the scid, and the payment
			// is not registered, there's nothing left to be done. Just continue.
			if !isRegistered {
				log.Debug().Msgf("Intercept: paymentHash=%s skipping unregistered payment (nextHop unknown)", reqPaymentHashStr)
				return interceptOutcome{result: core.InterceptResult{
					Action: core.INTERCEPT_RESUME,
				}}, nil
			}

			// The payment was registered, so the next hop is the registered
			// destination
			nextHop = destination
		}

		isConnected, err := i.client.IsConnected(nextHop)
		if err != nil {
			log.Error().Msgf("IsConnected(%x) error: %v", nextHop, err)
			return interceptOutcome{result: core.InterceptResult{
				Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
				FailureCode: core.FAILURE_TEMPORARY_CHANNEL_FAILURE,
			}}, nil
		}

		if isProbe {
			// If this is a known probe, we'll quit early for non-connected clients.
			if !isConnected {
				log.Debug().Msgf("Intercept: paymentHash=%s probe detected but peer not connected", reqPaymentHashStr)
				return interceptOutcome{result: core.InterceptResult{
					Action: core.INTERCEPT_RESUME,
				}}, nil
			}

			// If it's a probe, they are connected, but need a channel, we'll return
			// INCORRECT_OR_UNKNOWN_PAYMENT_DETAILS. This is an out-of-spec
			// error code, because it shouldn't be returned by an intermediate
			// node. But senders implementnig the probing-01: prefix should
			// know that the actual payment would probably succeed.
			if channelPoint == nil {
				log.Debug().Msgf("Intercept: paymentHash=%s probe detected, no channel exists", reqPaymentHashStr)
				return interceptOutcome{result: core.InterceptResult{
					Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
					FailureCode: core.FAILURE_INCORRECT_OR_UNKNOWN_PAYMENT_DETAILS,
				}}, nil
			}
		}

		if isConnected {
			// If the peer is connected, notify anyway, but don't wait for
			// the result (P-4): this is a courtesy push, nothing here
			// depends on whether it actually reached the client, so
			// NotifyAsync's durable-enqueue-and-return skips the up to
			// 7s-per-webhook synchronous delivery attempt Notify does.
			// Still backgrounded via go: Enqueue is a blocking DB write,
			// and there's no reason for even that to sit inline on the
			// interception path.
			go i.notificationService.NotifyAsync(
				hex.EncodeToString(nextHop),
				reqPaymentHashStr,
				i.nodePrivKey,
			)
		} else {
			// Make sure the client is connected by potentially notifying them to come online.
			notifyResult := i.notifyAndWait(reqPaymentHashStr, nextHop, isRegistered)
			if notifyResult != nil {
				log.Debug().Msgf("Intercept: paymentHash=%s peer not notified or did not come online, continuing", reqPaymentHashStr)
				// return *notifyResult, nil
			}
		}

		// The peer is online, we can resume the htlc if it's not a channel open.
		if !isRegistered {
			return interceptOutcome{result: core.InterceptResult{
				Action: core.INTERCEPT_RESUME,
			}}, nil
		}

		// Tracked so the routing policy (in particular TimeLockDelta, see
		// UpdateChannelPolicy's doc comment on lightning.Client) is applied
		// exactly once per channel, right after it's confirmed active below
		// -- not on every subsequent HTLC/retry that reuses an
		// already-open channelPoint.
		justOpened := channelPoint == nil

		// The first htlc of a MPP will open the channel.
		if channelPoint == nil {
			// TODO: When opening_fee_params is enforced, turn this check in a temporary channel failure.
			if params == nil {
				validity := time.Hour * 24
				if cfg.JitValidUntilSeconds > 0 {
					validity = time.Duration(cfg.JitValidUntilSeconds) * time.Second
				}
				maxDelay := uint32(10000)
				if cfg.JitMaxClientToSelfDelay > 0 {
					maxDelay = cfg.JitMaxClientToSelfDelay
				}

				log.Warn().Msgf("Intercept: DEPRECATED fee mechanism used, applying defaults. paymentHash=%s", reqPaymentHashStr)
				params = &core.OpeningFeeParams{
					MinFeeMloki:          uint64(cfg.ChannelMinimumFeeMloki),
					Proportional:         uint32(cfg.ChannelFeePermyriad * 100),
					ValidUntil:           time.Now().UTC().Add(validity).Format(common.TimeFormat),
					MinLifetime:          uint32(cfg.MaxInactiveDuration / 60), // seconds -> blocks (1 min/block)
					MaxClientToSelfDelay: maxDelay,
				}
			}

			// Make sure the cltv delta is enough.
			if int64(req.IncomingExpiry)-int64(req.OutgoingExpiry) < int64(cfg.TimeLockDelta) {
				log.Debug().Msgf("Intercept: paymentHash=%s CLTV insufficient, outgoingExpiry=%v incomingExpiry=%v timeLockDelta=%v", reqPaymentHashStr, req.OutgoingExpiry, req.IncomingExpiry, cfg.TimeLockDelta)
				return interceptOutcome{result: core.InterceptResult{
					Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
					FailureCode: core.FAILURE_TEMPORARY_CHANNEL_FAILURE,
				}}, nil
			}

			validUntil, err := time.Parse(common.TimeFormat, params.ValidUntil)
			if err != nil {
				log.Error().Msgf("paymentHash: %s, time.Parse(%s, %s) failed. Failing channel open: %v", reqPaymentHashStr, common.TimeFormat, params.ValidUntil, err)
				if data.orderId != "" {
					go i.notificationService.NotifyOrderStateChanged(hex.EncodeToString(nextHop), data.orderId, "FAILED", "", fmt.Sprintf("invalid valid_until format: %v", err))
				}
				return interceptOutcome{result: core.InterceptResult{
					Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
					FailureCode: core.FAILURE_TEMPORARY_CHANNEL_FAILURE,
				}}, nil
			}

			// Make sure the opening_fee_params are not expired.
			// If they are expired, but the current chain fee is fine, open channel anyway.
			if time.Now().UTC().After(validUntil) {
				// F-18: ask the same live fee estimator openChannel itself
				// uses, not a config-vs-config comparison that was nearly
				// always true regardless of real fee movement.
				var currentFeeratePerVByte *float64
				if feeEst, feeErr := i.feeEstimator.EstimateFeeRate(context.Background(), i.feeStrategy); feeErr == nil && feeEst != nil {
					currentFeeratePerVByte = feeEst.LokiPerVByte
				} else {
					log.Warn().Err(feeErr).Msgf("Intercept: paymentHash=%s failed to estimate current chain fee for expired-quote check", reqPaymentHashStr)
				}
				if !i.openingService.IsCurrentChainFeeCheaper(currentFeeratePerVByte, cfg.ReferenceFeeratePerVByte) {
					log.Warn().Msgf("Intercept: Expired payment registration, failing. paymentHash=%s validUntil=%s", reqPaymentHashStr, params.ValidUntil)
					if data.orderId != "" {
						go i.notificationService.NotifyOrderStateChanged(hex.EncodeToString(nextHop), data.orderId, "FAILED", "", "payment registration expired")
					}
					return interceptOutcome{result: core.InterceptResult{
						Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
						FailureCode: core.FAILURE_TEMPORARY_CHANNEL_FAILURE,
					}}, nil
				}

				log.Info().Msgf("Intercept: Expired registration but current fee is cheaper, proceeding. paymentHash=%s", reqPaymentHashStr)
			}

			// Phase 7 (F-15/F-22, T7-2): claim the registration BEFORE
			// calling openChannel, not after -- the actual concurrency
			// gate. A second, distinct-payment-hash HTLC (a sequential
			// replay once the first channel already exists, or a genuinely
			// concurrent race) must not reach openChannel a second time for
			// the same registration. Postgres row-level locking makes
			// exactly one concurrent ClaimLsps2Registration call win,
			// deterministically.
			if data.isLsps2Registration {
				claimed, claimErr := i.store.ClaimLsps2Registration(data.lsps2Scid)
				if claimErr != nil {
					log.Error().Err(claimErr).Msgf("Intercept: paymentHash=%s failed to claim LSPS2 registration scid=%d", reqPaymentHashStr, data.lsps2Scid)
					return interceptOutcome{result: core.InterceptResult{
						Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
						FailureCode: core.FAILURE_TEMPORARY_CHANNEL_FAILURE,
					}}, nil
				}
				if !claimed {
					// Another HTLC already claimed, consumed, or the
					// registration expired/was never ACTIVE -- do not open
					// a second channel for it. There is no real channel
					// yet for this HTLC to resume onto, so fail it (not
					// INTERCEPT_RESUME): once the winner's channel is
					// active, a retry's PaymentInfo(paymentHash) lookup
					// (or GetPeerId resolving the now-real channel)
					// resolves it directly.
					log.Warn().Msgf("Intercept: paymentHash=%s scid=%d LSPS2 registration already claimed/consumed/expired, refusing to open a second channel (F-15)", reqPaymentHashStr, data.lsps2Scid)
					return interceptOutcome{result: core.InterceptResult{
						Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
						FailureCode: core.FAILURE_TEMPORARY_CHANNEL_FAILURE,
					}}, nil
				}
			}

			channelPoint, err = i.openChannel(req.PaymentHash, destination, incomingAmountMloki, outgoingAmountMloki, tag, data.isJit)
			if err != nil && channelPoint == nil {
				// Clean failure: no channel was opened at all (the
				// on-chain OpenChannel call itself failed), so no capital
				// was committed. Safe to release the registration for a
				// later retry.
				log.Error().Msgf("paymentHash: %s, openChannel(%x, %v) err: %v", reqPaymentHashStr, destination, incomingAmountMloki, err)
				if data.isLsps2Registration {
					if relErr := i.store.ReleaseLsps2Registration(data.lsps2Scid); relErr != nil {
						log.Error().Err(relErr).Msgf("Intercept: paymentHash=%s failed to release LSPS2 registration scid=%d after openChannel failure", reqPaymentHashStr, data.lsps2Scid)
					}
				}
				if data.orderId != "" {
					go i.notificationService.NotifyOrderStateChanged(hex.EncodeToString(nextHop), data.orderId, "FAILED", "", fmt.Sprintf("failed to open channel: %v", err))
				}
				return interceptOutcome{result: core.InterceptResult{
					Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
					FailureCode: core.FAILURE_TEMPORARY_CHANNEL_FAILURE,
				}}, nil
			}
			if err != nil {
				// Partial success: openChannel's on-chain OpenChannel call
				// succeeded (channelPoint is real, capital is already
				// committed) but a later step inside it -- persisting
				// funding_tx_id via SetFundingTx -- failed. This must NOT
				// be treated the same as a clean failure: releasing the
				// registration here would let a later HTLC claim it and
				// open a SECOND real channel for capital that's already
				// spent. Log loudly for operator visibility and fall
				// through into the exact same success path below (Consume
				// + WaitChannelPointActive etc.), using the channelPoint
				// already in hand -- T7-13's crash-recovery sweep is the
				// backstop if payments.funding_tx_id itself never gets
				// backfilled.
				log.Error().Err(err).Msgf("Intercept: paymentHash=%s channel %s opened successfully but failed to persist the funding record; treating as consumed, not failed", reqPaymentHashStr, channelPoint.String())
			}

			if data.isLsps2Registration {
				// Non-fatal if this fails: the channel already opened
				// successfully and real capital is already committed --
				// failing the HTLC now would be strictly worse. Log and
				// continue; T7-13's crash-recovery sweep is the backstop
				// for a registration left at OPENING.
				feeMloki := uint64(0)
				if incomingAmountMloki > outgoingAmountMloki {
					feeMloki = uint64(incomingAmountMloki - outgoingAmountMloki)
				}
				if _, consumeErr := i.store.ConsumeLsps2Registration(data.lsps2Scid, channelPoint, uint64(incomingAmountMloki), feeMloki); consumeErr != nil {
					log.Error().Err(consumeErr).Msgf("Intercept: paymentHash=%s failed to consume LSPS2 registration scid=%d after successful channel open", reqPaymentHashStr, data.lsps2Scid)
				}
			}
		}

		// Scale the forwarding amount proportionally for MPP parts. This
		// value is only used for the history-store record below (about the
		// specific triggering HTLC) -- the InterceptResult actually
		// returned to EACH caller is recomputed fresh outside the shared
		// closure, per F-23 (see scaleForwardAmountMloki's doc comment).
		amt := scaleForwardAmountMloki(outgoingAmountMloki, incomingAmountMloki, int64(req.IncomingAmountMloki))

		deadline := time.Now().Add(60 * time.Second)

		// failChannelOpenTimeout is the shared failure path for both ways
		// this can fail to produce a usable scid: WaitChannelPointActive
		// itself timing out, or (rare) the channel becoming active with
		// no scid assigned at all.
		failChannelOpenTimeout := func(reason string) core.InterceptResult {
			log.Warn().Msgf("Intercept: Channel open timed out. paymentHash=%s (%s)", reqPaymentHashStr, reason)
			if data.orderId != "" {
				go i.notificationService.NotifyOrderStateChanged(hex.EncodeToString(nextHop), data.orderId, "FAILED", "", "channel open timed out")
			}
			return core.InterceptResult{
				Action:      core.INTERCEPT_FAIL_HTLC_WITH_CODE,
				FailureCode: core.FAILURE_TEMPORARY_CHANNEL_FAILURE,
			}
		}

		// P-1: event-driven wait (WaitChannelPointActive, backed by
		// listenChannelEvents' existing subscription) instead of polling
		// GetChannel on a fixed 1s timer.
		chanResult, err := i.client.WaitChannelPointActive(destination, *channelPoint, deadline)
		if err != nil {
			log.Debug().Msgf("Intercept: paymentHash=%s waiting for channel to open for peer %x: %v", reqPaymentHashStr, destination, err)
			return interceptOutcome{result: failChannelOpenTimeout(err.Error())}, nil
		}

		{
			log.Info().Msgf("paymentHash: %s, channel opened successfully alias: '%v', confirmed: '%v'", reqPaymentHashStr, chanResult.AliasScid.ToString(), chanResult.ConfirmedScid.ToString())

			var scid *lightning.ShortChannelID
			if chanResult.ConfirmedScid == nil {
				if chanResult.AliasScid == nil {
					log.Error().Msgf("Error: WaitChannelPointActive: Both confirmed scid and alias scid are nil: %+v", chanResult)
					return interceptOutcome{result: failChannelOpenTimeout("channel became active with no scid assigned")}, nil
				}
				scid = chanResult.AliasScid
			} else {
				scid = chanResult.ConfirmedScid
			}

			var targetScid uint64
			if scid != nil {
				targetScid = uint64(*scid)
			}

			// Routing policy (in particular TimeLockDelta) is applied and
			// verified inside RouteLoop below, not just once here --
			// UpdateChannelPolicy's own success return isn't proof the
			// change actually stuck (see its doc comment on
			// lightning.Client), and a single unverified call can silently
			// no-op: confirmed live 2026-07-16, a channel whose
			// UpdateChannelPolicy call returned no error still showed
			// time_lock_delta=800 (flnd's own devnet-wide default) in its
			// own graph record minutes later, with every payer attempt
			// failing IncorrectCltvExpiry (expected >=800, configured 40)
			// the whole time. RouteLoop retries the call on every tick
			// until IsChannelPolicyApplied confirms it, rather than
			// assuming one attempt is enough.
			routingPolicy := routingPolicyFor(cfg, data.isJit)

			// Give LND a moment to propagate the new channel state to the
			// router before resuming, to avoid "no_route" errors -- even
			// with OutgoingRequestedChanId, the switch link might not be
			// fully active immediately.
			//
			// Poll IsChannelRoutable (GetChanInfo), not QueryRoutes.
			// Root-caused live (2026-07-16): for a just-opened zero-conf
			// channel, QueryRoutes' pathfinding reads from flnd's in-memory
			// graph cache, which can lag 60-90+s behind the persisted graph
			// DB -- confirmed directly via GetChanInfo showing both edge
			// policies present within the same second the channel
			// activates, on every connection/flnd-branch/devnet-state
			// combination tested (ruling out flspd's own connection
			// management as the cause). Upstream LND's own zero-conf
			// channel itest (testZeroConfChannelOpen) never calls
			// QueryRoutes for this readiness check either -- it polls
			// DescribeGraph for both directional policies, which (like
			// GetChanInfo) reads straight from the graph DB rather than the
			// pathfinding cache. See BACKLOG.md's LSPS2 zero-conf
			// balance-sync race writeup for the full investigation.
			routeTimeout := time.After(60 * time.Second)
			routeTicker := time.NewTicker(500 * time.Millisecond)
			defer routeTicker.Stop()

		RouteLoop:
			for {
				routable, err := i.client.IsChannelRoutable(targetScid)
				policyApplied := true
				if justOpened {
					policyApplied, err = i.client.IsChannelPolicyApplied(targetScid, destination, routingPolicy.TimeLockDelta)
					if err == nil && !policyApplied {
						if uerr := i.client.UpdateChannelPolicy(*channelPoint, routingPolicy); uerr != nil {
							log.Warn().Err(uerr).Msgf("Intercept: paymentHash=%s failed to apply channel policy", reqPaymentHashStr)
						}
					}
				}
				switch {
				case err != nil:
					log.Trace().Err(err).Msgf("Intercept: IsChannelRoutable/IsChannelPolicyApplied failed (waiting for sync), paymentHash=%s", reqPaymentHashStr)
				case routable && policyApplied:
					log.Debug().Msgf("Intercept: paymentHash=%s channel %d routable, resuming", reqPaymentHashStr, targetScid)
					break RouteLoop
				}

				select {
				case <-routeTimeout:
					log.Warn().Msgf("Intercept: IsChannelRoutable timed out using Best Effort for paymentHash=%s, resuming anyway", reqPaymentHashStr)
					break RouteLoop
				case <-routeTicker.C:
					continue
				}
			}

			err = i.historyStore.UpdateChannels(context.TODO(), []*history.ChannelUpdate{{
				NodeID:        i.nodeid,
				PeerId:        destination,
				AliasScid:     chanResult.AliasScid,
				ConfirmedScid: chanResult.ConfirmedScid,
				ChannelPoint:  channelPoint,
				LastUpdate:    time.Now(),
			}})
			if err != nil {
				// Don't break here, this is not critical.
				log.Error().Msgf("paymentHash: %s, failed to insert newly opened channel in history store. %v: %v", reqPaymentHashStr, channelPoint.String(), err)
			}

			err = i.historyStore.AddOpenChannelHtlc(context.TODO(), &history.OpenChannelHtlc{
				NodeId:              i.nodeid,
				PeerId:              destination,
				ChannelPoint:        channelPoint,
				OriginalAmountMloki: req.OutgoingAmountMloki,
				ForwardAmountMloki:  uint64(amt),
				IncomingAmountMloki: req.IncomingAmountMloki,
				ForwardTime:         time.Now(),
			})
			if err != nil {
				// Don't break here, this is not critical.
				log.Error().Msgf(
					"paymentHash: %s, failed to insert htlc used for channel open in history store: channel: %v,"+
						" original amount: %v, forward amount: %v, error: %v",
					reqPaymentHashStr,
					channelPoint.String(),
					req.OutgoingAmountMloki,
					amt,
					err,
				)
			}

			if data.orderId != "" {
				go i.notificationService.NotifyOrderStateChanged(
					hex.EncodeToString(destination),
					data.orderId,
					"COMPLETED",
					channelPoint.String(),
					"",
				)
			}

			return interceptOutcome{
				result: core.InterceptResult{
					Action:           core.INTERCEPT_RESUME_WITH_ONION,
					Destination:      destination,
					ChannelPoint:     channelPoint,
					Scid:             *scid,
					PaymentSecret:    paymentSecret,
					AmountMloki:      uint64(amt), // recomputed per-caller below (F-23); this is only the triggering HTLC's own value, used for the history-store record above.
					TotalAmountMloki: uint64(outgoingAmountMloki),
				},
				outgoingAmountMloki: outgoingAmountMloki,
				incomingAmountMloki: incomingAmountMloki,
			}, nil
		}
	})

	outcome := resp.(interceptOutcome)
	result := outcome.result

	// F-23: recompute AmountMloki here, OUTSIDE the singleflight closure,
	// using THIS call's own req -- not whichever HTLC happened to win the
	// race above. Before this fix, every concurrent HTLC sharing a payment
	// hash received the exact same (winner's) AmountMloki, silently wrong
	// for every part but the winner.
	if result.Action == core.INTERCEPT_RESUME_WITH_ONION {
		result.AmountMloki = uint64(scaleForwardAmountMloki(outcome.outgoingAmountMloki, outcome.incomingAmountMloki, int64(req.IncomingAmountMloki)))
	}
	return result
}

// scaleForwardAmountMloki computes one HTLC's proportional share of the
// total outgoing amount: for a single-part payment, reqIncomingAmountMloki
// == incomingAmountMloki, so this returns outgoingAmountMloki unchanged;
// for a multi-part (MPP) payment, each part's own reqIncomingAmountMloki
// scales its share of outgoingAmountMloki accordingly. Deliberately a pure
// function of its three arguments only (Phase 7, F-23) -- Intercept calls
// it fresh, per caller, with THAT caller's own req, outside the
// payHashGroup singleflight closure that only runs once per shared
// payment hash; before this fix, every concurrent HTLC sharing a payment
// hash received the exact same computed amount, correct only for whichever
// one happened to win the race.
//
// Residual limitation, intentionally out of scope here: incomingAmountMloki
// is still whichever HTLC's own request first resolved the payment inside
// the shared closure, not a true payment-level total independent of any
// single part -- see interceptOutcome's doc comment for why fixing that
// fully would need a new field plumbed from LND's own HTLC intercept event.
func scaleForwardAmountMloki(outgoingAmountMloki, incomingAmountMloki, reqIncomingAmountMloki int64) int64 {
	if incomingAmountMloki == 0 {
		return 0
	}
	var bigProd, bigAmt big.Int
	return bigAmt.Div(bigProd.Mul(big.NewInt(outgoingAmountMloki), big.NewInt(reqIncomingAmountMloki)), big.NewInt(incomingAmountMloki)).Int64()
}

func (i *Interceptor) notifyAndWait(reqPaymentHashStr string, nextHop []byte, isRegistered bool) *core.InterceptResult {
	// If not connected, send a notification to the registered
	// notification service for this client if available.
	notified, err := i.notificationService.Notify(
		hex.EncodeToString(nextHop),
		reqPaymentHashStr,
		i.nodePrivKey,
	)

	// If this errors or the client is not notified, the client
	// is offline or unknown. We'll resume the HTLC (which will
	// result in UNKOWN_NEXT_PEER)
	if err != nil || !notified {
		return &core.InterceptResult{
			Action: core.INTERCEPT_RESUME,
		}
	}

	log.Debug().Msgf("Intercept: paymentHash=%s notified peer %x of pending HTLC", reqPaymentHashStr, nextHop)
	d, err := time.ParseDuration(i.config.Load().NotificationTimeout)
	if err != nil {
		log.Warn().Msg("Intercept: NotificationTimeout not set, using default 1m")
		d = time.Minute
	}
	timeout := time.Now().Add(d)

	// Wait for a while to allow the client to come online.
	err = i.client.WaitOnline(nextHop, timeout)

	// If there's an error waiting, resume the htlc. It will
	// probably fail with UNKNOWN_NEXT_PEER.
	if err != nil {
		log.Debug().Msgf(
			"Intercept: paymentHash=%s waiting for peer %x to come online failed: %v",
			reqPaymentHashStr,
			nextHop,
			err,
		)
		return &core.InterceptResult{
			Action: core.INTERCEPT_RESUME,
		}
	}

	log.Info().Msgf("paymentHash %s, Peer %x is back online. Continue htlc.", reqPaymentHashStr, nextHop)
	// At this point we know a few things.
	// - This is either a channel partner or a registered payment
	// - they were offline
	// - They got notified about the htlc
	// - They came back online
	// So if this payment was not registered, this is a channel
	// partner and we have to wait for the channel to become active
	// before we can forward.
	if !isRegistered {
		err = i.client.WaitChannelActive(nextHop, timeout)
		if err != nil {
			log.Debug().Msgf(
				"Intercept: paymentHash=%s waiting for channel with %x to become active failed: %v",
				reqPaymentHashStr,
				nextHop,
				err,
			)
			return &core.InterceptResult{
				Action: core.INTERCEPT_RESUME,
			}
		}
	}

	return nil
}

func (i *Interceptor) openChannel(paymentHash, destination []byte, incomingAmountMloki, outgoingAmountMloki int64, tag *string, isJit bool) (*wire.OutPoint, error) {
	// Snapshot once so this channel-open decision is made against one
	// internally consistent config even if the dashboard updates it
	// concurrently mid-flight (C-3).
	cfg := i.config.Load()

	// RequiredCapacityLoki (Phase 7, F-24) is the single shared computation
	// of real committed capacity -- also used by the LSPS1/LSPS2 handlers'
	// liquidity checks and reservation bookkeeping, so this function and
	// the ledger can never silently drift apart again. It folds in the
	// BOLT2 ~1% reserve top-up (see its own doc comment for why that term
	// exists -- root-caused 2026-07-16 from a live FAILURE_REASON_NO_ROUTE)
	// but deliberately does not clamp to the floors checked just below.
	capacity := int64(config.RequiredCapacityLoki(uint64(incomingAmountMloki/1000), uint64(outgoingAmountMloki/1000), cfg, isJit))
	if capacity == cfg.PublicChannelAmount {
		capacity++
	}

	fee, err := i.feeEstimator.EstimateFeeRate(
		context.Background(),
		i.feeStrategy,
	)
	if err != nil || fee == nil {
		log.Error().Msgf("Error estimating chain fee, fallback to target conf: %v", err)
		fee = &chain.FeeEstimation{
			TargetConf: &cfg.TargetConf,
		}
	}

	// Enforce Minimum Channel Capacity (Physical Floor) and, for JIT, the
	// Business Floor -- config.MeetsCapacityFloors is the same predicate
	// the LSPS1/LSPS2 handlers validate against before ever reaching here.
	// The business floor only applies when isJit AND JitEnabled, matching
	// this function's original gating exactly (isJit alone can be true
	// with JitEnabled since toggled off mid-flight, C-3-style).
	enforceJitFloor := isJit && cfg.JitEnabled
	if !config.MeetsCapacityFloors(uint64(capacity), cfg, enforceJitFloor) {
		if cfg.JitMinChannelBalanceLoki != nil && enforceJitFloor && uint64(capacity) < *cfg.JitMinChannelBalanceLoki {
			return nil, fmt.Errorf("channel capacity %d is smaller than business minimum %d", capacity, *cfg.JitMinChannelBalanceLoki)
		}
		return nil, fmt.Errorf("channel capacity %d is smaller than minimum %d (network floor)", capacity, cfg.MinChannelCapacityLoki)
	}

	// Enforce Business Limits for JIT
	if cfg.JitEnabled && isJit {
		// Check Maximum Payment Size
		if cfg.JitMaxPaymentSizeMloki > 0 && uint64(incomingAmountMloki) > cfg.JitMaxPaymentSizeMloki {
			return nil, fmt.Errorf("payment size %d exceeds maximum allowed %d", incomingAmountMloki, cfg.JitMaxPaymentSizeMloki)
		}
	}

	minConfs := cfg.MinConfs
	if cfg.JitEnabled && isJit {
		log.Debug().Msgf("Intercept: JIT enabled, overriding MinConfs to 0 for paymentHash=%x", paymentHash)
		zero := uint32(0)
		minConfs = &zero
	}

	log.Info().Msgf(
		"Opening zero conf channel. Paymenthash: %x, Destination: %x, capacity: %v, fee: %s, targetConf %v",
		paymentHash,
		destination,
		capacity,
		fee.String(),
		fee.TargetConf,
	)

	routingPolicy := routingPolicyFor(cfg, isJit)

	channelPoint, err := i.client.OpenChannel(&lightning.OpenChannelRequest{
		Destination:     destination,
		CapacityLoki:    uint64(capacity),
		MinConfs:        minConfs,
		FeeLokiPerVByte: fee.LokiPerVByte,
		TargetConf:      fee.TargetConf,
		RoutingPolicy:   routingPolicy,
		ZeroConf:        cfg.JitEnabled && isJit,
	})
	if err != nil {
		log.Error().Msgf("paymenthash %x, client.OpenChannelSync(%x, %v) error: %v", paymentHash, destination, capacity, err)
		return nil, err
	}
	sendOpenChannelDiscordNotification(
		paymentHash,
		incomingAmountMloki,
		destination,
		capacity,
		channelPoint.String(),
		tag,
	)
	err = i.store.SetFundingTx(paymentHash, channelPoint)
	return channelPoint, err
}

// routingPolicyFor builds the forwarding policy for a channel opened to
// service a registered payment. For JIT channels, use zero routing fees to
// avoid circular dependency: the client pays to their own invoice, so
// charging routing fees would require them to pay more than the invoice
// amount, causing FeeInsufficient errors. Shared between openChannel (the
// initial OpenChannelRequest) and the follow-up UpdateChannelPolicy call
// once the channel is confirmed active, so both agree on the same policy.
func routingPolicyFor(cfg *config.NodeConfig, isJit bool) *lightning.RoutingPolicy {
	if isJit {
		return &lightning.RoutingPolicy{
			Ppm:           0, // Zero fee rate for JIT
			BaseMloki:     0, // Zero base fee for JIT
			TimeLockDelta: cfg.TimeLockDelta,
		}
	}
	return &lightning.RoutingPolicy{
		Ppm:           uint32(cfg.FeeRate * 1_000_000),
		BaseMloki:     cfg.BaseFeeMloki,
		TimeLockDelta: cfg.TimeLockDelta,
	}
}

// computeJitOutgoingAmount derives the amount to forward for an LSPS2 JIT
// payment, given the gross amount the sender paid (paymentSizeMloki) and
// the opening-fee params. Lightning fees are charged on the forwarded
// (outgoing) amount, so given input = outgoing + outgoing*rate, solving
// for outgoing yields outgoing = input / (1 + rate). minFeeMloki acts as
// a floor: if the proportional fee implied by that formula falls below
// it, the floor is charged instead and outgoing is reduced accordingly.
func computeJitOutgoingAmount(paymentSizeMloki, minFeeMloki int64, proportional uint32) (outgoing int64, fee int64) {
	rate := float64(proportional) / 1_000_000.0

	outgoing = int64(float64(paymentSizeMloki) / (1.0 + rate))
	fee = paymentSizeMloki - outgoing

	if fee < minFeeMloki {
		fee = minFeeMloki
		outgoing = paymentSizeMloki - fee
	}

	if outgoing < 0 {
		outgoing = 0
	}

	return outgoing, fee
}

type interceptData struct {
	orderId             string
	params              *core.OpeningFeeParams
	paymentHash         []byte
	paymentSecret       []byte
	destination         []byte
	incomingAmountMloki int64
	outgoingAmountMloki int64
	channelPoint        *wire.OutPoint
	tag                 *string
	isJit               bool
	// isLsps2Registration marks a payment resolved via the LSPS2
	// buy_registrations flow (Phase 7, F-15/T7-2) -- the only kind of JIT
	// payment with a Claim/Consume/Release lifecycle to enforce. Derived
	// from the payments row's persisted tag (lsps2JitTag), so it's set
	// identically whether this call freshly registered the payment or
	// found an existing row from an earlier call for the same
	// payment_hash -- see resolvePayment.
	isLsps2Registration bool
	// lsps2Scid is the phantom SCID this registration lives at -- kept
	// separate from req.Scid (the caller's own copy) so Intercept's
	// Claim/Consume/Release calls read it from data, matching how every
	// other per-payment field here is threaded through.
	lsps2Scid uint64
}

func (i *Interceptor) resolvePayment(req core.InterceptRequest) (*interceptData, error) {
	token, orderId, params, paymentHash, paymentSecret, destination, incomingAmountMloki, outgoingAmountMloki, channelPoint, tag, err := i.store.PaymentInfo(req.PaymentHash)

	// If err is nil but paymentHash/token is empty, it means not found in payments table.
	// We should treat this as "need fallback" for LSPS2.
	if err != nil || (len(paymentHash) == 0 && token == "") {
		// Try LSPS2 lookup using SCID
		lsps2Order, err2 := i.store.GetLsps2Order(uint64(req.Scid))
		if err2 == nil && lsps2Order != nil {
			token = lsps2Order.Token
			destination, _ = hex.DecodeString(lsps2Order.PeerId)
			params = &core.OpeningFeeParams{
				MinFeeMloki:          lsps2Order.ParamsMinFeeMloki,
				Proportional:         lsps2Order.ParamsProportional,
				ValidUntil:           lsps2Order.ParamsValidUntil,
				MinLifetime:          lsps2Order.ParamsMinLifetime,
				MaxClientToSelfDelay: lsps2Order.ParamsMaxClientToSelfDelay,
				Promise:              lsps2Order.ParamsPromise,
			}
			paymentHash = req.PaymentHash
			paymentSecret = []byte("lsps2-jit") // Mark as registered

			// F-22: paymentSize is the amount to validate/compute the fee
			// against -- the client's own fixed size if they gave one, else
			// (BLIP-52's legal "variable size, single HTLC" mode) the real
			// incoming amount actually paid. Before this fix, a nil
			// PaymentSizeMloki left outgoingAmountMloki at its zero default
			// for the entire rest of this flow -- openChannel would still
			// open a real, fully-funded channel, then forward a
			// guaranteed-to-fail $0 HTLC.
			paymentSize := int64(req.IncomingAmountMloki)
			if lsps2Order.PaymentSizeMloki != nil {
				paymentSize = int64(*lsps2Order.PaymentSizeMloki)
				// FLSPD-0014: fixed-size mode reserved capacity sized against
				// exactly this payment_size at buy time (ReserveLsps2Buy).
				// BLIP-52 treats payment_size_msat as the exact amount the
				// payer sends, so an incoming HTLC larger than it would open
				// a channel sized on the actual (larger) incoming amount
				// while the reserved-liquidity ledger (F-16/F-20) still only
				// counts payment_size for this registration -- under-counting
				// real committed capital and letting concurrent orders
				// over-commit the wallet. Reject the over-payment, mirroring
				// the variable-mode worst-case upper cap below (the
				// underpayment floor is enforced separately a few lines on).
				if req.IncomingAmountMloki > *lsps2Order.PaymentSizeMloki {
					log.Warn().Msgf("LSPS2 JIT: incoming amount %d exceeds fixed payment size %d", req.IncomingAmountMloki, *lsps2Order.PaymentSizeMloki)
					return nil, fmt.Errorf("incoming amount %d exceeds fixed payment size %d", req.IncomingAmountMloki, *lsps2Order.PaymentSizeMloki)
				}
			} else if lsps2Order.MaxPaymentSizeMloki != nil && req.IncomingAmountMloki > *lsps2Order.MaxPaymentSizeMloki {
				// F-22 (redemption-time half): in variable-size mode, the
				// real incoming HTLC amount was only ever bounded by an
				// unrelated, node-wide ceiling (JitMaxPaymentSizeMloki),
				// never by THIS registration's own worst-case bound --
				// the exact figure its buy-time reservation
				// (ReservedAmountLoki) was sized against. Without this
				// check, the channel opened below (sized off
				// incomingAmountMloki) could exceed what the
				// reserved-liquidity ledger (F-16/F-20) believes is
				// committed for this registration.
				log.Warn().Msgf("LSPS2 JIT: incoming amount %d exceeds registration's own worst-case bound %d", req.IncomingAmountMloki, *lsps2Order.MaxPaymentSizeMloki)
				return nil, fmt.Errorf("incoming amount %d exceeds registration's reserved worst-case bound %d", req.IncomingAmountMloki, *lsps2Order.MaxPaymentSizeMloki)
			}
			minFee := int64(lsps2Order.ParamsMinFeeMloki)

			var fee int64
			outgoingAmountMloki, fee = computeJitOutgoingAmount(paymentSize, minFee, lsps2Order.ParamsProportional)

			log.Info().Msgf("LSPS2 JIT: PaymentSize=%d, Fee=%d, Outgoing=%d", paymentSize, fee, outgoingAmountMloki)

			// Validation: Ensure the sender paid enough to cover the PaymentSize (which fee is calculated from)
			if int64(req.IncomingAmountMloki) < paymentSize {
				log.Warn().Msgf("LSPS2 JIT: Underpayment detected. PaymentSize=%d > Incoming=%d", paymentSize, req.IncomingAmountMloki)
				return nil, fmt.Errorf("underpayment: incoming amount %d is less than required payment size %d", req.IncomingAmountMloki, paymentSize)
			}
			incomingAmountMloki = int64(req.IncomingAmountMloki)
			lsps2Tag := lsps2JitTag
			tag = &lsps2Tag

			orderId = lsps2Order.Id

			// Register the payment, persisting lsps2JitTag as this row's
			// tag -- read back by PaymentInfo on every later call for this
			// payment_hash, so isJit/isLsps2Registration below can be
			// re-derived from it instead of only being set here.
			err = i.store.RegisterPayment(token, orderId, params, destination, paymentHash, paymentSecret, incomingAmountMloki, outgoingAmountMloki, lsps2JitTag)
			if err != nil {
				log.Error().Msgf("Failed to register LSPS2 payment: %v", err)
				return nil, err
			}
		} else {
			// Check if we can identify the destination from the SCID to look up LSPS1 orders
			nextHop, errHop := i.client.GetPeerId(&req.Scid)
			if errHop == nil && nextHop != nil {
				clientNodeId := hex.EncodeToString(nextHop)

				// Check for PAID LSPS1 orders for this peer
				lsps1Order, errOrder := i.store.GetPaidLsps1Order(clientNodeId)
				if errOrder == nil && lsps1Order != nil {
					log.Info().Msgf("Found PAID LSPS1 order %s for peer %s, activating JIT", lsps1Order.OrderId, clientNodeId)

					// Found a matching order! Activate JIT channel opening.
					token = lsps1Order.Token
					destination = nextHop

					// Use default fee params for now as LSPS1 params aren't stored in DB in same format
					// TODO: Store actual params from order creation
					cfg := i.config.Load()
					validity := time.Hour * 24
					if cfg.JitValidUntilSeconds > 0 {
						validity = time.Duration(cfg.JitValidUntilSeconds) * time.Second
					}
					maxDelay := uint32(10000)
					if cfg.JitMaxClientToSelfDelay > 0 {
						maxDelay = cfg.JitMaxClientToSelfDelay
					}

					params = &core.OpeningFeeParams{
						MinFeeMloki:          uint64(cfg.ChannelMinimumFeeMloki),
						Proportional:         uint32(cfg.ChannelFeePermyriad * 100),
						ValidUntil:           time.Now().Add(validity).Format(common.TimeFormat),
						MinLifetime:          uint32(cfg.MaxInactiveDuration / 60), // seconds -> blocks (1 min/block)
						MaxClientToSelfDelay: maxDelay,
					}

					paymentHash = req.PaymentHash
					paymentSecret = []byte("lsps1-jit")

					// Set amounts
					if lsps1Order.ClientBalanceLoki > 0 {
						outgoingAmountMloki = int64(lsps1Order.ClientBalanceLoki * 1000)
					}
					incomingAmountMloki = int64(req.IncomingAmountMloki)
					lsps1Tag := lsps1JitTag
					tag = &lsps1Tag

					orderId = lsps1Order.OrderId

					// Register this specific payment hash so we handle retries correctly
					err = i.store.RegisterPayment(token, orderId, params, destination, paymentHash, paymentSecret, incomingAmountMloki, outgoingAmountMloki, lsps1Tag)
					if err != nil {
						log.Error().Msgf("Failed to register LSPS1 payment: %v", err)
						return nil, err
					}
				}
			}
		}
	}

	if err != nil {
		return nil, err
	}

	// isJit/isLsps2Registration are derived from the final, PERSISTED tag
	// here, unconditionally -- not only inside the "not found" fallback
	// above. Before this fix, they were zero-initialized locals only ever
	// flipped true inside that fallback, so any call that instead took the
	// "found" branch (a retry after openChannel failed and
	// ReleaseLsps2Registration already ran once, or any later HTLC arriving
	// after RegisterPayment already wrote this payment_hash's row) silently
	// lost the classification: Intercept would then skip
	// ClaimLsps2Registration entirely on that call, defeating the very
	// lifecycle F-15's fix depends on, and skip the JIT
	// business-floor/max-payment-size checks in openChannel for the LSPS1
	// case. Deriving from tag instead makes the classification the same on
	// every call for a given payment_hash, retry or not.
	isJit := false
	isLsps2Registration := false
	if tag != nil {
		switch *tag {
		case lsps2JitTag:
			isJit = true
			isLsps2Registration = true
		case lsps1JitTag:
			isJit = true
		}
	}

	return &interceptData{
		orderId:             orderId,
		params:              params,
		paymentHash:         paymentHash,
		paymentSecret:       paymentSecret,
		destination:         destination,
		incomingAmountMloki: incomingAmountMloki,
		outgoingAmountMloki: outgoingAmountMloki,
		channelPoint:        channelPoint,
		tag:                 tag,
		isJit:               isJit,
		isLsps2Registration: isLsps2Registration,
		lsps2Scid:           uint64(req.Scid),
	}, nil
}

// RecoverStuckLsps2Registrations is the LSPS2 analogue of
// settlement_service.go's recoverStuckOrders/reconcileUnclaimedChannel
// (Phase 7, T7-13): a registration can be left at OPENING if flspd crashes
// between openChannel succeeding (a real funding transaction already
// broadcast, real capital already committed) and the
// ConsumeLsps2Registration commit. Left alone, that registration is
// unreachable by ClaimLsps2Registration forever (it's not ACTIVE) --
// unaccounted for in reserved-liquidity terms, and, since it's exactly the
// class of "real channel, no DB record" bug this codebase already found
// and fixed once for LSPS1 (F-14), needs the same reconcile-against-LND
// treatment, not a blind retry. Intended to run once at startup and,
// optionally, on a periodic tick alongside OrderCleanupService.
func (i *Interceptor) RecoverStuckLsps2Registrations() {
	stuck, err := i.store.GetStuckLsps2Registrations()
	if err != nil {
		log.Error().Err(err).Msg("RecoverStuckLsps2Registrations: failed to list stuck registrations")
		return
	}
	if len(stuck) == 0 {
		return
	}
	log.Info().Int("count", len(stuck)).Msg("RecoverStuckLsps2Registrations: found registrations stuck at OPENING, reconciling against LND")

	channels, err := i.client.ListChannels()
	if err != nil {
		log.Error().Err(err).Msg("RecoverStuckLsps2Registrations: failed to list channels, deferring to the next pass")
		return
	}

	// Excludes channels already attributed to some other registration's
	// completed purchase, mirroring LSPS1's own
	// claimedChannelPoints/findUnclaimedChannelPoint (settlement_service.go)
	// -- without this, a peer with ANY pre-existing channel (from an
	// earlier LSPS1 order, a manual open, or a prior JIT purchase) would
	// have that unrelated channel wrongly attributed to this stuck
	// registration.
	claimed, err := i.store.GetClaimedLsps2ChannelPoints()
	if err != nil {
		log.Error().Err(err).Msg("RecoverStuckLsps2Registrations: failed to list already-claimed channel points, deferring to the next pass")
		return
	}

	for _, reg := range stuck {
		destination, err := hex.DecodeString(reg.PeerId)
		if err != nil {
			log.Error().Err(err).Str("registration_id", reg.Id).Msg("RecoverStuckLsps2Registrations: invalid peer_id, skipping")
			continue
		}

		// Deliberately conservative, same philosophy as
		// findUnclaimedChannelPoint: collect every unclaimed channel to
		// this peer, and only act if there's EXACTLY one. Zero or more
		// than one is ambiguous -- guessing risks either falsely
		// consuming a registration nothing was ever opened for, or
		// mis-attributing one registration's channel to another.
		var candidates []*lightning.Channel
		for _, ch := range channels {
			if ch.ChannelPoint == nil || !bytes.Equal(ch.PeerId, destination) {
				continue
			}
			if claimed[ch.ChannelPoint.String()] {
				continue
			}
			candidates = append(candidates, ch)
		}

		if len(candidates) > 1 {
			log.Warn().Str("registration_id", reg.Id).Int("candidate_count", len(candidates)).Msg("RecoverStuckLsps2Registrations: multiple unclaimed channels found for this peer, cannot disambiguate which (if any) belongs to this registration -- leaving stuck for manual review")
			continue
		}

		if len(candidates) == 1 {
			existing := candidates[0]
			// openChannel already succeeded before the crash -- finish
			// what ConsumeLsps2Registration would have done, using the
			// registration's own quoted figures as the best available
			// approximation for the audit record (the exact per-HTLC
			// amounts from the original triggering payment are gone with
			// the crashed process). Preference order: the client's own
			// fixed PaymentSizeMloki (exact, fixed-size mode) : the
			// registration's own worst-case MaxPaymentSizeMloki (F-22,
			// variable-size mode -- a real payment-size bound, still just
			// an approximation of the true redeemed amount but not
			// systematically inflated) : ReservedAmountLoki*1000 (last
			// resort only, for a registration that predates
			// MaxPaymentSizeMloki -- this one IS a known overstatement,
			// since ReservedAmountLoki also bakes in capacity padding and
			// the BOLT2 reserve top-up on top of the real payment size).
			var paymentSizeMloki uint64
			switch {
			case reg.PaymentSizeMloki != nil:
				paymentSizeMloki = *reg.PaymentSizeMloki
			case reg.MaxPaymentSizeMloki != nil:
				paymentSizeMloki = *reg.MaxPaymentSizeMloki
			default:
				paymentSizeMloki = reg.ReservedAmountLoki * 1000
			}
			consumed, err := i.store.ConsumeLsps2Registration(reg.Scid, existing.ChannelPoint, paymentSizeMloki, reg.ParamsMinFeeMloki)
			if err != nil {
				log.Error().Err(err).Str("registration_id", reg.Id).Msg("RecoverStuckLsps2Registrations: failed to consume registration for an already-open channel")
				continue
			}
			if consumed {
				log.Info().Str("registration_id", reg.Id).Str("channel_point", existing.ChannelPoint.String()).Msg("RecoverStuckLsps2Registrations: reconciled a crash-orphaned channel, registration marked CONSUMED")
			}
			continue
		}

		// No unclaimed channel found -- the crash happened before
		// openChannel ever succeeded (or it genuinely failed). Safe to
		// release back to ACTIVE so a real HTLC can retry.
		if err := i.store.ReleaseLsps2Registration(reg.Scid); err != nil {
			log.Error().Err(err).Str("registration_id", reg.Id).Msg("RecoverStuckLsps2Registrations: failed to release stuck registration")
			continue
		}
		log.Info().Str("registration_id", reg.Id).Msg("RecoverStuckLsps2Registrations: no matching channel found, released back to ACTIVE for retry")
	}
}
