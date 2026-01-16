package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type payKeysendParams struct {
	Amount     uint64               `json:"amount"`
	Pubkey     string               `json:"pubkey"`
	Preimage   string               `json:"preimage"`
	TLVRecords []lnclient.TLVRecord `json:"tlv_records"`
}

func (controller *nip47Controller) HandlePayKeysendEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	payKeysendParams := &payKeysendParams{}
	resp := decodeRequest(nip47Request, payKeysendParams)
	if resp != nil {
		publishResponse(resp, tags)
		return
	}
	controller.payKeysend(ctx, payKeysendParams, nip47Request, requestEventId, app, publishResponse, tags)
}

func (controller *nip47Controller) payKeysend(ctx context.Context, payKeysendParams *payKeysendParams, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, tags nostr.Tags) {
	logger.Logger.Info().
		Interface("request_event_id", requestEventId).
		Interface("appId", app.ID).
		Interface("senderPubkey", payKeysendParams.Pubkey).
		Msg("Sending keysend payment")

	transaction, err := controller.transactionsService.SendKeysend(payKeysendParams.Amount, payKeysendParams.Pubkey, payKeysendParams.TLVRecords, payKeysendParams.Preimage, controller.lnClient, &app.ID, &requestEventId)
	if err != nil {
		logger.Logger.Info().Err(err).
			Interface("request_event_id", requestEventId).
			Interface("appId", app.ID).
			Interface("recipientPubkey", payKeysendParams.Pubkey).
			Msg("Failed to send keysend payment")
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, tags)
		return
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: payResponse{
			Preimage: *transaction.Preimage,
			FeesPaid: transaction.FeeMloki,
		},
	}, tags)
}
