package lsps

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/rs/zerolog/log"
)

// LndClientWrapper is the subset of the lnd client Handlers itself needs
// directly -- issuing the bolt11 invoice an LSPS1 order is paid against.
// The message-transport methods (SubscribeCustomMessages/SendCustomMessage)
// live on transport/lsps's own, narrower interface instead: Handlers never
// touches the wire, only what a JSON-RPC method handler needs to do its
// job.
type LndClientWrapper interface {
	AddInvoice(ctx context.Context, req *lnrpc.Invoice) (*lnrpc.AddInvoiceResponse, error)
	// AddHoldInvoice is used instead of AddInvoice for new LSPS1 orders
	// (T-hold-invoice): it lets settlement hold the payment until the
	// channel open actually succeeds, so a failed order can be cancelled
	// -- an instant, on-chain-fee-free refund -- instead of requiring the
	// slow RefundOnchainAddress path.
	AddHoldInvoice(ctx context.Context, req *invoicesrpc.AddHoldInvoiceRequest) (*invoicesrpc.AddHoldInvoiceResp, error)
	// CancelInvoice releases a hold invoice's HTLCs. Used as a compensating
	// action (H-7) if AddHoldInvoice succeeds but the order's DB write
	// subsequently fails: without it, the invoice would be orphaned at LND
	// with its preimage lost (never persisted), stuck until LND's own
	// hold-invoice-expiry watcher eventually releases it.
	CancelInvoice(ctx context.Context, paymentHash []byte) error
}

// NotificationService interface for sending LSPS5 webhook notifications
type NotificationService interface {
	NotifyWebhookRegistered(pubkey string, webhook *core.Lsps5Webhook) bool
	NotifyOrderStateChanged(pubkey string, orderId string, state string, channelPoint string, errorMsg string) (bool, error)
}

// OrderWatcher attaches a live per-invoice watcher to a freshly-created
// hold-invoice order (H-12), so its ACCEPTED transition is observed
// near-instantly instead of waiting on the safety-net poll.
// *Lsps1SettlementService satisfies this directly. Nil-safe by convention:
// Handlers checks for a nil orderWatcher before calling it, since not
// every construction site (most tests, in particular) needs live
// watching.
type OrderWatcher interface {
	WatchOrder(order *core.Lsps1Order)
}

type Config struct {
	Lsps1MaxPendingOrders int
	// Lsps2MaxPendingRegistrations mirrors Lsps1MaxPendingOrders for LSPS2
	// buy registrations (Phase 7, F-17): before this existed, lsps2.buy
	// had no per-peer standing cap at all, unlike lsps1.create_order.
	Lsps2MaxPendingRegistrations int
	// MaxGlobalPendingReservationsPerNode (loki) is the fleet-wide ceiling
	// on total live reservations across BOTH protocols and ALL peers
	// (Phase 7, F-17): a per-peer cap alone is Sybil-able -- a fresh BOLT8
	// handshake with a new keypair costs nothing and needs no on-chain
	// action -- so this is the load-bearing limit against that. Checked
	// against the same GetReservedLiquidity sum CheckOnChainLiquidity
	// already reads. Zero means "no fleet ceiling enforced."
	MaxGlobalPendingReservationsPerNode uint64
	Lsps5MaxWebhooks                    int
	EnableLsps5Https                    bool   // Enable HTTPS webhook transport
	EnableLsps5Nostr                    bool   // Enable Nostr notification transport
	Lsps5NostrPubkey                    string // Hex-encoded Nostr public key for notifications
	// Lsps5AllowLoopbackWebhooks admits plain-http webhooks on localhost /
	// 127.0.0.1. Local development and tests only; never enable it on a
	// node reachable by untrusted peers.
	Lsps5AllowLoopbackWebhooks bool
}

