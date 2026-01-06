package controllers

import (
	"errors"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
)

func mapNip47Error(err error) *models.Error {
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

	return &models.Error{
		Code:    code,
		Message: err.Error(),
	}
}
