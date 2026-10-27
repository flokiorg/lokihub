package nip47

import (
	"context"
	"strings"
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

// I6 and I7 (NIP-CASH §Serving an Envelope): a cash_transfer item's request_id is
// refused when missing or malformed (BAD_REQUEST) or used (REQUEST_REUSED) before
// I8, so before an item carrying both a proof and a cash secret is looked at, and
// nothing is marked.
func TestPrivateItem_TransferRequestIDIsCheckedBeforeI8(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	recipientPriv := nostr.GeneratePrivateKey()
	recipientPub, _ := nostr.GetPublicKey(recipientPriv)
	walletPubkey, connPriv, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: recipientPub, AmountMloki: 1000},
	})
	used := tests.RandomHex32()
	require.NoError(t, cashrequest.New(svc.DB).MarkUsed(used, constants.NIP47MethodMintCash))
	fresh := tests.RandomHex32()

	hubXOnly := strings.Repeat("ab", 32)
	// Every params below also carries a cash_secret beside the item's proof, which
	// I8 would refuse; I6 and I7 must answer first.
	cases := []struct {
		name, params, wantCode string
	}{
		{"missing", `{"cash_secret":"00"}`, constants.ERROR_BAD_REQUEST},
		{"malformed", `{"cash_secret":"00","request_id":"NOT-HEX"}`, constants.ERROR_BAD_REQUEST},
		{"too short", `{"cash_secret":"00","request_id":"abcd"}`, constants.ERROR_BAD_REQUEST},
		{"used", `{"cash_secret":"00","request_id":"` + used + `"}`, constants.ERROR_REQUEST_REUSED},
		{"fresh reaches I8", `{"cash_secret":"00","request_id":"` + fresh + `"}`, constants.ERROR_BAD_REQUEST},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nonce := tests.RandomHex32()
			notAfter := time.Now().Add(time.Minute).Unix()
			item := buildItem(t, "x", walletPubkey, constants.NIP47MethodCashTransfer, tc.params, recipientPriv, connPriv, hubXOnly, nonce, notAfter)
			res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
				PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
			require.True(t, served)
			require.NotNil(t, res.Error)
			assert.Equal(t, tc.wantCode, res.Error.Code, res.Error.Message)
			if tc.name == "fresh reaches I8" {
				assert.Contains(t, res.Error.Message, "never both", "a fresh id passes on to I8")
			} else {
				assert.Contains(t, res.Error.Message, "request_id")
			}
		})
	}
	marked, err := cashrequest.New(svc.DB).IsUsed(fresh)
	require.NoError(t, err)
	assert.False(t, marked, "I6 and I7 only read: the method marks the id once the call is authorized")
}
