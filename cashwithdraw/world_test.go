package cashwithdraw

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chainutil"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// fakeNode is the node a test drives: a tip, regtest's HRP, the mock node's
// identity key, an injectable signing failure and the on-chain capability.
type fakeNode struct {
	mu      sync.Mutex
	tip     int64
	privHex string
	signErr error
	payer   lnclient.OnchainPayer
}

func (n *fakeNode) Tip(context.Context) (int64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.tip, nil
}
func (n *fakeNode) HRP(context.Context) (string, error) {
	return chaincfg.RegressionNetParams.Bech32HRPSegwit, nil
}
func (n *fakeNode) XOnly(context.Context) (string, error) {
	pub, err := nostr.GetPublicKey(n.privHex)
	return pub, err
}
func (n *fakeNode) SignEvent(_ context.Context, ev *nip01.Event) error {
	n.mu.Lock()
	err := n.signErr
	n.mu.Unlock()
	if err != nil {
		return err
	}
	return ev.Sign(n.privHex)
}
func (n *fakeNode) Payer() lnclient.OnchainPayer { return n.payer }
func (n *fakeNode) setTip(t int64)               { n.mu.Lock(); n.tip = t; n.mu.Unlock() }

// fakePayer answers the two calls the withdrawal service makes before the
// engine: fee estimates and the wallet's scripts.
type fakePayer struct {
	mu       sync.Mutex
	est      lnclient.FeeEstimate
	scripts  [][]byte
	listings int
}

var errUnused = errors.New("not used by this test")

func (p *fakePayer) ListUnspent(context.Context, int32, string) ([]lnclient.Utxo, error) {
	return nil, errUnused
}
func (p *fakePayer) LeaseOutput(context.Context, [32]byte, string, int64) (int64, error) {
	return 0, errUnused
}
func (p *fakePayer) ReleaseOutput(context.Context, [32]byte, string) error { return errUnused }
func (p *fakePayer) ListLeases(context.Context) ([]lnclient.Lease, error)  { return nil, errUnused }
func (p *fakePayer) FundPsbtFixed(context.Context, []byte, [32]byte, int64) ([]byte, error) {
	return nil, errUnused
}
func (p *fakePayer) FinalizePsbt(context.Context, []byte) ([]byte, error) { return nil, errUnused }
func (p *fakePayer) PublishTx(context.Context, []byte) error              { return errUnused }
func (p *fakePayer) SubmitPackage(context.Context, []byte, []byte) error  { return errUnused }
func (p *fakePayer) EstimateFee(context.Context, int) (lnclient.FeeEstimate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.est, nil
}
func (p *fakePayer) RequiredReserve(context.Context) (int64, error) { return 0, errUnused }
func (p *fakePayer) ListWalletScripts(context.Context) ([][]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listings++
	return append([][]byte(nil), p.scripts...), nil
}
func (p *fakePayer) NextChangeScript(context.Context) ([]byte, error) { return nil, errUnused }
func (p *fakePayer) WalletTx(context.Context, string) (lnclient.WalletTx, error) {
	return lnclient.WalletTx{}, errUnused
}
func (p *fakePayer) ConfirmedBalance(context.Context) (int64, error) { return 0, errUnused }
func (p *fakePayer) BestBlock(context.Context) (lnclient.BlockEpoch, error) {
	return lnclient.BlockEpoch{}, errUnused
}
func (p *fakePayer) SubscribeBlocks(context.Context) (<-chan lnclient.BlockEpoch, error) {
	return nil, errUnused
}

// fakeFunds stands in for the payout engine: switches for the bound and the
// set-aside, and a set-aside written as a row inside the commit's transaction
// (so it rolls back with it).
type fakeFunds struct {
	mu        sync.Mutex
	notReady  bool
	atBound   bool
	noCoins   bool
	setAside  []uint
	signalled int
}

const setAsidePrefix = "wdtest-setaside-"

func (f *fakeFunds) Ready() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.notReady }
func (f *fakeFunds) Snapshot(context.Context) (FundsSnapshot, error) {
	return FundsSnapshot{ConfirmedSat: 1e9}, nil
}
func (f *fakeFunds) AtUnconfirmedBoundTx(*gorm.DB, FundsSnapshot, uint64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.atBound, nil
}
func (f *fakeFunds) CanSetAsideTx(*gorm.DB, uint64, uint64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.noCoins, nil
}
func (f *fakeFunds) SetAsideTx(tx *gorm.DB, id uint, _, _ uint64) error {
	return tx.Create(&db.UserConfig{Key: fmt.Sprintf("%s%d", setAsidePrefix, id), Value: "1"}).Error
}
func (f *fakeFunds) Signal() { f.mu.Lock(); f.signalled++; f.mu.Unlock() }

