package lsps

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/rs/zerolog/log"
)

// Lsps1 Defaults (Limits)
// Note: Defaults are removed in favor of strict configuration validation at startup.
const (
	DefaultLsps1OrderExpiryDuration = 24 * time.Hour // Invoice and order expiry time
)

// Lsps1OrderState defines the state of an LSPS1 order
type Lsps1OrderState string

const (
	Lsps1OrderStateCreated   Lsps1OrderState = "CREATED"
	Lsps1OrderStateCompleted Lsps1OrderState = "COMPLETED"
	Lsps1OrderStateFailed    Lsps1OrderState = "FAILED"
	Lsps1OrderStateExpired   Lsps1OrderState = "EXPIRED" // Not in spec, but useful logic state
	Lsps1OrderStatePaid      Lsps1OrderState = "PAID"    // Internal state: Paid but not yet funded/completed
)

// Lsps1PaymentState defines the state of an LSPS1 payment
type Lsps1PaymentState string

const (
	Lsps1PaymentStateExpectPayment Lsps1PaymentState = "EXPECT_PAYMENT"
	Lsps1PaymentStateHold          Lsps1PaymentState = "HOLD"
	Lsps1PaymentStatePaid          Lsps1PaymentState = "PAID"
	Lsps1PaymentStateRefunded      Lsps1PaymentState = "REFUNDED"
	Lsps1PaymentStateExpired       Lsps1PaymentState = "EXPIRED" // Not in spec explicit return, but logic state
	Lsps1PaymentStateFailed        Lsps1PaymentState = "FAILED"
)

// toSpecOrderState maps an internal order_state to a blip-0051 wire value
// (FLSPD-0020). The spec order_state enum is only CREATED|COMPLETED|FAILED
// (blip-0051 §2.4.1): the internal PAID ("paid, not yet funded") reports as
// CREATED, and EXPIRED ("swept past expires_at before completing") reports as
// FAILED -- an expired order is a terminal non-completion. CREATED, COMPLETED
// and FAILED pass through unchanged.
func toSpecOrderState(internal string) string {
	switch internal {
	case string(Lsps1OrderStatePaid):
		return string(Lsps1OrderStateCreated)
	case string(Lsps1OrderStateExpired):
		return string(Lsps1OrderStateFailed)
	default:
		return internal
	}
}

// toSpecPaymentState maps an internal payment.bolt11.state to a blip-0051
// wire value (FLSPD-0020). The spec enum is only
// EXPECT_PAYMENT|HOLD|PAID|REFUNDED (blip-0051 §2.4.1). Internal EXPIRED and
// FAILED both report as REFUNDED, matching blip-0051 §3.1 ("the client gets
// refunded automatically in case ... the order expires") -- for an order that
// never captured funds this is vacuously true (nothing to return), and for
// one whose held HTLC was released it is the correct terminal state. The four
// spec values pass through unchanged.
func toSpecPaymentState(internal string) string {
	switch internal {
	case string(Lsps1PaymentStateExpired), string(Lsps1PaymentStateFailed):
		return string(Lsps1PaymentStateRefunded)
	default:
		return internal
	}
}

type Lsps1GetInfoResponse struct {
	Options                         []string                 `json:"options"`
	OpeningFeeParams                []*Lsps2OpeningFeeParams `json:"opening_fee_params,omitempty"`
	MinRequiredChannelConfirmations uint16                   `json:"min_required_channel_confirmations"`
	MinFundingConfirmsWithinBlocks  uint16                   `json:"min_funding_confirms_within_blocks"`
	SupportsZeroChannelReserve      bool                     `json:"supports_zero_channel_reserve"`
	MaxChannelExpiryBlocks          uint32                   `json:"max_channel_expiry_blocks"`
	MinInitialClientBalanceLoki     string                   `json:"min_initial_client_balance_loki"`
	MaxInitialClientBalanceLoki     string                   `json:"max_initial_client_balance_loki"`
	MinInitialLspBalanceLoki        string                   `json:"min_initial_lsp_balance_loki"`
	MaxInitialLspBalanceLoki        string                   `json:"max_initial_lsp_balance_loki"`
	MinChannelBalanceLoki           string                   `json:"min_channel_balance_loki"`
	MaxChannelBalanceLoki           string                   `json:"max_channel_balance_loki"`
}

