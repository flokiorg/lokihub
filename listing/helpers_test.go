package listing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/connstr"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// fakeRelays is Relays in memory: each relay keeps what it accepted; a relay in
// refuse answers OK false.
type fakeRelays struct {
	mu        sync.Mutex
	stored    map[string][]*nip01.Event
	refuse    map[string]bool
	published []published
}

type published struct {
	urls []string
	ev   *nip01.Event
}

func newFakeRelays() *fakeRelays {
	return &fakeRelays{stored: map[string][]*nip01.Event{}, refuse: map[string]bool{}}
}

func (f *fakeRelays) Publish(_ context.Context, urls []string, ev *nip01.Event) map[string]error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]error{}
	f.published = append(f.published, published{urls: slices.Clone(urls), ev: ev.Copy()})
	for _, u := range urls {
		if f.refuse[u] {
			out[u] = errors.New("blocked")
			continue
		}
		f.stored[u] = append(f.stored[u], ev.Copy())
		out[u] = nil
	}
	return out
}

func (f *fakeRelays) Fetch(_ context.Context, urls []string, flt nip01.SubscriptionFilter) ([]*nip01.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	var out []*nip01.Event
	for _, u := range urls {
		for _, ev := range f.stored[u] {
			if seen[ev.ID] || (len(flt.Authors) > 0 && !slices.Contains(flt.Authors, ev.PubKey)) ||
				(len(flt.Kinds) > 0 && !slices.Contains(flt.Kinds, ev.Kind)) {
				continue
			}
			seen[ev.ID] = true
			out = append(out, ev.Copy())
		}
	}
	return out, nil
}

func (f *fakeRelays) put(url string, ev *nip01.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored[url] = append(f.stored[url], ev)
}

// kinds returns the kinds of every published event, in order.
func (f *fakeRelays) kinds() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var k []int
	for _, p := range f.published {
		k = append(k, p.ev.Kind)
	}
	return k
}

func (f *fakeRelays) last(kind int) *published {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.published) - 1; i >= 0; i-- {
		if f.published[i].ev.Kind == kind {
			p := f.published[i]
			return &p
		}
	}
	return nil
}

// clock is a settable test clock.
type clock struct {
	mu  sync.Mutex
	now int64
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Unix(c.now, 0)
}

func (c *clock) set(t int64) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (c *clock) add(d int64) {
	c.mu.Lock()
	c.now += d
	c.mu.Unlock()
}

// mockNodePriv is tests.MockLn's node key (sha256 of its seed text), so the
// listing signer is the TLV 4 of the strings the mock node signs.
func mockNodePriv() (string, string) {
	sum := sha256.Sum256([]byte("lokihub mock node identity"))
	priv, pub := btcec.PrivKeyFromBytes(sum[:])
	_ = priv
	return hex.EncodeToString(sum[:]), hex.EncodeToString(schnorr.SerializePubKey(pub))
}

type fixture struct {
	svc    *tests.TestService
	s      *Service
	relays *fakeRelays
	clock  *clock
	node   string
	orders *fakeOrders
	inval  []uint
}

const (
	listRelay  = "wss://list.test"
	nwcRelay   = "wss://relay.test"
	opRelay    = "wss://operator.test"
	startClock = int64(1792713600)
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	require.NoError(t, svc.Cfg.SetUpdate(ListingRelaysKey, listRelay, ""))
	priv, pub := mockNodePriv()
	f := &fixture{svc: svc, relays: newFakeRelays(), clock: &clock{now: startClock}, node: pub, orders: &fakeOrders{}}
	f.s = New(Deps{
		DB:       svc.DB,
		Settings: svc.Cfg,
		Keys:     svc.Keys,
		Signer:   KeySigner{Priv: priv, Pubkey: pub, Netw: "testnet"},
		CircleString: func(ctx context.Context, pubkey string, relays []string, secret, label string) (string, error) {
			return connstr.CircleHub(ctx, svc.LNClient, pubkey, relays, secret, label)
		},
		Relays:            f.relays,
		Orders:            f.orders,
		Now:               f.clock.Now,
		InvalidateGetInfo: func(id uint) { f.inval = append(f.inval, id) },
	})
	return f
}

