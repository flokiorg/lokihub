package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

type orderFixture struct {
	svc      *tests.TestService
	ln       *tests.MockLn
	nip47svc *nip47Service
	hub      *db.App
	conn     *cashorder.Connection
	connApp  *db.App
	secret   string
	hubXOnly string
	// invoiceSkew is how far ahead of the Hub's clock the node stamps an order
	// invoice.
	invoiceSkew time.Duration
	// invoiceNow is the node's clock for an order invoice; nil is time.Now.
	invoiceNow func() time.Time
}

func u64(v uint64) *uint64 { return &v }

// orderTermsFixture: Lightning 1,000..1,000,000 millis, fee 1000 + 1000 ppm, a
// 3600 s window; on-chain 10,000..1,000,000, fee 21000 + 2000 ppm, 86400 s,
// tiers 1/3/6; open ceilings 2,000,000 each, at most 2 open on-chain orders.
func orderTermsFixture() cashorder.Settings {
	return cashorder.Settings{
		Terms: nipCO.Terms{
			Lightning: &nipCO.KindTerms{MinMillis: 1000, MaxMillis: 1_000_000, FeeBase: 1000, FeePpm: 1000, Window: 3600},
			Onchain: &nipCO.KindTerms{MinMillis: 10_000, MaxMillis: 1_000_000, FeeBase: 21000, FeePpm: 2000, Window: 86400,
				Confirmations: []nipCO.Tier{{UpToMillis: u64(100_000), N: 1}, {UpToMillis: u64(500_000), N: 3}, {N: 6}}},
			Bill: nipCO.BillTerms{RedeemFeePpm: 5000, MinTransferMillis: 1000},
		},
		OpenCeilingLnMillis: 2_000_000, OpenCeilingOnchainMillis: 2_000_000, OpenOnchainMax: 2,
	}
}

func newOrderFixture(t *testing.T) *orderFixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	ln := svc.LNClient.(*tests.MockLn)
	ln.Channels = []lnclient.Channel{{Active: true, RemoteBalance: 10_000_000}}
	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	deps := cashorder.Deps{DB: svc.DB, Keys: svc.Keys, Apps: svc.AppsService, LN: ln, Relays: []string{"wss://relay.example.com"}}
	conn, err := cashorder.CreateConnection(context.TODO(), deps, hub.ID, "shop")
	require.NoError(t, err)
	require.NoError(t, cashorder.PutSettings(svc.DB, hub.ID, orderTermsFixture(), time.Now()))
	var connApp db.App
	require.NoError(t, svc.DB.First(&connApp, conn.AppID).Error)
	decoded, err := nipCO.DecodeOrderConnection(conn.ConnectionString)
	require.NoError(t, err)
	return &orderFixture{svc: svc, ln: ln, nip47svc: NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil),
		hub: hub, conn: conn, connApp: &connApp, secret: decoded.Secret, hubXOnly: strings.Repeat("cd", 32)}
}

func (f *orderFixture) serve(t *testing.T, build func(b nipcash.ItemBinding) (transport.Item, error)) (transport.Result, bool) {
	t.Helper()
	nonce := tests.RandomHex32()
	notAfter := time.Now().Add(time.Minute).Unix()
	item, err := build(nipcash.ItemBinding{HubXOnly: f.hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.NoError(t, err)
	return f.nip47svc.ServePrivateItem(context.TODO(), f.ln, item,
		PrivateItemBinding{HubXOnly: f.hubXOnly, Nonce: nonce, NotAfter: notAfter, ArrivalRelay: "wss://arrival.example.com", AuthFailures: new(int)})
}

// order places one order and returns its result and the buyer key.
func (f *orderFixture) order(t *testing.T, payment string, amount uint64, mutate func(*nipCO.CashOrderParams)) (transport.Result, string, nipCO.CashOrderParams) {
	t.Helper()
	buyer := nostr.GeneratePrivateKey()
	notify, err := nipCO.NewNotify()
	require.NoError(t, err)
	recipient, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	p := nipCO.CashOrderParams{Recipient: nipcash.Pubkey(recipient), AmountMillis: amount, Payment: payment,
		Notify: notify, RequestID: tests.RandomHex32()}
	if mutate != nil {
		mutate(&p)
	}
	if payment == nipCO.PaymentLightning {
		stamp := time.Now()
		if f.invoiceNow != nil {
			stamp = f.invoiceNow()
		}
		invoice, hash := tests.SignedInvoice(1, stamp.Add(f.invoiceSkew), time.Hour)
		f.ln.MakeInvoiceQueue = append(f.ln.MakeInvoiceQueue, &lnclient.Transaction{PaymentHash: hash, Invoice: invoice})
	} else {
		f.ln.OnchainAddresses = append(f.ln.OnchainAddresses, "fcrt1q"+tests.RandomHex32()[:38])
	}
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return p.Item("o", f.conn.WalletPubkey, f.secret, buyer, b)
	})
	require.True(t, served)
	return res, buyer, p
}

