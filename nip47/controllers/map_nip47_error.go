package controllers

import (
	"errors"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
)

func mapNip47Error(err error) *models.Error {
	// A Circle Wallet refusal carries its own NIP-CW code.
	if refusal, ok := circlemoney.AsRefusal(err); ok {
		return &models.Error{Code: refusal.Code, Message: refusal.Message}
	}
	if errors.Is(err, lnclient.NewHoldInvoiceCanceledError()) {
		return &models.Error{Code: constants.ERROR_PAYMENT_FAILED, Message: err.Error()}
	}
	code := constants.ERROR_INTERNAL
	if errors.Is(err, transactions.NewNotFoundError()) {
		code = constants.ERROR_NOT_FOUND
	}
	if errors.Is(err, transactions.NewInsufficientBalanceError()) {
		code = constants.ERROR_INSUFFICIENT_BALANCE
	}
	if errors.Is(err, transactions.NewQuotaExceededError()) {
		code = constants.ERROR_QUOTA_EXCEEDED
	}
	if errors.Is(err, transactions.NewCashPartialSpendError()) {
		code = constants.ERROR_RESTRICTED
	}

	return &models.Error{
		Code:    code,
		Message: err.Error(),
	}
}