// Handlers implements the LSPS0/1/2/5 protocol methods: parsing a
// method's params, applying its business rules, and returning a result or
// error -- everything downstream of "a JSON-RPC request already arrived
// addressed to this method name." How that request arrived (the
// lnd-custom-message subscription loop, JSON-RPC envelope parsing, method
// dispatch) is transport/lsps's job, not this type's; see Route below for
// the one seam between them.
type Handlers struct {
	config              Config
	lndClient           LndClientWrapper
	nodesService        ports.NodesService
	openingService      ports.OpeningService
	liquidityManager    LiquidityManager
	notificationService NotificationService
	orderWatcher        OrderWatcher
	// burstLimiter is the cheapest, first line of defense against a spam
	// burst from one peer (Phase 7, F-17/T7-7) -- an in-memory, per-peer
	// counter checked before ANY DB/RPC work, including promise-signature
	// verification (a real cryptographic operation lsps2.buy otherwise
	// runs unconditionally on every call). Not durable across restarts by
	// design -- the DB-backed standing cap and fleet ceiling below it are
	// the restart-durable layers; this one only needs to shape bursts
	// within one process's uptime.
	burstLimiter *peerBurstLimiter
	// readLimiter is a second, looser per-peer shaper applied to EVERY method
	// at the top of Route -- including the read methods (lsps2.get_info,
	// lsps1.get_info/get_order, lsps0.*, lsps5 reads) that burstLimiter leaves
	// unguarded. A no-channel peer flooding lsps2.get_info otherwise drives
	// N_nodes x N_tokens x N_settings ECDSA signs per call; this caps the rate
	// before any GetNodes/signing work runs.
	readLimiter *peerBurstLimiter
}

// BurstLimiter exposes the per-peer burst shaper so callers (cmd/flspd's
// startup wiring) can run its periodic eviction loop -- see
// peerBurstLimiter.StartEvictionLoop.
func (s *Handlers) BurstLimiter() *peerBurstLimiter {
	return s.burstLimiter
}

// ReadLimiter exposes the per-peer read shaper so the startup wiring can run
// its periodic eviction loop -- see peerBurstLimiter.StartEvictionLoop.
func (s *Handlers) ReadLimiter() *peerBurstLimiter {
	return s.readLimiter
}

// checkFleetCeilingAndLiquidity enforces the fleet-wide pending-reservation
// ceiling (Phase 7, F-17/T7-7) AND the plain on-chain-liquidity-sufficiency
// check together, from ONE GetLiquiditySnapshot fetch -- before this was
// unified, each handler independently called GetReservedOnChainLiquidity
// for the fleet ceiling and then CheckOnChainLiquidity (its own separate
// wallet-balance + reserved-liquidity fetch) right after, doubling both
// round trips for every request once the fleet ceiling was enabled.
//
// The fleet ceiling is a Sybil-resistant cap a per-peer standing cap alone
// can't provide, since a fresh BOLT8 keypair costs nothing and needs no
// on-chain action; zero means no fleet ceiling configured. Both checks are
// the same cheap, non-atomic fast-fail character as before (good UX, not
// the final word) -- the advisory-locked Reserve* re-check is still the
// only atomicity guarantee. A snapshot-fetch failure now rejects the
// request outright (matching the liquidity check's own pre-existing
// fail-closed behavior) rather than the fleet-ceiling half's old
// fail-open-on-error behavior -- a deliberate, safe simplification: both
// figures come from the same underlying queries, so if one would have
// failed, the other was already very likely to as well, and rejecting is
// always the safe direction for a financial gate.
//
// insufficientLiquidityCode/insufficientLiquidityMsg let each protocol
// keep its own pre-existing error code/wording for that case (LSPS1 and
// LSPS2 have always differed here); the fleet-ceiling rejection message is
// already shared, since it's a new check with no prior API contract to
// preserve. Returns nil if the request may proceed.
func (s *Handlers) checkFleetCeilingAndLiquidity(req *JsonRpcRequest, component logging.LogComponent, requestedCapacity uint64, insufficientLiquidityCode int, insufficientLiquidityMsg string) *JsonRpcResponse {
	snap, err := s.liquidityManager.GetLiquiditySnapshot(context.Background())
	if err != nil {
		log.Warn().
			Err(err).
			Str("component", string(component)).
			Str("action", string(logging.LogActionReject)).
			Msg("Request rejected - failed to check on-chain liquidity")
		return NewJsonRpcErrorResponse(req.Id, insufficientLiquidityCode, insufficientLiquidityMsg, nil)
	}

	if s.config.MaxGlobalPendingReservationsPerNode > 0 && snap.ReservedLoki+requestedCapacity > s.config.MaxGlobalPendingReservationsPerNode {
		log.Warn().
			Str("component", string(component)).
			Str("action", string(logging.LogActionReject)).
			Uint64("reserved_total", snap.ReservedLoki).
			Uint64("requested_capacity", requestedCapacity).
			Uint64("fleet_ceiling", s.config.MaxGlobalPendingReservationsPerNode).
			Msg("Request rejected - fleet-wide pending reservation ceiling reached")
		return NewJsonRpcErrorResponse(req.Id, 203, "LSP at capacity, please retry later", nil)
	}

	if requestedCapacity > snap.AvailableLoki() {
		log.Warn().
			Str("component", string(component)).
			Str("action", string(logging.LogActionReject)).
			Msg("Request rejected - insufficient on-chain liquidity")
		return NewJsonRpcErrorResponse(req.Id, insufficientLiquidityCode, insufficientLiquidityMsg, nil)
	}

	return nil
}

