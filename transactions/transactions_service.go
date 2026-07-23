package transactions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/utils"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/rs/zerolog"
)

type transactionsService struct {
	db               *gorm.DB
	eventPublisher   events.EventPublisher
	liquidityManager *manager.LiquidityManager
	logger           zerolog.Logger
}

// InternalMakeInvoiceMeta carries trusted caller context to MakeInvoice.
// It is never populated from NIP-47 or HTTP request params — untrusted callers pass nil.
type InternalMakeInvoiceMeta struct {
	// OverrideAppID, when non-nil, replaces the appId parameter.
	// Used only by the Transfer endpoint to route an invoice to a specific sub-wallet.
	OverrideAppID *uint
	// InternalTransfer marks the invoice as a hub-internal fund movement,
	// skipping JIT liquidity provisioning and external fee checks.
	InternalTransfer bool
}

type TransactionsService interface {
	events.EventSubscriber
	MakeInvoice(ctx context.Context, amount uint64, description string, descriptionHash string, expiry uint64, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint, throughNodePubkey *string, lspJitChannelSCID *string, lspCltvExpiryDelta *uint16, lspFeeBaseMloki *uint64, lspFeeProportionalMillionths *uint32, internal *InternalMakeInvoiceMeta) (*Transaction, error)
	LookupTransaction(ctx context.Context, paymentHash string, transactionType *string, lnClient lnclient.LNClient, appId *uint) (*Transaction, error)
	ListTransactions(ctx context.Context, from, until, limit, offset uint64, unpaidOutgoing bool, unpaidIncoming bool, transactionType *string, lnClient lnclient.LNClient, appId *uint, forceFilterByAppId bool) (transactions []Transaction, totalCount uint64, err error)
	SendPaymentSync(payReq string, amountMloki *uint64, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*Transaction, error)
	SendKeysend(amount uint64, destination string, customRecords []lnclient.TLVRecord, preimage string, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*Transaction, error)
	MakeHoldInvoice(ctx context.Context, amount uint64, description string, descriptionHash string, expiry uint64, paymentHash string, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*Transaction, error)
	SettleHoldInvoice(ctx context.Context, preimage string, lnClient lnclient.LNClient) (*Transaction, error)
	CancelHoldInvoice(ctx context.Context, paymentHash string, lnClient lnclient.LNClient) error
	SetTransactionMetadata(ctx context.Context, id uint, metadata map[string]interface{}) error
	SetLiquidityManager(lm *manager.LiquidityManager)
	EstimateFee(payReq string) (uint64, error)
	SweepStalePendingOutgoing(ctx context.Context, lnClient lnclient.LNClient)
}

const (
	BoostagramTlvType = 7629169
	WhatsatTlvType    = 34349334
	CustomKeyTlvType  = 696969
)

// Payment atomicity is enforced at the database level:
//   - SQLite: _txlock=immediate acquires a write lock at the start of every
//     db.Transaction(), serialising all concurrent payment attempts.
//   - PostgreSQL: pg_advisory_xact_lock(appId) is acquired at the start of each
//     payment transaction, serialising concurrent payments to the same app.
//     The lock is released automatically on transaction commit or rollback.

type Transaction = db.Transaction

type Boostagram struct {
	AppName         string         `json:"app_name"`
	Name            string         `json:"name"`
	Podcast         string         `json:"podcast"`
	URL             string         `json:"url"`
	Episode         StringOrNumber `json:"episode,omitempty"`
	FeedId          StringOrNumber `json:"feedID,omitempty"`
	ItemId          StringOrNumber `json:"itemID,omitempty"`
	Timestamp       int64          `json:"ts,omitempty"`
	Message         string         `json:"message,omitempty"`
	SenderId        StringOrNumber `json:"sender_id"`
	SenderName      string         `json:"sender_name"`
	Time            string         `json:"time"`
	Action          string         `json:"action"`
	ValueMlokiTotal int64          `json:"value_msat_total"` // Podcasting 2.0 standard field name
}

type StringOrNumber struct {
	StringData string
	NumberData int64
}

func (sn *StringOrNumber) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &sn.StringData); err == nil {
		return nil
	}

	if err := json.Unmarshal(data, &sn.NumberData); err == nil {
		return nil
	}

	return fmt.Errorf("cannot unmarshal %s into StringOrNumber type", data)
}

func (sn StringOrNumber) String() string {
	if sn.StringData != "" {
		return sn.StringData
	}
	return fmt.Sprintf("%d", sn.NumberData)
}

type notFoundError struct {
}

func NewNotFoundError() error {
	return &notFoundError{}
}

func (err *notFoundError) Error() string {
	return "The transaction requested was not found"
}

type insufficientBalanceError struct {
}

func NewInsufficientBalanceError() error {
	return &insufficientBalanceError{}
}

func (err *insufficientBalanceError) Error() string {
	return "Insufficient balance remaining to make the requested payment"
}

type quotaExceededError struct {
}

func NewQuotaExceededError() error {
	return &quotaExceededError{}
}

func (err *quotaExceededError) Error() string {
	return "Your app does not have enough budget remaining to make this payment. Please review this app in the connections page of your Lokihub."
}

type jitPartialSpendError struct{}

func NewJITPartialSpendError() error {
	return &jitPartialSpendError{}
}

func (err *jitPartialSpendError) Error() string {
	return "JIT wallet must be drained in a single payment (no partial spends allowed)"
}

// enforceJITFullDrain returns jitPartialSpendError if a JIT wallet payment would
// leave more than its fee reserve behind. Shared by SendPaymentSync and
// SendKeysend so the two payment paths can't silently diverge on this check —
// they previously duplicated it, and only one of the two copies carried the
// internal-transfer exemption. isInternalTransfer and isJITClaimSlice are
// explicit at every call site (SendKeysend currently has neither path and
// passes false for both) so that a future addition to either path only needs
// to thread its own metadata flag through, not rediscover this rule.
//
// isJITClaimSlice exempts claim_funds' payout of one recipient's slice of a
// SHARED jit_wallet (see nip47/controllers/claim_funds_controller.go). This
// whole-wallet-balance check is wrong for that case: it would reject a
// recipient's payout whenever OTHER recipients' unclaimed slices are still
// sitting in the same balance. claim_funds already enforces the correct,
// stronger, per-slice exact-amount rule itself before calling
// SendPaymentSync/SendKeysend with this flag set — this check only ever
// exists to catch partial spends on an otherwise-unconstrained payment path.
func enforceJITFullDrain(parentKind string, balance int64, amount uint64, isInternalTransfer, isJITClaimSlice bool) error {
	if parentKind != db.ParentKindJIT || isInternalTransfer || isJITClaimSlice {
		return nil
	}
	feeReserve := CalculateFeeReserveMloki(amount)
	// amount was already validated against balance by validateCanPay before
	// this is called; balance/feeReserve are LN mloki amounts, always far
	// below int64 range.
	if balance-int64(amount) > int64(feeReserve) { //nolint:gosec
		return NewJITPartialSpendError()
	}
	return nil
}

func NewTransactionsService(db *gorm.DB, eventPublisher events.EventPublisher) *transactionsService {
	return &transactionsService{
		db:             db,
		eventPublisher: eventPublisher,
		logger:         logger.Logger.With().Str("component", "transactions").Logger(),
	}
}

func (svc *transactionsService) SetLiquidityManager(lm *manager.LiquidityManager) {
	svc.liquidityManager = lm
}

func (svc *transactionsService) MakeInvoice(ctx context.Context, amount uint64, description string, descriptionHash string, expiry uint64, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint, throughNodePubkey *string, lspJitChannelSCID *string, lspCltvExpiryDelta *uint16, lspFeeBaseMloki *uint64, lspFeeProportionalMillionths *uint32, internal *InternalMakeInvoiceMeta) (*Transaction, error) {
	svc.logger.Debug().
		Interface("app_id", appId).
		Interface("request_event_id", requestEventId).
		Uint64("amount", amount).
		Str("description", description).
		Str("description_hash", descriptionHash).
		Uint64("expiry", expiry).
		Interface("metadata", metadata).
		Msg("Making invoice")

	var metadataBytes []byte
	if metadata != nil {
		var err error
		metadataBytes, err = json.Marshal(metadata)
		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to serialize metadata")
			return nil, err
		}
		if len(metadataBytes) > constants.INVOICE_METADATA_MAX_LENGTH {
			return nil, fmt.Errorf("encoded invoice metadata provided is too large. Limit: %d Received: %d", constants.INVOICE_METADATA_MAX_LENGTH, len(metadataBytes))
		}
	}

	// Apply trusted caller overrides. Sanitize the user-supplied metadata map to
	// prevent any injected "app_id" or "internal_transfer" keys from having effect.
	if metadata != nil {
		delete(metadata, "app_id")
		delete(metadata, "internal_transfer")
	}
	isInternalTransfer := false
	if internal != nil {
		if internal.OverrideAppID != nil {
			svc.logger.Info().Uint("app_id", *internal.OverrideAppID).Msg("Making invoice with overwritten app ID")
			appId = internal.OverrideAppID
		}
		isInternalTransfer = internal.InternalTransfer
	}

	// JIT Liquidity Check
	invoiceAmount := amount // Default to requested amount

	if svc.liquidityManager != nil && lspJitChannelSCID == nil && !isInternalTransfer {
		jitHints, err := svc.liquidityManager.EnsureInboundLiquidity(ctx, amount) // Buy for GROSS amount
		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to ensure inbound liquidity")
			// We continue anyway, but log error
		} else if jitHints != nil {
			svc.logger.Info().Msg("Applying JIT channel hints to invoice")
			throughNodePubkey = &jitHints.LSPNodeID
			lspJitChannelSCID = &jitHints.SCID
			lspCltvExpiryDelta = &jitHints.CLTVExpiryDelta

			// APPLY ROUTE HINT FEE LOGIC
			// 1. Fee is declared in Route Hint
			fee := jitHints.FeeMloki
			lspFeeBaseMloki = &fee

			zero32 := uint32(0)
			lspFeeProportionalMillionths = &zero32 // All fee in base

			// 2. Invoice Amount = Net Amount (Gross - Fee)
			if amount > fee {
				invoiceAmount = amount - fee
			} else {
				svc.logger.Warn().Uint64("amount", amount).Uint64("fee", fee).Msg("JIT Fee exceeds payment amount! Invoice will be 0 net.")
				invoiceAmount = 0 // Should probably fail?
			}
		}
	}

	lnClientTransaction, err := lnClient.MakeInvoice(ctx, int64(invoiceAmount), description, descriptionHash, int64(expiry), throughNodePubkey, lspJitChannelSCID, lspCltvExpiryDelta, lspFeeBaseMloki, lspFeeProportionalMillionths) //nolint:gosec // invoice amount/expiry are always far below int64 range
	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to create transaction")
		return nil, err
	}

	var preimage *string
	if lnClientTransaction.Preimage != "" {
		preimage = &lnClientTransaction.Preimage
	}

	var expiresAt *time.Time
	if lnClientTransaction.ExpiresAt != nil {
		expiresAtValue := time.Unix(*lnClientTransaction.ExpiresAt, 0)
		expiresAt = &expiresAtValue
	}

	dbTransaction := db.Transaction{
		AppId:           appId,
		RequestEventId:  requestEventId,
		Type:            lnClientTransaction.Type,
		State:           constants.TRANSACTION_STATE_PENDING,
		AmountMloki:     uint64(lnClientTransaction.Amount), //nolint:gosec // LN-client-reported invoice amount is always non-negative
		Description:     description,
		DescriptionHash: descriptionHash,
		PaymentRequest:  lnClientTransaction.Invoice,
		PaymentHash:     lnClientTransaction.PaymentHash,
		ExpiresAt:       expiresAt,
		Preimage:        preimage,
		Metadata:        datatypes.JSON(metadataBytes),
	}
	err = svc.db.Create(&dbTransaction).Error
	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to create DB transaction")
		return nil, err
	}
	return &dbTransaction, nil
}

