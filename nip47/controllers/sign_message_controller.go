package controllers

import (
	"context"
	"strings"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

type signMessageParams struct {
	Message string `json:"message"`
}

type signMessageResponse struct {
	Message   string `json:"message"`
	Signature string `json:"signature"`
}

func (controller *nip47Controller) HandleSignMessageEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, publishResponse publishFunc) {
	signParams := &signMessageParams{}
	resp := decodeRequest(nip47Request, signParams)
	if resp != nil {
		publishResponse(resp, nostr.Tags{})
		return
	}

	// Never let an app borrow the node key to forge mint provenance. This
	// method signs an app-supplied string with the node's identity key, using
	// the very same prefixing and hashing that lokicash.VerifyMint recovers a
	// minter from — so a payload starting with lokicash.MintPayloadPrefix would
	// come back as a signature that verifies as this hub's own provenance for
	// whatever wallet pubkey and amount the app chose. Refused here rather than
	// filtered in the LN client, so the app gets a real NIP-47 error instead of
	// an opaque signing failure.
	//
	// Case-insensitive purely as belt-and-braces: MintPayload emits lowercase
	// and VerifyMint recomputes it the same way, so a differently-cased payload
	// could not verify anyway — but the check costs nothing and the refusal
	// should not hinge on that reasoning staying true.
	if strings.HasPrefix(strings.ToLower(signParams.Message), lokicash.MintPayloadPrefix) {
		logger.Logger.Warn().
			Interface("request_event_id", requestEventId).
			Msg("Refusing to sign a mint-provenance payload via sign_message")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED,
			"refusing to sign a "+lokicash.MintPayloadPrefix+" payload: mint provenance may only be produced by the hub's own minting path")
		return
	}

	logger.Logger.Info().
		Interface("request_event_id", requestEventId).
		Msg("Signing message")

	signature, err := controller.lnClient.SignMessage(ctx, signParams.Message)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request_event_id", requestEventId).
			Msg("Failed to sign message")
		publishResponse(&models.Response{
			ResultType: nip47Request.Method,
			Error:      mapNip47Error(err),
		}, nostr.Tags{})
		return
	}

	responsePayload := signMessageResponse{
		Message:   signParams.Message,
		Signature: signature,
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     responsePayload,
	}, nostr.Tags{})
}