// getLsps1ConfigValues also returns the raw, just-loaded *config.NodeConfig
// (last value) so a single call can both extract the usual scalar fields
// AND feed platform/config's shared RequiredCapacityLoki/MeetsCapacityFloors
// -- callers computing capacity should use this cfg rather than loading a
// second, potentially-inconsistent snapshot themselves (C-3).
func (s *Handlers) getLsps1ConfigValues() (uint16, uint16, bool, uint32, time.Duration, uint64, uint64, uint64, uint64, uint64, uint64, int64, *config.NodeConfig) {
	nodes := s.nodesService.GetNodes()
	if len(nodes) == 0 || nodes[0].NodeConfig == nil {
		// Should not happen if validation passed at startup
		return 0, 0, false, 0, 0, 0, 0, 0, 0, 0, 0, 0, nil
	}
	c := nodes[0].NodeConfig.Load()

	// Dereference pointers as validation ensures they are non-nil
	minReqConf := *c.Lsps1MinRequiredChannelConfirmations
	fundingConf := *c.Lsps1MinFundingConfirmsWithinBlocks
	supportsZeroReserve := *c.Lsps1SupportsZeroChannelReserve
	maxExpiry := *c.Lsps1MaxChannelExpiryBlocks
	orderExpiry := time.Duration(*c.Lsps1OrderExpirySeconds) * time.Second
	minClient := *c.Lsps1MinInitialClientBalanceLoki
	maxClient := *c.Lsps1MaxInitialClientBalanceLoki
	minLsp := *c.Lsps1MinInitialLspBalanceLoki
	maxLsp := *c.Lsps1MaxInitialLspBalanceLoki
	minChan := *c.Lsps1MinChannelBalanceLoki
	maxChan := *c.Lsps1MaxChannelBalanceLoki
	additional := c.AdditionalChannelCapacity

	return minReqConf, fundingConf, supportsZeroReserve, maxExpiry, orderExpiry, minClient, maxClient, minLsp, maxLsp, minChan, maxChan, additional, c
}

func (s *Handlers) handleLsps1GetInfo(req *JsonRpcRequest) *JsonRpcResponse {
	log.Trace().
		Str("component", string(logging.LogComponentLSPS1)).
		Str("action", string(logging.LogActionRequest)).
		Msg("LSPS1: Received get_info request")

	minReqConf, fundingConf, supportsZeroReserve, maxExpiry, _, minClient, maxClient, minLsp, maxLsp, minChan, maxChan, _, _ := s.getLsps1ConfigValues()

	nodes := s.nodesService.GetNodes()
	var openingFeeParams []*Lsps2OpeningFeeParams

	// Gather fee params for LSPS1 as well (extension for transparency)
	for _, node := range nodes {
		for _, token := range node.Tokens {
			fees, err := s.openingService.GetFeeParamsMenu(token, node.PrivateKey)
			if err == nil {
				for _, f := range fees {
					openingFeeParams = append(openingFeeParams, &Lsps2OpeningFeeParams{
						MinFeeMloki:          fmt.Sprintf("%d", f.MinFeeMloki),
						Proportional:         f.Proportional,
						ValidUntil:           f.ValidUntil,
						MinLifetime:          f.MinLifetime,
						MaxClientToSelfDelay: f.MaxClientToSelfDelay,
						MinPaymentSizeMloki:  fmt.Sprintf("%d", f.MinPaymentSizeMloki),
						MaxPaymentSizeMloki:  fmt.Sprintf("%d", f.MaxPaymentSizeMloki),
						Promise:              f.Promise,
					})
				}
			}
		}
	}

	resp := Lsps1GetInfoResponse{
		Options:                         []string{"lsps1.create_order", "lsps1.get_order"},
		OpeningFeeParams:                openingFeeParams,
		MinRequiredChannelConfirmations: minReqConf,
		MinFundingConfirmsWithinBlocks:  fundingConf,
		SupportsZeroChannelReserve:      supportsZeroReserve,
		MaxChannelExpiryBlocks:          maxExpiry,
		MinInitialClientBalanceLoki:     fmt.Sprintf("%d", minClient),
		MaxInitialClientBalanceLoki:     fmt.Sprintf("%d", maxClient),
		MinInitialLspBalanceLoki:        fmt.Sprintf("%d", minLsp),
		MaxInitialLspBalanceLoki:        fmt.Sprintf("%d", maxLsp),
		MinChannelBalanceLoki:           fmt.Sprintf("%d", minChan),
		MaxChannelBalanceLoki:           fmt.Sprintf("%d", maxChan),
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS1)).
		Str("action", string(logging.LogActionResponse)).
		Msgf("LSPS1: get_info result: %+v", resp)
	return NewJsonRpcResponse(req.Id, resp)
}