func (svc *transactionsService) MakeHoldInvoice(ctx context.Context, amount uint64, description string, descriptionHash string, expiry uint64, paymentHash string, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*Transaction, error) {
	var err error
	var metadataBytes []byte
	if metadata != nil {
		metadataBytes, err = json.Marshal(metadata)
		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to serialize metadata")
			return nil, err
		}
		if len(metadataBytes) > constants.INVOICE_METADATA_MAX_LENGTH {
			return nil, fmt.Errorf("encoded invoice metadata provided is too large. Limit: %d Received: %d", constants.INVOICE_METADATA_MAX_LENGTH, len(metadataBytes))
		}
	}

	lnClientTransaction, err := lnClient.MakeHoldInvoice(ctx, int64(amount), description, descriptionHash, int64(expiry), paymentHash) //nolint:gosec // invoice amount/expiry are always far below int64 range
	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to create hold invoice via FLN client")
		return nil, err
	}

	var preimage *string
	if lnClientTransaction.Preimage != "" {
		preimage = &lnClientTransaction.Preimage
	}

	var expiresAt *time.Time
	if lnClientTransaction.ExpiresAt != nil {
		expiresAtValue := time.Unix(*lnClientTransaction.ExpiresAt, 0)
		expiresAt = &expiresAtValue
	}

	dbTransaction := db.Transaction{
		AppId:           appId,
		RequestEventId:  requestEventId,
		Type:            constants.TRANSACTION_TYPE_INCOMING,
		State:           constants.TRANSACTION_STATE_PENDING,
		AmountMloki:     uint64(lnClientTransaction.Amount), //nolint:gosec // LN-client-reported invoice amount is always non-negative
		Description:     description,
		DescriptionHash: descriptionHash,
		PaymentRequest:  lnClientTransaction.Invoice,
		PaymentHash:     lnClientTransaction.PaymentHash,
		ExpiresAt:       expiresAt,
		Preimage:        preimage,
		Metadata:        datatypes.JSON(metadataBytes),
		Hold:            true,
	}
	err = svc.db.Create(&dbTransaction).Error
	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to create hold invoice DB transaction")
		return nil, err
	}
	return &dbTransaction, nil
}

func (svc *transactionsService) SendPaymentSync(payReq string, amountMloki *uint64, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*Transaction, error) {
	var metadataBytes []byte
	if metadata != nil {
		var err error
		metadataBytes, err = json.Marshal(metadata)
		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to serialize metadata")
			return nil, err
		}
		if len(metadataBytes) > constants.INVOICE_METADATA_MAX_LENGTH {
			return nil, fmt.Errorf("encoded payment metadata provided is too large. Limit: %d Received: %d", constants.INVOICE_METADATA_MAX_LENGTH, len(metadataBytes))
		}
	}

	payReq = strings.ToLower(payReq)
	paymentRequest, err := decodepay.Decode(payReq)
	if err != nil {
		svc.logger.Error().Err(err).
			Str("bolt11", payReq).
			Msg("Failed to decode bolt11 invoice")

		return nil, err
	}

	if time.Now().After(time.Unix(int64(paymentRequest.CreatedAt+paymentRequest.Expiry), 0)) {
		svc.logger.Error().
			Str("bolt11", payReq).
			Time("expiry", time.Unix(int64(paymentRequest.CreatedAt+paymentRequest.Expiry), 0)).
			Msg("this invoice has expired")

		return nil, errors.New("this invoice has expired")
	}

	selfPayment := false
	if paymentRequest.Payee != "" && paymentRequest.Payee == lnClient.GetPubkey() {
		var incomingTransaction db.Transaction
		result := svc.db.Limit(1).Find(&incomingTransaction, &db.Transaction{
			Type:        constants.TRANSACTION_TYPE_INCOMING,
			PaymentHash: paymentRequest.PaymentHash,
		})
		if result.Error == nil && result.RowsAffected > 0 {
			selfPayment = true
		}
	}

	var dbTransaction db.Transaction

	paymentAmount := uint64(paymentRequest.MSat) //nolint:gosec // msat amounts are always far below int/uint64 range
	if amountMloki != nil && paymentRequest.MSat == 0 {
		paymentAmount = *amountMloki
	}

	err = svc.db.Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "postgres" && appId != nil {
			if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(*appId)).Error; err != nil { //nolint:gosec // app IDs are small auto-increment DB primary keys
				return fmt.Errorf("acquire payment lock: %w", err)
			}
		}

		var existingSettledTransaction db.Transaction
		if tx.Limit(1).Find(&existingSettledTransaction, &db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			PaymentHash: paymentRequest.PaymentHash,
			State:       constants.TRANSACTION_STATE_SETTLED,
		}).RowsAffected > 0 {
			svc.logger.Debug().Str("payment_hash", dbTransaction.PaymentHash).Msg("this invoice has already been paid")
			return errors.New("this invoice has already been paid")
		}
		if tx.Limit(1).Find(&existingSettledTransaction, &db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			PaymentHash: paymentRequest.PaymentHash,
			State:       constants.TRANSACTION_STATE_PENDING,
		}).RowsAffected > 0 {
			svc.logger.Debug().Str("payment_hash", dbTransaction.PaymentHash).Msg("this invoice is already being paid")
			return errors.New("there is already a payment pending for this invoice")
		}

		// Internal transfers (hub cleanup, self-payment) and claim_funds' own
		// per-slice payout (which enforces its own, stronger exact-amount rule
		// upstream) are exempt from both the fee-reserve balance/quota headroom
		// (validateCanPay) and the whole-wallet full-drain shape check
		// (enforceJITFullDrain) below. Internal transfers are additionally
		// exempt from the MaxAmountLoki budget cap itself (skipBudgetCap) -
		// see validateCanPay's doc comment.
		isInternalTransfer, _ := metadata["internal_transfer"].(bool)
		isJITClaimSlice, _ := metadata["jit_claim_slice"].(bool)

		balance, parentKind, feeSkimMloki, err := svc.validateCanPay(tx, appId, paymentAmount, paymentRequest.Description, selfPayment, validateCanPayExemptions{
			SkipFeeReserve: isJITClaimSlice,
			SkipBudgetCap:  isInternalTransfer,
		})
		if err != nil {
			return err
		}
		// JIT wallets must drain their full balance in a single payment.
		// Enforced here (shared layer) so the HTTP API and keysend paths cannot bypass it.
		if err := enforceJITFullDrain(parentKind, balance, paymentAmount, isInternalTransfer, isJITClaimSlice); err != nil {
			return err
		}

		var expiresAt *time.Time
		if paymentRequest.Expiry > 0 {
			expiresAtValue := time.Now().Add(time.Duration(paymentRequest.Expiry) * time.Second)
			expiresAt = &expiresAtValue
		}
		dbTransaction = db.Transaction{
			AppId:           appId,
			RequestEventId:  requestEventId,
			Type:            constants.TRANSACTION_TYPE_OUTGOING,
			State:           constants.TRANSACTION_STATE_PENDING,
			FeeReserveMloki: CalculateFeeReserveMloki(paymentAmount),
			FeeSkimMloki:    feeSkimMloki,
			AmountMloki:     paymentAmount,
			PaymentRequest:  payReq,
			PaymentHash:     paymentRequest.PaymentHash,
			Description:     paymentRequest.Description,
			DescriptionHash: paymentRequest.DescriptionHash,
			ExpiresAt:       expiresAt,
			SelfPayment:     selfPayment,
			Metadata:        datatypes.JSON(metadataBytes),
		}
		return tx.Create(&dbTransaction).Error
	})

	if err != nil {
		svc.logger.Error().Err(err).
			Str("bolt11", payReq).
			Msg("Failed to create DB transaction")
		return nil, err
	}

	svc.logger.Debug().
		Interface("app_id", appId).
		Interface("request_event_id", requestEventId).
		Uint64("amount", paymentAmount).
		Str("description", paymentRequest.Description).
		Str("description_hash", paymentRequest.DescriptionHash).
		Int64("expiry", paymentRequest.Expiry).
		Bool("self_payment", selfPayment).
		Interface("metadata", metadata).
		Msg("Initiating payment")

	var response *lnclient.PayInvoiceResponse
	if selfPayment {
		response, err = svc.interceptSelfPayment(paymentRequest.PaymentHash, lnClient)
	} else {
		response, err = lnClient.SendPaymentSync(payReq, amountMloki)
	}

	if err != nil {
		rpcErr := err
		svc.logger.Error().Err(rpcErr).
			Str("bolt11", payReq).
			Msg("Failed to send payment")

		var failedTransaction *db.Transaction
		dbErr := svc.db.Transaction(func(tx *gorm.DB) error {
			var markErr error
			failedTransaction, markErr = svc.markPaymentFailed(tx, &dbTransaction, rpcErr.Error())
			return markErr
		})
		if dbErr != nil {
			return nil, dbErr
		}
		if failedTransaction != nil {
			return nil, rpcErr
		}

		// markPaymentFailed no-opped: most likely the payment-sent-event
		// subscription already settled this same payment hash before this
		// error branch ran (see markPaymentFailed's doc comment). Re-fetch and
		// return the settled transaction instead of surfacing a stale RPC
		// error for a payment that actually went out - mirrors the identical
		// re-fetch pattern used below for the success-path settle race.
		var existing db.Transaction
		if lookupErr := svc.db.Where(&db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			PaymentHash: dbTransaction.PaymentHash,
			State:       constants.TRANSACTION_STATE_SETTLED,
		}).First(&existing).Error; lookupErr != nil {
			// Not actually settled (e.g. the no-op was an already-FAILED
			// duplicate call) - the original RPC error is the right one to surface.
			return nil, rpcErr
		}
		return &existing, nil
	}

	// the payment definitely succeeded
	var settledTransaction *db.Transaction
	err = svc.db.Transaction(func(tx *gorm.DB) error {
		settledTransaction, err = svc.markTransactionSettled(tx, &dbTransaction, response.Preimage, response.Fee, selfPayment)
		return err
	})
	if err != nil {
		return nil, err
	}
	if settledTransaction != nil {
		svc.publishSettleEvent(settledTransaction)
	} else {
		// subscribePayments goroutine raced and settled the transaction first.
		// markTransactionSettled returns nil to avoid double-firing the settle event,
		// but we still need to return the record to the caller.
		var existing db.Transaction
		if err := svc.db.Where(&db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			PaymentHash: dbTransaction.PaymentHash,
			State:       constants.TRANSACTION_STATE_SETTLED,
		}).First(&existing).Error; err != nil {
			return nil, fmt.Errorf("payment settled but transaction not found: %w", err)
		}
		settledTransaction = &existing
	}

	return settledTransaction, nil
}

