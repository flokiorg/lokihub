package listing

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip01"
	nmlisting "github.com/ohstr/nmilat/nipcash/listing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func uintp(v uint) *uint { return &v }

func tagValues(ev *nip01.Event, name string) []string {
	var out []string
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == name {
			out = append(out, t[1])
		}
	}
	return out
}

func lastListing(t *testing.T, f *fixture) *nip01.Event {
	t.Helper()
	p := f.relays.last(nmlisting.KindListing)
	require.NotNil(t, p, "no listing published")
	return p.ev
}

func parse(t *testing.T, ev *nip01.Event) *nmlisting.Listing {
	t.Helper()
	l, err := nmlisting.ParseListing(ev, nmlisting.CheckOptions{Chains: []string{"flokicoin"}, TestNetworks: true})
	require.NoError(t, err)
	return l
}

// K-LIST-1, K-LIST-2, K-LDEL-1 through the service: K-CO-1 listed by key 1 under
// chain bitcoin, the NIP example's fields, then closed, then deleted.
func TestKAT_KList1_KList2_KLDel1(t *testing.T) {
	f := newFixture(t)
	f.s.d.Signer = KeySigner{Priv: key1, Pubkey: node1, Netw: "mainnet"}
	hub := tests.CreateCashHub(t, f.svc, 1_000_000, 3600)
	order := f.orderApp(t, kco1(t), hub.ID)
	f.orders.set(order.ID, &OrderOffer{Conn: kco1(t), HubAppID: hub.ID, Summary: []string{"lightning", "onchain"},
		Hints: []nmlisting.Hint{{Payment: "lightning", MinMillis: 1000000, MaxMillis: 1000000000, FeeBase: 1000, FeePpm: 1000}}})

	v, err := f.s.Create(context.Background(), CreateRequest{ConnAppID: uintp(order.ID), Fields: Fields{
		Title: "Alice's cash counter", Image: "https://example.com/counter.png",
		Content:  "Cash for the Lisbon meetup. Lightning or on-chain, paid out within the hour.",
		Location: "Lisbon, Baixa", Languages: []string{"pt", "en"}, Geohashes: []string{"eyc"}, Hints: true,
		OperatorPubkey: pub2, OperatorRelay: exRelay,
	}})
	require.NoError(t, err)
	assert.Equal(t, kList1ID, v.LastEventID, "K-LIST-1")
	assert.Equal(t, "31190:"+node1+":"+kcoConn, v.Address)
	lst, err := nmlisting.ParseListing(lastListing(t, f), nmlisting.CheckOptions{Chains: []string{"bitcoin"}})
	require.NoError(t, err)
	assert.Equal(t, int64(1795305600), lst.ExpiresAt(), "K-LEXP-1")

	f.clock.set(1793318400)
	_, err = f.s.Unlist(context.Background(), v.ID)
	require.NoError(t, err)
	assert.Equal(t, kList2ID, lastListing(t, f).ID, "K-LIST-2")

	f.clock.set(1793318460)
	row, err := f.s.load(v.ID)
	require.NoError(t, err)
	require.NoError(t, f.s.publishDeletion(context.Background(), row))
	assert.Equal(t, kLDel1ID, f.relays.last(5).ev.ID, "K-LDEL-1")
}

