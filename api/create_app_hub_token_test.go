package api

// Covers the new CashHubToken/CircleHubToken fields on CreateAppResponse
// (docs/nips/NIP-CASH.md §The Cash Hub Connection, NIP-CW.md §The Circle
// Wallet Hub Connection): populated only for the matching app kind, and
// decoding back to the exact same wallet pubkey/relay/secret PairingUri
// itself decodes to.

import (
	"strings"
	"testing"

	nmilatnip47 "github.com/ohstr/nmilat/nip47"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// newTestAPIForCreateApp wires a real DB/config/apps-service, matching what
// CreateApp actually touches for cash_hub/circle_hub with an allowlist
// identity: api.svc is only called for a *following*-policy circle_hub
// (WarmCircleFollowingCache), which these tests don't exercise, so it's left
// nil rather than mocked.
func newTestAPIForCreateApp(t *testing.T, svc *tests.TestService) *api {
	t.Helper()
	return &api{db: svc.DB, appsSvc: svc.AppsService, cfg: svc.Cfg, keys: svc.Keys}
}

func TestCreateApp_CashHub_EmitsCashHubToken(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	theAPI := newTestAPIForCreateApp(t, svc)
	resp, err := theAPI.CreateApp(&CreateAppRequest{
		Name:                  "My Cash Hub",
		Kind:                  db.AppKindCashHub,
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.GET_BALANCE_SCOPE},
		CashPerWalletMaxMloki: 100_000,
		CashMaxExpSecs:        3600,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	require.NotNil(t, resp.CashHubToken, "cash_hub creation must emit a cashHubToken")
	assert.Nil(t, resp.CircleHubToken, "a cash_hub must never emit a circleHubToken")
	assert.True(t, strings.HasPrefix(*resp.CashHubToken, constants.CashHubTokenHRP+"1"))

	wantPairing, err := nmilatnip47.ParsePairingURI(resp.PairingUri)
	require.NoError(t, err)

	got, err := nipcash.DecodeCashHubConnection(*resp.CashHubToken)
	require.NoError(t, err)
	assert.Equal(t, wantPairing.WalletPubkey, got.WalletPubkey)
	assert.Equal(t, wantPairing.Secret, got.Secret)
	assert.Equal(t, wantPairing.RelayURLs, got.RelayURLs)
	assert.Equal(t, "My Cash Hub", got.Label)
}

func TestCreateApp_CircleHub_EmitsCircleHubToken(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	theAPI := newTestAPIForCreateApp(t, svc)
	resp, err := theAPI.CreateApp(&CreateAppRequest{
		Name:                    "My Family Circle",
		Kind:                    db.AppKindCircleHub,
		Scopes:                  []string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		CirclePerWalletMaxMloki: 100_000,
		CircleMaxExpSecs:        3600,
		CircleIdentityName:      "family",
		CirclePolicy:            db.CirclePolicyAllowlist,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	require.NotNil(t, resp.CircleHubToken, "circle_hub creation must emit a circleHubToken")
	assert.Nil(t, resp.CashHubToken, "a circle_hub must never emit a cashHubToken")
	assert.True(t, strings.HasPrefix(*resp.CircleHubToken, constants.CircleHubTokenHRP+"1"))

	wantPairing, err := nmilatnip47.ParsePairingURI(resp.PairingUri)
	require.NoError(t, err)

	got, err := nipcw.DecodeCircleHubConnection(*resp.CircleHubToken)
	require.NoError(t, err)
	assert.Equal(t, wantPairing.WalletPubkey, got.WalletPubkey)
	assert.Equal(t, wantPairing.Secret, got.Secret)
	assert.Equal(t, wantPairing.RelayURLs, got.RelayURLs)
	assert.Equal(t, "My Family Circle", got.Label)
}

func TestCreateApp_StandardKind_EmitsNoHubToken(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	theAPI := newTestAPIForCreateApp(t, svc)
	resp, err := theAPI.CreateApp(&CreateAppRequest{
		Name:   "My Wallet",
		Kind:   db.AppKindStandard,
		Scopes: []string{constants.GET_BALANCE_SCOPE},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Nil(t, resp.CashHubToken)
	assert.Nil(t, resp.CircleHubToken)
}