func (svc *transactionsService) SendKeysend(amount uint64, destination string, customRecords []lnclient.TLVRecord, preimage string, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*Transaction, error) {
	if preimage == "" {
		preImageBytes, err := makePreimageHex()
		if err != nil {
			return nil, err
		}
		preimage = hex.EncodeToString(preImageBytes)
	}

	preImageBytes, err := hex.DecodeString(preimage)
	if err != nil || len(preImageBytes) != 32 {
		svc.logger.Error().Err(err).
			Str("preimage", preimage).
			Msg("Invalid preimage")
		return nil, err
	}

	paymentHash256 := sha256.New()
	paymentHash256.Write(preImageBytes)
	paymentHashBytes := paymentHash256.Sum(nil)
	paymentHash := hex.EncodeToString(paymentHashBytes)

	metadata := map[string]interface{}{}

	metadata["destination"] = destination

	metadata["tlv_records"] = customRecords
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to serialize transaction metadata")
		return nil, err
	}
	boostagramBytes := svc.getBoostagramBytesFromCustomRecords(customRecords)

	var dbTransaction db.Transaction

	selfPayment := destination == lnClient.GetPubkey()

	err = svc.db.Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "postgres" && appId != nil {
			if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(*appId)).Error; err != nil { //nolint:gosec // app IDs are small auto-increment DB primary keys
				return fmt.Errorf("acquire payment lock: %w", err)
			}
		}

		balance, parentKind, feeSkimMloki, err := svc.validateCanPay(tx, appId, amount, "", selfPayment, validateCanPayExemptions{})
		if err != nil {
			return err
		}
		// SendKeysend has no internal-transfer or claim_funds path today (see enforceJITFullDrain doc).
		if err := enforceJITFullDrain(parentKind, balance, amount, false, false); err != nil {
			return err
		}

		dbTransaction = db.Transaction{
			AppId:           appId,
			Description:     svc.getDescriptionFromCustomRecords(customRecords),
			RequestEventId:  requestEventId,
			Type:            constants.TRANSACTION_TYPE_OUTGOING,
			State:           constants.TRANSACTION_STATE_PENDING,
			FeeReserveMloki: CalculateFeeReserveMloki(uint64(amount)),
			FeeSkimMloki:    feeSkimMloki,
			AmountMloki:     amount,
			Metadata:        datatypes.JSON(metadataBytes),
			Boostagram:      datatypes.JSON(boostagramBytes),
			PaymentHash:     paymentHash,
			Preimage:        &preimage,
			SelfPayment:     selfPayment,
		}
		return tx.Create(&dbTransaction).Error
	})

	if err != nil {
		svc.logger.Error().Err(err).
			Str("destination", destination).
			Uint64("amount", amount).
			Msg("Failed to create DB transaction")
		return nil, err
	}

	var payKeysendResponse *lnclient.PayKeysendResponse

	if selfPayment {
		// for keysend self-payments we need to create an incoming payment at the time of the payment
		recipientAppId := svc.getAppIdFromCustomRecords(customRecords, svc.db)
		dbTransaction := db.Transaction{
			AppId:          recipientAppId,
			RequestEventId: nil, // it is related to this request but for a different app
			Type:           constants.TRANSACTION_TYPE_INCOMING,
			State:          constants.TRANSACTION_STATE_PENDING,
			AmountMloki:    amount,
			PaymentHash:    paymentHash,
			Preimage:       &preimage,
			Description:    svc.getDescriptionFromCustomRecords(customRecords),
			Metadata:       datatypes.JSON(metadataBytes),
			Boostagram:     datatypes.JSON(boostagramBytes),
			SelfPayment:    true,
		}
		err = svc.db.Create(&dbTransaction).Error
		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to create DB transaction")
			return nil, err
		}

		_, err = svc.interceptSelfPayment(paymentHash, lnClient)
		if err == nil {
			payKeysendResponse = &lnclient.PayKeysendResponse{
				Fee: 0,
			}
		}
	} else {
		payKeysendResponse, err = lnClient.SendKeysend(amount, destination, customRecords, preimage)
	}

	if err != nil {
		rpcErr := err
		svc.logger.Error().Err(rpcErr).
			Str("destination", destination).
			Uint64("amount", amount).
			Msg("Failed to send payment")

		// Route through markPaymentFailed (not a raw Updates() call) so this
		// shares its guard against downgrading an already-SETTLED transaction -
		// the async payment-sent-event subscription can settle this same
		// payment hash before this error branch runs, same as SendPaymentSync.
		var failedTransaction *db.Transaction
		dbErr := svc.db.Transaction(func(tx *gorm.DB) error {
			var markErr error
			failedTransaction, markErr = svc.markPaymentFailed(tx, &dbTransaction, rpcErr.Error())
			return markErr
		})
		if dbErr != nil {
			return nil, dbErr
		}
		if failedTransaction != nil {
			return nil, rpcErr
		}

		var existing db.Transaction
		if lookupErr := svc.db.Where(&db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			PaymentHash: dbTransaction.PaymentHash,
			State:       constants.TRANSACTION_STATE_SETTLED,
		}).First(&existing).Error; lookupErr != nil {
			return nil, rpcErr
		}
		return &existing, nil
	}

	// the payment definitely succeeded
	var settledTransaction *db.Transaction
	err = svc.db.Transaction(func(tx *gorm.DB) error {
		settledTransaction, err = svc.markTransactionSettled(tx, &dbTransaction, preimage, payKeysendResponse.Fee, selfPayment)
		return err
	})

	if err != nil {
		return nil, err
	}
	if settledTransaction != nil {
		svc.publishSettleEvent(settledTransaction)
	} else {
		// subscribePayments goroutine raced and settled the transaction first.
		// markTransactionSettled returns nil to avoid double-firing the settle event,
		// but we still need to return the record to the caller.
		var existing db.Transaction
		if err := svc.db.Where(&db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			PaymentHash: dbTransaction.PaymentHash,
			State:       constants.TRANSACTION_STATE_SETTLED,
		}).First(&existing).Error; err != nil {
			return nil, fmt.Errorf("payment settled but transaction not found: %w", err)
		}
		settledTransaction = &existing
	}

	return settledTransaction, nil
}