// U-25.1: refusals and acceptances.
func TestU25_1_Create(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cashHub := tests.CreateCashHub(t, f.svc, 1_000_000, 3600)

	_, err := f.s.Create(ctx, CreateRequest{ConnAppID: uintp(cashHub.ID)})
	require.ErrorIs(t, err, ErrRefused)
	assert.Contains(t, err.Error(), "never listed")

	future := f.clock.Now().Add(3600e9)
	wallet, _, err := f.svc.AppsService.CreateApp("bill", "", 10, constants.BUDGET_RENEWAL_NEVER, &future,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCashWallet, &cashHub.ID, db.ParentKindCash, nil)
	require.NoError(t, err)
	_, err = f.s.Create(ctx, CreateRequest{ConnAppID: uintp(wallet.ID)})
	require.ErrorIs(t, err, ErrRefused)

	// A retired Order connection.
	order := f.orderApp(t, kco1(t), cashHub.ID)
	f.orders.set(order.ID, &OrderOffer{Conn: kco1(t), HubAppID: cashHub.ID, Retired: true})
	_, err = f.s.Create(ctx, CreateRequest{ConnAppID: uintp(order.ID)})
	require.ErrorIs(t, err, ErrRefused)

	// A body carrying any connection string.
	for _, body := range []string{
		`{"type":"circle_wallet","connection":"circlehub1qqq"}`,
		`{"title":"cashhub1abc"}`,
		`{"title":"x","image":"nostr+walletconnect://abc"}`,
	} {
		assert.True(t, CarriesConnectionString([]byte(body)), body)
	}
	assert.False(t, CarriesConnectionString([]byte(`{"type":"circle_wallet","circle_hub_app_id":3,"title":"Cash club"}`)))

	// A circle: its own listing connection is created.
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{Title: "Cash club"}})
	require.NoError(t, err)
	var conn db.App
	require.NoError(t, f.svc.DB.First(&conn, v.ConnAppID).Error)
	assert.Equal(t, db.AppKindCircleHubConn, conn.Kind)
	assert.Equal(t, *hub.WalletPubkey, *conn.WalletPubkey)
	assert.NotEqual(t, hub.AppPubkey, conn.AppPubkey)
	assert.Equal(t, hub.ID, *conn.ParentAppID)
	assert.Equal(t, db.ParentKindListing, conn.ParentKind)

	// Listed once only.
	_, err = f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.ErrorIs(t, err, ErrRefused)
	// The listing connection itself is never listed.
	_, err = f.s.Create(ctx, CreateRequest{ConnAppID: uintp(conn.ID)})
	require.ErrorIs(t, err, ErrRefused)
}

// U-25.2: the built event: author = node identity = TLV 4, d = TLV 0, n, network,
// connection = the issued string, and it parses.
func TestU25_2_BuiltEvent(t *testing.T) {
	f := newFixture(t)
	hub := f.circle(t, db.CirclePolicyAllowlist)
	_, err := f.s.Create(context.Background(), CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{Title: "Cash club"}})
	require.NoError(t, err)
	ev := lastListing(t, f)
	l := parse(t, ev)
	assert.Equal(t, f.node, ev.PubKey)
	assert.Equal(t, f.node, l.Circle.HubIdentity)
	assert.Equal(t, *hub.WalletPubkey, l.D)
	assert.Equal(t, "flokicoin", l.Chain)
	assert.Equal(t, "testnet", l.Network)
	assert.Equal(t, []string{nwcRelay}, l.Circle.Relays)
	assert.Equal(t, "Cash club", l.Circle.Label)
	require.NoError(t, l.Circle.Verify())
}

// U-25.3: created_at is never above now; with the clock behind the last version
// the publish waits for the scheduler, then goes out later.
func TestU25_3_NeverPostDated(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	first := v.LastCreatedAt

	f.clock.add(-60)
	_, err = f.s.Pause(ctx, v.ID)
	require.NoError(t, err)
	row, _ := f.s.load(v.ID)
	assert.Equal(t, first, row.LastCreatedAt, "nothing post-dated or reused")
	assert.Equal(t, first+1, row.DueAt)
	assert.Equal(t, 1, countKind(f, nmlisting.KindListing))

	f.clock.set(first + 1)
	f.s.Tick(ctx)
	row, _ = f.s.load(v.ID)
	assert.Equal(t, first+1, row.LastCreatedAt)
	assert.Equal(t, "paused", eventStatus(lastListing(t, f)))
}

func countKind(f *fixture, kind int) int {
	n := 0
	for _, k := range f.relays.kinds() {
		if k == kind {
			n++
		}
	}
	return n
}

