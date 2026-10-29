package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Minting Cash: a caller-supplied cash_secret MUST be rejected — only the
// Hub can vouch for the entropy behind it.
func TestHandleMintCashEvent_CallerSuppliedCashSecret_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	const callerSecret = "1111111111111111111111111111111111111111111111111111111111111111"
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"method": "mint_cash",
		"params": {"request_id":"`+testRequestID()+`","recipients": [{"identity_type":"cash","amount_millis":1000,"cash_secret":"`+callerSecret+`"}], "expiry": 1800}
	}`), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)
	var resp *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(context.TODO(), nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) { resp = r }, testMintMeta())

	require.NotNil(t, resp.Error, "a caller-supplied cash_secret must be refused, not silently replaced")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Nil(t, resp.Result)

	var bills int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("kind = ?", db.AppKindCashWallet).Count(&bills).Error)
	assert.Zero(t, bills, "nothing is minted")
}