func (svc *transactionsService) LookupTransaction(ctx context.Context, paymentHash string, transactionType *string, lnClient lnclient.LNClient, appId *uint) (*Transaction, error) {
	transaction := db.Transaction{}

	tx := svc.db

	var appKind string
	if appId != nil {
		err := svc.db.
			Model(&db.App{}).
			Where("id", *appId).
			Pluck("kind", &appKind).
			Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, NewNotFoundError()
			}
			return nil, err
		}
	}

	if db.IsIsolatedKind(appKind) {
		tx = tx.Where("app_id = ?", *appId)
	}

	if transactionType != nil {
		tx = tx.Where("type = ?", *transactionType)
	}

	// order settled first, otherwise by created date, as there can be multiple outgoing payments
	// for the same payment hash (if you tried to pay an invoice multiple times - e.g. the first time failed)
	result := tx.Order("settled_at desc, created_at desc").Limit(1).Find(&transaction, &db.Transaction{
		// Type:        transactionType,
		PaymentHash: paymentHash,
	})

	if result.Error != nil {
		svc.logger.Error().Err(result.Error).Msg("Failed to lookup transaction")
		return nil, result.Error
	}

	if result.RowsAffected == 0 {
		svc.logger.Error().Err(result.Error).
			Str("payment_hash", paymentHash).
			Interface("app_id", appId).
			Msg("transaction not found")
		return nil, NewNotFoundError()
	}

	if transaction.State == constants.TRANSACTION_STATE_PENDING {
		_ = svc.checkUnsettledTransaction(ctx, &transaction, lnClient)
	}

	return &transaction, nil
}

func (svc *transactionsService) ListTransactions(ctx context.Context, from, until, limit, offset uint64, unpaidOutgoing bool, unpaidIncoming bool, transactionType *string, lnClient lnclient.LNClient, appId *uint, forceFilterByAppId bool) (transactions []Transaction, totalCount uint64, err error) {
	svc.checkUnsettledTransactions(ctx, lnClient)

	var appKind string
	if appId != nil {
		err := svc.db.
			Model(&db.App{}).
			Where("id", *appId).
			Pluck("kind", &appKind).
			Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, 0, NewNotFoundError()
			}
			return nil, 0, err
		}
	}

	tx := svc.db

	if db.IsIsolatedKind(appKind) || forceFilterByAppId {
		tx = tx.Where("app_id = ?", *appId)
	}

	if !unpaidOutgoing && !unpaidIncoming {
		tx = tx.Where("state = ?", constants.TRANSACTION_STATE_SETTLED)
	} else if unpaidOutgoing && !unpaidIncoming {
		tx = tx.Where("state = ? OR type = ?", constants.TRANSACTION_STATE_SETTLED, constants.TRANSACTION_TYPE_OUTGOING)
	} else if unpaidIncoming && !unpaidOutgoing {
		tx = tx.Where("state = ? OR type = ?", constants.TRANSACTION_STATE_SETTLED, constants.TRANSACTION_TYPE_INCOMING)
	}

	if transactionType != nil {
		tx = tx.Where("type = ?", *transactionType)
	}

	if from > 0 {
		tx = tx.Where("updated_at >= ?", time.Unix(utils.ClampUint64ToInt64(from), 0))
	}
	if until > 0 {
		tx = tx.Where("updated_at <= ?", time.Unix(utils.ClampUint64ToInt64(until), 0))
	}

	var totalCount64 int64
	result := tx.Model(&db.Transaction{}).Count(&totalCount64)
	if result.Error != nil {
		svc.logger.Error().Err(result.Error).Msg("Failed to count DB transactions")
		return nil, 0, result.Error
	}
	totalCount = uint64(totalCount64) //nolint:gosec // a DB row count is always non-negative

	tx = tx.Order("updated_at desc")

	if limit > 0 {
		tx = tx.Limit(utils.ClampUint64ToInt(limit))
	}
	if offset > 0 {
		tx = tx.Offset(utils.ClampUint64ToInt(offset))
	}

	result = tx.Find(&transactions)
	if result.Error != nil {
		svc.logger.Error().Err(result.Error).Msg("Failed to list DB transactions")
		return nil, 0, result.Error
	}

	return transactions, totalCount, nil
}

func (svc *transactionsService) checkUnsettledTransactions(ctx context.Context, lnClient lnclient.LNClient) {
	// Only check unsettled transactions for clients that don't support async events
	// checkUnsettledTransactions does not work for keysend payments!
	if slices.Contains(lnClient.GetSupportedNIP47NotificationTypes(), "payment_received") {
		return
	}

	// check pending payments less than a day old
	transactions := []Transaction{}
	result := svc.db.Where("state = ? AND created_at > ?", constants.TRANSACTION_STATE_PENDING, time.Now().Add(-24*time.Hour)).Find(&transactions)
	if result.Error != nil {
		svc.logger.Error().Err(result.Error).Msg("Failed to list DB transactions")
		return
	}
	for _, transaction := range transactions {
		_ = svc.checkUnsettledTransaction(ctx, &transaction, lnClient)
	}
}
func (svc *transactionsService) checkUnsettledTransaction(ctx context.Context, transaction *db.Transaction, lnClient lnclient.LNClient) error {
	if slices.Contains(lnClient.GetSupportedNIP47NotificationTypes(), "payment_received") {
		return nil
	}

	lnClientTransaction, err := lnClient.LookupInvoice(ctx, transaction.PaymentHash)
	if err != nil {
		svc.logger.Error().Err(err).
			Str("bolt11", transaction.PaymentRequest).
			Msg("Failed to check transaction")
		return err
	}
	// update transaction state
	if lnClientTransaction.SettledAt != nil {
		var settledTx *db.Transaction
		err = svc.db.Transaction(func(tx *gorm.DB) error {
			var txErr error
			settledTx, txErr = svc.markTransactionSettled(tx, transaction, lnClientTransaction.Preimage, uint64(lnClientTransaction.FeesPaid), false) //nolint:gosec // LN-client-reported fee is always non-negative
			return txErr
		})

		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to mark payment sent when checking unsettled transaction")
		} else if settledTx != nil {
			svc.publishSettleEvent(settledTx)
		}
	}
	return nil
}

const stalePendingTTL = 48 * time.Hour

// SweepStalePendingOutgoing cancels outgoing payments that have been stuck in
// PENDING state for longer than stalePendingTTL (2× the practical HTLC expiry
// window). Before cancelling, it attempts LN reconciliation so that a payment
// which did settle is correctly marked as successful rather than failed.
func (svc *transactionsService) SweepStalePendingOutgoing(ctx context.Context, lnClient lnclient.LNClient) {
	var stale []db.Transaction
	cutoff := time.Now().Add(-stalePendingTTL)
	if err := svc.db.Where(
		"type = ? AND state = ? AND created_at < ?",
		constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_PENDING, cutoff,
	).Find(&stale).Error; err != nil {
		svc.logger.Error().Err(err).Msg("SweepStalePendingOutgoing: failed to query stale transactions")
		return
	}

	for i := range stale {
		if err := svc.checkUnsettledTransaction(ctx, &stale[i], lnClient); err != nil {
			// LN node was unreachable — payment state is unknown; do not force-cancel.
			svc.logger.Warn().Err(err).Uint("tx_id", stale[i].ID).
				Msg("SweepStalePendingOutgoing: skipping — LN unreachable, state unknown")
			continue
		}

		var refreshed db.Transaction
		if err := svc.db.First(&refreshed, stale[i].ID).Error; err != nil {
			continue
		}
		if refreshed.State == constants.TRANSACTION_STATE_PENDING {
			if err := svc.db.Model(&refreshed).Update("state", constants.TRANSACTION_STATE_FAILED).Error; err != nil {
				svc.logger.Error().Err(err).Uint("tx_id", refreshed.ID).Msg("SweepStalePendingOutgoing: failed to cancel stale transaction")
			} else {
				svc.logger.Warn().Uint("tx_id", refreshed.ID).Str("payment_hash", refreshed.PaymentHash).Msg("SweepStalePendingOutgoing: cancelled stale pending outgoing payment")
			}
		}
	}
}

