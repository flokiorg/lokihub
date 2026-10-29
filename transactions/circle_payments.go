package transactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/atrest"
	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
)

// KeysendFeeLimiter is an LN client that can cap a keysend's routing fee. A
// Circle Wallet's Lightning keysend uses it to stay within its routing-fee
// reserve (NIP-CW §Own Funds and Caps); a client without it keeps its own
// default limit.
type KeysendFeeLimiter interface {
	SendKeysendWithFeeLimit(amount uint64, destination string, customRecords []lnclient.TLVRecord, preimage string, feeLimitMloki uint64) (*lnclient.PayKeysendResponse, error)
}

// loadApp is appId's app, or nil for no app.
func loadApp(tx *gorm.DB, appId *uint) (*db.App, error) {
	if appId == nil {
		return nil, nil
	}
	var app db.App
	if err := tx.Where("id = ?", *appId).Limit(1).Find(&app).Error; err != nil {
		return nil, err
	}
	if app.ID == 0 {
		return nil, nil
	}
	return &app, nil
}

// incomingApp is the app an invoice on this node was issued for, by payment
// hash: nil for the host's own wallet or no such invoice.
func incomingApp(tx *gorm.DB, paymentHash string) (*db.App, error) {
	var row db.Transaction
	if err := tx.Select("app_id").Where("type = ? AND payment_hash = ?", constants.TRANSACTION_TYPE_INCOMING, paymentHash).
		Limit(1).Find(&row).Error; err != nil {
		return nil, err
	}
	return loadApp(tx, row.AppId)
}

// lockCircleParties takes the locks a circle payment needs, in the one order
// every path uses: the Hubs involved in ascending id order, then the paying
// app.
func lockCircleParties(tx *gorm.DB, payer *db.App, others ...*db.App) error {
	var hubs []uint
	for _, app := range append([]*db.App{payer}, others...) {
		if circlemoney.IsCircleWallet(app) && !slices.Contains(hubs, *app.ParentAppID) {
			hubs = append(hubs, *app.ParentAppID)
		}
	}
	slices.Sort(hubs)
	for _, hub := range hubs {
		if err := db.LockApp(tx, hub); err != nil {
			return err
		}
	}
	if payer != nil {
		return db.LockApp(tx, payer.ID)
	}
	return nil
}

// stampSend writes a circle send quote onto an outgoing row.
func stampSend(row *db.Transaction, q circlemoney.SendQuote) {
	row.PaymentKind = q.Kind
	row.FeeSkimMloki = q.FeeSkim
	row.FeeReserveMloki = q.FeeReserve
	row.CountsSend = q.CountsSend
	row.PeriodStart = q.PeriodStart
	row.OutboundPeriodStart = q.OutboundPeriodStart
}

// stampCircleReceipt classifies an incoming row of a Circle Wallet as it
// settles: its kind from the payer's side (another app on this node for a
// self-payment, Lightning otherwise) and the budget period of the receipt.
func stampCircleReceipt(tx *gorm.DB, row *db.Transaction, selfPayment bool, now time.Time) error {
	if row.Type != constants.TRANSACTION_TYPE_INCOMING || row.AppId == nil {
		return nil
	}
	wallet, err := loadApp(tx, row.AppId)
	if err != nil || !circlemoney.IsCircleWallet(wallet) {
		return err
	}
	var payer *db.App
	if selfPayment {
		var out db.Transaction
		if err := tx.Select("app_id").Where("type = ? AND payment_hash = ?", constants.TRANSACTION_TYPE_OUTGOING, row.PaymentHash).
			Limit(1).Find(&out).Error; err != nil {
			return err
		}
		if payer, err = loadApp(tx, out.AppId); err != nil {
			return err
		}
	}
	caps, err := circlemoney.WalletCaps(tx, wallet)
	if err != nil {
		return err
	}
	start, _, err := circlemoney.Period(circlemoney.Anchor(wallet), caps.Renewal, now)
	if err != nil {
		return err
	}
	row.PaymentKind = circlemoney.Kind(wallet, payer, selfPayment)
	row.PeriodStart = start.Unix()
	return tx.Model(row).Updates(map[string]interface{}{
		"payment_kind": row.PaymentKind,
		"period_start": row.PeriodStart,
	}).Error
}