// world is one Cash Hub offering withdrawals on a fresh database.
type world struct {
	svc   *tests.TestService
	s     *Service
	hub   *db.App
	node  *fakeNode
	payer *fakePayer
	funds *fakeFunds
	now   time.Time
	mu    sync.Mutex
}

func (w *world) clock() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.now }
func (w *world) advance(d time.Duration) {
	w.mu.Lock()
	w.now = w.now.Add(d)
	w.mu.Unlock()
}

const (
	termsFeeBase = 500_000
	termsFeePpm  = 2_000
	termsMin     = 1_000_000
	termsMax     = 1_000_000_000
)

func newWorld(t *testing.T) *world {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	mock := svc.LNClient.(*tests.MockLn)
	hub := tests.CreateCashHub(t, svc, 9_000_000_000, 0)
	tests.FundApp(svc, hub.ID, 100_000_000_000, "hubfund")
	w := &world{svc: svc, hub: hub, now: time.Now().Truncate(time.Second)}
	w.payer = &fakePayer{est: lnclient.FeeEstimate{SatPerKw: 1250, MinRelaySatPerKw: 253}}
	w.node = &fakeNode{tip: 1000, privHex: hex.EncodeToString(mock.SigningKey.Serialize()), payer: w.payer}
	w.funds = &fakeFunds{}
	w.s = &Service{
		DB: svc.DB, Cfg: svc.Cfg, Keys: svc.Keys, Apps: svc.AppsService, Events: svc.EventPublisher,
		LN: func() lnclient.LNClient { return svc.LNClient }, NodeOverride: w.node,
		IA: apps.NewIdentityAuthorityManager(svc.DB), Funds: w.funds, Now: w.clock,
	}
	require.NoError(t, svc.Cfg.SetUpdate(config.CashWithdrawEnabledKey, "true", ""))
	minM, maxM := int64(termsMin), int64(termsMax)
	_, err = w.s.PutTerms(hub.ID, TermsInput{Enabled: true, FeeBaseMillis: termsFeeBase, FeePpm: termsFeePpm, MinMillis: &minM, MaxMillis: &maxM})
	require.NoError(t, err)
	return w
}

func (w *world) deps() cashwallet.Deps {
	return cashwallet.Deps{AppsService: w.svc.AppsService, TransactionsService: transactions.NewTransactionsService(w.svc.DB, w.svc.EventPublisher),
		LNClient: w.svc.LNClient, Keys: w.svc.Keys, DB: w.svc.DB, RelayURLs: []string{"wss://relay.test"},
		IAChecker: apps.NewIdentityAuthorityManager(w.svc.DB)}
}

// holder is one pubkey slice's key.
type holder struct{ priv, pub string }

func newHolder() holder {
	priv := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(priv)
	return holder{priv, pub}
}

// mintPubkey mints a bill with one pubkey slice per amount.
func (w *world) mintPubkey(t *testing.T, amounts ...uint64) (*db.App, []holder) {
	t.Helper()
	hs := make([]holder, len(amounts))
	recipients := make([]cashwallet.RecipientInput, len(amounts))
	for i, a := range amounts {
		hs[i] = newHolder()
		recipients[i] = cashwallet.RecipientInput{IdentityType: db.CashIdentityPubkey, IdentityValue: hs[i].pub, AmountMloki: a}
	}
	res, err := cashwallet.Create(context.TODO(), w.deps(), cashwallet.Params{HubApp: w.hub, Recipients: recipients, ExpirySecs: 0})
	require.NoError(t, err)
	return res.WalletApp, hs
}

// mintCash mints a cash-mode bill and returns its secret.
func (w *world) mintCash(t *testing.T, amount uint64) (*db.App, string) {
	t.Helper()
	secret, commitment, err := tests.GenerateCashSecret()
	require.NoError(t, err)
	res, err := cashwallet.Create(context.TODO(), w.deps(), cashwallet.Params{HubApp: w.hub,
		Recipients: []cashwallet.RecipientInput{{IdentityType: db.CashIdentityCash, IdentityValue: commitment, AmountMloki: amount}}})
	require.NoError(t, err)
	return res.WalletApp, secret
}