// isOrderExpired checks if an order is expired based on the invoice expiry duration
// isOrderExpired compares against the order's own persisted expires_at --
// computed once at creation time from the config live then (F-19) -- not
// createdAt + the CURRENTLY configured expiry duration. Before this fix,
// this recomputed from live config on every call, so changing
// Lsps1OrderExpirySeconds (hot-swappable via the dashboard, no restart
// needed) after an order already existed made get_order disagree with
// the DB-level sweep and reserved-liquidity accounting, which both
// correctly use the order's own frozen expires_at -- reproducing F-19's
// exact bug class for this one read path.
func isOrderExpired(expiresAt time.Time) bool {
	return time.Now().After(expiresAt)
}

type Lsps1CreateOrderRequest struct {
	LspBalanceLoki               string `json:"lsp_balance_loki"`
	ClientBalanceLoki            string `json:"client_balance_loki"`
	RequiredChannelConfirmations uint16 `json:"required_channel_confirmations"`
	FundingConfirmsWithinBlocks  uint16 `json:"funding_confirms_within_blocks"`
	ChannelExpiryBlocks          uint32 `json:"channel_expiry_blocks"`
	Token                        string `json:"token"`
	RefundOnchainAddress         string `json:"refund_onchain_address"`
	AnnounceChannel              bool   `json:"announce_channel"`
}

type Lsps1CreateOrderResponse struct {
	OrderId                      string        `json:"order_id"`
	LspBalanceLoki               string        `json:"lsp_balance_loki"`
	ClientBalanceLoki            string        `json:"client_balance_loki"`
	RequiredChannelConfirmations uint16        `json:"required_channel_confirmations"`
	FundingConfirmsWithinBlocks  uint16        `json:"funding_confirms_within_blocks"`
	ChannelExpiryBlocks          uint32        `json:"channel_expiry_blocks"`
	Token                        string        `json:"token"`
	AnnounceChannel              bool          `json:"announce_channel"`
	CreatedAt                    string        `json:"created_at"`
	OrderState                   string        `json:"order_state"`
	Payment                      *Lsps1Payment `json:"payment"`
	Channel                      interface{}   `json:"channel"` // null initially
}

type Lsps1Payment struct {
	Bolt11 *Lsps1PaymentBolt11 `json:"bolt11,omitempty"`
}

type Lsps1PaymentBolt11 struct {
	State          string `json:"state"`
	ExpiresAt      string `json:"expires_at"`
	FeeTotalLoki   string `json:"fee_total_loki"`
	OrderTotalLoki string `json:"order_total_loki"`
	Invoice        string `json:"invoice"`
}