// NewHandlers builds the handlers of one node; nodesService must hold only
// that node, since every answer and sale is read from the nodes it returns.
func NewHandlers(
	config Config,
	lndClient LndClientWrapper,
	nodesService ports.NodesService,
	openingService ports.OpeningService,
	liquidityManager LiquidityManager,
	notificationService NotificationService,
	orderWatcher OrderWatcher,
) *Handlers {
	return &Handlers{
		config:              config,
		lndClient:           lndClient,
		nodesService:        nodesService,
		openingService:      openingService,
		liquidityManager:    liquidityManager,
		notificationService: notificationService,
		orderWatcher:        orderWatcher,
		burstLimiter:        newPeerBurstLimiter(defaultBurstLimit, defaultBurstWindow),
		readLimiter:         newPeerBurstLimiter(defaultReadLimit, defaultReadWindow),
	}
}

// JSON-RPC Types
type JsonRpcRequest struct {
	JsonRpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Id      interface{}     `json:"id"`
}

type JsonRpcResponse struct {
	JsonRpc string        `json:"jsonrpc"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JsonRpcError `json:"error,omitempty"`
	Id      interface{}   `json:"id"`
}

type JsonRpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func NewJsonRpcResponse(id interface{}, result interface{}) *JsonRpcResponse {
	return &JsonRpcResponse{
		JsonRpc: "2.0",
		Id:      id,
		Result:  result,
	}
}

func NewJsonRpcErrorResponse(id interface{}, code int, message string, data interface{}) *JsonRpcResponse {
	// For LSPS0 compatibility, -32602 (Invalid params) must include an 'unrecognized' array.
	if code == -32602 && data == nil {
		data = map[string]interface{}{
			"unrecognized": []string{},
		}
	}

	return &JsonRpcResponse{
		JsonRpc: "2.0",
		Id:      id,
		Error: &JsonRpcError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

// Route dispatches an already-decoded JSON-RPC request to the handler for
// its method, by name. The only seam between transport/lsps (which owns
// the lnd-custom-message subscription loop and JSON-RPC envelope
// marshal/unmarshal) and Handlers (this type): transport/lsps calls Route
// once it has a *JsonRpcRequest, and does nothing else with the method
// name itself.
func (s *Handlers) Route(req *JsonRpcRequest, peer []byte) *JsonRpcResponse {
	// Every method -- reads included -- passes a generous per-peer read limiter
	// before any work, so a peer flooding an unauthenticated read like
	// lsps2.get_info cannot drive unbounded ECDSA signing / DB reads. This sits
	// above the lsps2. protocol gate below, which already calls GetNodes.
	if !s.readLimiter.Allow(hex.EncodeToString(peer)) {
		return NewJsonRpcErrorResponse(req.Id, -32603, "Rate limit exceeded, please retry later", nil)
	}
	// A protocol this node does not list in list_protocols is not served,
	// so the live answers match the listed (and announced) protocols: LSPS2
	// while JIT channels are off (F8-Q1).
	if strings.HasPrefix(req.Method, "lsps2.") && !s.offers(2) {
		return NewJsonRpcErrorResponse(req.Id, -32601, "Method not found", nil)
	}
	switch req.Method {
	case "lsps0.list_protocols":
		return s.handleLsps0ListProtocols(req)
	case "lsps0.get_info":
		return s.handleLsps0GetInfo(req)
	case "lsps2.get_versions":
		return s.handleLsps2GetVersions(req)
	case "lsps2.get_info":
		return s.lsps2GetInfo(req)
	case "lsps2.buy":
		return s.lsps2Buy(req, peer)
	case "lsps1.get_info":
		return s.handleLsps1GetInfo(req)
	case "lsps1.create_order":
		return s.handleLsps1CreateOrder(req, peer)
	case "lsps1.get_order":
		return s.handleLsps1GetOrder(req)
	case "lsps5.set_webhook":
		return s.handleLsps5SetWebhook(req, peer)
	case "lsps5.list_webhooks":
		return s.handleLsps5ListWebhooks(req, peer)
	case "lsps5.remove_webhook":
		return s.handleLsps5RemoveWebhook(req, peer)
	default:
		return NewJsonRpcErrorResponse(req.Id, -32601, "Method not found", nil)
	}
}

// offers reports whether protocol is in SupportedProtocols right now.
func (s *Handlers) offers(protocol int) bool {
	for _, p := range s.SupportedProtocols() {
		if p == protocol {
			return true
		}
	}
	return false
}

func generateRandomScid() (uint64, error) {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		return 0, err
	}
	raw := binary.BigEndian.Uint64(b)

	// Phantom SCIDs must be in LND's alias block-height range [16,000,000, 16,250,000)
	// so that XAddLocalChanAliases can register them after a JIT channel opens,
	// allowing the switch to route the resumed HTLC to the newly opened channel.
	const aliasStart = uint64(16_000_000)
	const aliasRange = uint64(250_000)

	blockHeight := aliasStart + (raw>>40)%aliasRange
	lower40 := raw & 0xFF_FFFF_FFFF // 24-bit tx index + 16-bit output index

	return (blockHeight << 40) | lower40, nil
}

func generateUUID() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func parseUint64(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

// ComputeOpeningFee is the single implementation of the LSPS1/LSPS2
// channel-opening fee (F-5): proportional * paymentSizeMloki, rounded up
// to the nearest mloki, floored at minFeeMloki. Exported so callers
// outside this package (e.g. the `flspd config explain fees` CLI) can
// reuse the exact same math flspd itself uses to quote a fee, rather than
// re-deriving it.
func ComputeOpeningFee(paymentSizeMloki, minFeeMloki, proportional uint64) (uint64, error) {
	var prod uint64
	if paymentSizeMloki != 0 {
		prod = paymentSizeMloki * proportional
		if prod/paymentSizeMloki != proportional {
			return 0, fmt.Errorf("overflow in multiplication")
		}
	} else {
		prod = 0
	}

	sum := prod + 999999
	if sum < prod {
		return 0, fmt.Errorf("overflow in addition")
	}

	openingFee := sum / 1000000

	if openingFee < minFeeMloki {
		openingFee = minFeeMloki
	}
	return openingFee, nil
}

func formatScid(scid uint64) string {
	block := scid >> 40
	tx := (scid >> 16) & 0xFFFFFF
	output := scid & 0xFFFF
	return fmt.Sprintf("%dx%dx%d", block, tx, output)
}
