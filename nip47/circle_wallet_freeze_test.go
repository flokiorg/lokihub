package nip47

import (
	"context"
	"encoding/json"
	"github.com/flokiorg/lokihub/circlelife"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// Membership resolution (NIP-CW §Membership): a removed member's wallet is
// Suspended by the next successful resolution, driven here through real
// requests on the standard transport, the only one that serves wallet methods.

// freezeMembership answers like the social cache: an allowlist from the
// database, a follow set from members. err, when set, stands for a set that
// can't be resolved yet.
type freezeMembership struct {
	mu        sync.Mutex
	followSet map[string]bool
	err       error
	// identities records which identity each call asked about.
	identities []uint
	// revokeAfterNextCall removes the member right after the next check, the
	// way an admin could while a join is between its check and its commit.
	revokeAfterNextCall string
}

func (m *freezeMembership) IsAuthorized(_ context.Context, pubkey string, identity *db.CircleIdentity, gormDB *gorm.DB) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.identities = append(m.identities, identity.ID)
	if m.err != nil {
		return false, m.err
	}
	var member bool
	switch identity.Policy {
	case db.CirclePolicyAllowlist:
		var count int64
		if err := gormDB.Model(&db.CircleIdentityAllowedPubkey{}).
			Where("circle_identity_id = ? AND pubkey = ?", identity.ID, pubkey).Count(&count).Error; err != nil {
			return false, err
		}
		member = count > 0
	case db.CirclePolicyFollowSet:
		member = m.followSet[pubkey]
	}
	if m.revokeAfterNextCall == pubkey {
		m.revokeAfterNextCall = ""
		m.removeLocked(gormDB, identity, pubkey)
	}
	return member, nil
}

func (m *freezeMembership) removeLocked(gormDB *gorm.DB, identity *db.CircleIdentity, pubkey string) {
	gormDB.Where("circle_identity_id = ? AND pubkey = ?", identity.ID, pubkey).Delete(&db.CircleIdentityAllowedPubkey{})
	delete(m.followSet, pubkey)
}

type freezeCircle struct {
	t          *testing.T
	svc        *tests.TestService
	nip47svc   *nip47Service
	membership *freezeMembership
	hub        *db.App
	hubSecret  string
	identity   db.CircleIdentity
}

func newFreezeCircle(t *testing.T, policy string) *freezeCircle {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	require.NoError(t, svc.Cfg.SetUpdate("Relay", "wss://relay.example", ""))

	ref := apps.CircleIdentityRef{Name: "circle " + policy, Policy: policy}
	if policy == db.CirclePolicyFollowSet {
		ref.ProviderPubkey = strings.Repeat("ab", 32)
		ref.FollowSetD = "family"
	}
	hub, hubSecret, err := svc.AppsService.CreateCircleHub("circle", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE}, nil, ref,
		db.CircleHubConfig{MaxExpSecs: 7200, PerWalletMaxMloki: 10_000_000})
	require.NoError(t, err)
	tests.FundApp(svc, hub.ID, 100_000_000, tests.RandomHex32())
	cfg, err := svc.AppsService.GetCircleHubConfig(hub.ID)
	require.NoError(t, err)

	membership := &freezeMembership{followSet: map[string]bool{}}
	return &freezeCircle{
		t: t, svc: svc, membership: membership, hub: hub, hubSecret: hubSecret, identity: cfg.CircleIdentity,
		nip47svc: NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, membership),
	}
}

func (c *freezeCircle) add(pubkey string) {
	c.t.Helper()
	switch c.identity.Policy {
	case db.CirclePolicyAllowlist:
		require.NoError(c.t, c.svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: c.identity.ID, Pubkey: pubkey}).Error)
	case db.CirclePolicyFollowSet:
		c.membership.mu.Lock()
		c.membership.followSet[pubkey] = true
		c.membership.mu.Unlock()
	}
}

func (c *freezeCircle) remove(pubkey string) {
	c.membership.mu.Lock()
	defer c.membership.mu.Unlock()
	c.membership.removeLocked(c.svc.DB, &c.identity, pubkey)
}

// memberWallet is a joined member's wallet, reached with the key only the
// member holds, as nmilat's client reaches it.
type memberWallet struct {
	c            *freezeCircle
	app          db.App
	walletPubkey string
	secret       string
}

func (c *freezeCircle) join(memberPriv string) *memberWallet {
	c.t.Helper()
	connection, err := nipcw.NewConnectionKey()
	require.NoError(c.t, err)
	join := nipcw.CreateCircleWalletParams{
		Credential:       nipcw.BySigning(memberPriv),
		Connection:       connection,
		MaxAmountMillis:  5_000_000,
		MaxReceiveMillis: 5_000_000,
		Expiry:           time.Hour,
	}
	prepared, err := join.Prepare(*c.hub.WalletPubkey, time.Now())
	require.NoError(c.t, err)
	// Joins are private-only (NIP-CW §Private Join).
	raw := privateJoin(c.t, c.svc, c.nip47svc, c.hub, c.hubSecret, memberPriv, prepared.Request)
	issued, err := prepared.ParseResult(raw)
	require.NoError(c.t, err)

	w := &memberWallet{c: c, walletPubkey: issued.WalletPubkey, secret: connection.Secret()}
	require.NoError(c.t, c.svc.DB.Where("wallet_pubkey = ?", issued.WalletPubkey).First(&w.app).Error)
	tests.FundApp(c.svc, w.app.ID, 1_000_000, tests.RandomHex32())
	return w
}