func (s *Handlers) handleLsps1CreateOrder(req *JsonRpcRequest, peer []byte) *JsonRpcResponse {
	peerId := hex.EncodeToString(peer)

	// Phase 7 (F-17/T7-7): the burst limiter is checked before anything
	// else in this handler -- including param parsing -- so a spam burst
	// from one peer never reaches the DB/RPC work below it.
	if !s.burstLimiter.Allow(peerId) {
		return NewJsonRpcErrorResponse(req.Id, -32603, "Rate limit exceeded, please retry later", nil)
	}

	var params Lsps1CreateOrderRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		log.Trace().
			Err(err).
			Str("component", string(logging.LogComponentLSPS1)).
			Str("action", string(logging.LogActionValidate)).
			Msg("LSPS1: create_order invalid params")
		return NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params", nil)
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS1)).
		Str("action", string(logging.LogActionRequest)).
		Interface("params", params).
		Msg("LSPS1: Received create_order request")

	clientBalance := parseUint64(params.ClientBalanceLoki)
	lspBalance := parseUint64(params.LspBalanceLoki)

	// Validation: At least one balance must be non-zero
	if clientBalance == 0 && lspBalance == 0 {
		log.Warn().
			Str("component", string(logging.LogComponentLSPS1)).
			Str("action", string(logging.LogActionReject)).
			Str("peer_id", peerId).
			Msg("LSPS1: Order rejected - at least one balance must be non-zero")
		return NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params: either client_balance or lsp_balance must be non-zero", nil)
	}

	// Validation: Check bounds. Loaded ONCE for this whole request (C-3) --
	// before this fix, this function called getLsps1ConfigValues() a
	// second time further down for the capacity/floor checks, so a
	// dashboard config update landing between the two calls could split
	// one request's decisions across two different config snapshots.
	_, _, _, _, orderExpiry, minClient, maxClient, _, maxLsp, minBusinessChan, maxChan, additionalPadding, cfg := s.getLsps1ConfigValues()

	if clientBalance < minClient || clientBalance > maxClient {
		return NewJsonRpcErrorResponse(req.Id, 100, "Client balance out of bounds", map[string]interface{}{
			"min": minClient,
			"max": maxClient,
		})
	}
	if lspBalance > maxLsp {
		return NewJsonRpcErrorResponse(req.Id, 100, "LSP balance out of bounds", map[string]interface{}{
			"max": maxLsp,
		})
	}

	// Phase 7 (F-17/T7-7): standing per-peer cap on live orders, mirroring
	// lsps2.buy's CountLsps2PendingRegistrations check. Moved here (right
	// after the cheap param-bounds checks, well before fee computation,
	// capacity/floor validation, and hold-invoice creation) so a peer
	// already at their standing limit is rejected before any of that
	// heavier work runs -- there was previously no protocol-specific
	// reason for this check to sit so much later than its LSPS2
	// counterpart's equivalent placement.
	// Only count orders created within the last 24 hours (non-expired).
	cutoffTime := time.Now().Add(-orderExpiry)
	pendingCount, err := s.openingService.CountLsps1Orders(peerId, string(Lsps1OrderStateCreated), &cutoffTime)
	if err == nil && pendingCount >= s.config.Lsps1MaxPendingOrders {
		return NewJsonRpcErrorResponse(req.Id, -32603, fmt.Sprintf("Too many pending orders (max %d)", s.config.Lsps1MaxPendingOrders), nil)
	}

	// Validation: Check minimum channel capacity
	// We check if (requested + additional [+ BOLT2 reserve top-up]) meets
	// both the physical floor and the business limit.
	channelCapacity := clientBalance + lspBalance
	minPhysicalFloor := s.nodesService.GetMinChannelCapacity()

	// F-24/F-17(#7 second pass): finalCapacity now goes through the same
	// shared config.RequiredCapacityLoki the interceptor's openChannel and
	// LSPS2's handler already use, instead of LSPS1's own inline
	// channelCapacity+padding math -- before this fix, an LSPS1 order's
	// reservation never included the BOLT2 ~1% reserve top-up term, so it
	// could under-reserve relative to what the interceptor's own JIT
	// reconnect-fallback path (resolvePayment's GetPaidLsps1Order branch)
	// would actually fund for this exact order if that's the path that
	// ends up opening its channel. clientBalance is the side that needs
	// reserve headroom here, mirroring that fallback path's own
	// convention of using ClientBalanceLoki as the "outgoing" term.
	finalCapacity := config.RequiredCapacityLoki(channelCapacity, clientBalance, cfg, false)

	// 1. Check Physical Floor (LND requirement)
	if finalCapacity < minPhysicalFloor {
		log.Warn().
			Str("component", string(logging.LogComponentLSPS1)).
			Str("action", string(logging.LogActionReject)).
			Uint64("requested_capacity", channelCapacity).
			Int64("additional_capacity", additionalPadding).
			Uint64("min_capacity", minPhysicalFloor).
			Msg("LSPS1: Order rejected - channel capacity too small for LND floor")
		return NewJsonRpcErrorResponse(req.Id, 100, fmt.Sprintf("Channel capacity too small, physical minimum is %d (including %d padding)", minPhysicalFloor, additionalPadding), map[string]interface{}{
			"min_capacity": minPhysicalFloor,
			"requested":    channelCapacity,
			"padding":      additionalPadding,
		})
	}

	// 2. Check Business Limits (Advertised in get_info)
	if finalCapacity < minBusinessChan || (maxChan > 0 && finalCapacity > maxChan) {
		log.Warn().
			Str("component", string(logging.LogComponentLSPS1)).
			Str("action", string(logging.LogActionReject)).
			Uint64("requested_capacity", channelCapacity).
			Int64("additional_capacity", additionalPadding).
			Uint64("min_business_limit", minBusinessChan).
			Uint64("max_business_limit", maxChan).
			Msg("LSPS1: Order rejected - channel capacity out of business bounds")
		return NewJsonRpcErrorResponse(req.Id, 100, fmt.Sprintf("Channel capacity out of bounds, business range is [%d, %d] (including %d padding)", minBusinessChan, maxChan, additionalPadding), map[string]interface{}{
			"min_capacity": minBusinessChan,
			"max_capacity": maxChan,
			"requested":    channelCapacity,
			"padding":      additionalPadding,
		})
	}

	// F-24: check (and later reserve) the real committed capacity --
	// finalCapacity, just computed above for the business-floor check
	// (clientBalance+lspBalance+padding) -- not lsp_balance_loki alone.
	// LSPS1 is 100%-LSP-funded (buildOpenChannelRequest funds this exact
	// figure, minus flooring); clientBalance is inbound liquidity the LSP
	// pays for out of its own capital, not the client's money, so it must
	// be part of what's liquidity-checked, not just lspBalance.
	if resp := s.checkFleetCeilingAndLiquidity(req, logging.LogComponentLSPS1, finalCapacity, 1, "Client rejected: insufficient liquidity"); resp != nil {
		return resp
	}

	// Calculate Fee
	// We'll use the first available node's policy for simplicity or a default policy.
	// NOTE: We need a policy. Let's try to get one from openingService using params.Token.
	var feeLoki uint64
	nodes := s.nodesService.GetNodes()
	var usedNode *core.Node
	for _, node := range nodes {
		// Just take the first valid one if no token, or match token
		if params.Token != "" {
			for _, t := range node.Tokens {
				if t == params.Token {
					usedNode = node
					break
				}
			}
		} else {
			usedNode = node
		}
		if usedNode != nil {
			break
		}
	}

	if usedNode != nil {
		// Use the token to get params? Or just assume some defaults?
		// We'll use a token if we found one, or the first token of the node
		t := ""
		if len(usedNode.Tokens) > 0 {
			t = usedNode.Tokens[0]
		}
		fees, _ := s.openingService.GetFeeParamsMenu(t, usedNode.PrivateKey)
		if len(fees) > 0 {
			// pick the first fee param
			f := fees[0]
			// F-5: use the same fee function LSPS2 uses (computeOpeningFee)
			// rather than a separately hand-rolled floor(max(proportional,
			// min)) calculation — this used to round differently (floor)
			// than LSPS2's ceiling-rounded, overflow-checked version of the
			// same logical "opening fee" concept.
			capacity := clientBalance + lspBalance
			feeMloki, err := ComputeOpeningFee(capacity*1000, f.MinFeeMloki, uint64(f.Proportional))
			if err != nil {
				return NewJsonRpcErrorResponse(req.Id, -32603, "Failed to compute opening fee", err.Error())
			}
			feeLoki = feeMloki / 1000
		} else {
			// Fallback default
			feeLoki = 1000 // 1000 loki default fee?
		}
	} else {
		feeLoki = 2000 // Default if no node found?
	}

	orderTotal := clientBalance + feeLoki

	// Generate a hold invoice: the LSP (not LND) generates and holds the
	// preimage, so the payer's HTLC is only ACCEPTED, not settled, until
	// settlement_service decides the channel open will actually happen --
	// letting a failed order be cancelled (instant, on-chain-fee-free)
	// instead of requiring the slow RefundOnchainAddress path.
	var preimage [32]byte
	if _, err := rand.Read(preimage[:]); err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS1)).
			Str("action", string(logging.LogActionFailure)).
			Msg("LSPS1: Failed to generate hold invoice preimage")
		return NewJsonRpcErrorResponse(req.Id, -32603, "Internal error creating invoice", nil)
	}
	paymentHash := sha256.Sum256(preimage[:])

	memo := "Inbound Liquidity Order"
	inv, err := s.lndClient.AddHoldInvoice(context.Background(), &invoicesrpc.AddHoldInvoiceRequest{
		Memo:    memo,
		Hash:    paymentHash[:],
		Value:   int64(orderTotal),
		Expiry:  int64(orderExpiry.Seconds()),
		Private: true,
	})
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS1)).
			Str("action", string(logging.LogActionFailure)).
			Msg("LSPS1: Failed to create LND hold invoice for order")
		return NewJsonRpcErrorResponse(req.Id, -32603, "Internal error creating invoice", nil)
	}

	orderID, _ := generateUUID()
	now := time.Now()
	expiry := now.Add(orderExpiry)
	preimageHex := hex.EncodeToString(preimage[:])

	// Persist Order
	order := &core.Lsps1Order{
		OrderId:                      orderID,
		PeerId:                       peerId,
		LspBalanceLoki:               int64(lspBalance),
		ClientBalanceLoki:            int64(clientBalance),
		FundingConfirmsWithinBlocks:  int(params.FundingConfirmsWithinBlocks),
		RequiredChannelConfirmations: int(params.RequiredChannelConfirmations),
		ChannelExpiryBlocks:          int(params.ChannelExpiryBlocks),
		Token:                        params.Token,
		AnnounceChannel:              params.AnnounceChannel,
		CreatedAt:                    now,
		OrderState:                   string(Lsps1OrderStateCreated),
		PaymentState:                 string(Lsps1PaymentStateExpectPayment),
		PaymentInvoice:               inv.PaymentRequest,
		PaymentPreimage:              &preimageHex,
		RefundOnchainAddress:         params.RefundOnchainAddress,
		OrderTotalLoki:               int64(orderTotal),
		FeeTotalLoki:                 int64(feeLoki),
		// Phase 7 (F-19/F-24/T7-1): expiry and reserved amount are computed
		// once, here, from live config -- never re-derived from
		// created_at+config later, and reserving the real capacity
		// (requiredCapacity), not lsp_balance_loki alone.
		ExpiresAt:          expiry,
		ReservedAmountLoki: finalCapacity,
	}

	// F-20/T7-5: re-check liquidity and persist atomically under an
	// advisory lock, immediately before committing -- the earlier
	// CheckOnChainLiquidity call above was a cheap, non-atomic fast-fail
	// (good UX: reject early rather than build a hold invoice for a
	// clearly-insufficient request), but time has passed since then (an
	// AddHoldInvoice round trip), and a bare read-then-insert would still
	// let two concurrent orders both pass against the same stale figure.
	reserved, reserveErr := s.liquidityManager.ReserveLsps1Order(context.Background(), order)
	// Distinct from reserveErr != nil (a hard DB/transaction failure):
	// insufficientLiquidity is the clean case where the re-check itself
	// succeeded but simply found no room left. Tracked separately so a
	// genuine error can never be misreported as "insufficient liquidity"
	// just because reserved's zero value also happens to be false.
	insufficientLiquidity := reserveErr == nil && !reserved
	err = reserveErr
	if insufficientLiquidity {
		err = fmt.Errorf("insufficient liquidity on re-check")
	}
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS1)).
			Str("action", string(logging.LogActionFailure)).
			Msg("LSPS1: Failed to persist order in database")

		// H-7: AddHoldInvoice already succeeded at LND, but the preimage
		// (a local variable) was never persisted since the DB write just
		// failed. Without a compensating cancel, this hold invoice is
		// orphaned -- flspd has no stored preimage to settle or cancel it
		// with later, so any HTLC that locks into it would sit ACCEPTED
		// until LND's own hold-invoice-expiry watcher eventually releases
		// it. Fired async (S-7): its outcome never changes the error
		// response already decided below, and CancelInvoice's own failure
		// is non-fatal either way -- that watcher is still the fallback --
		// so there's no reason to make the client wait on this RPC. Same
		// compensating action applies whether the DB write hard-failed or
		// the atomic re-check simply found liquidity gone in the meantime.
		go func() {
			if cancelErr := s.lndClient.CancelInvoice(context.Background(), paymentHash[:]); cancelErr != nil {
				log.Error().
					Err(cancelErr).
					Str("component", string(logging.LogComponentLSPS1)).
					Msg("LSPS1: Failed to cancel orphaned hold invoice after order persistence failure -- relying on LND's own hold-invoice expiry")
			}
		}()

		if insufficientLiquidity {
			return NewJsonRpcErrorResponse(req.Id, 1, "Client rejected: insufficient liquidity", nil)
		}
		return NewJsonRpcErrorResponse(req.Id, -32603, "Database error", nil)
	}

	// H-12: attach a live per-invoice watcher now, right at order-creation
	// time -- gives near-instant ACCEPTED detection instead of waiting up
	// to heldInvoiceSafetyNetPollInterval for the safety-net poll to catch
	// it. Nil-safe: not every construction site wires this up (most tests
	// don't need live watching).
	if s.orderWatcher != nil {
		s.orderWatcher.WatchOrder(order)
	}

	resp := Lsps1CreateOrderResponse{
		OrderId:                      orderID,
		LspBalanceLoki:               params.LspBalanceLoki,
		ClientBalanceLoki:            params.ClientBalanceLoki,
		RequiredChannelConfirmations: params.RequiredChannelConfirmations,
		FundingConfirmsWithinBlocks:  params.FundingConfirmsWithinBlocks,
		ChannelExpiryBlocks:          params.ChannelExpiryBlocks,
		Token:                        params.Token,
		AnnounceChannel:              params.AnnounceChannel,
		CreatedAt:                    now.Format(time.RFC3339),
		OrderState:                   string(Lsps1OrderStateCreated),
		Payment: &Lsps1Payment{
			Bolt11: &Lsps1PaymentBolt11{
				State:          string(Lsps1PaymentStateExpectPayment),
				ExpiresAt:      expiry.Format(time.RFC3339),
				FeeTotalLoki:   fmt.Sprintf("%d", feeLoki),
				OrderTotalLoki: fmt.Sprintf("%d", orderTotal),
				Invoice:        inv.PaymentRequest,
			},
		},
		Channel: nil,
	}

	// Notify client of CREATED state
	if s.notificationService != nil {
		go s.notificationService.NotifyOrderStateChanged(peerId, resp.OrderId, string(Lsps1OrderStateCreated), "", "")
	}

	log.Trace().
		Str("component", string(logging.LogComponentLSPS1)).
		Str("action", string(logging.LogActionResponse)).
		Msgf("LSPS1: create_order success, order_id=%s", resp.OrderId)
	return NewJsonRpcResponse(req.Id, resp)
}

