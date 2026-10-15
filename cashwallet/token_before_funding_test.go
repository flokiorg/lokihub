package cashwallet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Minting Cash: a Hub MUST NOT return an empty cash_token. A relay URL too
// long for the token format used to pass, fund the wallet, and then come back as "".
func TestCreate_TokenThatCannotBeEncoded_RefusedBeforeFunding(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	deps := newTestDeps(svc)
	deps.RelayURLs = []string{"wss://relay.test/" + strings.Repeat("a", 300)}
	var funded uint64
	deps.FundInternalOverride = func(_ context.Context, _, _ uint, amountMloki uint64, _ string) error {
		funded += amountMloki
		return nil
	}

	result, err := Create(context.TODO(), deps, Params{HubApp: hub, Recipients: oneCashRecipient(1000), ExpirySecs: 1800})
	require.Error(t, err, "a mint whose token cannot be produced must fail")
	assert.Nil(t, result)
	assert.Zero(t, funded, "the refusal comes before any funds move")
	assert.Zero(t, countCashWallets(t, svc), "no bill is left behind")
}

func newSplitSource(t *testing.T, svc *tests.TestService, hub *db.App) *db.App {
	t.Helper()
	src, _, err := svc.AppsService.CreateApp(
		"source-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, src.ID, 200_000, "sourcefundtxhash")
	svc.LNClient.(*tests.MockLn).MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-split", Amount: 1000},
	}
	return src
}

// NIP-CASH §Relay Hints Are Mandatory: an encoder MUST refuse a credential with no
// usable relay, and the split must fail before the source's funds move.
func TestSplit_NoUsableRelay_RefusedBeforeFunding(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	src := newSplitSource(t, svc, hub)
	srcBefore := queries.GetIsolatedBalance(svc.DB, src.ID)
	billsBefore := countCashWallets(t, svc)

	deps := newTestDeps(svc)
	deps.RelayURLs = nil // what config.GetRelayUrls returns for Relay="," or " "

	exp := time.Now().Add(time.Hour)
	res, err := Split(context.TODO(), deps, SplitParams{
		HubApp: hub, SourceWalletApp: src, AmountMloki: 1000,
		NewIdentityType: db.CashIdentityCash, NewIdentityValue: strings.Repeat("ef", 32),
		ExpiresAt: &exp,
	})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Equal(t, srcBefore, queries.GetIsolatedBalance(svc.DB, src.ID), "the source's funds did not move")
	assert.Equal(t, billsBefore, countCashWallets(t, svc), "no split-off bill is left behind")
}

// A split-off bill's token is stored like a minted one's, for the archive and the
// admin "get connection" call, and carries the Hub's fingerprint.
func TestSplit_StoresTokenWithHubFingerprint(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	src := newSplitSource(t, svc, hub)

	exp := time.Now().Add(time.Hour)
	res, err := Split(context.TODO(), newTestDeps(svc), SplitParams{
		HubApp: hub, SourceWalletApp: src, AmountMloki: 1000,
		NewIdentityType: db.CashIdentityCash, NewIdentityValue: strings.Repeat("ef", 32),
		ExpiresAt: &exp,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.CashToken)

	var stored db.App
	require.NoError(t, svc.DB.First(&stored, res.WalletApp.ID).Error)
	assert.Equal(t, res.CashToken, stored.CashToken, "the issued token is stored verbatim")

	tok, err := lokicash.Decode(res.CashToken)
	require.NoError(t, err)
	assert.Equal(t, lokicash.HubGroupFor(hub.AppPubkey), tok.HubGroup, "the split-off bill carries its Hub's fingerprint")
}

func TestConsolidate_NoUsableRelay_RefusedBeforeFunding(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	billsBefore := countCashWallets(t, svc)

	deps := newTestDeps(svc)
	deps.RelayURLs = nil
	var funded int
	deps.FundInternalOverride = func(context.Context, uint, uint, uint64, string) error {
		funded++
		return nil
	}

	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	result, stranded, err := Consolidate(context.TODO(), deps, ConsolidateParams{
		HubApp: hub,
		Sources: []ConsolidateSource{
			{WalletApp: &db.App{ID: 9001}, AmountMloki: 3000},
			{WalletApp: &db.App{ID: 9002}, AmountMloki: 2000},
		},
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPk,
	})
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Empty(t, stranded)
	assert.Zero(t, funded, "no source was drained")
	assert.Equal(t, billsBefore, countCashWallets(t, svc), "no merged bill is left behind")
}

// A consolidated bill's token is stored, says whether an identity is required for
// the target's type, and carries the Hub's fingerprint.
func TestConsolidate_StoresTokenWithHintAndFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name             string
		identityType     string
		identityValue    func() string
		identityRequired bool
	}{
		{"pubkey target", db.CashIdentityPubkey, func() string { pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey()); return pk }, true},
		{"cash target", db.CashIdentityCash, func() string { return strings.Repeat("cd", 32) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
			deps := newTestDeps(svc)
			deps.FundInternalOverride = func(context.Context, uint, uint, uint64, string) error { return nil }

			result, _, err := Consolidate(context.TODO(), deps, ConsolidateParams{
				HubApp: hub,
				Sources: []ConsolidateSource{
					{WalletApp: &db.App{ID: 9001}, AmountMloki: 3000},
					{WalletApp: &db.App{ID: 9002}, AmountMloki: 2000},
				},
				NewIdentityType:  tc.identityType,
				NewIdentityValue: tc.identityValue(),
			})
			require.NoError(t, err)

			var stored db.App
			require.NoError(t, svc.DB.First(&stored, result.WalletApp.ID).Error)
			assert.Equal(t, result.CashToken, stored.CashToken, "the issued token is stored verbatim")

			tok, err := lokicash.Decode(result.CashToken)
			require.NoError(t, err)
			require.NotNil(t, tok.IdentityRequired)
			assert.Equal(t, tc.identityRequired, *tok.IdentityRequired)
			assert.Equal(t, lokicash.HubGroupFor(hub.AppPubkey), tok.HubGroup)
		})
	}
}