func (f *fixture) circle(t *testing.T, policy string) *db.App {
	t.Helper()
	ref := apps.CircleIdentityRef{Name: "Circle " + tests.RandomHex32()[:6], Policy: policy}
	if policy == db.CirclePolicyFollowSet {
		ref.ProviderPubkey = f.node
		ref.FollowSetD = "members"
	}
	hub, _, err := f.svc.AppsService.CreateCircleHub("Circle "+tests.RandomHex32()[:6], "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE, constants.CIRCLE_WALLET_SCOPE}, nil, ref,
		db.CircleHubConfig{MaxExpSecs: 7200, FeesPpm: 500, PerWalletMaxMloki: 100_000, MinBudgetRenewal: constants.BUDGET_RENEWAL_WEEKLY})
	require.NoError(t, err)
	return hub
}

// fakeOrders is an OrderSource holding Order connections by app id.
type fakeOrders struct {
	mu     sync.Mutex
	offers map[uint]*OrderOffer
}

func (o *fakeOrders) Order(_ context.Context, id uint) (*OrderOffer, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	offer, ok := o.offers[id]
	if !ok {
		return nil, errors.New("not an Order connection")
	}
	cp := *offer
	return &cp, nil
}

func (o *fakeOrders) set(id uint, offer *OrderOffer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.offers == nil {
		o.offers = map[uint]*OrderOffer{}
	}
	o.offers[id] = offer
}

// orderApp is a stand-in app row for an Order connection (WP-21 adds the kind):
// a standard app whose wallet pubkey is the connection's TLV 0.
func (f *fixture) orderApp(t *testing.T, conn *nipCO.OrderConnection, hubID uint) *db.App {
	t.Helper()
	w := conn.Pubkey
	app := &db.App{Name: "order " + tests.RandomHex32()[:6], Kind: "cash_order", AppPubkey: tests.RandomHex32(), WalletPubkey: &w}
	require.NoError(t, f.svc.DB.Create(app).Error)
	f.orders.set(app.ID, &OrderOffer{Conn: conn, HubAppID: hubID, Summary: []string{"lightning", "onchain"}})
	return app
}

const (
	key1     = "0000000000000000000000000000000000000000000000000000000000000001"
	node1    = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	pub2     = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	kcoConn  = "f9308a019258c31049344f85f89d5229b531c845836f99b08601f113bce036f9"
	exRelay  = "wss://relay.example.com"
	kList1ID = "a2aeaaa40b57de5807c1a466060e58111c23eaa96390a0b894982bcafbe555b0"
	kList2ID = "25d3a99b0b593aa70ad5d848a91cd300062f6c4a479bc7005d66277897a96506"
	kLDel1ID = "5aa2a615eb258246769d21389dee11481066e3ef3809e9a25a7847acd848d37b"
)

// kco1 is K-CO-1 (NIP-CO's example Order connection), signed by key 1 under
// chain bitcoin: LocalChainSigner(01×32, "bitcoin") in place of the node.
func kco1(t *testing.T) *nipCO.OrderConnection {
	t.Helper()
	sign, err := nipcash.LocalChainSigner(key1, "bitcoin")
	require.NoError(t, err)
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(0x40 + i)
	}
	c, err := nipCO.SignOrderConnection(nipCO.OrderConnection{Pubkey: kcoConn, Relays: []string{exRelay},
		Secret: hex.EncodeToString(secret), Label: "Alice's cash counter", HasLabel: true, HubIdentity: node1, Chain: "bitcoin"}, sign, node1)
	require.NoError(t, err)
	return &c
}
