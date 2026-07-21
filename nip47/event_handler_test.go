package nip47

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
)

// TODO: test if an app doesn't exist it returns the right error code

func TestCreateResponse_Nip04(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestCreateResponse(t, svc, constants.ENCRYPTION_TYPE_NIP04)
}

func TestCreateResponse_Nip44(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestCreateResponse(t, svc, constants.ENCRYPTION_TYPE_NIP44_V2)
}

func doTestCreateResponse(t *testing.T, svc *tests.TestService, nip47Encryption string) {
	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:    models.REQUEST_KIND,
		PubKey:  reqPubkey,
		Content: "1",
	}

	reqEvent.ID = "12345"

	nip47Cipher, err := cipher.NewNip47Cipher(nip47Encryption, reqPubkey, svc.Keys.GetNostrSecretKey())
	assert.NoError(t, err)

	type dummyResponse struct {
		Foo int
	}

	nip47Response := &models.Response{
		ResultType: "dummy_method",
		Result: dummyResponse{
			Foo: 1000,
		},
	}

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	res, err := nip47svc.CreateResponse(reqEvent, nip47Response, nostr.Tags{}, nip47Cipher, svc.Keys.GetNostrSecretKey())
	assert.NoError(t, err)
	assert.Equal(t, reqPubkey, res.Tags.Find("p")[1])
	assert.Equal(t, reqEvent.ID, res.Tags.Find("e")[1])
	assert.Equal(t, svc.Keys.GetNostrPublicKey(), res.PubKey)

	decrypted, err := nip47Cipher.Decrypt(res.Content)
	assert.NoError(t, err)
	unmarshalledResponse := models.Response{
		Result: &dummyResponse{},
	}

	err = json.Unmarshal([]byte(decrypted), &unmarshalledResponse)
	assert.NoError(t, err)
	assert.Nil(t, nip47Response.Error)
	assert.Equal(t, nip47Response.ResultType, unmarshalledResponse.ResultType)
	assert.Equal(t, nip47Response.Result, *unmarshalledResponse.Result.(*dummyResponse))
}

func TestHandleResponse_Nip04_WithPermission(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_WithPermission(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP04)
}

func TestHandleResponse_Nip44_WithPermission(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_WithPermission(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
}

func doTestHandleResponse_WithPermission(t *testing.T, svc *tests.TestService, createAppFn tests.CreateAppFn, encryption string) {
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	app, cipher, err := createAppFn(svc, reqPrivateKey, encryption)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.GET_BALANCE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	content := map[string]interface{}{
		"method": models.GET_INFO_METHOD,
	}

	payloadBytes, err := json.Marshal(content)
	assert.NoError(t, err)

	msg, err := cipher.Encrypt(string(payloadBytes))
	assert.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
		Content:   msg,
	}

	if encryption != constants.ENCRYPTION_TYPE_NIP04 {
		reqEvent.Tags = append(reqEvent.Tags, []string{"encryption", encryption})
	}

	err = reqEvent.Sign(reqPrivateKey)
	assert.NoError(t, err)

	pool := tests.NewMockSimplePool()

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	assert.NotNil(t, pool.PublishedEvents[0])
	assert.NotEmpty(t, pool.PublishedEvents[0].Content)

	decrypted, err := cipher.Decrypt(pool.PublishedEvents[0].Content)
	assert.NoError(t, err)

	type getInfoResult struct {
		Methods []string `json:"methods"`
	}

	type getInfoResponseWrapper struct {
		models.Response
		Result getInfoResult `json:"result"`
	}

	unmarshalledResponse := getInfoResponseWrapper{}

	err = json.Unmarshal([]byte(decrypted), &unmarshalledResponse)
	assert.NoError(t, err)
	assert.Nil(t, unmarshalledResponse.Error)
	assert.Equal(t, models.GET_INFO_METHOD, unmarshalledResponse.ResultType)
	expectedMethods := slices.Concat([]string{constants.GET_BALANCE_SCOPE}, permissions.GetAlwaysGrantedMethods())
	assert.ElementsMatch(t, expectedMethods, unmarshalledResponse.Result.Methods)
}

func TestHandleResponse_Nip04_DuplicateRequest(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_DuplicateRequest(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP04)
}

