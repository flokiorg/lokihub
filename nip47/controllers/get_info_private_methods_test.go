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
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Scope Surface: the bill methods are refused on the standard
// transport, so they MUST NOT appear in get_info's methods, and MUST instead
// appear in private_methods (CASH-scope-surface-04), on the wire.
func TestHandleGetInfoEvent_CashWallet_ListsBillMethodsAsPrivate(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	bill, _, err := svc.AppsService.CreateApp("cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.CASH_CONSOLIDATE_SCOPE,
			constants.GET_INFO_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil)
	require.NoError(t, err)

	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var response *models.Response
	NewTestNip47Controller(svc).HandleGetInfoEvent(context.TODO(), &models.Request{Method: "get_info"}, ev.ID, bill,
		func(r *models.Response, _ nostr.Tags) { response = r })
	require.NotNil(t, response)
	require.Nil(t, response.Error, "%+v", response.Error)

	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	var wire struct {
		Methods        []string `json:"methods"`
		PrivateMethods []string `json:"private_methods"`
	}
	require.NoError(t, json.Unmarshal(raw, &wire))
	for _, method := range []string{constants.NIP47MethodCashRedeem, constants.NIP47MethodCashTransfer, constants.NIP47MethodCashConsolidate} {
		assert.Contains(t, wire.PrivateMethods, method)
		assert.NotContains(t, wire.Methods, method)
	}
}

// NIP-CW §Fees: a Hub that charges no fee MUST still report fees_ppm: 0, not
// omit the field (CW-fees-02).
func TestCreateCircleWallet_ZeroFeeIsReportedNotOmitted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000) // fees_ppm 0

	member := nostr.GeneratePrivateKey()
	response := joinCircle(t, context.TODO(), svc, provider, member)
	require.Nil(t, response.Error, "%+v", response.Error)

	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *provider.WalletPubkey, member)
	require.NoError(t, err)
	plaintext, err := c.Decrypt(response.Result.(createCircleWalletResponse).EncryptedDetails)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(plaintext), &fields))
	require.Contains(t, fields, "fees_ppm")
	assert.JSONEq(t, "0", string(fields["fees_ppm"]))
}
