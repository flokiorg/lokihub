//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// clearlyTooLargeAmountMloki is chosen to reliably exceed any sane real
// PerWalletMaxMloki a test operator would configure, without needing to know
// the hub's actual configured cap.
const clearlyTooLargeAmountMloki = 10_000_000_000

// clearlyTooLargeExpirySecs (100 years) reliably exceeds any sane real
// MaxExpSecs.
const clearlyTooLargeExpirySecs = 100 * 365 * 24 * 3600

// happyPathAmountMloki is a small amount every provisioned JIT hub is
// expected to be funded well above (see integration/README.md).
const happyPathAmountMloki = 5_000
const happyPathExpirySecs = 3600

func TestJITHubs(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "jit-hub", nil)
	testJITHub(t, cfg, hub)
}

func onePubkeyRecipient(pubkey string, amountMloki uint64) []JITWalletRecipientParam {
	return []JITWalletRecipientParam{
		{IdentityType: "pubkey", IdentityValue: pubkey, AmountMloki: amountMloki},
	}
}

func testJITHub(t *testing.T, cfg *Config, hub JITHubConfig) {
	t.Logf("connecting to jit hub %q", hub.Name)
	hubClient := mustConnect(t, hub.Connection)

	t.Run("CreateWallet_SingleRecipient_HappyPath", func(t *testing.T) {
		beneficiaryPriv := newTestPrivkey(t)
		beneficiaryPub, err := nostr.GetPublicKey(beneficiaryPriv)
		require.NoError(t, err)
		t.Logf("beneficiary pubkey: %s", beneficiaryPub)

		var result CreateJITWalletResult
		err = hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &result)
		require.NoError(t, err)
		t.Logf("created jit wallet: pubkey=%s", result.WalletPubkey)
		require.NotEmpty(t, result.WalletPubkey)
		require.NotEmpty(t, result.PairingURI, "the connection is shared/known upfront now — no more encrypted reveal")
		require.Len(t, result.Recipients, 1)
		require.EqualValues(t, happyPathAmountMloki, result.Recipients[0].AmountMloki)

		child := mustConnect(t, result.PairingURI)

		var balance GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balance))
		t.Logf("child balance: %d mloki", balance.Balance)
		require.EqualValues(t, happyPathAmountMloki, balance.Balance, "child JIT wallet should be pre-funded with exactly the requested amount")

		var info GetInfoResult
		require.NoError(t, child.Call(ctxT(t), "get_info", struct{}{}, &info))
		t.Logf("child methods: %v", info.Methods)
		require.NotContains(t, info.Methods, "make_invoice", "JIT wallets must be spend-only")
		require.NotContains(t, info.Methods, "pay_invoice", "JIT wallets no longer carry the generic pay_invoice scope")
		require.NotContains(t, info.Methods, "list_transactions", "list_transactions would leak other recipients' payout history on a shared connection")
		require.NotContains(t, info.Methods, "lookup_invoice")
		require.Contains(t, info.Methods, constants.NIP47MethodClaimFunds)
		require.Contains(t, info.Methods, constants.NIP47MethodListRecipients)

		// Behavioral check, not just advertised-methods: actually calling
		// make_invoice/pay_invoice against a jit_wallet must be rejected, not
		// merely absent from get_info's method list.
		var invoice MakeInvoiceResult
		err = child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Amount: 1000}, &invoice)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)

		var payResult PayInvoiceResult
		err = child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: "lnbc1..."}, &payResult)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	})

	t.Run("CreateWallet_InvalidIdentityType_Rejected", func(t *testing.T) {
		beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var result CreateJITWalletResult
		err = hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "email", IdentityValue: beneficiaryPub, AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_MultipleRecipients_OneSharedWallet", func(t *testing.T) {
		pub1, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		pub2, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var result CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "pubkey", IdentityValue: pub1, AmountMloki: happyPathAmountMloki},
				{IdentityType: "pubkey", IdentityValue: pub2, AmountMloki: happyPathAmountMloki * 2},
			},
			Expiry: happyPathExpirySecs,
		}, &result))
		require.Len(t, result.Recipients, 2)

		child := mustConnect(t, result.PairingURI)

		var recipients ListRecipientsResult
		require.NoError(t, child.Call(ctxT(t), constants.NIP47MethodListRecipients, struct{}{}, &recipients))
		require.Len(t, recipients.Recipients, 2, "one shared connection must show both recipients' slices")

		var balance GetBalanceResult
		require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balance))
		require.EqualValues(t, happyPathAmountMloki*3, balance.Balance, "the shared wallet must be funded with the SUM of both recipients")
	})

	t.Run("CreateWallet_SumOfRecipients_ExceedsPerWalletCap", func(t *testing.T) {
		beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		t.Logf("beneficiary pubkey: %s, requesting amount=%d (expect rejection)", beneficiaryPub, clearlyTooLargeAmountMloki)

		var result CreateJITWalletResult
		err = hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, clearlyTooLargeAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_QUOTA_EXCEEDED)
	})

	t.Run("CreateWallet_ExpiryExceedsMax", func(t *testing.T) {
		beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var result CreateJITWalletResult
		err = hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     clearlyTooLargeExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_ConnectionKeyMode_MissingIAPubkey_Rejected", func(t *testing.T) {
		var result CreateJITWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: newTestConnectionKey(t), AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_ConnectionKeyMode_InvalidConnectionKeyHex_Rejected", func(t *testing.T) {
		iaPub := mustPubkey(t, newTestPrivkey(t))

		var result CreateJITWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: "not-valid-hex", IAPubkey: iaPub, AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_ConnectionKeyMode_UntrustedIA_Rejected", func(t *testing.T) {
		// A syntactically valid but never-registered IA pubkey: "untrusted"
		// just means it was never added via the admin Identity Authorities
		// API, so no config dependency is needed for this negative case.
		iaPub := mustPubkey(t, newTestPrivkey(t))

		var result CreateJITWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: newTestConnectionKey(t), IAPubkey: iaPub, AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_ConnectionKeyMode_HappyPath", func(t *testing.T) {
		iaPriv := createEphemeralTrustedIA(t, cfg)
		connectionKey := newTestConnectionKey(t)

		var result CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: mustPubkey(t, iaPriv), AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &result))
		require.NotEmpty(t, result.PairingURI, "connection_key mode also gets an immediate, shared connection now — no separate claim_jit_wallet reveal step")
	})

	t.Run("CreateWallet_EmptyRecipients_Rejected", func(t *testing.T) {
		var result CreateJITWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{},
			Expiry:     happyPathExpirySecs,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_PubkeyMode_AllowsMultipleWalletsSameBeneficiary", func(t *testing.T) {
		beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var first, second CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &first))
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &second))

		require.NotEqual(t, first.WalletPubkey, second.WalletPubkey,
			"pubkey mode has no dedupe: two independent creates for the same beneficiary must yield two distinct wallets")
	})

	t.Run("CreateWallet_OmittedExpiry_DefaultsToHubMax", func(t *testing.T) {
		beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var result CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			// Expiry deliberately omitted (zero value).
		}, &result))
		require.Greater(t, result.ExpiresAt, time.Now().Unix(),
			"an omitted expiry must default to the hub's own max, not produce an already-expired wallet")
	})

	t.Run("RateLimiting_EleventhCreateIsRateLimited", func(t *testing.T) {
		skipIfEnvUnset(t, "INTEGRATION_RUN_RATE_LIMIT_TESTS")

		var lastErr error
		for i := 0; i < 11; i++ {
			beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
			require.NoError(t, err)

			var result CreateJITWalletResult
			lastErr = hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
				Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
				Expiry:     happyPathExpirySecs,
			}, &result)
			if lastErr != nil {
				t.Logf("create #%d/11: error: %v", i+1, lastErr)
			} else {
				t.Logf("create #%d/11: ok, wallet pubkey=%s", i+1, result.WalletPubkey)
			}
		}
		requireNWCErrorCode(t, lastErr, constants.ERROR_RATE_LIMITED)
	})
}