func (svc *transactionsService) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	switch event.Event {
	case "nwc_lnclient_payment_received":
		lnClientTransaction, ok := event.Properties.(*lnclient.Transaction)
		if !ok {
			svc.logger.Error().Interface("event", event).Msg("Failed to cast event")
			return
		}

		var dbTransaction db.Transaction
		var settledIncoming *db.Transaction
		err := svc.db.Transaction(func(tx *gorm.DB) error {

			result := tx.Limit(1).Find(&dbTransaction, &db.Transaction{
				Type:        constants.TRANSACTION_TYPE_INCOMING,
				PaymentHash: lnClientTransaction.PaymentHash,
			})

			if result.RowsAffected == 0 {
				var appId *uint
				description := lnClientTransaction.Description
				var metadataBytes []byte
				var boostagramBytes []byte
				if lnClientTransaction.Metadata != nil {
					var err error
					metadataBytes, err = json.Marshal(lnClientTransaction.Metadata)
					if err != nil {
						svc.logger.Error().Err(err).Msg("Failed to serialize transaction metadata")
						return err
					}

					var customRecords []lnclient.TLVRecord
					customRecords, _ = lnClientTransaction.Metadata["tlv_records"].([]lnclient.TLVRecord)
					boostagramBytes = svc.getBoostagramBytesFromCustomRecords(customRecords)
					extractedDescription := svc.getDescriptionFromCustomRecords(customRecords)
					if extractedDescription != "" {
						description = extractedDescription
					}
					// find app by custom key/value records
					appId = svc.getAppIdFromCustomRecords(customRecords, tx)
				}
				var expiresAt *time.Time
				if lnClientTransaction.ExpiresAt != nil {
					expiresAtValue := time.Unix(*lnClientTransaction.ExpiresAt, 0)
					expiresAt = &expiresAtValue
				}
				dbTransaction = db.Transaction{
					Type:            constants.TRANSACTION_TYPE_INCOMING,
					AmountMloki:     uint64(lnClientTransaction.Amount), //nolint:gosec // LN-client-reported invoice amount is always non-negative
					PaymentRequest:  lnClientTransaction.Invoice,
					PaymentHash:     lnClientTransaction.PaymentHash,
					Description:     description,
					DescriptionHash: lnClientTransaction.DescriptionHash,
					ExpiresAt:       expiresAt,
					Metadata:        datatypes.JSON(metadataBytes),
					Boostagram:      datatypes.JSON(boostagramBytes),
					AppId:           appId,
				}
				err := tx.Create(&dbTransaction).Error
				if err != nil {
					svc.logger.Error().Err(err).
						Str("payment_hash", lnClientTransaction.PaymentHash).
						Msg("Failed to create transaction")
					return err
				}
			}

			// Serialise against any other transaction locked on this app (e.g.
			// DeleteCircleHub checking+deleting a circle_wallet child) so this
			// settlement can't commit in the gap between a balance check and a
			// dependent write on the same app — same lock key/semantics as the
			// outgoing-payment paths above.
			if tx.Name() == "postgres" && dbTransaction.AppId != nil {
				if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(*dbTransaction.AppId)).Error; err != nil { //nolint:gosec // app IDs are small auto-increment DB primary keys
					return fmt.Errorf("acquire payment lock: %w", err)
				}
			}

			var txErr error
			settledIncoming, txErr = svc.markTransactionSettled(tx, &dbTransaction, lnClientTransaction.Preimage, uint64(lnClientTransaction.FeesPaid), false) //nolint:gosec // LN-client-reported fee is always non-negative
			return txErr
		})

		if err != nil {
			svc.logger.Error().Err(err).
				Str("payment_hash", lnClientTransaction.PaymentHash).
				Msg("Failed to execute DB transaction")
			return
		}
		if settledIncoming != nil {
			svc.publishSettleEvent(settledIncoming)
		}

	case "nwc_lnclient_hold_invoice_accepted":
		lnClientTransaction, ok := event.Properties.(*lnclient.Transaction)
		if !ok {
			svc.logger.Error().Interface("event", event).Msg("Failed to cast event properties for hold invoice accepted")
			return
		}
		if lnClientTransaction.SettleDeadline == nil {
			svc.logger.Error().Interface("event", event).Msg("Transaction has no settle deadline")
			return
		}
		svc.markHoldInvoiceAccepted(lnClientTransaction.PaymentHash, *lnClientTransaction.SettleDeadline, false)

	case "nwc_lnclient_payment_sent":
		lnClientTransaction, ok := event.Properties.(*lnclient.Transaction)
		if !ok {
			svc.logger.Error().Interface("event", event).Msg("Failed to cast event")
			return
		}

		var dbTransaction db.Transaction
		var settledOutgoing *db.Transaction
		err := svc.db.Transaction(func(tx *gorm.DB) error {

			// first lookup by pending
			result := tx.Limit(1).Find(&dbTransaction, &db.Transaction{
				Type:        constants.TRANSACTION_TYPE_OUTGOING,
				State:       constants.TRANSACTION_STATE_PENDING,
				PaymentHash: lnClientTransaction.PaymentHash,
			})

			if result.Error != nil {
				return result.Error
			}

			if result.RowsAffected == 0 {
				// if no pending payment was found, lookup by failed, latest updated first
				result := tx.Limit(1).Order("updated_at DESC").Find(&dbTransaction, &db.Transaction{
					Type:        constants.TRANSACTION_TYPE_OUTGOING,
					State:       constants.TRANSACTION_STATE_FAILED,
					PaymentHash: lnClientTransaction.PaymentHash,
				})

				if result.Error != nil {
					return result.Error
				}

				if result.RowsAffected == 0 {
					// check if it was already settled
					result := tx.Limit(1).Find(&dbTransaction, &db.Transaction{
						Type:        constants.TRANSACTION_TYPE_OUTGOING,
						State:       constants.TRANSACTION_STATE_SETTLED,
						PaymentHash: lnClientTransaction.PaymentHash,
					})
					if result.RowsAffected > 0 {
						svc.logger.Debug().Str("payment_hash", lnClientTransaction.PaymentHash).Msg("payment already settled, ignoring payment sent event")
						return nil
					}

					// Note: payments made from outside cannot be associated with an app
					// for now this is disabled as it only applies to FLND, and we do not import FLND transactions either.
					svc.logger.Error().Str("payment_hash", lnClientTransaction.PaymentHash).Msg("failed to mark payment as sent: payment not found")
					return NewNotFoundError()
				}
			}

			var txErr error
			settledOutgoing, txErr = svc.markTransactionSettled(tx, &dbTransaction, lnClientTransaction.Preimage, uint64(lnClientTransaction.FeesPaid), false) //nolint:gosec // LN-client-reported fee is always non-negative
			return txErr
		})

		if err != nil {
			svc.logger.Error().Err(err).
				Str("payment_hash", lnClientTransaction.PaymentHash).
				Msg("Failed to update transaction")
			return
		}
		if settledOutgoing != nil {
			svc.publishSettleEvent(settledOutgoing)
		}
	case "nwc_lnclient_payment_failed":
		paymentFailedAsyncProperties, ok := event.Properties.(*lnclient.PaymentFailedEventProperties)
		if !ok {
			svc.logger.Error().Interface("event", event).Msg("Failed to cast event")
			return
		}

		lnClientTransaction := paymentFailedAsyncProperties.Transaction

		var dbTransaction db.Transaction
		result := svc.db.Limit(1).Find(&dbTransaction, &db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			State:       constants.TRANSACTION_STATE_PENDING,
			PaymentHash: lnClientTransaction.PaymentHash,
		})

		if result.RowsAffected == 0 {
			svc.logger.Error().Interface("event", event).Msg("Failed to find pending outgoing transaction by payment hash")
			return
		}

		if err := svc.db.Transaction(func(tx *gorm.DB) error {
			_, markErr := svc.markPaymentFailed(tx, &dbTransaction, paymentFailedAsyncProperties.Reason)
			return markErr
		}); err != nil {
			svc.logger.Error().Err(err).Str("payment_hash", lnClientTransaction.PaymentHash).Msg("Failed to mark transaction as failed from async event")
		}
	}
}

func (svc *transactionsService) markHoldInvoiceAccepted(paymentHash string, settleDeadline uint32, selfPayment bool) {
	svc.logger.Info().
		Str("payment_hash", paymentHash).
		Bool("self_payment", selfPayment).
		Msg("Processing hold invoice accepted event")

	var dbTransaction db.Transaction
	err := svc.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("payment_hash = ? AND type = ? AND state = ?", paymentHash, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_PENDING).First(&dbTransaction)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				svc.logger.Warn().
					Str("payment_hash", paymentHash).
					Msg("No corresponding pending incoming transaction found in DB for accepted hold invoice")
			}
			svc.logger.Error().Err(result.Error).
				Str("payment_hash", paymentHash).
				Msg("Failed to query DB for accepted hold invoice")
			return result.Error
		}

		err := tx.Model(&dbTransaction).UpdateColumns(map[string]interface{}{
			"state":           constants.TRANSACTION_STATE_ACCEPTED,
			"self_payment":    selfPayment,
			"settle_deadline": settleDeadline,
		}).Error
		if err != nil {
			svc.logger.Error().Err(err).
				Str("payment_hash", paymentHash).
				Uint("dbTxID", dbTransaction.ID).
				Msg("Failed to update hold invoice state to accepted in DB")
			return err
		}

		svc.logger.Info().
			Str("payment_hash", paymentHash).
			Uint("dbTxID", dbTransaction.ID).
			Msg("Updated hold invoice state to accepted in DB")

		return nil
	})
	if err != nil {
		svc.logger.Error().Err(err).
			Str("payment_hash", paymentHash).
			Msg("Failed DB transaction for hold invoice accepted event")
	} else {
		svc.eventPublisher.Publish(&events.Event{
			Event:      "nwc_hold_invoice_accepted",
			Properties: &dbTransaction,
		})
	}
}

func (svc *transactionsService) interceptSelfPayment(paymentHash string, lnClient lnclient.LNClient) (*lnclient.PayInvoiceResponse, error) {
	svc.logger.Debug().Str("payment_hash", paymentHash).Msg("Intercepting self payment")
	incomingTransaction := db.Transaction{}
	result := svc.db.Limit(1).Find(&incomingTransaction, &db.Transaction{
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_PENDING,
		PaymentHash: paymentHash,
	})
	if result.Error != nil {
		return nil, result.Error
	}

	if result.RowsAffected == 0 {
		return nil, NewNotFoundError()
	}

	if incomingTransaction.Hold {
		return svc.interceptSelfHoldPayment(paymentHash, lnClient)
	}

	if incomingTransaction.Preimage == nil {
		return nil, errors.New("preimage is not set on transaction. Self payments not supported")
	}

	var settledSelfTx *db.Transaction
	err := svc.db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		settledSelfTx, txErr = svc.markTransactionSettled(tx, &incomingTransaction, *incomingTransaction.Preimage, uint64(0), true)
		return txErr
	})

	if err != nil {
		return nil, err
	}
	if settledSelfTx != nil {
		svc.publishSettleEvent(settledSelfTx)
	}

	return &lnclient.PayInvoiceResponse{
		Preimage: *incomingTransaction.Preimage,
		Fee:      0,
	}, nil
}