func (w *memberWallet) call(method string, params map[string]interface{}) models.Response {
	w.c.t.Helper()
	payload := map[string]interface{}{"method": method}
	if params != nil {
		payload["params"] = params
	}
	return sendOverStandardTransport(w.c.t, w.c.svc, w.c.nip47svc, w.walletPubkey, w.secret, payload)
}

var freezePolicies = []string{db.CirclePolicyAllowlist, db.CirclePolicyFollowSet}

func errorCode(resp models.Response) string {
	if resp.Error == nil {
		return ""
	}
	return resp.Error.Code
}

// resolve applies the circle's current membership, as a successful resolution
// does (an allowlist edit, or a follow-set refresh).
func (c *freezeCircle) resolve(now time.Time) {
	c.t.Helper()
	members := map[string]bool{}
	var pubkeys []string
	require.NoError(c.t, c.svc.DB.Model(&db.CircleIdentityAllowedPubkey{}).Where("circle_identity_id = ?", c.identity.ID).Pluck("pubkey", &pubkeys).Error)
	for _, pk := range pubkeys {
		members[pk] = true
	}
	c.membership.mu.Lock()
	for pk := range c.membership.followSet {
		members[pk] = true
	}
	c.membership.mu.Unlock()
	require.NoError(c.t, c.svc.DB.Transaction(func(tx *gorm.DB) error {
		_, err := circlelife.ApplyResolution(tx, c.identity.ID, func(pk string) bool { return members[pk] }, now)
		return err
	}))
}

// NIP-CW §Membership: a member a resolution no longer names is Suspended at
// once and can only pay out; named again within the grace, the same wallet is
// Active again. Nothing changes between resolutions (U-17.1, U-17.5).
func TestMembership_RemovedMemberSuspended_ReaddedResumed(t *testing.T) {
	for _, policy := range freezePolicies {
		t.Run(policy, func(t *testing.T) {
			c := newFreezeCircle(t, policy)
			member := nostr.GeneratePrivateKey()
			memberPub, _ := nostr.GetPublicKey(member)
			c.add(memberPub)
			w := c.join(member)

			c.remove(memberPub)
			require.Nil(t, w.call(models.MAKE_INVOICE_METHOD, map[string]interface{}{"amount": 1000}).Error,
				"no resolution yet: nothing changes")

			c.resolve(time.Now())
			assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(w.call(models.MAKE_INVOICE_METHOD, map[string]interface{}{"amount": 1000})))
			assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(w.call(models.GET_BUDGET_METHOD, nil)))
			assert.Nil(t, w.call(models.GET_BALANCE_METHOD, nil).Error)
			pay := w.call(models.PAY_INVOICE_METHOD, map[string]interface{}{"invoice": tests.MockInvoice})
			assert.Nil(t, pay.Error, "a Suspended wallet still pays out: %+v", pay.Error)

			c.add(memberPub)
			c.resolve(time.Now())
			resp := w.call(models.MAKE_INVOICE_METHOD, map[string]interface{}{"amount": 1000})
			assert.Nil(t, resp.Error, "Active again, the same wallet: %+v", resp.Error)
		})
	}
}

// Past its grace a Suspended member named again stays Suspended (the job closes
// it).
func TestMembership_ReaddedAfterGraceStaysSuspended(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	c.add(memberPub)
	w := c.join(member)
	c.remove(memberPub)
	c.resolve(time.Now())
	c.add(memberPub)
	c.resolve(time.Now().Add(8 * 24 * time.Hour))
	var stored db.App
	require.NoError(t, c.svc.DB.First(&stored, w.app.ID).Error)
	assert.Equal(t, circlelife.StateSuspended, stored.CircleState)
}

// privateJoin sends a join as one private-transport item: the bill proof signed
// with the Hub connection's secret, the item proof by the joining member.
func privateJoin(t *testing.T, ts *tests.TestService, svc Nip47Service, hub *db.App, hubSecret, memberPriv string, req nipcw.CreateCircleWalletRequest) json.RawMessage {
	t.Helper()
	params, err := json.Marshal(req)
	require.NoError(t, err)
	hubXOnly := strings.Repeat("ab", 32)
	nonce := tests.RandomHex32()
	notAfter := time.Now().Add(time.Minute).Unix()
	item := buildItem(t, "join", *hub.WalletPubkey, constants.NIP47MethodCreateCircleWallet, string(params),
		memberPriv, hubSecret, hubXOnly, nonce, notAfter)
	res, served := svc.ServePrivateItem(context.TODO(), ts.LNClient, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	return res.Result
}