// U-25.4: tags per type; U-25.RC3: p relay hint fallbacks, L with l, g and
// location order.
func TestU25_4_Tags(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyFollowSet)

	_, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{FollowSetA: true}})
	require.ErrorIs(t, err, ErrRefused, "a tag needs the confirmation")
	_, err = f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{Hints: true}})
	require.ErrorIs(t, err, ErrRefused, "no hint for a circle")
	for _, g := range []string{"ey", "eycde", "eycdef"} {
		_, err = f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{Geohashes: []string{g}}})
		require.ErrorIs(t, err, ErrRefused, g)
	}

	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{
		Title: "Club", Languages: []string{"en", "pt"}, Geohashes: []string{"eyc", "eycd"}, Location: "Lisbon",
		FollowSetA: true, ConfirmFollowSetPublic: true, OperatorPubkey: pub2,
	}})
	require.NoError(t, err)
	ev := lastListing(t, f)
	assert.Equal(t, []string{"circle_wallet", "follow_set"}, tagValues(ev, "t"))
	assert.Empty(t, tagValues(ev, "hint"))
	assert.Equal(t, []string{"30000:" + f.node + ":members"}, tagValues(ev, "a"))
	assert.Equal(t, []string{"ISO-639-1"}, tagValues(ev, "L"))
	assert.Equal(t, []string{"en", "pt"}, tagValues(ev, "l"))
	assert.Equal(t, []string{"Cash offer: Club"}, tagValues(ev, "alt"))
	var names []string
	for _, tg := range ev.Tags {
		names = append(names, tg[0])
	}
	joined := strings.Join(names, ",")
	assert.Contains(t, joined, "g,g,location", "location after the g tags")
	// p hint fallback 3: the first listing relay.
	assert.Equal(t, []string{pub2, listRelay}, ev.GetTag("p"))

	// Fallback 2: the operator's newest 10002 write relay.
	op10002 := RelayListEvent(pub2, []string{opRelay}, startClock-10)
	require.NoError(t, op10002.Sign("0000000000000000000000000000000000000000000000000000000000000002"))
	f.relays.put(listRelay, op10002)
	f.clock.add(10)
	_, err = f.s.Update(ctx, v.ID, Fields{Title: "Club", OperatorPubkey: pub2})
	require.NoError(t, err)
	assert.Equal(t, []string{pub2, opRelay}, lastListing(t, f).GetTag("p"))
	// Fallback 1: the relay entered.
	f.clock.add(10)
	_, err = f.s.Update(ctx, v.ID, Fields{Title: "Club", OperatorPubkey: pub2, OperatorRelay: "wss://given.test"})
	require.NoError(t, err)
	assert.Equal(t, []string{pub2, "wss://given.test"}, lastListing(t, f).GetTag("p"))
	// No a without follow_set_a.
	assert.Empty(t, tagValues(lastListing(t, f), "a"))
}

// U-25.5: expiration = created_at + 30 days; republish after 7 days and on each
// trigger; none after closed.
func TestU25_5_Republish(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	ev := lastListing(t, f)
	assert.Equal(t, []string{strconv.FormatInt(startClock+ExpirationSecs, 10)}, tagValues(ev, "expiration"))

	f.clock.add(RepublishSecs - 1)
	f.s.Tick(ctx)
	assert.Equal(t, 1, countKind(f, nmlisting.KindListing))
	f.clock.add(1)
	f.s.Tick(ctx)
	assert.Equal(t, 2, countKind(f, nmlisting.KindListing))
	assert.EqualValues(t, startClock+RepublishSecs, lastListing(t, f).CreatedAt)

	// A relay change republishes at once (on the next tick).
	f.clock.add(5)
	require.NoError(t, f.s.SetListingRelays(ctx, []string{listRelay, "wss://second.test"}))
	f.s.Tick(ctx)
	assert.Equal(t, 3, countKind(f, nmlisting.KindListing))

	f.clock.add(5)
	_, err = f.s.Unlist(ctx, v.ID)
	require.NoError(t, err)
	n := countKind(f, nmlisting.KindListing)
	f.clock.add(RepublishSecs * 2)
	f.s.Tick(ctx)
	assert.Equal(t, n, countKind(f, nmlisting.KindListing), "never republished after closed")
}