func orderOf(t *testing.T, res transport.Result) nipCO.Order {
	t.Helper()
	require.Nil(t, res.Error, "%+v", res.Error)
	var o nipCO.Order
	require.NoError(t, json.Unmarshal(res.Result, &o))
	return o
}

// U-21.1: a Lightning order is placed: open, the fee and pay amount of NIP-CO
// §Response, an invoice for exactly pay_millis expiring at pay_by, no bills;
// the order, its reservation and its record are written; no ledger row.
func TestCashOrder_LightningPlaced(t *testing.T) {
	f := newOrderFixture(t)
	// The node stamps the invoice with its own clock, a little after the Hub
	// read its own: pay_by must still be the invoice's expiry.
	f.invoiceSkew = 3 * time.Second
	var txBefore int64
	require.NoError(t, f.svc.DB.Model(&db.Transaction{}).Count(&txBefore).Error)

	res, buyer, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	o := orderOf(t, res)
	assert.Equal(t, nipCO.OrderOpen, o.State)
	assert.EqualValues(t, 21_000, o.AmountMillis)
	assert.EqualValues(t, 1000+21, o.FeeMillis)
	assert.EqualValues(t, 22_021, o.PayMillis)
	assert.NotEmpty(t, o.Invoice)
	assert.Empty(t, o.Address)
	assert.NotNil(t, o.Bills)
	assert.InDelta(t, time.Now().Unix()+3600, o.PayBy, 5)
	// The invoice expires at pay_by exactly (NIP-CO §Lightning).
	inv, err := decodepay.Decode(o.Invoice)
	require.NoError(t, err)
	assert.Equal(t, inv.CreatedAt+inv.Expiry, int64(o.PayBy))
	assert.Equal(t, int64(22_021), f.ln.MakeInvoiceAmounts()[len(f.ln.MakeInvoiceAmounts())-1], "never amountless")

	var row db.CashOrder
	require.NoError(t, f.svc.DB.Where("request_id = ?", p.RequestID).First(&row).Error)
	buyerPub, _ := nostr.GetPublicKey(buyer)
	assert.Equal(t, buyerPub, row.BuyerPubkey)
	assert.Equal(t, int64(21_000), row.ReservedMillis)
	assert.Equal(t, "wss://arrival.example.com", row.ArrivalRelay)
	assert.Equal(t, p.Notify, row.Notify)
	var rec db.CashRequestRecord
	require.NoError(t, f.svc.DB.Where("request_id = ?", p.RequestID).First(&rec).Error)
	assert.Equal(t, constants.NIP47MethodCashOrder, rec.Method)
	assert.Equal(t, buyerPub, rec.Authorizer)
	require.NotNil(t, rec.OrderID)
	assert.Nil(t, rec.DropAfter, "its retention is the order's")

	var txAfter int64
	require.NoError(t, f.svc.DB.Model(&db.Transaction{}).Count(&txAfter).Error)
	assert.Equal(t, txBefore, txAfter, "placing an order moves no funds")
}