func TestHandleResponse_Nip44_DuplicateRequest(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_DuplicateRequest(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
}

func doTestHandleResponse_DuplicateRequest(t *testing.T, svc *tests.TestService, createAppFn tests.CreateAppFn, encryption string) {
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	app, cipher, err := createAppFn(svc, reqPrivateKey, encryption)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.GET_BALANCE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	content := map[string]interface{}{
		"method": models.GET_INFO_METHOD,
	}

	payloadBytes, err := json.Marshal(content)
	assert.NoError(t, err)

	msg, err := cipher.Encrypt(string(payloadBytes))
	assert.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
		Content:   msg,
	}

	if encryption != constants.ENCRYPTION_TYPE_NIP04 {
		reqEvent.Tags = append(reqEvent.Tags, []string{"encryption", encryption})
	}

	err = reqEvent.Sign(reqPrivateKey)
	assert.NoError(t, err)

	pool := tests.NewMockSimplePool()

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	assert.NotNil(t, pool.PublishedEvents[0])
	assert.NotEmpty(t, pool.PublishedEvents[0].Content)

	pool.PublishedEvents = nil

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	// second time it should not publish
	assert.Nil(t, pool.PublishedEvents)
}

func TestHandleResponse_Nip04_NoPermission(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_NoPermission(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP04)
}

func TestHandleResponse_Nip44_NoPermission(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_NoPermission(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
}

func doTestHandleResponse_NoPermission(t *testing.T, svc *tests.TestService, createAppFn tests.CreateAppFn, encryption string) {
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	_, cipher, err := createAppFn(svc, reqPrivateKey, encryption)
	assert.NoError(t, err)

	content := map[string]interface{}{
		"method": models.GET_BALANCE_METHOD,
	}

	payloadBytes, err := json.Marshal(content)
	assert.NoError(t, err)

	msg, err := cipher.Encrypt(string(payloadBytes))
	assert.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
		Content:   msg,
	}

	if encryption != constants.ENCRYPTION_TYPE_NIP04 {
		reqEvent.Tags = append(reqEvent.Tags, []string{"encryption", encryption})
	}

	err = reqEvent.Sign(reqPrivateKey)
	assert.NoError(t, err)

	pool := tests.NewMockSimplePool()

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	assert.NotNil(t, pool.PublishedEvents[0])
	assert.NotEmpty(t, pool.PublishedEvents[0].Content)

	decrypted, err := cipher.Decrypt(pool.PublishedEvents[0].Content)
	assert.NoError(t, err)

	unmarshalledResponse := models.Response{}

	err = json.Unmarshal([]byte(decrypted), &unmarshalledResponse)
	assert.NoError(t, err)
	assert.Nil(t, unmarshalledResponse.Result)
	assert.Equal(t, models.GET_BALANCE_METHOD, unmarshalledResponse.ResultType)
	assert.Equal(t, "RESTRICTED", unmarshalledResponse.Error.Code)
	assert.Equal(t, "This app does not have the get_balance scope", unmarshalledResponse.Error.Message)
}

func TestHandleResponse_Nip04_OldRequestForPayment(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_OldRequestForPayment(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP04)
}

func TestHandleResponse_Nip44_OldRequestForPayment(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_OldRequestForPayment(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
}

func doTestHandleResponse_OldRequestForPayment(t *testing.T, svc *tests.TestService, createAppFn tests.CreateAppFn, encryption string) {
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	app, cipher, err := createAppFn(svc, reqPrivateKey, encryption)
	assert.NoError(t, err)

	content := map[string]interface{}{
		"method": models.PAY_INVOICE_METHOD,
	}

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	payloadBytes, err := json.Marshal(content)
	assert.NoError(t, err)

	msg, err := cipher.Encrypt(string(payloadBytes))
	assert.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Timestamp(time.Now().Add(time.Duration(-6) * time.Hour).Unix()),
		Tags:      nostr.Tags{},
		Content:   msg,
	}

	if encryption != constants.ENCRYPTION_TYPE_NIP04 {
		reqEvent.Tags = append(reqEvent.Tags, []string{"encryption", encryption})
	}

	err = reqEvent.Sign(reqPrivateKey)
	assert.NoError(t, err)

	pool := tests.NewMockSimplePool()

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	// it shouldn't return anything for an old request
	assert.Nil(t, pool.PublishedEvents)

	// change the request to now
	reqEvent.CreatedAt = nostr.Now()
	err = reqEvent.Sign(reqPrivateKey)
	assert.NoError(t, err)

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)
	assert.NotNil(t, pool.PublishedEvents)
}

func TestHandleResponse_Nip04_IncorrectPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_IncorrectPubkey(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP04)
}

func TestHandleResponse_Nip44_IncorrectPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	doTestHandleResponse_IncorrectPubkey(t, svc, tests.CreateAppWithPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
}

