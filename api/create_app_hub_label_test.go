package api

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// A hub's connection token carries its name in a label of at most 255 bytes, and
// is only encoded after the hub is saved. A longer name must be refused up front,
// not leave a saved hub whose one-time secret was never shown.
func TestCreateApp_HubNameTooLongForItsToken_RefusedBeforeSaving(t *testing.T) {
	longName := strings.Repeat("a", 300)
	for _, tc := range []struct {
		name string
		req  CreateAppRequest
	}{
		{"cash hub", CreateAppRequest{
			Name: longName, Kind: db.AppKindCashHub,
			Scopes:                []string{constants.CASH_HUB_SCOPE, constants.GET_BALANCE_SCOPE},
			CashPerWalletMaxMloki: 100_000, CashMaxExpSecs: 3600,
		}},
		{"circle hub", CreateAppRequest{
			Name: longName, Kind: db.AppKindCircleHub,
			Scopes:                  []string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
			CirclePerWalletMaxMloki: 100_000, CircleMaxExpSecs: 3600,
			CircleIdentityName: "family", CirclePolicy: db.CirclePolicyAllowlist,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			resp, err := newTestAPIForCreateApp(t, svc).CreateApp(&tc.req)
			require.Error(t, err)
			assert.Nil(t, resp)

			var hubs int64
			require.NoError(t, svc.DB.Model(&db.App{}).Where("kind = ?", tc.req.Kind).Count(&hubs).Error)
			assert.Zero(t, hubs, "no hub is left behind")
		})
	}
}

func TestHubLabel_FitsTheTokenAtARuneBoundary(t *testing.T) {
	assert.Equal(t, "Family", hubLabel("Family"))

	// 254 one-byte runes, then a 3-byte one: 257 bytes, cut before the last rune.
	name := strings.Repeat("a", 254) + "€"
	label := hubLabel(name)
	assert.Equal(t, strings.Repeat("a", 254), label)
	assert.True(t, utf8.ValidString(label))
	assert.LessOrEqual(t, len(label), 255)
}