// U-25.6 and U-25.14: closed version with expiration ≥ the last open expiry, the
// deletion at delete_due_at and not before; a restart (a fresh service on the
// same table) resumes.
func TestU25_6_RetireOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := tests.CreateCashHub(t, f.svc, 1_000_000, 3600)
	order := f.orderApp(t, kco1(t), hub.ID)
	f.s.d.Signer = KeySigner{Priv: key1, Pubkey: node1, Netw: "mainnet"}
	v, err := f.s.Create(ctx, CreateRequest{ConnAppID: uintp(order.ID)})
	require.NoError(t, err)
	lastOpen := *v.LastOpenExpiresAt

	// (a) the retire is committed first (WP-21), then (b).
	f.clock.add(100)
	f.orders.set(order.ID, &OrderOffer{Conn: kco1(t), HubAppID: hub.ID, Retired: true})
	require.NoError(t, f.s.RetireOrder(ctx, order.ID))
	closed := lastListing(t, f)
	assert.Equal(t, "closed", eventStatus(closed))
	exp, _ := strconv.ParseInt(tagValues(closed, "expiration")[0], 10, 64)
	assert.GreaterOrEqual(t, exp, lastOpen)
	assert.Contains(t, f.inval, order.ID, "get_info cache dropped")

	// Restart: a new service on the same table.
	f.s = New(f.s.d)
	f.clock.set(lastOpen - 1)
	f.s.Tick(ctx)
	assert.Nil(t, f.relays.last(5), "no deletion before delete_due_at")
	f.clock.set(lastOpen)
	f.s.Tick(ctx)
	del := f.relays.last(5)
	require.NotNil(t, del)
	assert.Equal(t, []string{nmlisting.Address(node1, kcoConn)}, del.ev.GetTag("a"))
	assert.Equal(t, []string{"31190"}, del.ev.GetTag("k"))
	row, _ := f.s.load(v.ID)
	assert.Equal(t, db.ListingPhaseDeleted, row.Phase)
}

// U-25.7: closing a circle (accepting false committed) closes its listing; the
// scheduler also closes one whose circle stopped accepting.
func TestU25_7_CloseCircle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	no := false
	require.NoError(t, f.svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", hub.ID).Update("accepting_joins", &no).Error)
	f.clock.add(10)
	require.NoError(t, f.s.CloseCircle(ctx, hub.ID))
	assert.Equal(t, "closed", eventStatus(lastListing(t, f)))

	// Second circle: no explicit call, the scheduler notices.
	hub2 := f.circle(t, db.CirclePolicyAllowlist)
	v2, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub2.ID)})
	require.NoError(t, err)
	require.NoError(t, f.svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", hub2.ID).Update("accepting_joins", &no).Error)
	require.NoError(t, f.svc.DB.Model(&db.OfferListing{}).Where("id = ?", v2.ID).Update("due_at", 0).Error)
	f.clock.add(10)
	f.s.Tick(ctx)
	row, _ := f.s.load(v2.ID)
	assert.Equal(t, db.ListingPhaseClosing, row.Phase)
	_ = v
}

// U-25.8: pause and resume.
func TestU25_8_PauseResume(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	f.clock.add(1)
	v, err = f.s.Pause(ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, "paused", v.Status)
	assert.Equal(t, "paused", eventStatus(lastListing(t, f)))
	f.clock.add(1)
	v, err = f.s.Resume(ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, "open", eventStatus(lastListing(t, f)))
	assert.Equal(t, startClock+2+ExpirationSecs, *v.LastOpenExpiresAt)
}

// U-25.11: the 10002 names the listing relays as write relays; targets are the
// listing relays, the operator's write relays and the TLV 1 relays.
func TestU25_11_Targets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	op10002 := RelayListEvent(pub2, []string{opRelay}, startClock-10)
	require.NoError(t, op10002.Sign("0000000000000000000000000000000000000000000000000000000000000002"))
	f.relays.put(listRelay, op10002)
	hub := f.circle(t, db.CirclePolicyAllowlist)
	_, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{OperatorPubkey: pub2}})
	require.NoError(t, err)
	rl := f.relays.last(10002)
	require.NotNil(t, rl)
	assert.Equal(t, f.node, rl.ev.PubKey)
	assert.Equal(t, [][]string{{"r", listRelay, "write"}}, rl.ev.Tags)
	p := f.relays.last(nmlisting.KindListing)
	assert.ElementsMatch(t, []string{listRelay, opRelay, nwcRelay}, p.urls)
}

// U-25.12 (service side): revoking deletes the listing connection after
// unlisting; no info event is ever published for it.
func TestU25_12_RevokeConnection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	assert.False(t, db.PublishesNip47InfoEvent(db.AppKindCircleHubConn))
	conn := v.ConnAppID
	f.clock.add(5)
	v, err = f.s.RevokeConnection(ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, uint(0), v.ConnAppID)
	assert.Equal(t, db.ListingPhaseClosing, v.Phase)
	var n int64
	f.svc.DB.Model(&db.App{}).Where("id = ?", conn).Count(&n)
	assert.Zero(t, n)
	f.svc.DB.Model(&db.App{}).Where("id = ?", hub.ID).Count(&n)
	assert.Equal(t, int64(1), n, "the hub is untouched")
	for _, k := range f.relays.kinds() {
		assert.NotEqual(t, 13194, k)
	}
}