func doTestHandleResponse_IncorrectPubkey(t *testing.T, svc *tests.TestService, createAppFn tests.CreateAppFn, encryption string) {
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	reqPrivateKey2 := nostr.GeneratePrivateKey()

	app, cipher, err := createAppFn(svc, reqPrivateKey, encryption)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.GET_BALANCE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	content := map[string]interface{}{
		"method": models.GET_BALANCE_METHOD,
	}

	payloadBytes, err := json.Marshal(content)
	assert.NoError(t, err)

	msg, err := cipher.Encrypt(string(payloadBytes))
	assert.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
		Content:   msg,
	}

	if encryption != constants.ENCRYPTION_TYPE_NIP04 {
		reqEvent.Tags = append(reqEvent.Tags, []string{"encryption", encryption})
	}

	err = reqEvent.Sign(reqPrivateKey2)
	assert.NoError(t, err)

	// set a different pubkey (this will not pass validation)
	reqEvent.PubKey = reqPubkey

	pool := tests.NewMockSimplePool()

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	assert.Nil(t, pool.PublishedEvents)
}

func TestHandleResponse_NoApp(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	app, cipher, err := tests.CreateAppWithPrivateKey(svc, reqPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
	assert.NoError(t, err)

	// delete the app
	err = svc.DB.Delete(app).Error
	assert.NoError(t, err)

	content := map[string]interface{}{
		"method": models.GET_BALANCE_METHOD,
	}

	payloadBytes, err := json.Marshal(content)
	assert.NoError(t, err)

	msg, err := cipher.Encrypt(string(payloadBytes))
	assert.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{[]string{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2}},
		Content:   msg,
	}
	err = reqEvent.Sign(reqPrivateKey)
	assert.NoError(t, err)

	pool := tests.NewMockSimplePool()

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	// it shouldn't return anything for an invalid app key
	assert.Nil(t, pool.PublishedEvents)
}

func TestHandleResponse_UnknownEncryptionTag(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	doTestHandleResponse_UnknownEncryptionTag(t, svc, "nip44")
	doTestHandleResponse_UnknownEncryptionTag(t, svc, "nip44v2")
	doTestHandleResponse_UnknownEncryptionTag(t, svc, "nip44_v3")
	doTestHandleResponse_UnknownEncryptionTag(t, svc, "")
}

func doTestHandleResponse_UnknownEncryptionTag(t *testing.T, svc *tests.TestService, requestEncryptionTag string) {
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	app, cipher, err := tests.CreateAppWithPrivateKey(svc, reqPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.GET_BALANCE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	content := map[string]interface{}{
		"method": models.GET_INFO_METHOD,
	}

	payloadBytes, err := json.Marshal(content)
	assert.NoError(t, err)

	msg, err := cipher.Encrypt(string(payloadBytes))
	assert.NoError(t, err)

	// don't pass correct encryption
	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{[]string{"encryption", requestEncryptionTag}},
		Content:   msg,
	}

	err = reqEvent.Sign(reqPrivateKey)
	assert.NoError(t, err)

	pool := tests.NewMockSimplePool()

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	assert.NotNil(t, pool.PublishedEvents)
	responseContent := pool.PublishedEvents[0].Content
	msg, err = cipher.Decrypt(responseContent)
	assert.NoError(t, err)
	assert.NotEqual(t, "", msg)

	unmarshalledResponse := models.Response{}

	err = json.Unmarshal([]byte(msg), &unmarshalledResponse)
	assert.NoError(t, err)
	assert.Nil(t, unmarshalledResponse.Result)
	// assert.Equal(t, models.GET_INFO_METHOD, unmarshalledResponse.ResultType)
	assert.Equal(t, constants.ERROR_UNSUPPORTED_ENCRYPTION, unmarshalledResponse.Error.Code)
	assert.Contains(t, unmarshalledResponse.Error.Message, "invalid encryption:")
}

func TestHandleResponse_EncryptionTagDoesNotMatchPayload(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	// encryption specifies what cipher will use. If constants.ENCRYPTION_TYPE_NIP44_V2 is passed,
	// cipher must be NIP-44, otherwise cipher MUST be NIP-04
	doTestHandleResponse_EncryptionTagDoesNotMatchPayload(t, svc, constants.ENCRYPTION_TYPE_NIP44_V2, constants.ENCRYPTION_TYPE_NIP04)
	doTestHandleResponse_EncryptionTagDoesNotMatchPayload(t, svc, constants.ENCRYPTION_TYPE_NIP04, constants.ENCRYPTION_TYPE_NIP44_V2)
	doTestHandleResponse_EncryptionTagDoesNotMatchPayload(t, svc, constants.ENCRYPTION_TYPE_NIP44_V2, "")
}

