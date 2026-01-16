package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/flokiorg/lokihub/events"
	decodepay "github.com/flokiorg/lokihub/lndecodepay"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/pkg/version"
)

func (api *api) RebalanceChannel(ctx context.Context, rebalanceChannelRequest *RebalanceChannelRequest) (*RebalanceChannelResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}

	if !api.cfg.EnableRebalance() {
		return nil, errors.New("rebalance feature is disabled")
	}

	receiveMetadata := map[string]interface{}{
		"receive_through": rebalanceChannelRequest.ReceiveThroughNodePubkey,
	}

	receiveInvoice, err := api.svc.GetTransactionsService().MakeInvoice(ctx, rebalanceChannelRequest.AmountLoki*1000, "Lokihub Rebalance through "+rebalanceChannelRequest.ReceiveThroughNodePubkey, "", 0, receiveMetadata, api.svc.GetLNClient(), nil, nil, &rebalanceChannelRequest.ReceiveThroughNodePubkey, nil, nil, nil, nil)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to generate rebalance receive invoice")
		return nil, err
	}

	type rspCreateOrderRequest struct {
		Token                   string `json:"token"`
		PayRequest              string `json:"pay_request"`
		PayThroughThisPublicKey string `json:"pay_through_this_public_key"`
	}

	newRspCreateOrderRequest := rspCreateOrderRequest{
		Token:                   "loki-hub",
		PayRequest:              receiveInvoice.PaymentRequest,
		PayThroughThisPublicKey: rebalanceChannelRequest.ReceiveThroughNodePubkey,
	}

	payloadBytes, err := json.Marshal(newRspCreateOrderRequest)
	if err != nil {
		return nil, err
	}
	bodyReader := bytes.NewReader(payloadBytes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api.cfg.GetRebalanceServiceUrl(), bodyReader)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request", newRspCreateOrderRequest).
			Msg("Failed to create new rebalance request")
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Lokihub/"+version.Tag)

	client := http.Client{
		Timeout: time.Second * 60,
	}

	res, err := client.Do(req)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request", newRspCreateOrderRequest).
			Msg("Failed to request new rebalance order")
		return nil, err
	}

	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request", newRspCreateOrderRequest).
			Msg("Failed to read response body")
		return nil, errors.New("failed to read response body")
	}

	if res.StatusCode >= 300 {
		logger.Logger.Error().
			Interface("request", newRspCreateOrderRequest).
			Str("body", string(body)).
			Int("statusCode", res.StatusCode).
			Msg("rebalance create_order endpoint returned non-success code")
		return nil, fmt.Errorf("rebalance create_order endpoint returned non-success code: %s", string(body))
	}

	type rspRebalanceCreateOrderResponse struct {
		OrderId    string `json:"order_id"`
		PayRequest string `json:"pay_request"`
	}

	var rebalanceCreateOrderResponse rspRebalanceCreateOrderResponse

	err = json.Unmarshal(body, &rebalanceCreateOrderResponse)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("request", newRspCreateOrderRequest).
			Msg("Failed to deserialize json")
		return nil, fmt.Errorf("failed to deserialize json from rebalance create order response: %s", string(body))
	}

	logger.Logger.Info().Interface("response", rebalanceCreateOrderResponse).Msg("New rebalance order created")

	paymentRequest, err := decodepay.Decodepay(rebalanceCreateOrderResponse.PayRequest)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to decode bolt11 invoice")
		return nil, err
	}

	if paymentRequest.MLoki > int64(float64(rebalanceChannelRequest.AmountLoki)*float64(1000)*float64(1.003)+1 /*0.3% fees*/) {
		return nil, errors.New("rebalance payment is more expensive than expected")
	}

	payMetadata := map[string]interface{}{
		"receive_through": rebalanceChannelRequest.ReceiveThroughNodePubkey,
		"amount_sat":      rebalanceChannelRequest.AmountLoki,
		"order_id":        rebalanceCreateOrderResponse.OrderId,
	}

	payRebalanceInvoiceResponse, err := api.svc.GetTransactionsService().SendPaymentSync(rebalanceCreateOrderResponse.PayRequest, nil, payMetadata, api.svc.GetLNClient(), nil, nil)

	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to pay rebalance invoice")
		return nil, err
	}

	api.eventPublisher.Publish(&events.Event{
		Event:      "nwc_rebalance_succeeded",
		Properties: map[string]interface{}{},
	})

	return &RebalanceChannelResponse{
		TotalFeeLoki: uint64(paymentRequest.MLoki)/1000 + payRebalanceInvoiceResponse.FeeMloki/1000 - rebalanceChannelRequest.AmountLoki,
	}, nil
}
