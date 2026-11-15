package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func mintWith(t *testing.T, svc *tests.TestService, hub *db.App, params string, meta models.CallMeta) *models.Response {
	t.Helper()
	req := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(`{"method":"mint_cash","params":`+params+`}`), req))
	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var resp *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(context.TODO(), req, ev.ID, hub, func(r *models.Response, _ nostr.Tags) { resp = r }, meta)
	require.NotNil(t, resp)
	return resp
}

func marked(t *testing.T, svc *tests.TestService, id string) bool {
	t.Helper()
	var n int64
	require.NoError(t, svc.DB.Model(&db.CashUsedRequestID{}).Where("request_id = ?", id).Count(&n).Error)
	return n > 0
}

func onePubkeyMint(id string) string {
	return fmt.Sprintf(`{"request_id":%q,"recipients":[%s],"expiry":1800}`, id, onePubkeyRecipientJSON(tests.RandomHex32(), 1000))
}

// U-5.1 (WP-5 step 3): a missing or malformed request_id is BAD_REQUEST and marks nothing.
func TestU_5_1_RequestIDRequired(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)

	for _, params := range []string{
		fmt.Sprintf(`{"recipients":[%s]}`, onePubkeyRecipientJSON(tests.RandomHex32(), 1000)),
		onePubkeyMint("ABCD"),
		onePubkeyMint("abc"),
	} {
		resp := mintWith(t, svc, hub, params, testMintMeta())
		require.NotNil(t, resp.Error, params)
		assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	}
	var n int64
	require.NoError(t, svc.DB.Model(&db.CashUsedRequestID{}).Count(&n).Error)
	assert.Zero(t, n)
}

// U-5.2 (S-CASH-02, S-CASH-06): the id is marked at authorization, before the
// deadline and the recipients are checked, so a refused call leaves it used and a
// resend is REQUEST_REUSED whatever else is wrong with it.
func TestU_5_2_MarkedBeforeTheRestIsChecked(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	now := time.Now().Unix()
	h := svc.Cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds

	cases := map[string]models.CallMeta{
		"missing expiration":   {Transport: models.TransportStandard, ArrivedAt: now},
		"malformed expiration": {Transport: models.TransportStandard, ArrivedAt: now, DeadlinePresent: true, DeadlineMalformed: true},
		"passed":               {Transport: models.TransportStandard, ArrivedAt: now, Deadline: now - 1, DeadlinePresent: true},
		"over H":               {Transport: models.TransportStandard, ArrivedAt: now, Deadline: now + h + 1, DeadlinePresent: true},
	}
	for name, meta := range cases {
		t.Run(name, func(t *testing.T) {
			id := testRequestID()
			resp := mintWith(t, svc, hub, onePubkeyMint(id), meta)
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code, resp.Error.Message)
			assert.True(t, marked(t, svc, id), "the id is used once the call is authorized")

			// A corrected resend under the same id: REQUEST_REUSED, not a mint.
			resp = mintWith(t, svc, hub, onePubkeyMint(id), testMintMeta())
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ERROR_REQUEST_REUSED, resp.Error.Code)
		})
	}

	// Exactly H ahead is accepted (NIP-CASH §Deadlines); a refusal later in the call
	// (the Hub holds no balance) still leaves the id used.
	id := testRequestID()
	resp := mintWith(t, svc, hub, onePubkeyMint(id), models.CallMeta{Transport: models.TransportStandard, ArrivedAt: now, Deadline: now + h, DeadlinePresent: true})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_INSUFFICIENT_BALANCE, resp.Error.Code)
	assert.True(t, marked(t, svc, id))
	resp = mintWith(t, svc, hub, `{"request_id":"`+id+`","recipients":[]}`, testMintMeta())
	assert.Equal(t, constants.ERROR_REQUEST_REUSED, resp.Error.Code, "REQUEST_REUSED wins over an invalid body")
}

// U-5.3: a successful mint marks its id; resending it is REQUEST_REUSED and mints nothing.
func TestU_5_3_MintedIDIsUsed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	id := testRequestID()
	resp := mintWith(t, svc, hub, onePubkeyMint(id), testMintMeta())
	require.Nil(t, resp.Error)
	assert.True(t, marked(t, svc, id))

	resp = mintWith(t, svc, hub, onePubkeyMint(id), testMintMeta())
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_REQUEST_REUSED, resp.Error.Code)
	var bills int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Count(&bills).Error)
	assert.EqualValues(t, 1, bills)
}
