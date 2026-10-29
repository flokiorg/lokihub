package nip47

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chainutil"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/cashwithdraw/withdrawtest"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// withdrawFixture is a Cash Hub offering on-chain withdrawals behind the private
// dispatch.
type withdrawFixture struct {
	svc      *tests.TestService
	n47      *nip47Service
	ws       *cashwithdraw.Service
	node     *withdrawtest.Node
	hub      *db.App
	hubXOnly string
}

func newWithdrawFixture(t *testing.T) *withdrawFixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := tests.CreateCashHub(t, svc, 9_000_000_000, 0)
	tests.FundApp(svc, hub.ID, 10_000_000_000, "hubfund")
	require.NoError(t, svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", hub.ID).Update("spent_retention_secs", 86400).Error)
	mock := svc.LNClient.(*tests.MockLn)
	node := &withdrawtest.Node{Height: 1000, PrivHex: hex.EncodeToString(mock.SigningKey.Serialize()),
		OnChain: &withdrawtest.Payer{Est: lnclient.FeeEstimate{SatPerKw: 1250, MinRelaySatPerKw: 250}}}
	ws := &cashwithdraw.Service{DB: svc.DB, Cfg: svc.Cfg, Keys: svc.Keys, Apps: svc.AppsService, Events: svc.EventPublisher,
		LN: func() lnclient.LNClient { return svc.LNClient }, NodeOverride: node,
		IA: apps.NewIdentityAuthorityManager(svc.DB), Funds: &withdrawtest.Funds{}}
	require.NoError(t, svc.Cfg.SetUpdate(config.CashWithdrawEnabledKey, "true", ""))
	_, err = ws.PutTerms(hub.ID, cashwithdraw.TermsInput{Enabled: true, FeeBaseMillis: 500_000, FeePpm: 2_000})
	require.NoError(t, err)
	n47 := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	n47.SetWithdraw(ws)
	xonly, err := node.XOnly(context.TODO())
	require.NoError(t, err)
	return &withdrawFixture{svc: svc, n47: n47, ws: ws, node: node, hub: hub, hubXOnly: xonly}
}

type billKeys struct {
	bill     *db.App
	connPriv string
	priv     []string
	pub      []string
}

func (f *withdrawFixture) mint(t *testing.T, amounts ...uint64) billKeys {
	t.Helper()
	var bk billKeys
	recipients := make([]cashwallet.RecipientInput, len(amounts))
	for i, a := range amounts {
		priv := nostr.GeneratePrivateKey()
		pub, _ := nostr.GetPublicKey(priv)
		bk.priv, bk.pub = append(bk.priv, priv), append(bk.pub, pub)
		recipients[i] = cashwallet.RecipientInput{IdentityType: db.CashIdentityPubkey, IdentityValue: pub, AmountMloki: a}
	}
	res, err := cashwallet.Create(context.TODO(), cashwallet.Deps{AppsService: f.svc.AppsService,
		TransactionsService: transactions.NewTransactionsService(f.svc.DB, f.svc.EventPublisher), LNClient: f.svc.LNClient,
		Keys: f.svc.Keys, DB: f.svc.DB, RelayURLs: []string{"wss://relay.test"}, IAChecker: apps.NewIdentityAuthorityManager(f.svc.DB)},
		cashwallet.Params{HubApp: f.hub, Recipients: recipients})
	require.NoError(t, err)
	bk.bill = res.WalletApp
	bk.connPriv, err = f.svc.Keys.GetCashPairingKey(res.WalletApp.ID)
	require.NoError(t, err)
	return bk
}

func (f *withdrawFixture) serve(t *testing.T, build func(b nipcash.ItemBinding) (transport.Item, error)) (transport.Result, bool) {
	t.Helper()
	nonce := tests.RandomHex32()
	notAfter := time.Now().Add(time.Minute).Unix()
	item, err := build(nipcash.ItemBinding{HubXOnly: f.hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.NoError(t, err)
	return f.n47.ServePrivateItem(context.TODO(), f.svc.LNClient, item,
		PrivateItemBinding{HubXOnly: f.hubXOnly, Nonce: nonce, NotAfter: notAfter, AuthFailures: new(int), ArrivalRelay: "wss://relay.test"})
}

func regtestAddress(t *testing.T) string {
	a, err := chainutil.NewAddressWitnessPubKeyHash(make([]byte, 20), &chaincfg.RegressionNetParams)
	require.NoError(t, err)
	return a.EncodeAddress()
}

func (f *withdrawFixture) quote(t *testing.T, bk billKeys, i int, addr string) (transport.Result, bool) {
	return f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashWithdrawQuoteParams{Address: addr, Credential: nipcash.BySigning(bk.priv[i])}.Item("q", *bk.bill.WalletPubkey, bk.connPriv, b)
	})
}

func (f *withdrawFixture) cancel(t *testing.T, bk billKeys, signer, params string) (transport.Result, bool) {
	return f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return buildItem(t, "c", *bk.bill.WalletPubkey, constants.NIP47MethodCashWithdraw, params, signer, bk.connPriv, b.HubXOnly, b.Nonce, b.NotAfter), nil
	})
}