func (svc *transactionsService) interceptSelfHoldPayment(paymentHash string, lnClient lnclient.LNClient) (*lnclient.PayInvoiceResponse, error) {
	settledChannel := make(chan *db.Transaction)
	canceledChannel := make(chan *db.Transaction)

	holdInvoiceUpdatedConsumer := newHoldInvoiceUpdatedConsumer(paymentHash, settledChannel, canceledChannel)

	svc.eventPublisher.RegisterSubscriber(holdInvoiceUpdatedConsumer)
	defer svc.eventPublisher.RemoveSubscriber(holdInvoiceUpdatedConsumer)

	clientInfo, err := lnClient.GetInfo(context.Background())
	if err != nil {
		return nil, errors.New("failed to get client info")
	}
	if clientInfo.BlockHeight == 0 {
		return nil, errors.New("invalid client block height")
	}

	fakeSettleDeadline := clientInfo.BlockHeight + 24

	svc.markHoldInvoiceAccepted(paymentHash, fakeSettleDeadline, true)

	select {
	case settledTransaction := <-settledChannel:
		svc.logger.Info().Interface("settled_transaction", settledTransaction).Msg("self hold payment was settled")
		if settledTransaction.Preimage == nil {
			return nil, errors.New("preimage is not set on self hold payment")
		}

		return &lnclient.PayInvoiceResponse{
			Preimage: *settledTransaction.Preimage,
			Fee:      0,
		}, nil
	case canceledTransaction := <-canceledChannel:
		svc.logger.Info().Interface("canceled_transaction", canceledTransaction).Msg("self hold payment was canceled")
		return nil, lnclient.NewHoldInvoiceCanceledError()
	}
}

// validateCanPay checks whether the given app is permitted to pay the given amount.
// Returns the isolated balance (0 if not isolated) and the app's parent_kind so
// the JIT full-drain check can be applied by the caller without an extra DB query.
//
// skipFeeReserve additionally exempts claim_funds' per-slice payout from the
// fee-reserve headroom below, for the same reason it's exempt from
// enforceJITFullDrain: a shared JIT wallet is funded with EXACTLY the sum of
// its recipients' declared slices (jitwallet.Commit), and each slice's own
// budget cap (AppPermission.MaxAmountLoki) is set to that same exact sum —
// so "balance/budget must cover amount + reserve" is never satisfiable for a
// wallet's last (or only) recipient no matter the amount's scale, since the
// reserve is strictly positive and the balance available for that specific
// claim can be exactly the claimed amount. claim_funds already independently
// enforces its own, stronger exact-match rule (invoice amount == the proven
// slice, checked before payment) — this reserve is a generic conservative
// pre-check for arbitrary payments, and is redundant/counter-productive for
// a payout whose amount is already pinned by that stronger rule.
//
// skipBudgetCap exempts a hub-internal reclaim transfer (a wallet's leftover
// balance being paid back to its own parent on teardown - see
// service.ReclaimAndDeleteSubWallet) from the MaxAmountLoki/budget-usage
// check below. Without this, a wallet whose lifetime spend has already
// reached its own cap could never be reclaimed/deleted again: returning its
// residual balance home is itself an outgoing payment from that same app,
// so it would trip the very cap it's trying to close out, permanently
// stranding both the leftover funds and the DB row. The isolated-balance
// check above this still applies unconditionally - a reclaim still can't
// move more than the wallet actually holds.
// validateCanPayExemptions bundles the two exemption flags below into named
// fields. Both are plain bools with unrelated meanings (see validateCanPay's
// own doc comments on skipFeeReserve/skipBudgetCap for what each actually
// exempts a payment from and why that's safe) - passed positionally into
// validateCanPay, a same-type pair like this gets no compiler protection
// against being swapped or misordered by a future edit; naming them at every
// call site removes that risk.
type validateCanPayExemptions struct {
	SkipFeeReserve bool
	SkipBudgetCap  bool
}

func (svc *transactionsService) validateCanPay(tx *gorm.DB, appId *uint, amount uint64, description string, selfPayment bool, exemptions validateCanPayExemptions) (isolatedBalance int64, parentKind string, feeSkimMloki uint64, err error) {
	skipFeeReserve := exemptions.SkipFeeReserve
	skipBudgetCap := exemptions.SkipBudgetCap
	if appId == nil {
		return 0, "", 0, nil
	}

	// Fetch app and its pay-capable permission in a single JOIN so we only hit
	// the DB once. parent_kind is returned so the caller can enforce JIT
	// full-drain without a second query. Matches against
	// constants.PayCapableScopes (not just PAY_INVOICE_SCOPE alone) since
	// jit_wallet children carry JIT_CLAIM_FUNDS_SCOPE instead — without this,
	// claim_funds would fail every call with "app does not have pay_invoice
	// scope" the moment it reaches this shared payment layer.
	//
	// The LEFT JOIN to circle_hub_configs resolves a circle_wallet's parent
	// hub's forwarding-fee rate in the same query: apps.parent_app_id only
	// ever matches a circle_hub_configs.app_id row when this app is a
	// circle_wallet child of a circle_hub, so chc.fees_ppm is harmlessly NULL
	// (coalesced to 0) for every other app kind/lineage.
	var row struct {
		AppName       string
		AppKind       string
		ParentKind    string
		MaxAmountLoki int
		BudgetRenewal string
		PermAppId     uint
		FeesPpm       int
	}
	// LEFT JOIN (not INNER) so apps.kind/parent_kind still resolve even when
	// no PayCapableScopes permission row exists — needed for skipBudgetCap's
	// hub-decrease case below, where the payer (e.g. a circle_hub, which is
	// deliberately never NWC-granted pay_invoice — see
	// create_circle_wallet_controller.go) has no such row by design.
	result := tx.Table("apps").
		Select("apps.name AS app_name, apps.kind AS app_kind, apps.parent_kind AS parent_kind, ap.max_amount_loki, ap.budget_renewal, ap.app_id AS perm_app_id, COALESCE(chc.fees_ppm, 0) AS fees_ppm").
		Joins("LEFT JOIN app_permissions ap ON ap.app_id = apps.id AND ap.scope IN ?", constants.PayCapableScopes).
		Joins("LEFT JOIN circle_hub_configs chc ON chc.app_id = apps.parent_app_id").
		Where("apps.id = ?", *appId).
		Scan(&row)
	if result.Error != nil {
		return 0, "", 0, result.Error
	}
	if row.AppKind == "" {
		return 0, "", 0, NewNotFoundError()
	}
	if row.PermAppId == 0 {
		// skipBudgetCap (internal_transfer) is only ever set by trusted
		// server-side call sites (api.Transfer's admin-initiated balance
		// decrease, hub reclaim/cleanup) — never reachable from an external
		// NWC pay_invoice/keysend/claim_funds request, which all strip this
		// flag from caller-supplied metadata before it gets here (see
		// pay_invoice_controller.go/claim_funds_controller.go). So it's safe
		// to let an admin manually decrease a hub's own balance (still fully
		// subject to the isolated-balance check below - just not gated on a
		// pay_invoice scope the hub was deliberately never granted over NWC)
		// without opening any real payment capability to that hub's own,
		// often-shared/public NWC connection.
		if !skipBudgetCap {
			return 0, "", 0, errors.New("app does not have pay_invoice scope")
		}
	}

	// A selfPayment (lnClient's own self-payment-interception shortcut, hit
	// whenever the invoice being paid was minted by an app on this same
	// lokihub instance) is exempt from the skim — by design, not just for the
	// hub's own internal_transfer reclaim. Every JIT wallet, circle wallet,
	// and generic NWC app sharing this instance settles between each other
	// this way, so this single flag is exactly "member-to-member / member-to-
	// any-other-app-on-this-instance" traffic, which should stay fee-free;
	// only a payment that genuinely leaves this instance over the real
	// Lightning network is skimmable.
	if row.AppKind == db.AppKindCircleWallet && !selfPayment {
		feeSkimMloki = CalculateFeeSkimMloki(amount, row.FeesPpm)
	}

	amountWithFeeReserve := amount + feeSkimMloki
	if !selfPayment && !skipFeeReserve {
		amountWithFeeReserve += CalculateFeeReserveMloki(amount)
	}

	if db.IsIsolatedKind(row.AppKind) {
		isolatedBalance = queries.GetIsolatedBalance(tx, *appId)
		// amountWithFeeReserve is built from a caller-suppliable amount (an
		// "amountless invoice" override); a value beyond int64 range would
		// wrap negative and bypass this balance check below, so fail closed
		// on the overflow itself rather than relying on the comparison.
		if amountWithFeeReserve > math.MaxInt64 || int64(amountWithFeeReserve) > isolatedBalance {
			svc.logger.Debug().
				Int64("balance", isolatedBalance).
				Bool("self_payment", selfPayment).
				Uint64("amount", amount).
				Uint64("fee_skim", feeSkimMloki).
				Uint64("amount_with_fee_reserve", amountWithFeeReserve).
				Msg("Insufficient budget to make payment from isolated app")
			message := NewInsufficientBalanceError().Error()
			if description != "" {
				message += " " + description
			}
			svc.eventPublisher.Publish(&events.Event{
				Event: "nwc_permission_denied",
				Properties: map[string]interface{}{
					"app_name": row.AppName,
					"code":     constants.ERROR_INSUFFICIENT_BALANCE,
					"message":  message,
				},
			})
			return 0, "", 0, NewInsufficientBalanceError()
		}
	}

	if row.MaxAmountLoki > 0 && !skipBudgetCap {
		appPermission := db.AppPermission{
			AppId:         *appId,
			MaxAmountLoki: row.MaxAmountLoki,
			BudgetRenewal: row.BudgetRenewal,
		}
		budgetUsageSat := queries.GetBudgetUsageSat(tx, &appPermission)
		// Compare as amountLoki+budgetUsageSat > maxAmountLoki rather than
		// amountLoki > maxAmountLoki-budgetUsageSat: arithmetically
		// equivalent, but avoids narrowing amountWithFeeReserve/1000 (uint64)
		// to int, which could wrap negative for an oversized amount and make
		// this quota check silently fail open.
		if amountWithFeeReserve/1000+budgetUsageSat > uint64(row.MaxAmountLoki) { //nolint:gosec // row.MaxAmountLoki > 0 is checked above
			message := NewQuotaExceededError().Error()
			if description != "" {
				message += " " + description
			}
			svc.eventPublisher.Publish(&events.Event{
				Event: "nwc_permission_denied",
				Properties: map[string]interface{}{
					"app_name": row.AppName,
					"code":     constants.ERROR_QUOTA_EXCEEDED,
					"message":  message,
				},
			})
			return 0, "", 0, NewQuotaExceededError()
		}
	}

	return isolatedBalance, row.ParentKind, feeSkimMloki, nil
}