// U-25.13: the operator line; lokihub builds no 11191.
func TestU25_13_Operator(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{OperatorPubkey: pub2, OperatorRelay: opRelay}})
	require.NoError(t, err)

	ops := nmlisting.NewOperatorOffers([]nmlisting.Ref{{Pubkey: v.Address, Relay: listRelay}}, nil)
	ops.PubKey, ops.CreatedAt = pub2, uint64(startClock)
	require.NoError(t, ops.Sign("0000000000000000000000000000000000000000000000000000000000000002"))
	f.relays.put(listRelay, ops)
	v, err = f.s.CheckOperator(ctx, v.ID)
	require.NoError(t, err)
	require.NotNil(t, v.OperatorVerified)
	assert.True(t, *v.OperatorVerified)

	newer := nmlisting.NewOperatorOffers(nil, nil)
	newer.PubKey, newer.CreatedAt = pub2, uint64(startClock+1)
	require.NoError(t, newer.Sign("0000000000000000000000000000000000000000000000000000000000000002"))
	f.relays.put(opRelay, newer)
	v, err = f.s.CheckOperator(ctx, v.ID)
	require.NoError(t, err)
	assert.False(t, *v.OperatorVerified, "the newest 11191 no longer names it")

	for _, k := range f.relays.kinds() {
		assert.NotEqual(t, nmlisting.KindOperatorOffers, k)
	}
}

// U-25.13 guard: no lokihub code builds or signs a kind 11191.
func TestU25_13_NoOperatorOffersBuilder(t *testing.T) {
	root := filepath.Join("..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "frontend" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "NewOperatorOffers", "MergeOperatorOffers", "KindOperatorOffers":
					if sel.Sel.Name == "KindOperatorOffers" && strings.HasSuffix(path, filepath.Join("listing", "view.go")) {
						return true // the read-only fetch filter
					}
					t.Errorf("%s uses %s: lokihub never builds the operator's 11191", path, sel.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
}

// U-25.RC4: relay A took the open version but refuses the closed one: re-sent
// until it accepts or delete_due_at passes; relay B, which took both, is not.
func TestU25_RC4_ClosedResent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.Cfg.SetUpdate(ListingRelaysKey, "wss://a.test,wss://b.test", ""))
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)

	f.relays.refuse["wss://a.test"] = true
	f.clock.add(10)
	_, err = f.s.Unlist(ctx, v.ID)
	require.NoError(t, err)
	row, _ := f.s.load(v.ID)
	assert.Equal(t, []string{"wss://b.test", nwcRelay}, decodeList(row.ClosedRelays))
	assert.Equal(t, f.clock.Now().Unix()+retryFirst, row.DueAt)

	f.clock.add(retryFirst)
	f.s.Tick(ctx)
	p := f.relays.last(nmlisting.KindListing)
	assert.Equal(t, []string{"wss://a.test"}, p.urls, "only the relay lacking it")

	f.relays.refuse["wss://a.test"] = false
	f.clock.add(2 * retryFirst)
	f.s.Tick(ctx)
	row, _ = f.s.load(v.ID)
	assert.ElementsMatch(t, []string{"wss://a.test", "wss://b.test", nwcRelay}, decodeList(row.ClosedRelays))
	assert.Equal(t, *row.DeleteDueAt, row.DueAt, "next is the deletion")
}

// U-25.3/U-25.5: a version no relay accepted is retried with backoff and
// alerted, then recorded once accepted.
func TestPublishBackoff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.relays.refuse[listRelay] = true
	f.relays.refuse[nwcRelay] = true
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	assert.True(t, v.Failing)
	row, _ := f.s.load(v.ID)
	assert.Equal(t, int64(retryFirst), row.RetryDelay)
	f.clock.add(retryFirst)
	f.s.Tick(ctx)
	row, _ = f.s.load(v.ID)
	assert.Equal(t, int64(2*retryFirst), row.RetryDelay)
	assert.Equal(t, v.LastEventID, row.LastEventID, "the same version re-sent")
	f.relays.refuse[listRelay] = false
	f.clock.add(2 * retryFirst)
	f.s.Tick(ctx)
	row, _ = f.s.load(v.ID)
	assert.Nil(t, row.FailingSince)
	assert.Equal(t, 1, row.RelaysOK)
}