// NIP-CASH §Multiple Relay Hints: every configured relay is emitted, in order.
func TestEncodeCashToken_EmitsEveryRelayHint(t *testing.T) {
	identityRequired := true
	relays := []string{"wss://one.relay.test", "wss://two.relay.test"}
	tok, err := lokicash.Decode(mustEncodeCashToken(t, walletHex(4), secretHex(4), relays, &identityRequired, fakeMintSig(), 1000, "hub"))
	require.NoError(t, err)
	assert.Equal(t, relays, tok.RelayURLs)
}

// secondSignRefused lets the first node signature through and refuses the second.
type secondSignRefused struct {
	*tests.MockLn
	calls    int
	onRefuse func()
}

func (s *secondSignRefused) SignMessage(ctx context.Context, msg string) (string, error) {
	s.calls++
	if s.calls >= 2 {
		s.onRefuse()
		return "", errors.New("node refused to sign")
	}
	return s.MockLn.SignMessage(ctx, msg)
}

// NIP-CASH §Mint Provenance: a Hub that cannot obtain a signature MUST fail before
// committing any funds. Splitting in two needs two signatures, so the second one's
// refusal must come before the first bill is funded.
func TestSplitInTwo_RemainderSignatureRefused_BeforeAnyFundsMove(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mock := svc.LNClient.(*tests.MockLn)
	mock.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: 3000},
	}
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	src := newProvenanceTestSourceWallet(t, svc, hub)
	before := queries.GetIsolatedBalance(svc.DB, src.ID)
	billsBefore := countCashWallets(t, svc)

	var atRefusal int64
	signer := &secondSignRefused{MockLn: mock}
	signer.onRefuse = func() { atRefusal = queries.GetIsolatedBalance(svc.DB, src.ID) }
	deps := newTestDeps(svc)
	deps.LNClient = signer

	a, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	b, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	_, intact, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{
		HubApp: hub, SourceWalletApp: src,
		CarvedIdentityType: db.CashIdentityPubkey, CarvedIdentityValue: a, CarvedAmountMloki: 2000,
		RemainderIdentityType: db.CashIdentityPubkey, RemainderIdentityValue: b, RemainderAmountMloki: 3000,
	})
	require.Error(t, err)
	require.Equal(t, 2, signer.calls, "test premise: the remainder's signature was the refused one")
	assert.True(t, intact, "nothing left the source, so its claim can be restored")
	assert.Equal(t, before, atRefusal, "no funds had moved when the signature was refused")
	assert.Equal(t, before, queries.GetIsolatedBalance(svc.DB, src.ID))
	assert.Equal(t, billsBefore, countCashWallets(t, svc), "neither prepared bill is left behind")
}

func countCashWallets(t *testing.T, svc *tests.TestService) int64 {
	t.Helper()
	var n int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("kind = ?", db.AppKindCashWallet).Count(&n).Error)
	return n
}