func doTestHandleResponse_EncryptionTagDoesNotMatchPayload(t *testing.T, svc *tests.TestService, requestEncryption, requestEncryptionTag string) {
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	assert.NoError(t, err)

	app, _, err := tests.CreateAppWithPrivateKey(svc, reqPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
	assert.NoError(t, err)

	appPermission := &db.AppPermission{
		AppId: app.ID,
		App:   *app,
		Scope: constants.GET_BALANCE_SCOPE,
	}
	err = svc.DB.Create(appPermission).Error
	assert.NoError(t, err)

	content := map[string]interface{}{
		"method": models.GET_INFO_METHOD,
	}

	payloadBytes, err := json.Marshal(content)
	assert.NoError(t, err)

	reqCipher, err := cipher.NewNip47Cipher(requestEncryption, *app.WalletPubkey, reqPrivateKey)
	assert.NoError(t, err)
	// whenever we are unable to handle the request encryption, we always respond with our preferred encryption (NIP44)
	nip44Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	assert.NoError(t, err)
	msg, err := reqCipher.Encrypt(string(payloadBytes))
	assert.NoError(t, err)

	// don't pass correct encryption
	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
		Content:   msg,
	}

	if requestEncryptionTag != "" {
		reqEvent.Tags = append(reqEvent.Tags, []string{"encryption", requestEncryptionTag})
	}

	err = reqEvent.Sign(reqPrivateKey)
	assert.NoError(t, err)

	pool := tests.NewMockSimplePool()

	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	assert.NotNil(t, pool.PublishedEvents)
	responseContent := pool.PublishedEvents[0].Content
	msg, err = nip44Cipher.Decrypt(responseContent)
	assert.NoError(t, err)
	assert.NotEqual(t, "", msg)

	unmarshalledResponse := models.Response{}
	err = json.Unmarshal([]byte(msg), &unmarshalledResponse)
	assert.NoError(t, err)
	assert.Nil(t, unmarshalledResponse.Result)
	// assert.Equal(t, models.GET_INFO_METHOD, unmarshalledResponse.ResultType)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, unmarshalledResponse.Error.Code)
	assert.Contains(t, unmarshalledResponse.Error.Message, "failed to decrypt:")
}

// TestHandleEvent_JITWallet_GetBudget_Rejected_DespiteAlwaysGrantedList
// verifies the jit_wallet-specific carve-out from the system-wide
// always-granted method list: get_budget would otherwise reveal a shared
// wallet's total funded amount (across every recipient) to anyone holding
// the connection, with no proof required — this must be blocked even though
// get_budget is unconditionally allowed for every other app kind.
func TestHandleEvent_JITWallet_GetBudget_Rejected_DespiteAlwaysGrantedList(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	app, _, err := svc.AppsService.CreateApp(
		"jit-wallet", reqPubkey, 1, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindJITWallet, nil, "", nil,
	)
	require.NoError(t, err)

	nip47Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	require.NoError(t, err)

	response := doHandleEventForMethod(t, svc, nip47svc, nip47Cipher, reqPrivateKey, reqPubkey, models.GET_BUDGET_METHOD)
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

// TestHandleEvent_JITWallet_GetInfo_StillAllowed proves the carve-out is
// correctly scoped: get_info (harmless capability/introspection metadata,
// needed for standard NWC client handshake compatibility) must keep working
// for a jit_wallet even though get_budget doesn't.
func TestHandleEvent_JITWallet_GetInfo_StillAllowed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	app, _, err := svc.AppsService.CreateApp(
		"jit-wallet", reqPubkey, 1, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindJITWallet, nil, "", nil,
	)
	require.NoError(t, err)

	nip47Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	require.NoError(t, err)

	response := doHandleEventForMethod(t, svc, nip47svc, nip47Cipher, reqPrivateKey, reqPubkey, models.GET_INFO_METHOD)
	assert.Nil(t, response.Error)
	assert.Equal(t, models.GET_INFO_METHOD, response.ResultType)
}