// Re-listing a deleted address publishes a new open version with a later
// created_at.
func TestRelistAfterDeletion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	f.clock.add(10)
	_, err = f.s.Unlist(ctx, v.ID)
	require.NoError(t, err)
	f.clock.add(ExpirationSecs)
	f.s.Tick(ctx)
	row, _ := f.s.load(v.ID)
	require.Equal(t, db.ListingPhaseDeleted, row.Phase)
	del := f.relays.last(5).ev

	f.clock.add(10)
	v2, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	assert.Equal(t, v.ID, v2.ID)
	assert.Equal(t, "open", v2.Status)
	assert.Greater(t, v2.LastCreatedAt, int64(del.CreatedAt)) //nolint:gosec // a unix time
	assert.NotZero(t, v2.ConnAppID)
}

// Preview is the unsigned next version.
func TestPreview(t *testing.T) {
	f := newFixture(t)
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(context.Background(), CreateRequest{CircleHubAppID: uintp(hub.ID), Fields: Fields{Title: "A"}})
	require.NoError(t, err)
	ev, err := f.s.Preview(context.Background(), v.ID)
	require.NoError(t, err)
	assert.Empty(t, ev.Sig)
	assert.Greater(t, int64(ev.CreatedAt), v.LastCreatedAt) //nolint:gosec // a unix time
	raw, _ := json.Marshal(ev)
	assert.Contains(t, string(raw), "circlehub1")
}

// A deleted circle hub closes its listing and drops the listing connection at
// once.
func TestHubDeletedClosesListing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	require.NoError(t, f.svc.DB.Exec("DELETE FROM circle_hub_configs WHERE app_id = ?", hub.ID).Error)
	require.NoError(t, f.svc.DB.Delete(&db.App{}, hub.ID).Error)
	require.NoError(t, f.svc.DB.Model(&db.OfferListing{}).Where("id = ?", v.ID).Update("due_at", 0).Error)
	f.clock.add(10)
	f.s.Tick(ctx)
	row, _ := f.s.load(v.ID)
	assert.Equal(t, db.ListingPhaseClosing, row.Phase)
	assert.Zero(t, row.ConnAppID)
	assert.Equal(t, "closed", eventStatus(lastListing(t, f)))
}

// 503 when the node cannot sign: nothing is listed and the new listing
// connection is dropped.
func TestCreate_CannotSign(t *testing.T) {
	f := newFixture(t)
	ks := f.s.d.Signer.(KeySigner)
	ks.SignErr = errors.New("no signer")
	f.s.d.Signer = ks
	hub := f.circle(t, db.CirclePolicyAllowlist)
	_, err := f.s.Create(context.Background(), CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.ErrorIs(t, err, ErrCannotSign)
	var n int64
	f.svc.DB.Model(&db.App{}).Where("kind = ?", db.AppKindCircleHubConn).Count(&n)
	assert.Zero(t, n)
	views, err := f.s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, db.ListingPhaseDeleted, views[0].Phase)
}

// R2-L3: closing a circle drops the cached get_info of every connection its
// listings name, a closing one's too, but closes only the listed one again.
func TestCloseCircle_InvalidatesEveryListingsConnection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	listed, err := f.s.load(v.ID)
	require.NoError(t, err)
	closing := *listed
	closing.ID, closing.ConnAppID, closing.Phase, closing.D = 0, 987654, db.ListingPhaseClosing, listed.D[:len(listed.D)-4]+"-old" // d fits its 64 characters
	require.NoError(t, f.svc.DB.Create(&closing).Error)

	f.inval = nil
	f.clock.add(10)
	require.NoError(t, f.s.CloseCircle(ctx, hub.ID))
	assert.ElementsMatch(t, []uint{listed.ConnAppID, closing.ConnAppID}, f.inval, "a closing listing's connection is invalidated too")
	assert.Equal(t, "closed", eventStatus(lastListing(t, f)))
	stillClosing, err := f.s.load(closing.ID)
	require.NoError(t, err)
	assert.Equal(t, db.ListingPhaseClosing, stillClosing.Phase, "not closed a second time")
}
