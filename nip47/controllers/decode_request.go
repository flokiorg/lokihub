package controllers

import (
	"encoding/json"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/sirupsen/logrus"
)

func decodeRequest(request *models.Request, methodParams interface{}) *models.Response {
	err := json.Unmarshal(request.Params, methodParams)
	if err != nil {
		logger.Logger.WithFields(logrus.Fields{
			"request": request,
		}).WithError(err).Error("Failed to decode NIP-47 request")
		return &models.Response{
			ResultType: request.Method,
			Error: &models.Error{
				Code:    constants.ERROR_BAD_REQUEST,
				Message: err.Error(),
			}}
	}
	return nil
}