type Lsps1GetOrderRequest struct {
	OrderId string `json:"order_id"`
}

func (s *Handlers) handleLsps1GetOrder(req *JsonRpcRequest) *JsonRpcResponse {
	var params Lsps1GetOrderRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		log.Trace().
			Err(err).
			Str("component", string(logging.LogComponentLSPS1)).
			Str("action", string(logging.LogActionValidate)).
			Msg("LSPS1: get_order invalid params")
		return NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params", nil)
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS1)).
		Str("action", string(logging.LogActionRequest)).
		Interface("params", params).
		Msg("LSPS1: Received get_order request")

	order, err := s.openingService.GetLsps1Order(params.OrderId)
	if err != nil {
		// Assume error means not found for now, or distinguish DB error?
		// Spec says 101 for Not found
		return NewJsonRpcErrorResponse(req.Id, 101, "Order not found", nil)
	}

	// TODO: Check if payment is actually paid by querying LND if state is EXPECT_PAYMENT?
	// For now, trust DB state.

	// The order's own persisted expiry (F-19/Phase 7) -- not recomputed
	// from live config, so this stays consistent with the DB sweep and
	// reserved-liquidity accounting no matter what Lsps1OrderExpirySeconds
	// is configured to now.
	expiry := order.ExpiresAt

	// Check if order is expired and override state if necessary
	orderState := order.OrderState
	paymentState := order.PaymentState
	if orderState == string(Lsps1OrderStateCreated) && isOrderExpired(order.ExpiresAt) {
		orderState = string(Lsps1OrderStateExpired)
		paymentState = string(Lsps1PaymentStateExpired)
	}

	// Construct fee (approximate if not stored separately, but we stored total invoice amount)
	// We stored ClientBalanceLoki and LspBalanceLoki.
	// OrderTotal is in PaymentInvoice (decoded) or we can infer it.
	// Actually, we didn't store the Fee separately in DB, but we have LspBalance and ClientBalance.
	// Wait, we didn't store Fee in DB. But we have `payment_invoice`.
	// We can't easily get the amount from the invoice string without decoding it.
	// Ideally we should have stored `fee_loki` or `order_total_loki` in DB.
	// For this iteration, I'll calculate it on the fly if I can, or update DB schema?
	// The DB schema didn't have `fee_loki`.
	// I'll update `lsps1.orders` schema to include `order_total_loki` or `fee_loki`?
	// The `lsps1.orders` table I created in Step 614 does NOT have fee columns.
	// However, I can compute fee = OrderTotal - ClientBalance.
	// But I don't have OrderTotal easily without decoding invoice.
	// I'll skip returning fee_total_sat for get_order for now, or just return "0" and fix later.
	// Wait, spec says response is SAME as create_order.
	// I should probably decode the invoice or store the total.
	// I'll add `fee_loki` to the DB schema in a "migration fix" or just alter table?
	// Since I can't easily alter table right now without migration tool...
	// AND I just created the table...
	// Maybe I can just decode the invoice... but I don't have a bolt11 decoder imported.
	// I will just store `order_total_loki` in the DB. I'll issue a new schema migration command now.

	// Map internal-only states back to the blip-0051 wire enums before they
	// reach the client (FLSPD-0020). order_state is only CREATED|COMPLETED|
	// FAILED on the wire and payment.bolt11.state is only EXPECT_PAYMENT|HOLD|
	// PAID|REFUNDED; the internal PAID/EXPIRED/FAILED values leaking out as-is
	// made a strict LSPS1 client fail to parse the response.
	respOrderState := toSpecOrderState(orderState)
	paymentState = toSpecPaymentState(paymentState)

	// Only report channel details once the order has actually reached
	// COMPLETED. FundingTxid alone is no longer sufficient: for a
	// hold-invoice order, recordHeldOrderFundingAndSettle now records
	// FundingTxid *before* settlement is confirmed (for crash-safety, see
	// settlement_service.go), so a HOLD order can have a non-nil
	// FundingTxid while its payment is still uncaptured. Gating on
	// OrderState==COMPLETED keeps this response internally consistent --
	// a client is never told "here is your funded channel" for an order
	// whose Payment.Bolt11.State still reads HOLD/EXPECT_PAYMENT.
	var channelInfo interface{}
	if orderState == string(Lsps1OrderStateCompleted) && order.FundingTxid != nil && *order.FundingTxid != "" {
		vout := 0
		if order.FundingOutpoint != nil {
			vout = *order.FundingOutpoint
		}
		channelInfo = map[string]interface{}{
			"funded_at":        order.CreatedAt.Format(time.RFC3339), // Fallback to CreatedAt if FundedAt missing
			"funding_outpoint": fmt.Sprintf("%s:%d", *order.FundingTxid, vout),
			"expires_at":       order.CreatedAt.Add(time.Duration(order.ChannelExpiryBlocks) * time.Minute).Format(time.RFC3339),
		}
	}

	resp := Lsps1CreateOrderResponse{
		OrderId:                      order.OrderId,
		LspBalanceLoki:               fmt.Sprintf("%d", order.LspBalanceLoki),
		ClientBalanceLoki:            fmt.Sprintf("%d", order.ClientBalanceLoki),
		RequiredChannelConfirmations: uint16(order.RequiredChannelConfirmations),
		FundingConfirmsWithinBlocks:  uint16(order.FundingConfirmsWithinBlocks),
		ChannelExpiryBlocks:          uint32(order.ChannelExpiryBlocks),
		Token:                        order.Token,
		AnnounceChannel:              order.AnnounceChannel,
		CreatedAt:                    order.CreatedAt.Format(time.RFC3339),
		OrderState:                   respOrderState,
		Payment: &Lsps1Payment{
			Bolt11: &Lsps1PaymentBolt11{
				State:          paymentState,
				ExpiresAt:      expiry.Format(time.RFC3339),
				FeeTotalLoki:   fmt.Sprintf("%d", order.FeeTotalLoki),
				OrderTotalLoki: fmt.Sprintf("%d", order.OrderTotalLoki),
				Invoice:        order.PaymentInvoice,
			},
		},
		Channel: channelInfo,
	}

	log.Trace().
		Str("component", string(logging.LogComponentLSPS1)).
		Str("action", string(logging.LogActionResponse)).
		Msgf("LSPS1: get_order result: %+v", resp)
	return NewJsonRpcResponse(req.Id, resp)
}