// commit quotes and withdraws slice i, returning the request_id and the reply.
func (f *withdrawFixture) commit(t *testing.T, bk billKeys, i int) (string, *nipcash.Payout) {
	t.Helper()
	res, served := f.quote(t, bk, i, regtestAddress(t))
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var qr nipcash.CashWithdrawQuoteResult
	require.NoError(t, json.Unmarshal(res.Result, &qr))
	q, err := nipcash.VerifyQuoteAnswer(qr, nipcash.QuoteExpectation{HubXOnly: f.hubXOnly, Wallet: *bk.bill.WalletPubkey,
		IdentityValue: bk.pub[i], Address: qr.Address, AmountMillis: qr.AmountMillis})
	require.NoError(t, err)
	rid := tests.RandomHex32()
	res, served = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashWithdrawParams{Quote: q, Credential: nipcash.BySigning(bk.priv[i]), RequestID: rid, Notify: tests.RandomHex32()}.
			Item("w", *bk.bill.WalletPubkey, bk.connPriv, b)
	})
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	p, err := nipcash.ParsePayout(res.Result)
	require.NoError(t, err)
	return rid, p
}

// The whole private path: quote, call, lookup done, cancel request.
func TestPrivateWithdraw_QuoteCallLookupCancel(t *testing.T) {
	f := newWithdrawFixture(t)
	bk := f.mint(t, 21_000_000, 3_000_000)
	rid, p := f.commit(t, bk, 0)
	assert.Equal(t, nipcash.PayoutQueued, p.State)

	var wd db.CashWithdrawal
	require.NoError(t, f.svc.DB.Where("request_id = ?", rid).First(&wd).Error)
	assert.Equal(t, "wss://relay.test", wd.ArrivalRelay)

	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(bk.priv[0]), Request: nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Add(time.Minute).Unix()}}.
			Item("l", *bk.bill.WalletPubkey, bk.connPriv, b)
	})
	require.True(t, served)
	require.Nil(t, res.Error)
	var lk nipcash.CashLookupResult
	require.NoError(t, json.Unmarshal(res.Result, &lk))
	assert.Equal(t, nipcash.LookupDone, lk.Status)
	lp, err := nipcash.ParsePayout(lk.Result)
	require.NoError(t, err)
	assert.Equal(t, p.Receipt, lp.Receipt)

	// Another signer's lookup is UNAUTHORIZED.
	res, _ = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(bk.priv[1]), Request: nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Add(time.Minute).Unix()}}.
			Item("l", *bk.bill.WalletPubkey, bk.connPriv, b)
	})
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, res.Error.Code)

	// The same id again is REQUEST_REUSED at I7.
	res, _ = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		params := fmt.Sprintf(`{"quote":"{}","request_id":%q,"notify":%q}`, rid, tests.RandomHex32())
		return buildItem(t, "w", *bk.bill.WalletPubkey, constants.NIP47MethodCashWithdraw, params, bk.priv[0], bk.connPriv, b.HubXOnly, b.Nonce, b.NotAfter), nil
	})
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_REQUEST_REUSED, res.Error.Code)

	// A cancel request before sent_by + 7 days is RESTRICTED; it marks nothing.
	used := countUsed(t, f)
	res, served = f.cancel(t, bk, bk.priv[0], fmt.Sprintf(`{"cancel":true,"request_id":%q}`, rid))
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code)
	assert.Equal(t, used, countUsed(t, f))
	// By another signer: UNAUTHORIZED. Unknown id: NOT_FOUND. Malformed: BAD_REQUEST.
	res, _ = f.cancel(t, bk, bk.priv[1], fmt.Sprintf(`{"cancel":true,"request_id":%q}`, rid))
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, res.Error.Code)
	res, _ = f.cancel(t, bk, bk.priv[0], fmt.Sprintf(`{"cancel":true,"request_id":%q}`, tests.RandomHex32()))
	assert.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)
	res, _ = f.cancel(t, bk, bk.priv[0], fmt.Sprintf(`{"cancel":true,"request_id":%q,"notify":"x"}`, rid))
	assert.Equal(t, constants.ERROR_BAD_REQUEST, res.Error.Code)
	res, _ = f.cancel(t, bk, bk.priv[0], fmt.Sprintf(`{"cancel":false,"request_id":%q}`, rid))
	assert.Equal(t, constants.ERROR_BAD_REQUEST, res.Error.Code)
}

func countUsed(t *testing.T, f *withdrawFixture) int64 {
	var n int64
	require.NoError(t, f.svc.DB.Model(&db.CashUsedRequestID{}).Count(&n).Error)
	return n
}

