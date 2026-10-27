package nip47

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Freezing a removed member's circle wallet (#143). Membership is decided again
// on every payment or new invoice, so these drive real requests through the
// standard transport, the only one that serves wallet methods.

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
		Credential:      nipcw.BySigning(memberPriv),
		Connection:      connection,
		MaxAmountMillis: 5_000_000,
		Expiry:          time.Hour,
	}
	prepared, err := join.Prepare(*c.hub.WalletPubkey, time.Now())
	require.NoError(c.t, err)
	params := prepared.Request
	resp := sendOverStandardTransport(c.t, c.svc, c.nip47svc, *c.hub.WalletPubkey, c.hubSecret,
		map[string]interface{}{"method": constants.NIP47MethodCreateCircleWallet, "params": params})
	require.Nil(c.t, resp.Error, "%+v", resp.Error)
	raw, err := json.Marshal(resp.Result)
	require.NoError(c.t, err)
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

// gatedCalls are the methods that move money or open a way for it to arrive.
func gatedCalls() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		models.PAY_INVOICE_METHOD:       {"invoice": tests.MockInvoice},
		models.PAY_KEYSEND_METHOD:       {"amount": 1000, "pubkey": strings.Repeat("02", 1) + strings.Repeat("ab", 32)},
		models.MULTI_PAY_INVOICE_METHOD: {"invoices": []map[string]interface{}{{"invoice": tests.MockInvoice}}},
		models.MULTI_PAY_KEYSEND_METHOD: {"keysends": []map[string]interface{}{{"amount": 1000, "pubkey": strings.Repeat("02", 1) + strings.Repeat("ab", 32)}}},
		models.MAKE_INVOICE_METHOD:      {"amount": 1000},
		models.MAKE_HOLD_INVOICE_METHOD: {"amount": 1000, "payment_hash": tests.RandomHex32()},
	}
}

// readCalls show a member their wallet's state, so they keep working while
// it is frozen.
func readCalls() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		models.GET_BALANCE_METHOD:       nil,
		models.GET_INFO_METHOD:          nil,
		models.LIST_TRANSACTIONS_METHOD: {},
		models.LOOKUP_INVOICE_METHOD:    {"payment_hash": tests.MockPaymentHash},
	}
}

func errorCode(resp models.Response) string {
	if resp.Error == nil {
		return ""
	}
	return resp.Error.Code
}

var freezePolicies = []string{db.CirclePolicyAllowlist, db.CirclePolicyFollowSet}

// TP: a member still listed keeps full use of their wallet.
func TestCircleWalletFreeze_ListedMember_PaysAndInvoices(t *testing.T) {
	for _, policy := range freezePolicies {
		t.Run(policy, func(t *testing.T) {
			c := newFreezeCircle(t, policy)
			member := nostr.GeneratePrivateKey()
			memberPub, _ := nostr.GetPublicKey(member)
			c.add(memberPub)
			w := c.join(member)

			pay := w.call(models.PAY_INVOICE_METHOD, map[string]interface{}{"invoice": tests.MockInvoice})
			assert.Nil(t, pay.Error, "%+v", pay.Error)
			invoice := w.call(models.MAKE_INVOICE_METHOD, map[string]interface{}{"amount": 1000})
			assert.Nil(t, invoice.Error, "%+v", invoice.Error)
		})
	}
}

// FP closed: once removed, every method that moves money is refused, and the
// check asked about this hub's own identity.
func TestCircleWalletFreeze_RemovedMember_GatedMethodsRestricted(t *testing.T) {
	for _, policy := range freezePolicies {
		t.Run(policy, func(t *testing.T) {
			c := newFreezeCircle(t, policy)
			member := nostr.GeneratePrivateKey()
			memberPub, _ := nostr.GetPublicKey(member)
			c.add(memberPub)
			w := c.join(member)
			c.remove(memberPub)

			for method, params := range gatedCalls() {
				resp := w.call(method, params)
				assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp), "%s must be refused for a removed member: %+v", method, resp.Error)
			}
			c.membership.mu.Lock()
			for _, id := range c.membership.identities {
				assert.Equal(t, c.identity.ID, id, "membership is checked against this hub's identity")
			}
			c.membership.mu.Unlock()
		})
	}
}

