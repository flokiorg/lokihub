package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/tests"
)

const testZapTarget = "30023:e493dbf1c10d80f3581e4904930b1404cc6c13900ee0758474fa94abe8c4cd13:my-article"

func openToken(t *testing.T, walletPubkey, token, recipientPriv string) nipcash.Token {
	t.Helper()
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPubkey, recipientPriv)
	require.NoError(t, err)
	plain, err := c.Decrypt(token)
	require.NoError(t, err)
	tok, err := nipcash.Decode(plain)
	require.NoError(t, err)
	return tok
}

// A zap transfer (NIP-CZ) spins the slice off into a bill signed under the zap
// scheme, with issued_at taken inside the commit, expires_at inherited from the
// source, and both returned exactly as signed. A partial zap's remainder is an
// ordinary bill. The zap_target is signed as its sha256 and never stored.
func TestCashTransfer_ZapTransfer(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "partial"}[partial], func(t *testing.T) {
			f := newTransferFixture(t, 5_000)
			expiry := time.Now().Add(time.Hour).Truncate(time.Second)
			require.NoError(t, f.svc.DB.Model(&db.App{}).Where("id = ?", f.wallet.ID).Update("expires_at", expiry).Error)
			f.wallet.ExpiresAt = &expiry
			toPriv := nostr.GeneratePrivateKey()
			to, _ := nostr.GetPublicKey(toPriv)
			amount := uint64(5_000)
			if partial {
				amount = 2_000
			}
			p := f.params(t, to, amount, partial, testRequestID())
			target := testZapTarget
			p.ZapTarget = &target

			before := time.Now().Unix()
			resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
			after := time.Now().Unix()
			require.Nil(t, resp.Error)
			result := resp.Result.(cashTransferResponse)
			require.NotNil(t, result.IssuedAt, "a zap reply carries issued_at")
			require.NotNil(t, result.ExpiresAt, "a zap reply carries expires_at")
			assert.GreaterOrEqual(t, int64(*result.IssuedAt), before) //nolint:gosec // a unix time
			assert.LessOrEqual(t, int64(*result.IssuedAt), after)     //nolint:gosec // a unix time
			require.NotNil(t, f.wallet.ExpiresAt)
			assert.EqualValues(t, f.wallet.ExpiresAt.Unix(), *result.ExpiresAt, "the zap bill inherits the source's expiry")

			// The zap bill verifies under scheme 2 against the Cash Zap's fields.
			zapTok := openToken(t, result.NewWalletPubkey, result.NewWalletToken, f.ownerPriv)
			assert.Equal(t, nipcash.SchemeZapV1, zapTok.Scheme)
			minter, ok := nipcash.VerifyZapProvenance(zapTok, nipcash.ZapContext{Recipient: to, Sender: f.ownerPub,
				ZapTarget: target, IssuedAt: *result.IssuedAt, ExpiresAt: *result.ExpiresAt})
			require.True(t, ok, "the zap bill's signature verifies")
			assert.Equal(t, tests.MockNodePubkey, minter, "signed by this node")

			if partial {
				remTok := openToken(t, result.RemainderWalletPubkey, result.RemainderWalletToken, f.ownerPriv)
				assert.Equal(t, nipcash.SchemeMintV1, remTok.Scheme, "the remainder is an ordinary bill")
				_, ok := nipcash.VerifyProvenance(remTok)
				assert.True(t, ok)
			}

			// The bill keeps its scheme-2 fields; the target only as its hash.
			sum := sha256.Sum256([]byte(target))
			var zapBill db.App
			require.NoError(t, f.svc.DB.Where("wallet_pubkey = ?", result.NewWalletPubkey).First(&zapBill).Error)
			require.NotNil(t, zapBill.ZapTargetHash)
			assert.Equal(t, hex.EncodeToString(sum[:]), *zapBill.ZapTargetHash)
			require.NotNil(t, zapBill.ZapIssuedAt)
			assert.EqualValues(t, *result.IssuedAt, *zapBill.ZapIssuedAt)
			assert.Equal(t, "pubkey/"+to, *zapBill.ZapRecipient)
			assert.Equal(t, "pubkey/"+f.ownerPub, *zapBill.ZapSender)
			rec, err := cashrequest.New(f.svc.DB).Get(p.RequestID)
			require.NoError(t, err)
			require.NotNil(t, rec)
			assert.NotContains(t, rec.ResultMeta, "my-article", "the zap_target is never stored")
			bills, err := cashrequest.New(f.svc.DB).Bills(rec.ID)
			require.NoError(t, err)
			require.NotEmpty(t, bills)
			require.NotNil(t, bills[0].ZapTargetHash)
			assert.Equal(t, hex.EncodeToString(sum[:]), *bills[0].ZapTargetHash)
			var stored int64
			require.NoError(t, f.svc.DB.Model(&db.App{}).Where("zap_target_hash LIKE ?", "%my-article%").Count(&stored).Error)
			assert.Zero(t, stored)
		})
	}
}

// A zap bill of a source that never expires carries expires_at 0, not omitted.
func TestCashTransfer_ZapOfANeverExpiringBill(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	require.NoError(t, f.svc.DB.Model(&db.App{}).Where("id = ?", f.wallet.ID).Update("expires_at", nil).Error)
	f.wallet.ExpiresAt = nil
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	p := f.params(t, to, 5_000, false, testRequestID())
	target := tests.RandomHex32()
	p.ZapTarget = &target
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.Nil(t, resp.Error)
	result := resp.Result.(cashTransferResponse)
	require.NotNil(t, result.ExpiresAt)
	assert.Zero(t, *result.ExpiresAt)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(raw), `"expires_at":0`), "0 is sent, not omitted: %s", raw)
}

// A zap transfer's source is a pubkey (RESTRICTED otherwise, after the mark) and
// its new_identity a pubkey (BAD_REQUEST otherwise).
func TestCashTransfer_ZapShapes(t *testing.T) {
	t.Run("cash source", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()
		hub := tests.CreateCashHub(t, svc, 100_000, 3600)
		wallet := newFundedCashWallet(t, svc, hub, 1_000)
		secret, commitment := cashSecretAndHash(t)
		require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
			{IdentityType: db.CashIdentityCash, IdentityValue: commitment, AmountMloki: 1_000},
		}))
		to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
		target := tests.RandomHex32()
		id := testRequestID()
		resp := transferWithCall(t, svc, wallet, cashTransferParams{
			CashSecret: secret, ZapTarget: &target, RequestID: id,
			NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: to},
		}, testPrivateCall(""))
		require.NotNil(t, resp.Error)
		assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
		assert.True(t, isUsed(t, svc, id))
	})
	t.Run("cash new_identity", func(t *testing.T) {
		f := newTransferFixture(t, 5_000)
		_, commitment := cashSecretAndHash(t)
		proof := buildTransferProofEvent(t, f.ownerPriv, *f.wallet.WalletPubkey, db.CashIdentityCash, commitment, "", 5_000, nil, time.Now())
		target := tests.RandomHex32()
		p := cashTransferParams{
			IdentityType: db.CashIdentityPubkey, IdentityValue: f.ownerPub, IdentityEvent: mustMarshal(t, proof),
			NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: commitment},
			ZapTarget:   &target, RequestID: testRequestID(),
		}
		resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
		require.NotNil(t, resp.Error)
		assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
		assert.Contains(t, resp.Error.Message, "pubkey")
	})
}