// TestHandleEvent_NonJITWallet_GetBudget_StillAllowed confirms the carve-out
// is specific to AppKindJITWallet and doesn't regress get_budget for any
// other app kind.
func TestHandleEvent_NonJITWallet_GetBudget_StillAllowed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	app, cipher, err := tests.CreateAppWithPrivateKey(svc, reqPrivateKey, constants.ENCRYPTION_TYPE_NIP44_V2)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Create(&db.AppPermission{
		AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE,
	}).Error)

	response := doHandleEventForMethod(t, svc, nip47svc, cipher, reqPrivateKey, reqPubkey, models.GET_BUDGET_METHOD)
	assert.Nil(t, response.Error)
	assert.Equal(t, models.GET_BUDGET_METHOD, response.ResultType)
}

// TestHandleEvent_JITWallet_ClaimFunds_RejectedWhenWalletExpired verifies
// that a jit_wallet's own ExpiresAt is enforced for claim_funds. Unlike
// get_budget, claim_funds is not on the always-granted list, so it goes
// through the normal HasPermission(app, scope) path in HandleEvent — this
// confirms that path's ExpiresAt check actually fires for a jit_wallet's
// shared connection, before any claim proof is ever parsed.
func TestHandleEvent_JITWallet_ClaimFunds_RejectedWhenWalletExpired(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	expiresAt := time.Now().Add(-time.Hour)
	app, _, err := svc.AppsService.CreateApp(
		"jit-wallet", reqPubkey, 1, constants.BUDGET_RENEWAL_NEVER, &expiresAt,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindJITWallet, nil, "", nil,
	)
	require.NoError(t, err)

	nip47Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	require.NoError(t, err)

	response := doHandleEventForMethod(t, svc, nip47svc, nip47Cipher, reqPrivateKey, reqPubkey, constants.NIP47MethodClaimFunds)
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_EXPIRED, response.Error.Code)
}

// TestHandleEvent_JITWallet_ListRecipients_RejectedWhenWalletExpired covers
// the read-only sibling method under the same jit_claim_funds scope —
// expiry must block roster visibility too, not just the payout call.
func TestHandleEvent_JITWallet_ListRecipients_RejectedWhenWalletExpired(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	expiresAt := time.Now().Add(-time.Hour)
	app, _, err := svc.AppsService.CreateApp(
		"jit-wallet", reqPubkey, 1, constants.BUDGET_RENEWAL_NEVER, &expiresAt,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindJITWallet, nil, "", nil,
	)
	require.NoError(t, err)

	nip47Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	require.NoError(t, err)

	response := doHandleEventForMethod(t, svc, nip47svc, nip47Cipher, reqPrivateKey, reqPubkey, constants.NIP47MethodListRecipients)
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_EXPIRED, response.Error.Code)
}

// TestHandleEvent_JITWallet_CreateConnection_Rejected is a privilege-
// escalation probe: create_connection requires SUPERUSER_SCOPE, which a
// jit_wallet is never granted (only jit_claim_funds + get_balance, see
// jitwallet/create.go). If this method were ever reachable from a jit_wallet's
// shared connection, holding it would be enough to mint a brand new,
// unrestricted sibling connection and drain the parent hub — this confirms
// the generic scope gate actually blocks it, the same way it blocks every
// other ungranted method, as a regression guard specifically for the
// highest-value method to leave open by accident.
func TestHandleEvent_JITWallet_CreateConnection_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	app, _, err := svc.AppsService.CreateApp(
		"jit-wallet", reqPubkey, 1, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindJITWallet, nil, "", nil,
	)
	require.NoError(t, err)

	nip47Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	require.NoError(t, err)

	response := doHandleEventForMethod(t, svc, nip47svc, nip47Cipher, reqPrivateKey, reqPubkey, models.CREATE_CONNECTION_METHOD)
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

// doHandleEventForMethod builds, signs, and dispatches a bare (no-params)
// NIP-47 request event for method, and returns the decrypted response.
func doHandleEventForMethod(t *testing.T, svc *tests.TestService, nip47svc *nip47Service, nip47Cipher *cipher.Nip47Cipher, reqPrivateKey, reqPubkey, method string) models.Response {
	t.Helper()

	content := map[string]interface{}{"method": method}
	payloadBytes, err := json.Marshal(content)
	require.NoError(t, err)

	msg, err := nip47Cipher.Encrypt(string(payloadBytes))
	require.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2}},
		Content:   msg,
	}
	require.NoError(t, reqEvent.Sign(reqPrivateKey))

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool, reqEvent, svc.LNClient)

	require.NotEmpty(t, pool.PublishedEvents)
	decrypted, err := nip47Cipher.Decrypt(pool.PublishedEvents[0].Content)
	require.NoError(t, err)

	var response models.Response
	require.NoError(t, json.Unmarshal([]byte(decrypted), &response))
	return response
}
