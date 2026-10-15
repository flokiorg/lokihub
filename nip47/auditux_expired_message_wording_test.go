package nip47

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditUX_ExpiredCashWallet_ErrorMessageSpeaksCashVocabulary: once a cash
// wallet's ExpiresAt has passed, and before service.runCashCleanup's sweep deletes
// it, every bill method is refused with EXPIRED and a message in the recipient's
// own terms — "cash wallet", "redemption deadline", the deadline itself — rather
// than the generic "This app has expired" every other app kind gets.
//
// Driven over the private transport, the only one that serves bill methods: the
// standard transport refuses them with NOT_IMPLEMENTED before any expiry check
// (TestStandardTransport_BillMethod_Scoped_IsNotImplemented).
func TestAuditUX_ExpiredCashWallet_ErrorMessageSpeaksCashVocabulary(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)

	recipientPriv := nostr.GeneratePrivateKey()
	recipientPub, _ := nostr.GetPublicKey(recipientPriv)
	walletPubkey, connPriv, walletID := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: recipientPub, AmountMloki: 1000},
	})

	// Expired an hour ago, not yet swept.
	expiresAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, svc.DB.Model(&db.AppPermission{}).Where("app_id = ?", walletID).
		Update("expires_at", expiresAt).Error)

	hubXOnly := strings.Repeat("ab", 32)
	for _, method := range []string{
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer,
		constants.NIP47MethodCashConsolidate,
		constants.NIP47MethodCashStatus,
	} {
		t.Run(method, func(t *testing.T) {
			nonce := tests.RandomHex32()
			notAfter := time.Now().Add(time.Minute).Unix()
			item := buildItem(t, "x", walletPubkey, method, `{}`, recipientPriv, connPriv, hubXOnly, nonce, notAfter)

			res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
				PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
			require.True(t, served, "%s must get an answer while the bill still exists", method)
			require.NotNil(t, res.Error, "%s must be refused once the wallet's ExpiresAt has passed", method)
			assert.Equal(t, constants.ERROR_EXPIRED, res.Error.Code)

			assert.Contains(t, res.Error.Message, "cash wallet", "%s: message must name the mechanism, not generic NIP-47 jargon", method)
			assert.Contains(t, res.Error.Message, "redemption deadline")
			assert.Contains(t, res.Error.Message, expiresAt.UTC().Format(time.RFC3339), "%s: message must surface the actual deadline that passed", method)
			assert.NotEqual(t, "This app has expired", res.Error.Message)
		})
	}
}