// FN guard: a frozen wallet still shows its balance and history.
func TestCircleWalletFreeze_RemovedMember_ReadMethodsStillWork(t *testing.T) {
	for _, policy := range freezePolicies {
		t.Run(policy, func(t *testing.T) {
			c := newFreezeCircle(t, policy)
			member := nostr.GeneratePrivateKey()
			memberPub, _ := nostr.GetPublicKey(member)
			c.add(memberPub)
			w := c.join(member)
			c.remove(memberPub)

			for method, params := range readCalls() {
				resp := w.call(method, params)
				assert.NotEqual(t, constants.ERROR_RESTRICTED, errorCode(resp), "%s must keep working: %+v", method, resp.Error)
				assert.NotEqual(t, constants.ERROR_NOT_READY, errorCode(resp), "%s must not depend on membership: %+v", method, resp.Error)
			}
			balance := w.call(models.GET_BALANCE_METHOD, nil)
			require.Nil(t, balance.Error)
		})
	}
}

// FN guard: re-adding the member unfreezes the same wallet; nothing was stored.
func TestCircleWalletFreeze_ReaddedMember_Unfrozen(t *testing.T) {
	for _, policy := range freezePolicies {
		t.Run(policy, func(t *testing.T) {
			c := newFreezeCircle(t, policy)
			member := nostr.GeneratePrivateKey()
			memberPub, _ := nostr.GetPublicKey(member)
			c.add(memberPub)
			w := c.join(member)
			c.remove(memberPub)
			require.Equal(t, constants.ERROR_RESTRICTED, errorCode(w.call(models.MAKE_INVOICE_METHOD, map[string]interface{}{"amount": 1000})))

			c.add(memberPub)
			resp := w.call(models.MAKE_INVOICE_METHOD, map[string]interface{}{"amount": 1000})
			assert.Nil(t, resp.Error, "%+v", resp.Error)
			pay := w.call(models.PAY_INVOICE_METHOD, map[string]interface{}{"invoice": tests.MockInvoice})
			assert.Nil(t, pay.Error, "%+v", pay.Error)
		})
	}
}

// FP guard: membership that can't be decided is NOT_READY, never a payment.
func TestCircleWalletFreeze_MembershipUnresolved_NotReady(t *testing.T) {
	for _, policy := range freezePolicies {
		t.Run(policy, func(t *testing.T) {
			c := newFreezeCircle(t, policy)
			member := nostr.GeneratePrivateKey()
			memberPub, _ := nostr.GetPublicKey(member)
			c.add(memberPub)
			w := c.join(member)

			for _, err := range []error{constants.ErrCircleMembershipUnresolved, constants.ErrSocialCacheWarmingUp} {
				c.membership.mu.Lock()
				c.membership.err = err
				c.membership.mu.Unlock()
				resp := w.call(models.PAY_INVOICE_METHOD, map[string]interface{}{"invoice": tests.MockInvoice})
				assert.Equal(t, constants.ERROR_NOT_READY, errorCode(resp), "%v: %+v", err, resp.Error)
			}
			balance := w.call(models.GET_BALANCE_METHOD, nil)
			assert.Nil(t, balance.Error, "reading the balance doesn't need membership")
		})
	}
}

