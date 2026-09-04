package nip47

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditUX_ExpiredCashWallet_ErrorMessageSpeaksCashVocabulary is a
// UX-audit finding, FIXED same round (2026-08-31): event_handler_test.go
// already proves (TestHandleEvent_CashWallet_ClaimFunds_RejectedWhenWalletExpired /
// ...ListRecipients_RejectedWhenWalletExpired) that permissions.HasPermission
// CORRECTLY fires constants.ERROR_EXPIRED for every method a cash_wallet
// grants (cash_redeem, cash_transfer, cash_consolidate, list_recipients, and
// get_balance) once the wallet's own AppPermission.ExpiresAt has passed —
// well BEFORE service.runCashCleanup's periodic sweep actually deletes the
// row (see cash_audit_ux_unknown_app_silent_drop_test.go's own doc comment
// for what happens AFTER the sweep: total silence, a separate, still-open
// finding this fix doesn't touch).
//
// This test captures the actual wire-level error MESSAGE a real recipient's
// NWC client receives in that pre-sweep window, across all four
// cash_wallet-granted methods. It USED to be the generic,
// connection-management-flavored NIP-47 string "This app has expired" —
// reused verbatim from permissions.go's HasPermission for every app kind in
// this codebase — which never mentioned "cash", "slice", "redeem",
// "expires_at", or any concept from the recipient's own mental model of
// holding and redeeming a lokicash1... token, and never surfaced the actual
// deadline that passed. permissions.HasPermission now branches on
// db.AppKindCashWallet to return a cash-specific message naming the actual
// deadline, mirroring the cash-specific wording cash_redeem_controller.go
// and cash_transfer_controller.go already use for their OTHER rejection
// paths. Every other app kind (standard connections, Circle Wallets) keeps
// the original generic message — proven by the control subtest below.
func TestAuditUX_ExpiredCashWallet_ErrorMessageSpeaksCashVocabulary(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	// A wallet whose slice expired 1 hour ago but has NOT yet been reclaimed
	// by the background sweep — the real window every recipient who's even a
	// little late passes through before the sweep (every 5 minutes, per
	// service/cash_cleanup_service.go) actually removes the connection.
	expiresAt := time.Now().Add(-time.Hour)
	app, _, err := svc.AppsService.CreateApp(
		"cash-wallet", reqPubkey, 1, constants.BUDGET_RENEWAL_NEVER, &expiresAt,
		[]string{
			constants.CASH_REDEEM_SCOPE,
			constants.CASH_TRANSFER_SCOPE,
			constants.CASH_CONSOLIDATE_SCOPE,
			constants.GET_BALANCE_SCOPE,
		},
		db.AppKindCashWallet, nil, "", nil,
	)
	require.NoError(t, err)

	nip47Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	require.NoError(t, err)

	for _, method := range []string{
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer,
		constants.NIP47MethodCashConsolidate,
		constants.NIP47MethodListRecipients,
	} {
		t.Run(method, func(t *testing.T) {
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
			require.NotEmpty(t, pool.PublishedEvents, "%s must at least get a response (contrast with the post-sweep silent-drop case)", method)

			decrypted, err := nip47Cipher.Decrypt(pool.PublishedEvents[0].Content)
			require.NoError(t, err)
			t.Logf("%s wire response while expired: %s", method, decrypted)

			var response models.Response
			require.NoError(t, json.Unmarshal([]byte(decrypted), &response))
			require.NotNil(t, response.Error, "%s must be rejected once the wallet's ExpiresAt has passed", method)
			assert.Equal(t, constants.ERROR_EXPIRED, response.Error.Code)

			// The fix: cash-specific wording, naming the actual deadline that
			// passed, in the recipient's own vocabulary.
			assert.Contains(t, response.Error.Message, "cash wallet", "%s: message must name the mechanism, not generic NIP-47 jargon", method)
			assert.Contains(t, response.Error.Message, "redemption deadline")
			assert.Contains(t, response.Error.Message, expiresAt.UTC().Format(time.RFC3339), "%s: message must surface the actual deadline that passed", method)
			assert.NotEqual(t, "This app has expired", response.Error.Message)
		})
	}
}

// TestAuditUX_ExpiredNonCashApp_ErrorMessageStaysGeneric is the control: a
// standard (non-cash_wallet) app's expiry message is deliberately unchanged
// — the fix is scoped to db.AppKindCashWallet specifically, not a blanket
// rewording of every app kind's expiry message.
func TestAuditUX_ExpiredNonCashApp_ErrorMessageStaysGeneric(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	expiresAt := time.Now().Add(-time.Hour)
	app, _, err := svc.AppsService.CreateApp(
		"standard-app", reqPubkey, 0, constants.BUDGET_RENEWAL_NEVER, &expiresAt,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindStandard, nil, "", nil,
	)
	require.NoError(t, err)

	nip47Cipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *app.WalletPubkey, reqPrivateKey)
	require.NoError(t, err)

	content := map[string]interface{}{"method": "get_balance"}
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
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_EXPIRED, response.Error.Code)
	assert.Equal(t, "This app has expired", response.Error.Message)
}