// U-21.2: an on-chain order gets a fresh address bound to it and the
// confirmations of the tier its pay_millis falls in; the fee is rounded up to
// whole base units.
func TestCashOrder_OnchainPlaced(t *testing.T) {
	f := newOrderFixture(t)
	res, _, p := f.order(t, nipCO.PaymentOnchain, 21_000, nil)
	o := orderOf(t, res)
	assert.Zero(t, o.PayMillis%1000, "pay_millis is whole base units")
	assert.EqualValues(t, 22_000, o.FeeMillis, "21042 rounded up so 21000 + fee is whole")
	assert.EqualValues(t, 43_000, o.PayMillis)
	assert.NotEmpty(t, o.Address)
	assert.EqualValues(t, 1, o.Confirmations)

	var addr db.CashOrderAddress
	require.NoError(t, f.svc.DB.Where("address = ?", o.Address).First(&addr).Error)
	var row db.CashOrder
	require.NoError(t, f.svc.DB.Where("request_id = ?", p.RequestID).First(&row).Error)
	require.NotNil(t, addr.OrderID)
	assert.Equal(t, row.ID, *addr.OrderID)
}

// U-21.3: the processing algorithm's refusals, each with its code.
func TestCashOrder_Refusals(t *testing.T) {
	f := newOrderFixture(t)
	code := func(res transport.Result) string {
		require.NotNil(t, res.Error)
		return res.Error.Code
	}

	res, _, _ := f.order(t, nipCO.PaymentLightning, 500, nil)
	assert.Equal(t, constants.ERROR_RESTRICTED, code(res), "below min_millis")
	res, _, _ = f.order(t, nipCO.PaymentLightning, 1_000_001, nil)
	assert.Equal(t, constants.ERROR_RESTRICTED, code(res), "above max_millis")

	// The ceiling of open Lightning orders: 2,000,000.
	res, _, _ = f.order(t, nipCO.PaymentLightning, 1_000_000, nil)
	orderOf(t, res)
	res, _, _ = f.order(t, nipCO.PaymentLightning, 1_000_000, nil)
	orderOf(t, res)
	res, _, _ = f.order(t, nipCO.PaymentLightning, 1000, nil)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, code(res), "the open-order ceiling")

	// At most 2 open on-chain orders.
	for range 2 {
		res, _, _ = f.order(t, nipCO.PaymentOnchain, 10_000, nil)
		orderOf(t, res)
	}
	res, _, _ = f.order(t, nipCO.PaymentOnchain, 10_000, nil)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, code(res), "the open on-chain order count")
	var burnt int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrderAddress{}).Where("burnt = ?", true).Count(&burnt).Error)
	assert.Zero(t, burnt, "a refusal before step 6 takes no address")

	// A request_id is used once.
	res, _, p := f.order(t, nipCO.PaymentOnchain, 10_000, nil)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, code(res))
	res, _, _ = f.order(t, nipCO.PaymentLightning, 1000, func(q *nipCO.CashOrderParams) { q.RequestID = p.RequestID })
	assert.Equal(t, constants.ERROR_REQUEST_REUSED, code(res), "marked used once the item proof verified")
}

// U-21.4: a Lightning order the node cannot expect to receive is QUOTA_EXCEEDED.
func TestCashOrder_LightningNotReceivable(t *testing.T) {
	f := newOrderFixture(t)
	f.ln.Channels = []lnclient.Channel{{Active: true, RemoteBalance: 20_000}, {Active: false, RemoteBalance: 1_000_000}}
	res, _, _ := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, res.Error.Code)
	assert.Empty(t, f.ln.MakeInvoiceAmounts(), "no invoice is issued for it")
}