// The race: the member is removed after the join checked them but before it
// committed. The wallet is issued, and refused on its first payment.
func TestCircleWalletFreeze_RemovedDuringJoin_FrozenOnFirstUse(t *testing.T) {
	for _, policy := range freezePolicies {
		t.Run(policy, func(t *testing.T) {
			c := newFreezeCircle(t, policy)
			member := nostr.GeneratePrivateKey()
			memberPub, _ := nostr.GetPublicKey(member)
			c.add(memberPub)
			c.membership.mu.Lock()
			c.membership.revokeAfterNextCall = memberPub
			c.membership.mu.Unlock()
			w := c.join(member)

			resp := w.call(models.PAY_INVOICE_METHOD, map[string]interface{}{"invoice": tests.MockInvoice})
			assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp), "%+v", resp.Error)
		})
	}
}

// A circle wallet with no membership row has no member to check, so it is
// refused rather than trusted. The join always writes that row in the same
// transaction as the wallet, so this only guards against a broken database.
func TestCircleWalletFreeze_NoMembershipRow_Restricted(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	c.add(memberPub)
	w := c.join(member)
	require.NoError(t, c.svc.DB.Where("wallet_app_id = ?", w.app.ID).Delete(&db.CircleWalletMembership{}).Error)

	resp := w.call(models.PAY_INVOICE_METHOD, map[string]interface{}{"invoice": tests.MockInvoice})
	assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp), "%+v", resp.Error)
}

// The private transport never serves wallet methods, so it is no way around
// the freeze: a payment item naming a circle wallet is omitted.
func TestCircleWalletFreeze_PrivateTransportDoesNotServeWalletMethods(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	c.add(memberPub)
	w := c.join(member)
	c.remove(memberPub)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("09", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	for _, method := range []string{models.PAY_INVOICE_METHOD, models.MAKE_INVOICE_METHOD, models.PAY_KEYSEND_METHOD} {
		item := buildItem(t, "p1", w.walletPubkey, method, `{"invoice":"`+tests.MockInvoice+`"}`,
			member, w.secret, hubXOnly, nonce, notAfter)
		res, served := c.nip47svc.ServePrivateItem(context.TODO(), c.svc.LNClient, item,
			PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
		// I5: the holder of the connection learns the method is not served here.
		require.True(t, served, method)
		require.NotNil(t, res.Error, method)
		assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, res.Error.Code, "%s is not served over the private transport", method)
	}
}

// Decided, not an oversight: a hold invoice issued before the member was
// removed can still settle. It only brings money in, capped when it was made,
// and refusing would leave the payer's funds held until the HTLC times out.
func TestCircleWalletFreeze_RemovedMember_CanSettleHoldInvoiceIssuedBefore(t *testing.T) {
	for _, policy := range freezePolicies {
		t.Run(policy, func(t *testing.T) {
			c := newFreezeCircle(t, policy)
			member := nostr.GeneratePrivateKey()
			memberPub, _ := nostr.GetPublicKey(member)
			c.add(memberPub)
			w := c.join(member)

			preimage := tests.RandomHex32()
			preimageBytes, err := hex.DecodeString(preimage)
			require.NoError(t, err)
			hash := sha256.Sum256(preimageBytes)
			paymentHash := hex.EncodeToString(hash[:])
			hold := w.call(models.MAKE_HOLD_INVOICE_METHOD, map[string]interface{}{"amount": 1000, "payment_hash": paymentHash})
			require.Nil(t, hold.Error, "%+v", hold.Error)
			// The payer's HTLC arrives and is held.
			require.NoError(t, c.svc.DB.Model(&db.Transaction{}).
				Where("app_id = ? AND payment_hash = ?", w.app.ID, paymentHash).
				Update("state", constants.TRANSACTION_STATE_ACCEPTED).Error)

			c.remove(memberPub)

			settle := w.call(models.SETTLE_HOLD_INVOICE_METHOD, map[string]interface{}{"preimage": preimage})
			assert.Nil(t, settle.Error, "%+v", settle.Error)
			assert.Contains(t, c.svc.LNClient.(*tests.MockLn).SettleHoldPreimages(), preimage)
		})
	}
}
