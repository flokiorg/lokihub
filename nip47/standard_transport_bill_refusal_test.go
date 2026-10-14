package nip47

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §The Private Transport: a bill method on the standard transport MUST be
// refused with NOT_IMPLEMENTED, whatever the connection's scopes or expiry.
func TestStandardTransport_BillMethod_NoScope_IsNotImplemented(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)
	app, _, err := svc.AppsService.CreateApp(
		"plain", reqPubkey, 1, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindStandard, nil, "", nil,
	)
	require.NoError(t, err)
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	require.NoError(t, err)

	for _, m := range []string{constants.NIP47MethodCashStatus, constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer, constants.NIP47MethodCashConsolidate} {
		resp := doHandleEventForMethod(t, svc, nip47svc, c, reqPrivateKey, reqPubkey, m)
		require.NotNil(t, resp.Error, m)
		require.Equal(t, constants.ERROR_NOT_IMPLEMENTED, resp.Error.Code, "%s refused with %s", m, resp.Error.Code)
	}
}

// A bill connection with the scopes gets the same refusal, and so does an expired one.
func TestStandardTransport_BillMethod_Scoped_IsNotImplemented(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	for name, expiresAt := range map[string]*time.Time{"live": nil, "expired": &expired} {
		t.Run(name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

			reqPrivateKey := nostr.GeneratePrivateKey()
			reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
			require.NoError(t, err)
			app, _, err := svc.AppsService.CreateApp(
				"cash-wallet", reqPubkey, 1, constants.BUDGET_RENEWAL_NEVER, expiresAt,
				[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE},
				db.AppKindCashWallet, nil, "", nil,
			)
			require.NoError(t, err)
			c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
			require.NoError(t, err)
			for _, m := range []string{constants.NIP47MethodCashStatus, constants.NIP47MethodCashRedeem,
				constants.NIP47MethodCashTransfer, constants.NIP47MethodCashConsolidate} {
				resp := doHandleEventForMethod(t, svc, nip47svc, c, reqPrivateKey, reqPubkey, m)
				require.NotNil(t, resp.Error, m)
				require.Equal(t, constants.ERROR_NOT_IMPLEMENTED, resp.Error.Code, "%s refused with %s", m, resp.Error.Code)
			}
		})
	}
}