// U-21.5: a kind the terms do not offer and a retired connection are RESTRICTED;
// a retired connection answers get_info with "cash_order": {}.
func TestCashOrder_RetiredAndNotOffered(t *testing.T) {
	f := newOrderFixture(t)
	s := orderTermsFixture()
	s.Terms.Onchain = nil
	require.NoError(t, cashorder.PutSettings(f.svc.DB, f.hub.ID, s, time.Now()))
	res, _, _ := f.order(t, nipCO.PaymentOnchain, 10_000, nil)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code, "on-chain is not sold")

	info := f.getInfo(t)
	assert.Contains(t, string(info["cash_order"]), `"lightning"`)

	require.NoError(t, cashorder.RetireConnection(cashorder.Deps{DB: f.svc.DB}, f.hub.ID, f.conn.AppID, time.Now()))
	res, _, _ = f.order(t, nipCO.PaymentLightning, 21_000, nil)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code)
	assert.JSONEq(t, `{}`, string(f.getInfo(t)["cash_order"]), "retired: no kind and no bill")
}

// standardCall sends method on the standard transport over the Order
// connection, signed by its (public) client key.
func (f *orderFixture) standardCall(t *testing.T, method string) models.Response {
	t.Helper()
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, f.conn.WalletPubkey, f.secret)
	require.NoError(t, err)
	pub, err := nostr.GetPublicKey(f.secret)
	require.NoError(t, err)
	return doHandleEventForMethod(t, f.svc, f.nip47svc, c, f.secret, pub, method)
}

func (f *orderFixture) getInfo(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	resp := f.standardCall(t, models.GET_INFO_METHOD)
	require.Nil(t, resp.Error, "%+v", resp.Error)
	b, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var out map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// U-21.6: get_info on an Order connection: get_info only, the two private
// methods, the Hub identity, network and chain, and the terms.
func TestCashOrder_GetInfo(t *testing.T) {
	f := newOrderFixture(t)
	info := f.getInfo(t)
	assert.JSONEq(t, `["get_info"]`, string(info["methods"]))
	assert.JSONEq(t, `["cash_lookup","cash_order"]`, string(info["private_methods"]))
	identity, err := nipcash.MinterXOnly(f.ln.GetPubkey())
	require.NoError(t, err)
	assert.JSONEq(t, `"`+identity+`"`, string(info["pubkey"]))
	assert.NotEmpty(t, info["network"])
	assert.JSONEq(t, `"flokicoin"`, string(info["chain"]))
	terms, err := nipCO.ParseOrderInfo(info["cash_order"])
	require.NoError(t, err)
	assert.Equal(t, orderTermsFixture().Terms, *terms)

	decoded, err := nipCO.DecodeOrderConnection(f.conn.ConnectionString)
	require.NoError(t, err)
	require.NoError(t, decoded.Verify(), "the string is signed by the node")
	assert.Equal(t, identity, decoded.HubIdentity)
	assert.Equal(t, "shop", decoded.Label)
}

// U-21.7: cash_lookup reads the order with its buyer key; any other signer is
// UNAUTHORIZED.
func TestCashOrder_Lookup(t *testing.T) {
	f := newOrderFixture(t)
	res, buyer, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	placed := orderOf(t, res)

	lookup := func(signer string) transport.Result {
		res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
			return nipcash.CashLookupParams{Credential: nipcash.BySigning(signer),
				Request: nipcash.CashLookupRequest{RequestID: p.RequestID, Deadline: time.Now().Unix()}}.Item("l", f.conn.WalletPubkey, f.secret, b)
		})
		require.True(t, served)
		return res
	}
	res = lookup(buyer)
	require.Nil(t, res.Error, "%+v", res.Error)
	var answer nipcash.CashLookupResult
	require.NoError(t, json.Unmarshal(res.Result, &answer))
	require.Equal(t, nipcash.LookupDone, answer.Status)
	got, err := p.ParseLookupResult(answer.Result)
	require.NoError(t, err)
	assert.Equal(t, placed.Invoice, got.Invoice)
	assert.Equal(t, nipCO.OrderOpen, got.State)

	res = lookup(nostr.GeneratePrivateKey())
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, res.Error.Code)
}

