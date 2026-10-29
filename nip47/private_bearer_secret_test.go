package nip47

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH I8: a cash_status item carrying a cash_secret is refused BAD_REQUEST. A
// cash-mode cash_status is answered from the bill proof alone, so a secret in it
// is a client error that would put the secret where the roster read does not need
// it. nmilat refuses the shape when the envelope is decoded.
func TestPrivateItem_ProoflessCashStatus_SecretIsRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	secret, commitment, err := tests.GenerateCashSecret()
	require.NoError(t, err)
	walletPubkey, connPriv, walletID := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: commitment, AmountMloki: 4200},
	})

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("ef", 32)
	notAfter := time.Now().Add(time.Minute).Unix()

	// Even the RIGHT secret is refused: the roster read never needs it.
	params := json.RawMessage(`{"cash_secret":"` + secret + `","scope":"all"}`)
	hash, err := transport.CanonicalParamsHash(params)
	require.NoError(t, err)
	billProof, err := transport.BuildBillProof(connPriv, transport.ProofBinding{Target: walletPubkey,
		HubXOnly: hubXOnly, Method: constants.NIP47MethodCashStatus, ParamsHash: hash, Nonce: nonce, NotAfter: notAfter})
	require.NoError(t, err)
	item := transport.Item{ID: "w", Target: walletPubkey, Method: constants.NIP47MethodCashStatus,
		Params: params, BillProof: billProof}

	res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_BAD_REQUEST, res.Error.Code)
	require.Nil(t, res.Result)
	var events int64
	require.NoError(t, svc.DB.Model(&db.RequestEvent{}).Where("nostr_id = ?", nonce+":w").Count(&events).Error)
	require.Zero(t, events)
	_ = walletID
}

// TestU_9_R3a_WrongCashSecretIsUnauthorized pins NIP-CASH §Errors and §Bearer Items: a
// cash secret that does not verify is refused UNAUTHORIZED, never NOT_FOUND, on every
// value method that carries one (a cash_status never carries one: I8), and on an identity-bound bill too (I9). The refusal comes
// before the controller, so the slice stays unclaimed and no request is recorded.
func TestU_9_R3a_WrongCashSecretIsUnauthorized(t *testing.T) {
	realSecret := strings.Repeat("11", 32)
	sb, _ := hex.DecodeString(realSecret)
	commit := sha256.Sum256(sb)
	wrongSecret := strings.Repeat("22", 32)
	_, newCommit := cashSecretPair(t)

	cashClaim := db.CashWalletClaim{IdentityType: db.CashIdentityCash, IdentityValue: hex.EncodeToString(commit[:]), AmountMloki: 4200}
	pubkeyClaim := db.CashWalletClaim{IdentityType: db.CashIdentityPubkey, IdentityValue: strings.Repeat("cd", 32), AmountMloki: 4200}

	cases := []struct {
		name   string
		claim  db.CashWalletClaim
		method string
		params string
		want   string
	}{
		{"cash_redeem", cashClaim, constants.NIP47MethodCashRedeem, `{"cash_secret":"` + wrongSecret + `","invoice":"lnfc1"}`, ""},
		{"cash_transfer", cashClaim, constants.NIP47MethodCashTransfer, `{"cash_secret":"` + wrongSecret + `","request_id":"` + strings.Repeat("ab", 16) + `","new_identity":{"identity_type":"cash","identity_value":"` + newCommit + `"}}`, ""},
		{"identity_bound_bill", pubkeyClaim, constants.NIP47MethodCashRedeem, `{"cash_secret":"` + wrongSecret + `","invoice":"lnfc1"}`, ""},
		// A secret in any other form is BAD_REQUEST on a cash-mode bill (NIP-CASH
		// §Cash-Mode Slices); an identity-bound bill still answers UNAUTHORIZED.
		{"malformed_on_cash_bill", cashClaim, constants.NIP47MethodCashRedeem, `{"cash_secret":"` + strings.ToUpper(realSecret[:62]) + `AB","invoice":"lnfc1"}`, constants.ERROR_BAD_REQUEST},
		{"short_on_cash_bill", cashClaim, constants.NIP47MethodCashRedeem, `{"cash_secret":"1111","invoice":"lnfc1"}`, constants.ERROR_BAD_REQUEST},
		{"malformed_on_identity_bound_bill", pubkeyClaim, constants.NIP47MethodCashRedeem, `{"cash_secret":"1111","invoice":"lnfc1"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
			hub := tests.CreateCashHub(t, svc, 100_000, 3600)
			tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")
			walletPubkey, connPriv, walletID := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{tc.claim})

			hubXOnly := strings.Repeat("ab", 32)
			nonce := strings.Repeat("ef", 32)
			notAfter := time.Now().Add(time.Minute).Unix()
			params := json.RawMessage(tc.params)
			hash, err := transport.CanonicalParamsHash(params)
			require.NoError(t, err)
			billProof, err := transport.BuildBillProof(connPriv, transport.ProofBinding{Target: walletPubkey,
				HubXOnly: hubXOnly, Method: tc.method, ParamsHash: hash, Nonce: nonce, NotAfter: notAfter})
			require.NoError(t, err)
			item := transport.Item{ID: "w", Target: walletPubkey, Method: tc.method, Params: params, BillProof: billProof}

			res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
				PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
			require.True(t, served)
			require.NotNil(t, res.Error, "a wrong cash_secret must not be served: %s", string(res.Result))
			want := tc.want
			if want == "" {
				want = constants.ERROR_UNAUTHORIZED
			}
			require.Equal(t, want, res.Error.Code)
			require.Nil(t, res.Result)

			var claim db.CashWalletClaim
			require.NoError(t, svc.DB.Where("wallet_app_id = ?", walletID).First(&claim).Error)
			require.Nil(t, claim.ClaimedAt, "the slice is not claimed")
			var events int64
			require.NoError(t, svc.DB.Model(&db.RequestEvent{}).Where("nostr_id = ?", nonce+":w").Count(&events).Error)
			require.Zero(t, events, "no request is recorded for a refused item")
		})
	}
}

func cashSecretPair(t *testing.T) (secret, commitment string) {
	t.Helper()
	s := strings.Repeat("33", 32)
	b, _ := hex.DecodeString(s)
	sum := sha256.Sum256(b)
	return s, hex.EncodeToString(sum[:])
}