// address is a regtest address of a version and program length.
func address(t *testing.T, version, n int) string {
	t.Helper()
	prog := make([]byte, n)
	for i := range prog {
		prog[i] = byte(i + 7*version + n)
	}
	var a chainutil.Address
	var err error
	switch {
	case version == 0 && n == 20:
		a, err = chainutil.NewAddressWitnessPubKeyHash(prog, &chaincfg.RegressionNetParams)
	case version == 0 && n == 32:
		a, err = chainutil.NewAddressWitnessScriptHash(prog, &chaincfg.RegressionNetParams)
	case version == 1 && n == 32:
		a, err = chainutil.NewAddressTaproot(prog, &chaincfg.RegressionNetParams)
	default:
		t.Fatalf("no address for %d/%d", version, n)
	}
	require.NoError(t, err)
	return a.EncodeAddress()
}

func scriptOf(t *testing.T, addr string) []byte {
	v, prog, err := nipcash.DecodeWitnessAddress(addr, chaincfg.RegressionNetParams.Bech32HRPSegwit)
	require.NoError(t, err)
	return nipcash.WitnessScript(v, prog)
}

func raw(t *testing.T, v any) json.RawMessage {
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// quote asks a quote as h for bill.
func (w *world) quote(t *testing.T, bill *db.App, h holder, params map[string]any) (*nipcash.CashWithdrawQuoteResult, error) {
	t.Helper()
	return w.s.Quote(context.TODO(), bill, Caller{ItemSigner: h.pub, LimitKey: h.pub}, raw(t, params))
}

func (w *world) mustQuote(t *testing.T, bill *db.App, h holder, addr string) *nipcash.WithdrawQuote {
	t.Helper()
	res, err := w.quote(t, bill, h, map[string]any{"address": addr})
	require.NoError(t, err)
	q, err := nipcash.ParseWithdrawQuote(res.Quote)
	require.NoError(t, err)
	return q
}

// request builds a cash_withdraw request as h through nmilat's client builder.
func request(t *testing.T, bill *db.App, cred nipcash.Credential, q *nipcash.WithdrawQuote, rid string, remint *nipcash.CashTarget) json.RawMessage {
	t.Helper()
	req, err := nipcash.CashWithdrawParams{Quote: q, Credential: cred, RequestID: rid, Notify: tests.RandomHex32(), Remint: remint}.Request(*bill.WalletPubkey)
	require.NoError(t, err)
	return raw(t, req)
}

func (w *world) meta() CallMeta {
	return CallMeta{Deadline: w.clock().Add(time.Hour).Unix(), ArrivalRelay: "wss://relay.test"}
}

func (w *world) withdraw(t *testing.T, bill *db.App, h holder, params json.RawMessage) (*nipcash.Payout, error) {
	t.Helper()
	return w.s.Withdraw(context.TODO(), bill, Caller{ItemSigner: h.pub, LimitKey: h.pub}, w.meta(), params, nil)
}

// code is a refusal's code and detail.
func code(err error) (string, nipcash.Detail) {
	var we *nipcash.WithdrawError
	var r *Refusal
	switch {
	case errors.As(err, &we):
		return we.Code, we.Detail
	case errors.As(err, &r):
		return r.Code, r.Detail
	case err == nil:
		return "", ""
	}
	return "OTHER:" + err.Error(), ""
}

func requireCode(t *testing.T, err error, want string, detail nipcash.Detail) {
	t.Helper()
	got, d := code(err)
	require.Equal(t, want, got, "error: %v", err)
	require.Equal(t, detail, d, "error: %v", err)
}

// snapshot counts the rows a withdrawal may write.
type snapshot struct {
	withdrawals, events, records, proofs, setAsides, used, transactions, apps int64
	claimed                                                                   int64
}

func (w *world) snapshot(t *testing.T) snapshot {
	t.Helper()
	var s snapshot
	d := w.svc.DB
	require.NoError(t, d.Model(&db.CashWithdrawal{}).Count(&s.withdrawals).Error)
	require.NoError(t, d.Model(&db.CashWithdrawEvent{}).Count(&s.events).Error)
	require.NoError(t, d.Model(&db.CashRequestRecord{}).Count(&s.records).Error)
	require.NoError(t, d.Model(&db.CashConsumedProof{}).Count(&s.proofs).Error)
	require.NoError(t, d.Model(&db.UserConfig{}).Where("key LIKE ?", setAsidePrefix+"%").Count(&s.setAsides).Error)
	require.NoError(t, d.Model(&db.CashUsedRequestID{}).Count(&s.used).Error)
	require.NoError(t, d.Model(&db.Transaction{}).Count(&s.transactions).Error)
	require.NoError(t, d.Model(&db.App{}).Count(&s.apps).Error)
	require.NoError(t, d.Model(&db.CashWalletClaim{}).Where("claimed_at IS NOT NULL").Count(&s.claimed).Error)
	return s
}

func balance(w *world, appID uint) int64 { return queries.GetIsolatedBalance(w.svc.DB, appID) }

func newRID() string { return tests.RandomHex32() }