// checkCircleKeysendCredit decides, inside the paying transaction and under the
// receiving Hub's lock, whether a keysend may credit recipient: every keysend
// names none of a wallet's invoices, so its receive room and its circle's
// holdings ceiling apply (NIP-CW §Own Funds and Caps).
func checkCircleKeysendCredit(tx *gorm.DB, recipient, payer *db.App, sameNode bool, amount uint64) error {
	if !circlemoney.IsCircleWallet(recipient) {
		return nil
	}
	now := time.Now()
	return circlemoney.CheckCredit(tx, circlemoney.CreditInput{
		Wallet: recipient, Payer: payer, SameNode: sameNode, Amount: amount, Now: now,
		PayerActive: circlemoney.IsCircleWallet(payer) && circlelife.IsActive(payer, now),
	})
}

// MakeCircleInvoice issues a Circle Wallet's invoice (NIP-CW §Own Funds and
// Caps), checked under its Hub's lock: an amount is required (RESTRICTED), the
// wallet's receive room (QUOTA_EXCEEDED), the circle's holdings ceiling
// (INSUFFICIENT_CAPACITY), the open-invoice cap and the creation rate
// (RATE_LIMITED). The invoice is a hold invoice the Hub settles itself once it
// re-checks the payment (ResolveCircleHold), so every payment to a Circle
// Wallet reaches a point where the Hub can still refuse it. The preimage is the
// Hub's, kept on the row like any invoice's.
func (svc *transactionsService) MakeCircleInvoice(ctx context.Context, wallet *db.App, amount uint64, description, descriptionHash string,
	expiry uint64, metadata map[string]interface{}, lnClient lnclient.LNClient, requestEventId *uint) (*Transaction, error) {
	if amount == 0 {
		return nil, &circlemoney.Refusal{Code: constants.ERROR_RESTRICTED, Message: "a circle wallet invoice needs an amount"}
	}
	if metadata != nil {
		delete(metadata, "app_id")
		delete(metadata, "internal_transfer")
	}
	var metadataBytes []byte
	if metadata != nil {
		var err error
		if metadataBytes, err = json.Marshal(metadata); err != nil {
			return nil, err
		}
		if len(metadataBytes) > constants.INVOICE_METADATA_MAX_LENGTH {
			return nil, fmt.Errorf("encoded invoice metadata provided is too large. Limit: %d Received: %d", constants.INVOICE_METADATA_MAX_LENGTH, len(metadataBytes))
		}
	}
	now := time.Now()
	if !circlemoney.Invoices.Allow(wallet.ID, circlemoney.InvoicesPerMinute, now) {
		return nil, &circlemoney.Refusal{Code: constants.ERROR_RATE_LIMITED, Message: "too many invoices; retry later"}
	}
	preimageBytes, err := makePreimageHex()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(preimageBytes)
	paymentHash := hex.EncodeToString(sum[:])
	// The preimage is kept sealed until the Hub settles: whoever reads the
	// database alone cannot settle, or prove payment of, an open invoice.
	sealed, err := atrest.Seal(hex.EncodeToString(preimageBytes))
	if err != nil {
		return nil, fmt.Errorf("seal the invoice's preimage: %w", err)
	}

	// The row is reserved under the Hub's lock and the node is asked outside any
	// transaction, so a slow node never holds the write lock. The reservation
	// counts against every cap at once; nobody can pay it before the node issues
	// the invoice and this call returns it.
	reserveExpiry := int64(expiry) //nolint:gosec // bounded by the caller
	if reserveExpiry <= 0 {
		reserveExpiry = lnclient.DEFAULT_INVOICE_EXPIRY
	}
	reservedUntil := now.Add(time.Duration(reserveExpiry) * time.Second)
	row := db.Transaction{
		AppId:              &wallet.ID,
		RequestEventId:     requestEventId,
		Type:               constants.TRANSACTION_TYPE_INCOMING,
		State:              constants.TRANSACTION_STATE_PENDING,
		AmountMloki:        amount,
		Description:        description,
		DescriptionHash:    descriptionHash,
		PaymentHash:        paymentHash,
		ExpiresAt:          &reservedUntil,
		HoldPreimageSealed: &sealed,
		Metadata:           datatypes.JSON(metadataBytes),
		Hold:               true,
	}
	err = svc.db.Transaction(func(tx *gorm.DB) error {
		if err := lockCircleParties(tx, wallet); err != nil {
			return err
		}
		// Only an Active wallet receives (NIP-CW §Own Funds and Caps).
		current, err := loadApp(tx, &wallet.ID)
		if err != nil {
			return err
		}
		if current == nil || !circlelife.IsActive(current, now) {
			return &circlemoney.Refusal{Code: constants.ERROR_RESTRICTED, Message: "this circle wallet is not active, so it cannot receive"}
		}
		room, err := circlemoney.ReceiveRoom(tx, wallet, now)
		if err != nil {
			return err
		}
		if int64(min(amount, uint64(1)<<62)) > room { //nolint:gosec // capped at 2^62 by min
			return &circlemoney.Refusal{Code: constants.ERROR_QUOTA_EXCEEDED, Message: "the invoice is larger than the wallet's receive room"}
		}
		cfg, err := circlemoney.HubConfig(tx, *wallet.ParentAppID)
		if err != nil {
			return err
		}
		if err := circlemoney.CheckHoldings(tx, *wallet.ParentAppID, cfg, amount, now); err != nil {
			return err
		}
		if _, open := circlemoney.OpenInvoices(tx, []uint{wallet.ID}, now); open >= circlemoney.MaxOpenInvoices {
			return &circlemoney.Refusal{Code: constants.ERROR_RATE_LIMITED, Message: "too many open invoices for this wallet"}
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return nil, err
	}

	invoice, err := lnClient.MakeHoldInvoice(ctx, int64(amount), description, descriptionHash, int64(expiry), paymentHash) //nolint:gosec // bounded by the receive room
	if err != nil {
		// Nothing was issued: the reservation goes.
		if delErr := svc.db.Where("id = ? AND state = ?", row.ID, constants.TRANSACTION_STATE_PENDING).Delete(&db.Transaction{}).Error; delErr != nil {
			svc.logger.Error().Err(delErr).Str("payment_hash", paymentHash).Msg("Failed to drop a circle invoice reservation the node did not issue")
		}
		return nil, err
	}
	var expiresAt *time.Time
	if invoice.ExpiresAt != nil {
		at := time.Unix(*invoice.ExpiresAt, 0)
		expiresAt = &at
	}
	// The reservation is completed only while it is still open: a wallet that
	// stopped receiving, or was deleted, meanwhile failed it, so the invoice is
	// cancelled at the node before anyone learns it.
	done := svc.db.Model(&db.Transaction{}).Where("id = ? AND state = ?", row.ID, constants.TRANSACTION_STATE_PENDING).
		Updates(map[string]interface{}{"payment_request": invoice.Invoice, "expires_at": expiresAt})
	if done.Error != nil || done.RowsAffected != 1 {
		if cancelErr := lnClient.CancelHoldInvoice(ctx, paymentHash); cancelErr != nil {
			svc.logger.Error().Err(cancelErr).Str("payment_hash", paymentHash).
				Msg("Failed to cancel a circle invoice whose reservation was closed")
		}
		if done.Error != nil {
			return nil, done.Error
		}
		return nil, &circlemoney.Refusal{Code: constants.ERROR_RESTRICTED, Message: "this circle wallet stopped receiving; the invoice was cancelled"}
	}
	row.PaymentRequest, row.ExpiresAt = invoice.Invoice, expiresAt
	return &row, nil
}

// holdPreimage is the preimage of a Hub-settled hold invoice: opened from its
// sealed copy, or the plain one once it settled; "" when it holds none.
func holdPreimage(row *db.Transaction) (string, error) {
	if row.HoldPreimageSealed != nil && *row.HoldPreimageSealed != "" {
		return atrest.Open(*row.HoldPreimageSealed)
	}
	if row.Preimage != nil {
		return *row.Preimage, nil
	}
	return "", nil
}

// SealHoldPreimages seals the plain preimage of every Hub-settled hold invoice
// not yet settled, as one written before preimages were sealed at rest. It is
// idempotent, and runs once the at-rest key is loaded.
func SealHoldPreimages(gormDB *gorm.DB) (int, error) {
	var rows []db.Transaction
	if err := gormDB.Table("transactions").Select("transactions.id", "transactions.preimage").
		Joins("JOIN apps ON apps.id = transactions.app_id").
		Where("apps.kind = ? AND transactions.type = ? AND transactions.hold = ? AND transactions.state <> ?",
			db.AppKindCircleWallet, constants.TRANSACTION_TYPE_INCOMING, true, constants.TRANSACTION_STATE_SETTLED).
		Where("transactions.preimage IS NOT NULL AND transactions.preimage <> '' AND transactions.hold_preimage_sealed IS NULL").
		Find(&rows).Error; err != nil {
		return 0, err
	}
	sealedCount := 0
	for _, r := range rows {
		sealed, err := atrest.Seal(*r.Preimage)
		if err != nil {
			return sealedCount, err
		}
		res := gormDB.Model(&db.Transaction{}).Where("id = ? AND hold_preimage_sealed IS NULL AND state <> ?", r.ID, constants.TRANSACTION_STATE_SETTLED).
			Updates(map[string]interface{}{"hold_preimage_sealed": sealed, "preimage": nil})
		if res.Error != nil {
			return sealedCount, res.Error
		}
		sealedCount += int(res.RowsAffected)
	}
	return sealedCount, nil
}

// isCircleHold reports whether row is a Hub-settled Circle Wallet invoice:
// every hold invoice of a Circle Wallet is one, since a member cannot issue a
// hold invoice of its own.
func isCircleHold(tx *gorm.DB, row *db.Transaction) bool {
	if !row.Hold || row.Type != constants.TRANSACTION_TYPE_INCOMING {
		return false
	}
	app, err := loadApp(tx, row.AppId)
	return err == nil && circlemoney.IsCircleWallet(app)
}

// circleHoldAcceptable re-checks an accepted Circle Wallet payment before the
// Hub settles it: the wallet still exists and is Active, the amount paid is the invoice's
// (amountPaid < 0 when not known), and the invoice had not expired.
func circleHoldAcceptable(tx *gorm.DB, row *db.Transaction, amountPaid int64, now time.Time) (bool, string) {
	wallet, err := loadApp(tx, row.AppId)
	if err != nil || wallet == nil {
		return false, "the wallet is gone"
	}
	if !circlelife.IsActive(wallet, now) {
		return false, "the wallet is no longer active"
	}
	if amountPaid >= 0 && uint64(amountPaid) != row.AmountMloki {
		return false, "the amount paid is not the invoice's"
	}
	if row.ExpiresAt != nil && now.After(*row.ExpiresAt) {
		return false, "the invoice had expired"
	}
	return true, ""
}

// ResolveCircleHold settles an accepted Circle Wallet payment, or cancels it
// when the re-check refuses it. A hold that is not a Circle Wallet's, or is no
// longer accepted, is left alone.
func (svc *transactionsService) ResolveCircleHold(ctx context.Context, paymentHash string, amountPaid int64, lnClient lnclient.LNClient) error {
	var row db.Transaction
	if err := svc.db.Where("type = ? AND state = ? AND payment_hash = ?", constants.TRANSACTION_TYPE_INCOMING,
		constants.TRANSACTION_STATE_ACCEPTED, paymentHash).Limit(1).Find(&row).Error; err != nil {
		return err
	}
	if row.ID == 0 || !isCircleHold(svc.db, &row) {
		return nil
	}
	ok, reason := circleHoldAcceptable(svc.db, &row, amountPaid, time.Now())
	if ok {
		preimage, err := holdPreimage(&row)
		if err != nil {
			return fmt.Errorf("open the invoice's preimage: %w", err)
		}
		if preimage != "" {
			_, err = svc.SettleHoldInvoice(ctx, preimage, lnClient)
			return err
		}
	}
	if !ok {
		svc.logger.Info().Str("payment_hash", paymentHash).Str("reason", reason).Msg("Cancelling a circle wallet payment the Hub refuses")
	}
	return svc.CancelHoldInvoice(ctx, paymentHash, lnClient)
}

// ResolveAcceptedCircleHolds resolves every Circle Wallet payment left accepted,
// as after a restart.
func (svc *transactionsService) ResolveAcceptedCircleHolds(ctx context.Context, lnClient lnclient.LNClient) {
	var rows []db.Transaction
	if err := svc.db.Table("transactions").Select("transactions.payment_hash", "transactions.self_payment").
		Joins("JOIN apps ON apps.id = transactions.app_id").
		Where("apps.kind = ? AND transactions.type = ? AND transactions.state = ? AND transactions.hold = ?",
			db.AppKindCircleWallet, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_ACCEPTED, true).
		Find(&rows).Error; err != nil {
		svc.logger.Error().Err(err).Msg("Failed to list accepted circle wallet payments")
		return
	}
	for _, row := range rows {
		// The amount the node holds is checked against the invoice's, as when
		// the payment was accepted live: an over-payment is cancelled, never
		// settled with the excess left to the host. A same-node payment has no
		// node invoice; its amount is the payer's own.
		amountPaid := int64(-1)
		if !row.SelfPayment {
			inv, err := lnClient.LookupInvoice(ctx, row.PaymentHash)
			if err != nil || inv == nil {
				svc.logger.Warn().Err(err).Str("payment_hash", row.PaymentHash).
					Msg("Could not read an accepted circle wallet payment's amount; left for the next run")
				continue
			}
			amountPaid = inv.AmountPaid
		}
		if err := svc.ResolveCircleHold(ctx, row.PaymentHash, amountPaid, lnClient); err != nil {
			svc.logger.Error().Err(err).Str("payment_hash", row.PaymentHash).Msg("Failed to resolve an accepted circle wallet payment")
		}
	}
}

// SetLNClientProvider gives the service the node, for resolving a Circle Wallet
// payment the node reports accepted.
func (svc *transactionsService) SetLNClientProvider(provider func() lnclient.LNClient) {
	svc.lnClientProvider = provider
}

// interceptSelfCircleHold settles a same-node payment of a Circle Wallet's
// invoice at once: the Hub accepts it, re-checks it and settles or cancels it,
// with no node involved.
func (svc *transactionsService) interceptSelfCircleHold(row *db.Transaction, lnClient lnclient.LNClient) (*lnclient.PayInvoiceResponse, error) {
	// Only the payment that accepted the invoice may take its outcome: another
	// payer's settlement must not read as this one's.
	if !svc.markHoldInvoiceAccepted(row.PaymentHash, 0, true) {
		return nil, errors.New("this invoice is already being paid by another payment")
	}
	if err := svc.ResolveCircleHold(context.Background(), row.PaymentHash, int64(row.AmountMloki), lnClient); err != nil { //nolint:gosec // invoice amount
		return nil, err
	}
	var settled db.Transaction
	if err := svc.db.Where("id = ?", row.ID).First(&settled).Error; err != nil {
		return nil, err
	}
	if settled.State != constants.TRANSACTION_STATE_SETTLED || settled.Preimage == nil {
		return nil, lnclient.NewHoldInvoiceCanceledError()
	}
	return &lnclient.PayInvoiceResponse{Preimage: *settled.Preimage}, nil
}

// publishHoldAccepted reports whether an accepted hold is announced to its app:
// a Circle Wallet's are the Hub's own, never the member's to settle.
func publishHoldAccepted(tx *gorm.DB, row *db.Transaction) bool {
	return !isCircleHold(tx, row)
}

// CancelHoldInvoicesAtNode cancels at the node each hold invoice of
// paymentHashes, whose rows the caller already failed (a deleted Circle
// Wallet's): an invoice issued after the wallet's last cancel would otherwise
// stay payable, and its HTLC held. Failures are logged; a payment that still
// arrives is cancelled when the node reports it (cancelHeldForFailedRow).
func (svc *transactionsService) CancelHoldInvoicesAtNode(ctx context.Context, paymentHashes []string, lnClient lnclient.LNClient) {
	if lnClient == nil {
		return
	}
	for _, hash := range paymentHashes {
		if err := lnClient.CancelHoldInvoice(ctx, hash); err != nil && !errors.Is(err, lnclient.ErrInvoiceNotFound) {
			svc.logger.Warn().Err(err).Str("payment_hash", hash).Msg("Failed to cancel a closed hold invoice at the node")
		}
	}
}

// cancelHeldForFailedRow cancels at the node a payment the node holds for an
// invoice whose row is already failed: nothing will ever settle it, so it is
// released now rather than at its CLTV.
func (svc *transactionsService) cancelHeldForFailedRow(ctx context.Context, paymentHash string) {
	var n int64
	if err := svc.db.Model(&db.Transaction{}).Where("payment_hash = ? AND type = ? AND state = ? AND hold = ?",
		paymentHash, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_FAILED, true).Count(&n).Error; err != nil || n == 0 {
		return
	}
	if svc.lnClientProvider == nil {
		return
	}
	lnClient := svc.lnClientProvider()
	if lnClient == nil {
		return
	}
	svc.logger.Info().Str("payment_hash", paymentHash).Msg("Cancelling a payment held for a closed invoice")
	if err := lnClient.CancelHoldInvoice(ctx, paymentHash); err != nil {
		svc.logger.Error().Err(err).Str("payment_hash", paymentHash).Msg("Failed to cancel a payment held for a closed invoice")
	}
}

// CancelOpenCircleInvoices cancels every open invoice of a Circle Wallet at the
// node and fails its row, as a wallet that leaves Active or is deleted must
// (NIP-CW §Own Funds and Caps, §Deleting a Circle Wallet). A held payment the
// Hub accepted is cancelled too. The first node failure is returned; the rows
// already cancelled stay so.
func (svc *transactionsService) CancelOpenCircleInvoices(ctx context.Context, walletID uint, lnClient lnclient.LNClient) error {
	var rows []db.Transaction
	if err := svc.db.Where("app_id = ? AND type = ? AND state IN ?", walletID, constants.TRANSACTION_TYPE_INCOMING,
		[]string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_ACCEPTED}).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		row := &rows[i]
		if row.State == constants.TRANSACTION_STATE_ACCEPTED {
			if err := svc.CancelHoldInvoice(ctx, row.PaymentHash, lnClient); err != nil {
				return err
			}
			continue
		}
		// A node with no such invoice (never issued, or forgotten) has nothing
		// left to cancel: the row is failed all the same.
		if row.Hold && !row.SelfPayment && lnClient != nil {
			if err := lnClient.CancelHoldInvoice(ctx, row.PaymentHash); err != nil && !errors.Is(err, lnclient.ErrInvoiceNotFound) {
				return fmt.Errorf("cancel invoice %s at the node: %w", row.PaymentHash, err)
			}
		}
		if err := svc.db.Model(&db.Transaction{}).Where("id = ? AND state = ?", row.ID, constants.TRANSACTION_STATE_PENDING).
			Updates(map[string]interface{}{"state": constants.TRANSACTION_STATE_FAILED, "failure_reason": "cancelled: the circle wallet cannot receive"}).Error; err != nil {
			return err
		}
	}
	return nil
}

// SetOrderIntake sets the order intake (see the interface).
func (svc *transactionsService) SetOrderIntake(intake func(ctx context.Context, inv *lnclient.Transaction) bool,
	selfPay func(ctx context.Context, paymentHash string, amountMloki uint64) (string, bool, error)) {
	svc.orderIntake = intake
	svc.orderSelfPay = selfPay
}