// max of 1% or 10000 milliloki (10 loki)
func CalculateFeeReserveMloki(amountMloki uint64) uint64 {
	return uint64(math.Max(math.Ceil(float64(amountMloki)*0.01), 10000))
}

// CalculateFeeSkimMloki computes a circle_hub's forwarding-fee cut of an
// outgoing payment: floor(amountMloki * feesPpm / constants.PPM_DIVISOR).
// Pure integer math (no floating point) so it's exact and can't round a skim
// up past what CircleHubConfig.FeesPpm actually authorizes. feesPpm <= 0
// (unset, or a defensively-clamped negative) always yields zero — callers
// don't need to guard the call themselves.
func CalculateFeeSkimMloki(amountMloki uint64, feesPpm int) uint64 {
	if feesPpm <= 0 {
		return 0
	}
	return amountMloki * uint64(feesPpm) / constants.PPM_DIVISOR
}

func makePreimageHex() ([]byte, error) {
	bytes := make([]byte, 32) // 32 bytes * 8 bits/byte = 256 bits
	_, err := rand.Read(bytes)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (svc *transactionsService) getBoostagramBytesFromCustomRecords(customRecords []lnclient.TLVRecord) []byte {
	for _, record := range customRecords {
		if record.Type == BoostagramTlvType {
			bytes, err := hex.DecodeString(record.Value)
			if err != nil {
				svc.logger.Error().Err(err).Str("value", record.Value).Msg("failed to decode boostagram tlv hex value")
				return nil
			}

			// ensure the boostagram is valid json
			var boostagram Boostagram
			if err := json.Unmarshal(bytes, &boostagram); err != nil {
				svc.logger.Error().Err(err).Str("value", string(bytes)).Msg("failed to unmarshal boostagram to json")
				return nil
			}

			return bytes
		}
	}

	return nil
}

func (svc *transactionsService) getDescriptionFromCustomRecords(customRecords []lnclient.TLVRecord) string {
	var description string

	for _, record := range customRecords {
		switch record.Type {
		case BoostagramTlvType:
			bytes, err := hex.DecodeString(record.Value)
			if err != nil {
				continue
			}
			var boostagram Boostagram
			if err := json.Unmarshal(bytes, &boostagram); err != nil {
				continue
			}
			return boostagram.Message

		// TODO: consider adding support for this in LDK
		case WhatsatTlvType:
			bytes, err := hex.DecodeString(record.Value)
			if err == nil {
				description = string(bytes)
			}
		}
	}

	return description
}

func (svc *transactionsService) getAppIdFromCustomRecords(customRecords []lnclient.TLVRecord, tx *gorm.DB) *uint {
	app := db.App{}
	for _, record := range customRecords {
		if record.Type == CustomKeyTlvType {
			decodedString, err := hex.DecodeString(record.Value)
			if err != nil {
				svc.logger.Error().Err(err).Msg("Failed to parse custom key TLV record as hex")
				continue
			}
			customValue, err := strconv.ParseUint(string(decodedString), 10, 64)
			if err != nil {
				svc.logger.Error().Err(err).Msg("Failed to parse custom key TLV record as number")
				continue
			}
			err = tx.Take(&app, &db.App{
				ID: uint(customValue),
			}).Error
			if err != nil {
				svc.logger.Error().Err(err).Msg("Failed to find app by id from custom key TLV record")
				continue
			}
			return &app.ID
		}
	}
	return nil
}

func (svc *transactionsService) SettleHoldInvoice(ctx context.Context, preimage string, lnClient lnclient.LNClient) (*Transaction, error) {
	if len(preimage) != 64 {
		return nil, errors.New("invalid preimage format")
	}
	preimageBytes, err := hex.DecodeString(preimage)
	if err != nil {
		return nil, fmt.Errorf("invalid preimage hex: %w", err)
	}

	paymentHashBytes := sha256.Sum256(preimageBytes)
	paymentHash := hex.EncodeToString(paymentHashBytes[:])

	var dbTransaction db.Transaction
	result := svc.db.Limit(1).Find(&dbTransaction, &db.Transaction{
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_ACCEPTED,
		PaymentHash: paymentHash,
	})

	if result.RowsAffected == 0 {
		svc.logger.Error().Str("payment_hash", paymentHash).Msg("Failed to find accepted hold invoice")
		return nil, errors.New("failed to find accepted hold invoice")
	}

	if !dbTransaction.SelfPayment {
		err = lnClient.SettleHoldInvoice(ctx, preimage)
	}

	if err != nil {
		svc.logger.Error().Err(err).
			Str("preimage", preimage).
			Msg("Failed to settle hold invoice via FLN client")
		// Don't mark DB as failed here, as the settle might succeed later or might have already succeeded.
		return nil, err
	}

	var settledTransaction *db.Transaction
	err = svc.db.Transaction(func(tx *gorm.DB) error {
		var err error
		settledTransaction, err = svc.markTransactionSettled(tx, &dbTransaction, preimage, 0, dbTransaction.SelfPayment)
		return err
	})

	if err != nil {
		svc.logger.Error().Err(err).
			Str("payment_hash", paymentHash).
			Str("preimage", preimage).
			Msg("Failed DB transaction while settling hold invoice")
		return nil, err
	}
	if settledTransaction != nil {
		svc.publishSettleEvent(settledTransaction)
	}

	return settledTransaction, nil
}

func (svc *transactionsService) CancelHoldInvoice(ctx context.Context, paymentHash string, lnClient lnclient.LNClient) error {

	var dbTransaction db.Transaction
	result := svc.db.Limit(1).Find(&dbTransaction, &db.Transaction{
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_ACCEPTED,
		PaymentHash: paymentHash,
	})

	if result.RowsAffected == 0 {
		svc.logger.Error().Str("payment_hash", paymentHash).Msg("Failed to find accepted hold invoice")
		return NewNotFoundError()
	}

	if !dbTransaction.SelfPayment {
		err := lnClient.CancelHoldInvoice(ctx, paymentHash)
		if err != nil {
			svc.logger.Error().Err(err).
				Str("payment_hash", paymentHash).
				Msg("Failed to cancel hold invoice via FLN client")
			// Don't mark DB as failed here, cancellation might have already happened or might succeed later.
			return err
		}
	}

	err := svc.db.Transaction(func(tx *gorm.DB) error {
		var dbTransaction db.Transaction
		result := tx.Limit(1).Find(&dbTransaction, &db.Transaction{
			Type:        constants.TRANSACTION_TYPE_INCOMING,
			State:       constants.TRANSACTION_STATE_ACCEPTED,
			PaymentHash: paymentHash,
		})

		if result.Error != nil {
			svc.logger.Error().Err(result.Error).
				Str("payment_hash", paymentHash).
				Msg("Failed to find accepted hold invoice in DB for cancellation")
			return result.Error
		}
		if result.RowsAffected == 0 {
			svc.logger.Warn().
				Str("payment_hash", paymentHash).
				Msg("No accepted hold invoice found in DB to mark as failed due to cancellation")
			return NewNotFoundError()
		}

		_, markErr := svc.markPaymentFailed(tx, &dbTransaction, "Hold invoice was cancelled")
		return markErr
	})

	if err != nil {
		svc.logger.Error().Err(err).
			Str("payment_hash", paymentHash).
			Msg("Failed DB transaction while canceling hold invoice")
		return err
	}

	svc.logger.Info().
		Str("payment_hash", paymentHash).
		Msg("Marked hold invoice as failed in DB due to cancellation")

	svc.eventPublisher.Publish(&events.Event{
		Event:      "nwc_hold_invoice_canceled",
		Properties: &dbTransaction,
	})

	return nil
}

func (svc *transactionsService) SetTransactionMetadata(ctx context.Context, id uint, metadata map[string]interface{}) error {
	var metadataBytes []byte
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to serialize metadata")
		return err
	}
	if len(metadataBytes) > constants.INVOICE_METADATA_MAX_LENGTH {
		return fmt.Errorf("encoded invoice metadata provided is too large. Limit: %d Received: %d", constants.INVOICE_METADATA_MAX_LENGTH, len(metadataBytes))
	}

	err = svc.db.Model(&db.Transaction{}).Where("id", id).Update("metadata", datatypes.JSON(metadataBytes)).Error
	if err != nil {
		svc.logger.Error().Err(err).Interface("metadata", metadata).Msg("Failed to update transaction metadata")
		return err
	}

	return nil
}

