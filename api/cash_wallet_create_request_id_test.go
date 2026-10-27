package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func adminRequestID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func adminMint(requestID string, expiration int64) *CreateCashWalletRequest {
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	return &CreateCashWalletRequest{
		Recipients: []CashWalletRecipient{{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 1000}},
		ExpirySecs: 1800,
		RequestID:  requestID,
		Expiration: expiration,
	}
}

func countBills(t *testing.T, svc *tests.TestService) int64 {
	t.Helper()
	var n int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("kind = ?", db.AppKindCashWallet).Count(&n).Error)
	return n
}

// U-5.4: the admin mint shares mint_cash's mark and record. A mint with a
// request_id commits its record with the bill; resending the id, from the admin
// path or over NWC, is REQUEST_REUSED and mints nothing.
func TestCreateCashWallet_RequestIDIsMarkedAndRecorded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")
	theAPI := newTestAPIWithService(t, svc)

	id := adminRequestID(t)
	res, err := theAPI.CreateCashWallet(hub.ID, adminMint(id, time.Now().Add(time.Minute).Unix()))
	require.NoError(t, err)

	rec, err := cashrequest.New(svc.DB).Get(id)
	require.NoError(t, err)
	require.NotNil(t, rec, "the record commits with the bill")
	assert.Equal(t, constants.NIP47MethodMintCash, rec.Method)
	bills, err := cashrequest.New(svc.DB).Bills(rec.ID)
	require.NoError(t, err)
	require.Len(t, bills, 1)
	assert.Equal(t, res.AppID, bills[0].WalletAppID)

	_, err = theAPI.CreateCashWallet(hub.ID, adminMint(id, time.Now().Add(time.Minute).Unix()))
	require.True(t, errors.Is(err, cashrequest.ErrRequestReused), "got %v", err)
	assert.EqualValues(t, 1, countBills(t, svc), "a reused id mints nothing")
}

// The mark comes first: an admin mint whose id is used is REQUEST_REUSED even
// when everything else about it is wrong.
func TestCreateCashWallet_UsedIDBeatsEverything(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	theAPI := newTestAPIWithService(t, svc)

	id := adminRequestID(t)
	require.NoError(t, cashrequest.New(svc.DB).MarkUsed(id, constants.NIP47MethodMintCash))
	req := adminMint(id, time.Now().Add(-time.Minute).Unix()) // passed, and the Hub holds nothing
	req.Recipients[0].AmountMloki = 10_000_000_000
	_, err = theAPI.CreateCashWallet(hub.ID, req)
	require.True(t, errors.Is(err, cashrequest.ErrRequestReused), "got %v", err)
}

// U-5.8 and the deadline: expiration without request_id is refused; a request_id
// without a valid, unpassed expiration is refused after it was marked used; no
// bill is minted either way.
func TestCreateCashWallet_DeadlineRules(t *testing.T) {
	cases := []struct {
		name       string
		requestID  bool
		expiration func() int64
		wantMarked bool
	}{
		{"expiration without request_id", false, func() int64 { return time.Now().Add(time.Minute).Unix() }, false},
		{"request_id without expiration", true, func() int64 { return 0 }, true},
		{"a passed expiration", true, func() int64 { return time.Now().Add(-time.Second).Unix() }, true},
		{"an expiration beyond H", true, func() int64 { return time.Now().Add(24 * time.Hour).Unix() }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			hub := tests.CreateCashHub(t, svc, 100_000, 3600)
			tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")
			theAPI := newTestAPIWithService(t, svc)

			id := ""
			if tc.requestID {
				id = adminRequestID(t)
			}
			_, err = theAPI.CreateCashWallet(hub.ID, adminMint(id, tc.expiration()))
			require.Error(t, err)
			assert.True(t, errors.Is(err, constants.ErrInvalidParams), "got %v", err)
			assert.Zero(t, countBills(t, svc))
			if id != "" {
				used, err := cashrequest.New(svc.DB).IsUsed(id)
				require.NoError(t, err)
				assert.Equal(t, tc.wantMarked, used)
			}
		})
	}
}

// U-5.11, admin side: the reply carries the bill's app_id for the operator's
// screen, its cash_token, expires_at and the recipients, and no URI form; the
// connection endpoint returns the token alone.
func TestCreateCashWallet_ReplyHasNoURIForm(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")
	theAPI := newTestAPIWithService(t, svc)

	res, err := theAPI.CreateCashWallet(hub.ID, adminMint("", 0))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"app_id", "cash_token", "expires_at", "recipients"}, jsonKeys(t, res))

	conn, err := theAPI.GetCashWalletConnection(res.AppID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"cash_token"}, jsonKeys(t, conn))
}

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
