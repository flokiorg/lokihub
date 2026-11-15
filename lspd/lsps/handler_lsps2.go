package lsps

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/flokiorg/lokihub/lspd/common"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/rs/zerolog/log"
)

// lsps2RegistrationMaxTTL bounds how long an LSPS2 buy registration can
// stay ACTIVE regardless of the quote's own valid_until (Phase 7, F-21):
// before this existed, a registration never expired at all, no matter how
// far out its quoted valid_until was set.
const lsps2RegistrationMaxTTL = 24 * time.Hour

type Lsps2GetInfoLegacyParams struct {
	Token string `json:"token"`
}

type Lsps2OpeningFeeParams struct {
	MinFeeMloki          string `json:"min_fee_mloki"`
	Proportional         uint32 `json:"proportional"`
	ValidUntil           string `json:"valid_until"`
	MinLifetime          uint32 `json:"min_lifetime"`
	MaxClientToSelfDelay uint32 `json:"max_client_to_self_delay"`
	MinPaymentSizeMloki  string `json:"min_payment_size_mloki"`
	MaxPaymentSizeMloki  string `json:"max_payment_size_mloki"`
	Promise              string `json:"promise"`
}

type Lsps2BuyParams struct {
	OpeningFeeParams Lsps2OpeningFeeParams `json:"opening_fee_params"`
	PaymentSizeMloki string                `json:"payment_size_mloki,omitempty"`
}

type Lsps2BuyResult struct {
	JITChannelScid     string `json:"jit_channel_scid"`
	LspCltvExpiryDelta uint16 `json:"lsp_cltv_expiry_delta"`
	ClientTrustsLsp    bool   `json:"client_trusts_lsp"`
	LspNodeId          string `json:"lsp_node_id"`
}

func (s *Handlers) handleLsps2GetVersions(req *JsonRpcRequest) *JsonRpcResponse {
	log.Trace().
		Str("component", string(logging.LogComponentLSPS2)).
		Str("action", string(logging.LogActionRequest)).
		Msg("LSPS2: Received get_versions request")
	result := map[string][]int{
		"versions": {1},
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS2)).
		Str("action", string(logging.LogActionResponse)).
		Msgf("LSPS2: get_versions result: %+v", result)
	return NewJsonRpcResponse(req.Id, result)
}

func (s *Handlers) lsps2GetInfo(req *JsonRpcRequest) *JsonRpcResponse {
	return s.lsps2GetInfoMenu(req, false)
}

