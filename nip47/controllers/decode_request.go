package controllers

import (
	"encoding/json"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
)

func decodeRequest(request *models.Request, methodParams interface{}) *models.Response {
	err := json.Unmarshal(request.Params, methodParams)
	if err != nil {
		// Params are never logged: they can carry a cash secret or a proof.
		logger.Logger.Error().Err(err).
			Str("method", request.Method).
			Msg("Failed to decode NIP-47 request")
		return &models.Response{
			ResultType: request.Method,
			Error: &models.Error{
				Code:    constants.ERROR_BAD_REQUEST,
				Message: err.Error(),
			}}
	}
	return nil
}