func (svc *transactionsService) markTransactionSettled(tx *gorm.DB, dbTransaction *db.Transaction, preimage string, fee uint64, selfPayment bool) (*db.Transaction, error) {
	if preimage == "" {
		return nil, errors.New("no preimage in payment")
	}

	if tx.Name() == "postgres" {
		// lock based on payment hash to ensure we only mark one transaction as settled
		// (in sqlite transactions are serializable by default)
		transactionsWithPaymentHash := []db.Transaction{}
		tx.Where(&db.Transaction{
			PaymentHash: dbTransaction.PaymentHash,
		}).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&transactionsWithPaymentHash)
	}

	var existingSettledTransaction db.Transaction
	if tx.Limit(1).Find(&existingSettledTransaction, &db.Transaction{
		Type:        dbTransaction.Type,
		PaymentHash: dbTransaction.PaymentHash,
		State:       constants.TRANSACTION_STATE_SETTLED,
	}).RowsAffected > 0 {
		svc.logger.Debug().Str("payment_hash", dbTransaction.PaymentHash).Msg("payment already marked as sent")
		// Return nil so callers know not to fire a settlement event for an already-settled payment.
		return nil, nil
	}

	now := time.Now()
	err := tx.Model(dbTransaction).Updates(map[string]interface{}{
		"State":           constants.TRANSACTION_STATE_SETTLED,
		"Preimage":        &preimage,
		"FeeMloki":        fee,
		"FeeReserveMloki": 0,
		"SettledAt":       &now,
		"SelfPayment":     selfPayment,
	}).Error
	if err != nil {
		svc.logger.Error().Err(err).
			Str("payment_hash", dbTransaction.PaymentHash).
			Msg("Failed to update DB transaction")
		return nil, err
	}

	svc.logger.Info().
		Str("payment_hash", dbTransaction.PaymentHash).
		Str("type", dbTransaction.Type).
		Msg("Marked transaction as settled")

	if dbTransaction.Type == constants.TRANSACTION_TYPE_OUTGOING && dbTransaction.FeeSkimMloki > 0 {
		if err := svc.creditCircleHubFeeSkim(tx, dbTransaction); err != nil {
			svc.logger.Error().Err(err).
				Str("payment_hash", dbTransaction.PaymentHash).
				Msg("Failed to credit circle hub fee skim")
			return nil, err
		}
	}

	if dbTransaction.Type == constants.TRANSACTION_TYPE_OUTGOING && dbTransaction.AppId != nil {
		svc.checkBudgetUsage(dbTransaction, tx)
	}

	return dbTransaction, nil
}

// creditCircleHubFeeSkim credits a circle_hub with the forwarding fee it
// skimmed off one of its circle_wallet children's just-settled outgoing
// payment. The debit side already happened at payment initiation —
// dbTransaction.FeeSkimMloki was set on the child's own pending transaction
// row by validateCanPay and is included in every isolated-balance/budget-usage
// calculation (see db/queries), so this only needs to add the matching credit
// for the hub. Runs inside the same DB transaction as the child's settlement
// update (its caller, markTransactionSettled), so the debit and credit commit
// atomically together.
func (svc *transactionsService) creditCircleHubFeeSkim(tx *gorm.DB, dbTransaction *db.Transaction) error {
	var childApp db.App
	if tx.Select("parent_app_id").Limit(1).Find(&childApp, *dbTransaction.AppId).RowsAffected == 0 {
		return fmt.Errorf("failed to look up circle_wallet parent for fee skim: app %d not found", *dbTransaction.AppId)
	}
	if childApp.ParentAppID == nil {
		// Unreachable in practice — validateCanPay only ever computes a nonzero
		// FeeSkimMloki for a circle_wallet, which always has a parent hub — but
		// never silently skim into the void if lineage is somehow missing.
		svc.logger.Error().Uint("app_id", *dbTransaction.AppId).
			Msg("circle_wallet has FeeSkimMloki but no parent hub app; skipping fee credit")
		return nil
	}

	metadataBytes, err := json.Marshal(map[string]interface{}{
		"circle_fee_skim_source_app_id":       *dbTransaction.AppId,
		"circle_fee_skim_source_payment_hash": dbTransaction.PaymentHash,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	hubCredit := db.Transaction{
		AppId:       childApp.ParentAppID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: dbTransaction.FeeSkimMloki,
		PaymentHash: deriveCircleFeeSkimPaymentHash(dbTransaction.PaymentHash),
		Description: "Circle hub forwarding fee",
		Metadata:    datatypes.JSON(metadataBytes),
		SettledAt:   &now,
		SelfPayment: true,
	}
	return tx.Create(&hubCredit).Error
}

// deriveCircleFeeSkimPaymentHash generates a distinct, deterministic synthetic
// payment hash for a circle_hub's fee-skim ledger credit, derived from the
// child payment's own hash. It deliberately never collides with a real bolt11
// payment hash (a second-preimage attack would be required), so PaymentHash-
// scoped lookups (LookupTransaction et al.) never confuse this internal
// accounting row with the real payment it was skimmed from.
func deriveCircleFeeSkimPaymentHash(sourcePaymentHash string) string {
	sum := sha256.Sum256([]byte(sourcePaymentHash + ":circle_fee_skim"))
	return hex.EncodeToString(sum[:])
}

func (svc *transactionsService) publishSettleEvent(dbTransaction *db.Transaction) {
	event := "nwc_payment_sent"
	if dbTransaction.Type == constants.TRANSACTION_TYPE_INCOMING {
		event = "nwc_payment_received"
	}
	svc.eventPublisher.Publish(&events.Event{
		Event:      event,
		Properties: dbTransaction,
	})
}

func (svc *transactionsService) checkBudgetUsage(dbTransaction *db.Transaction, gormTransaction *gorm.DB) {
	var app db.App
	result := gormTransaction.Limit(1).Find(&app, &db.App{
		ID: *dbTransaction.AppId,
	})
	if result.RowsAffected == 0 {
		svc.logger.Error().Interface("app_id", dbTransaction.AppId).Msg("failed to find app by id")
		return
	}
	if app.IsIsolated() {
		return
	}

	var appPermission db.AppPermission
	result = gormTransaction.Limit(1).Find(&appPermission, &db.AppPermission{
		AppId: app.ID,
		Scope: constants.PAY_INVOICE_SCOPE,
	})
	if result.RowsAffected == 0 {
		svc.logger.Error().Interface("app_id", dbTransaction.AppId).Msg("failed to find pay_invoice scope")
		return
	}

	budgetUsage := queries.GetBudgetUsageSat(gormTransaction, &appPermission)
	warningUsage := uint64(math.Floor(float64(appPermission.MaxAmountLoki) * 0.8))
	if budgetUsage >= warningUsage && budgetUsage-dbTransaction.AmountMloki/1000 < warningUsage {
		svc.eventPublisher.Publish(&events.Event{
			Event: "nwc_budget_warning",
			Properties: map[string]interface{}{
				"name": app.Name,
				"id":   app.ID,
			},
		})
	}
}

// markPaymentFailed marks dbTransaction as FAILED, unless it's already FAILED
// or SETTLED, in which case it's a no-op and returns (nil, nil) - mirroring
// markTransactionSettled's own no-op-returns-nil convention below, so callers
// use the same "re-fetch the settled row if nil" pattern for both races.
func (svc *transactionsService) markPaymentFailed(tx *gorm.DB, dbTransaction *db.Transaction, reason string) (*db.Transaction, error) {
	var existingTransaction db.Transaction
	result := tx.Limit(1).Find(&existingTransaction, &db.Transaction{
		ID: dbTransaction.ID,
	})

	if result.Error != nil {
		svc.logger.Error().Err(result.Error).Str("payment_hash", dbTransaction.PaymentHash).Msg("could not find transaction to mark as failed")
		return nil, result.Error
	}

	if existingTransaction.State == constants.TRANSACTION_STATE_FAILED {
		svc.logger.Info().Str("payment_hash", dbTransaction.PaymentHash).Msg("payment already marked as failed")
		return nil, nil
	}

	// The payment-sent-event subscription can race the synchronous SendPaymentSync
	// call and settle this same row (see markTransactionSettled's own reverse-direction
	// guard) before this failure branch runs. Without this check, a payment that
	// actually succeeded would get flipped to FAILED here, dropping its amount out of
	// isolated-balance accounting (which only sums SETTLED rows) even though the funds
	// already left the node — and, for a JIT claim, incorrectly reopening the slice for
	// a second payout. Never downgrade an already-settled transaction to failed.
	if existingTransaction.State == constants.TRANSACTION_STATE_SETTLED {
		svc.logger.Info().Str("payment_hash", dbTransaction.PaymentHash).Msg("payment already settled; ignoring late failure notification")
		return nil, nil
	}

	err := tx.Model(dbTransaction).Updates(map[string]interface{}{
		"State":           constants.TRANSACTION_STATE_FAILED,
		"FeeReserveMloki": 0,
		"FailureReason":   reason,
	}).Error
	if err != nil {
		svc.logger.Error().Err(err).
			Str("payment_hash", dbTransaction.PaymentHash).
			Msg("Failed to mark transaction as failed")
		return nil, err
	}
	svc.logger.Info().Str("payment_hash", dbTransaction.PaymentHash).Msg("Marked transaction as failed")
	svc.eventPublisher.Publish(&events.Event{
		Event:      "nwc_payment_failed",
		Properties: dbTransaction,
	})
	return dbTransaction, nil
}

// EstimateFee calculates potential fees based on route hints in the invoice
func (svc *transactionsService) EstimateFee(payReq string) (uint64, error) {
	paymentRequest, err := decodepay.Decode(payReq)
	if err != nil {
		return 0, err
	}

	var maxHintFeeMloki uint64 = 0

	for _, route := range paymentRequest.Route {
		var routeFeeMloki uint64 = 0
		for _, hop := range route {
			fee := uint64(hop.FeeBaseMloki) + (uint64(paymentRequest.MSat) * uint64(hop.FeeProportionalMillionths) / 1000000) //nolint:gosec // invoice-declared msat amount is always non-negative and far below int64 range; this is a fee preview only, doesn't gate actual payment execution
			routeFeeMloki += fee
		}
		if routeFeeMloki > maxHintFeeMloki {
			maxHintFeeMloki = routeFeeMloki
		}
	}

	return maxHintFeeMloki, nil
}