// U-21.8: the standard transport refuses every method but get_info on an Order
// connection, and cash_order everywhere, with NOT_IMPLEMENTED; the private
// transport serves cash_order on an Order connection only.
func TestCashOrder_TransportsAndTargets(t *testing.T) {
	f := newOrderFixture(t)
	for _, method := range []string{models.GET_BALANCE_METHOD, models.PAY_INVOICE_METHOD, constants.NIP47MethodCashOrder} {
		resp := f.standardCall(t, method)
		require.NotNil(t, resp.Error, method)
		assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, resp.Error.Code, method)
	}

	// A cash_redeem aimed at the Order connection is not served there.
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.BuildItem("s", f.conn.WalletPubkey, f.secret, nipcash.MethodCashRedeem,
			map[string]any{"invoice": "lnbcrt1x"}, nipcash.BySigning(nostr.GeneratePrivateKey()), b)
	})
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, res.Error.Code)

	// A cash_order without an item proof is BAD_REQUEST.
	res, served = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		item, err := nipCO.CashOrderParams{Recipient: nipcash.Pubkey(strings.Repeat("ab", 32)), AmountMillis: 21_000,
			Payment: nipCO.PaymentLightning, Notify: strings.Repeat("ef", 32), RequestID: tests.RandomHex32()}.
			Item("o", f.conn.WalletPubkey, f.secret, nostr.GeneratePrivateKey(), b)
		item.Proof = nil
		return item, err
	})
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, res.Error.Code)
}

// U-21.9: the terms are refused, never clamped, outside their bounds; the Cash
// Hub's per-wallet ceiling may not drop below a kind's max_millis.
func TestCashOrder_TermsBounds(t *testing.T) {
	f := newOrderFixture(t)
	for name, mutate := range map[string]func(*cashorder.Settings){
		"lightning window too short":   func(s *cashorder.Settings) { s.Terms.Lightning.Window = 59 },
		"onchain window too long":      func(s *cashorder.Settings) { s.Terms.Onchain.Window = 604801 },
		"max above the ceiling":        func(s *cashorder.Settings) { s.Terms.Lightning.MaxMillis = 1_000_001 },
		"max below 1/100 of ceiling":   func(s *cashorder.Settings) { s.Terms.Lightning.MaxMillis = 9_999 },
		"no open ceiling":              func(s *cashorder.Settings) { s.OpenCeilingOnchainMillis = 0 },
		"onchain not whole base units": func(s *cashorder.Settings) { s.Terms.Onchain.MinMillis = 10_500 },
		"redeem fee ppm above million": func(s *cashorder.Settings) { s.Terms.Bill.RedeemFeePpm = 1_000_001 },
		"tiers not ascending": func(s *cashorder.Settings) {
			s.Terms.Onchain.Confirmations = []nipCO.Tier{{UpToMillis: u64(500_000), N: 1}, {UpToMillis: u64(100_000), N: 3}, {N: 6}}
		},
	} {
		s := orderTermsFixture()
		mutate(&s)
		err := cashorder.PutSettings(f.svc.DB, f.hub.ID, s, time.Now())
		assert.ErrorIs(t, err, constants.ErrInvalidParams, name)
	}
	lower := 999_999
	err := f.svc.AppsService.UpdateCashHubConfig(f.hub.ID, &lower, nil, nil, nil, nil, nil)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.Contains(t, err.Error(), "max_millis")
}

// U-21.10: an Order connection cannot be deleted; it goes with its Hub.
func TestCashOrder_ConnectionLivesAsLongAsItsHub(t *testing.T) {
	f := newOrderFixture(t)
	assert.ErrorIs(t, f.svc.AppsService.DeleteApp(f.connApp), constants.ErrInvalidParams)
	require.NoError(t, f.svc.AppsService.DeleteApp(f.hub))
	assert.Nil(t, f.svc.AppsService.GetAppById(f.conn.AppID))
	var rows int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrderConnection{}).Count(&rows).Error)
	assert.Zero(t, rows)
}