// lsps2GetInfoMenu answers get_info. A token whose menu cannot be read is
// left out for a client (the others still sell); with strict, as the
// announcement asks (F-L4), it fails the answer instead, so a transient
// error never publishes a shorter menu.
func (s *Handlers) lsps2GetInfoMenu(req *JsonRpcRequest, strict bool) *JsonRpcResponse {
	var params Lsps2GetInfoLegacyParams
	// params might be empty or null
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS2)).
		Str("action", string(logging.LogActionRequest)).
		Interface("params", params).
		Msg("LSPS2: Received get_info request")

	nodes := s.nodesService.GetNodes()
	var menu []*Lsps2OpeningFeeParams

	// Gather fee params from ALL configured nodes (policies)
	// If a specific token was requested, we could filter, but LSPS2 get_info implies
	// returning the available options. FLSPD associates options with tokens.
	// We will return all valid options from all nodes.
	for _, node := range nodes {
		// If the request provided a token, and it doesn't match this node, skip?
		// The client might provide a token to "login" or identify partially.
		// If provided token matches one of the node's tokens, we prioritize it?
		// For now, let's include all.
		// Note: node.Tokens is []string.
		if params.Token != "" {
			found := false
			for _, t := range node.Tokens {
				if t == params.Token {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Use the first token of the node to fetch fee params?
		// OpeningService.GetFeeParamsMenu takes a token.
		// We should iterate over node.Tokens?
		for _, token := range node.Tokens {
			fees, err := s.openingService.GetFeeParamsMenu(token, node.PrivateKey)
			if err != nil {
				log.Warn().
					Err(err).
					Str("component", string(logging.LogComponentLSPS2)).
					Str("action", string(logging.LogActionFailure)).
					Str("token", token).
					Msg("LSPS2: Failed to get fee params for token")
				if strict {
					return NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
				}
				continue
			}

			for _, fee := range fees {
				item := &Lsps2OpeningFeeParams{
					MinFeeMloki:          fmt.Sprintf("%d", fee.MinFeeMloki),
					Proportional:         fee.Proportional,
					ValidUntil:           fee.ValidUntil,
					MinLifetime:          fee.MinLifetime,
					MaxClientToSelfDelay: fee.MaxClientToSelfDelay,
					MinPaymentSizeMloki:  fmt.Sprintf("%d", fee.MinPaymentSizeMloki),
					MaxPaymentSizeMloki:  fmt.Sprintf("%d", fee.MaxPaymentSizeMloki),
					Promise:              fee.Promise,
				}
				menu = append(menu, item)
			}
		}
	}

	log.Trace().
		Str("component", string(logging.LogComponentLSPS2)).
		Str("action", string(logging.LogActionResponse)).
		Msgf("LSPS2: get_info success, menu items=%d", len(menu))
	return NewJsonRpcResponse(req.Id, map[string]interface{}{
		"opening_fee_params_menu": menu,
	})
}

func (s *Handlers) lsps2Buy(req *JsonRpcRequest, peer []byte) *JsonRpcResponse {
	// Phase 7 (F-17/T7-7): checked before anything else in this handler --
	// including promise-signature verification, a real cryptographic
	// operation this method otherwise runs unconditionally on every call --
	// so a spam burst from one peer never reaches that work, let alone the
	// DB/RPC calls further down.
	peerId := hex.EncodeToString(peer)
	if !s.burstLimiter.Allow(peerId) {
		return NewJsonRpcErrorResponse(req.Id, -32603, "Rate limit exceeded, please retry later", nil)
	}

	// Phase 7 (F-17/T7-7): standing per-peer cap on live registrations,
	// mirroring lsps1.create_order's CountLsps1Orders check below. Unlike
	// the burst limiter (in-memory, resets on restart), this reads the
	// durable DB state, so it holds even across a spread-out attack that
	// stays under the burst window.
	if s.config.Lsps2MaxPendingRegistrations > 0 {
		pendingCount, err := s.openingService.CountLsps2PendingRegistrations(peerId)
		if err == nil && pendingCount >= s.config.Lsps2MaxPendingRegistrations {
			return NewJsonRpcErrorResponse(req.Id, -32603, fmt.Sprintf("Too many pending registrations (max %d)", s.config.Lsps2MaxPendingRegistrations), nil)
		}
	}

	log.Trace().Msgf("LSPS2: lsps2Buy raw params: %s", string(req.Params))
	var params Lsps2BuyParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		log.Trace().
			Err(err).
			Str("component", string(logging.LogComponentLSPS2)).
			Str("action", string(logging.LogActionValidate)).
			Msg("LSPS2: buy invalid params")
		return NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params", nil)
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS2)).
		Str("action", string(logging.LogActionRequest)).
		Interface("params", params).
		Msg("LSPS2: Received buy request")

	// Validate params
	minFee, _ := strconv.ParseUint(params.OpeningFeeParams.MinFeeMloki, 10, 64)
	commonParams := &core.OpeningFeeParams{
		MinFeeMloki:          minFee,
		Proportional:         params.OpeningFeeParams.Proportional,
		ValidUntil:           params.OpeningFeeParams.ValidUntil,
		MinLifetime:          params.OpeningFeeParams.MinLifetime,
		MaxClientToSelfDelay: params.OpeningFeeParams.MaxClientToSelfDelay,
		// These fields are not in core.OpeningFeeParams, but needed for paramsHash if we updated it?
		// Wait, we updated core.OpeningFeeParams to include them. We need to parse them from request params.
		MinPaymentSizeMloki: parseUint64(params.OpeningFeeParams.MinPaymentSizeMloki),
		MaxPaymentSizeMloki: parseUint64(params.OpeningFeeParams.MaxPaymentSizeMloki),
		Promise:             params.OpeningFeeParams.Promise,
	}

	nodes := s.nodesService.GetNodes()
	var validNode *core.Node
	var validToken string

Loop:
	for _, node := range nodes {
		if s.openingService.ValidateOpeningFeeParams(commonParams, node.PublicKey) {
			validNode = node
			if len(node.Tokens) > 0 {
				validToken = node.Tokens[0]
			}
			break Loop
		}
	}

	if validNode == nil {
		return NewJsonRpcErrorResponse(req.Id, 100, "Invalid promise or expired params", nil)
	}
	// Snapshot once so this request sees one consistent config even if the
	// dashboard updates it concurrently (C-3).
	validCfg := validNode.NodeConfig.Load()

	scid, err := generateRandomScid()
	if err != nil {
		return NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}

	var paymentSize *uint64
	if params.PaymentSizeMloki != "" {
		size, _ := strconv.ParseUint(params.PaymentSizeMloki, 10, 64)
		paymentSize = &size

		// Validate against min/max
		if *paymentSize < commonParams.MinPaymentSizeMloki || *paymentSize > commonParams.MaxPaymentSizeMloki {
			return NewJsonRpcErrorResponse(req.Id, 203, "Payment size out of bounds", nil)
		}
	}

	// F-22: every registration must be checked against SOME bound, even the
	// spec-legal "variable size, single HTLC" mode where the client omits
	// payment_size_mloki entirely. Before this fix, that mode skipped every
	// capacity/liquidity check below, and at redemption the JIT forward
	// amount silently computed as zero -- the LSP would open a real,
	// fully-funded channel and then forward a guaranteed-to-fail $0 HTLC.
	// worstCaseMloki is the largest amount this registration could ever be
	// redeemed for: the client's own requested size if given, else the
	// quote's own MaxPaymentSizeMloki ceiling.
	worstCaseMloki := commonParams.MaxPaymentSizeMloki
	if paymentSize != nil {
		worstCaseMloki = *paymentSize
	}
	if worstCaseMloki == 0 {
		return NewJsonRpcErrorResponse(req.Id, 203, "No usable payment size bound (payment_size_mloki not set and quote has no max_payment_size_mloki)", nil)
	}

	// Compute Opening Fee and the net (outgoing) amount for the worst case,
	// mirroring the gross-minus-fee relationship the interceptor's own
	// computeJitOutgoingAmount uses at redemption time -- needed for the
	// BOLT2 reserve-headroom term in RequiredCapacityLoki below.
	openingFee, err := ComputeOpeningFee(worstCaseMloki, commonParams.MinFeeMloki, uint64(commonParams.Proportional))
	if err != nil {
		return NewJsonRpcErrorResponse(req.Id, 203, "Fee computation overflow", nil)
	}
	if openingFee >= worstCaseMloki {
		return NewJsonRpcErrorResponse(req.Id, 202, "Payment size too small (fee >= amount)", nil)
	}
	worstOutgoingMloki := worstCaseMloki - openingFee

	requiredCapacity := config.RequiredCapacityLoki(worstCaseMloki/1000, worstOutgoingMloki/1000, validCfg, true)

	// Enforce Physical Floor and (JIT) Business Floor -- same predicate the
	// interceptor's openChannel checks before it ever gets there.
	if !config.MeetsCapacityFloors(requiredCapacity, validCfg, true) {
		physicalFloor := s.nodesService.GetMinChannelCapacity()
		businessFloor := uint64(0)
		if validCfg.JitMinChannelBalanceLoki != nil {
			businessFloor = *validCfg.JitMinChannelBalanceLoki
		}
		return NewJsonRpcErrorResponse(req.Id, 203, fmt.Sprintf("Payment size too small even with padding (physical min %d, business min %d)", physicalFloor, businessFloor), nil)
	}

	// Validation: Check On-Chain Liquidity against the real committed
	// capacity (F-16/F-24), not the raw payment size.
	if resp := s.checkFleetCeilingAndLiquidity(req, logging.LogComponentLSPS2, requiredCapacity, 203, "Payment size too large: insufficient liquidity"); resp != nil {
		return resp
	}

	regID, _ := generateUUID()

	// Phase 7 (F-15/F-19/F-21/T7-1/T7-2): a registration now carries its
	// own expiry and reservation, and starts life ACTIVE (not yet
	// consumed). ExpiresAt is bounded by both the quote's own valid_until
	// AND a hard ceiling, so a registration can never outlive a sane bound
	// even if a client somehow obtained a very long-lived quote.
	now := time.Now().UTC()
	expiresAt := now.Add(lsps2RegistrationMaxTTL)
	if validUntil, err := time.Parse(common.TimeFormat, commonParams.ValidUntil); err == nil && validUntil.Before(expiresAt) {
		expiresAt = validUntil
	}

	// F-20/T7-5: re-check liquidity and persist atomically under an
	// advisory lock, immediately before committing -- closes the same
	// TOCTOU race as LSPS1's create_order (see its handler for the full
	// rationale).
	reserved, err := s.liquidityManager.ReserveLsps2Buy(context.Background(), &core.Lsps2BuyRegistration{
		Id:                         regID,
		LspId:                      hex.EncodeToString(validNode.PublicKey.SerializeCompressed()),
		PeerId:                     peerId,
		Scid:                       scid,
		Mode:                       0,
		PaymentSizeMloki:           paymentSize,
		ParamsMinFeeMloki:          commonParams.MinFeeMloki,
		ParamsProportional:         commonParams.Proportional,
		ParamsValidUntil:           commonParams.ValidUntil,
		ParamsMinLifetime:          commonParams.MinLifetime,
		ParamsMaxClientToSelfDelay: commonParams.MaxClientToSelfDelay,
		ParamsPromise:              commonParams.Promise,
		Token:                      validToken,
		CreatedAt:                  now,
		ExpiresAt:                  expiresAt,
		ReservedAmountLoki:         requiredCapacity,
		State:                      core.Lsps2RegistrationActive,
		// F-22 (redemption-time half): persist the same worst-case bound
		// the reservation itself was sized against, so the interceptor's
		// resolvePayment can enforce it at redemption -- not just here at
		// buy time -- in variable-size mode, where the real HTLC amount
		// isn't known until redemption.
		MaxPaymentSizeMloki: &worstCaseMloki,
	})
	if err == nil && !reserved {
		return NewJsonRpcErrorResponse(req.Id, 203, "Payment size too large: insufficient liquidity", nil)
	}
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS2)).
			Str("action", string(logging.LogActionFailure)).
			Msg("LSPS2: Failed to register buy in database")
		return NewJsonRpcErrorResponse(req.Id, -32603, "Internal DB error", nil)
	}

	result := Lsps2BuyResult{
		JITChannelScid:     formatScid(scid),
		LspCltvExpiryDelta: uint16(validCfg.TimeLockDelta),
		ClientTrustsLsp:    false,
		LspNodeId:          hex.EncodeToString(validNode.NodeId),
	}
	log.Trace().Msgf("LSPS2: lsps2Buy result: %+v", result)

	return NewJsonRpcResponse(req.Id, result)
}