// U-1.10: a withdrawal refusal carries its detail on the private reply.
func TestPrivateWithdraw_Detail(t *testing.T) {
	f := newWithdrawFixture(t)
	bk := f.mint(t, 21_000_000)
	other, err := chainutil.NewAddressWitnessPubKeyHash(make([]byte, 20), &chaincfg.TestNet3Params)
	require.NoError(t, err)
	res, served := f.quote(t, bk, 0, other.EncodeAddress())
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code)
	assert.Equal(t, string(nipcash.DetailNetwork), res.Error.Detail)
}

// U-23.18 / U-23.19: switch or hub off → both methods NOT_IMPLEMENTED at I5 and
// nothing in private_methods; committed payouts are still looked up and their
// cancel requests served. Never in methods.
func TestPrivateWithdraw_NotOffered(t *testing.T) {
	f := newWithdrawFixture(t)
	bk := f.mint(t, 21_000_000, 3_000_000)
	rid, _ := f.commit(t, bk, 0)
	perms := permissions.NewPermissionsService(f.svc.DB, f.svc.EventPublisher)
	assert.Subset(t, perms.GetPrivateMethods(bk.bill), []string{constants.NIP47MethodCashWithdraw, constants.NIP47MethodCashWithdrawQuote})
	assert.False(t, slices.Contains(perms.GetPermittedMethods(bk.bill, f.svc.LNClient), constants.NIP47MethodCashWithdraw))

	require.NoError(t, f.svc.Cfg.SetUpdate(config.CashWithdrawEnabledKey, "false", ""))
	assert.NotContains(t, perms.GetPrivateMethods(bk.bill), constants.NIP47MethodCashWithdrawQuote)
	res, served := f.quote(t, bk, 1, regtestAddress(t))
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, res.Error.Code)

	res, served = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(bk.priv[0]), Request: nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Add(time.Minute).Unix()}}.
			Item("l", *bk.bill.WalletPubkey, bk.connPriv, b)
	})
	require.True(t, served)
	assert.Nil(t, res.Error)
	res, served = f.cancel(t, bk, bk.priv[0], fmt.Sprintf(`{"cancel":true,"request_id":%q}`, rid))
	require.True(t, served)
	assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code, "served, not NOT_IMPLEMENTED")
}

// A cancel request and a lookup name a destroyed bill while its record is kept;
// a quote on the destroyed bill gets the tombstone, with withdraw_request_id
// for the call's authorizer only.
func TestPrivateWithdraw_DestroyedBill(t *testing.T) {
	f := newWithdrawFixture(t)
	bk := f.mint(t, 21_000_000)
	rid, _ := f.commit(t, bk, 0)
	var live int64
	require.NoError(t, f.svc.DB.Model(&db.App{}).Where("id = ?", bk.bill.ID).Count(&live).Error)
	require.Zero(t, live, "the drained bill is gone")

	res, served := f.cancel(t, bk, bk.priv[0], fmt.Sprintf(`{"cancel":true,"request_id":%q}`, rid))
	require.True(t, served)
	assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code)

	res, served = f.quote(t, bk, 0, regtestAddress(t))
	require.True(t, served)
	require.Nil(t, res.Error)
	tomb, ok := nipcash.ParseTombstone(res.Result)
	require.True(t, ok)
	assert.Equal(t, rid, tomb.WithdrawRequestID)
	assert.Equal(t, "redeemed", tomb.Reason)

	stranger := nostr.GeneratePrivateKey()
	res, _ = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashWithdrawQuoteParams{Address: regtestAddress(t), Credential: nipcash.BySigning(stranger)}.Item("q", *bk.bill.WalletPubkey, bk.connPriv, b)
	})
	tomb, ok = nipcash.ParseTombstone(res.Result)
	require.True(t, ok)
	assert.Empty(t, tomb.WithdrawRequestID)
}

// U-23.16: cash_status carries the withdraw object while offered and
// withdraw_request_id on the authorizer's row.
func TestPrivateWithdraw_CashStatus(t *testing.T) {
	f := newWithdrawFixture(t)
	bk := f.mint(t, 21_000_000, 3_000_000)
	rid, _ := f.commit(t, bk, 0)
	status := func(i int) nipcash.CashStatusResult {
		res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
			return buildItem(t, "s", *bk.bill.WalletPubkey, constants.NIP47MethodCashStatus, `{"scope":"all"}`, bk.priv[i], bk.connPriv, b.HubXOnly, b.Nonce, b.NotAfter), nil
		})
		require.True(t, served)
		require.Nil(t, res.Error, "%+v", res.Error)
		var out nipcash.CashStatusResult
		require.NoError(t, json.Unmarshal(res.Result, &out))
		return out
	}
	st := status(0)
	require.NotNil(t, st.Withdraw)
	assert.Equal(t, 6, st.Withdraw.FinalDepth)
	byID := map[string]string{}
	for _, r := range st.Recipients {
		byID[r.IdentityValue] = r.WithdrawRequestID
	}
	assert.Equal(t, rid, byID[bk.pub[0]])
	assert.Empty(t, byID[bk.pub[1]])
	for _, r := range status(1).Recipients {
		assert.Empty(t, r.WithdrawRequestID, "only for its authorizer")
	}
}
