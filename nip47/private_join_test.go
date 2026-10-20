package nip47

// Private Join (NIP-CW): create_circle_wallet served over the private transport,
// driven through ServePrivateItem.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

type joinAllowlistCache struct{ allowed map[string]bool }

func (c *joinAllowlistCache) IsAuthorized(_ context.Context, pk string, _ *db.CircleIdentity, _ *gorm.DB) (bool, error) {
	return c.allowed[pk], nil
}

type joinHub struct {
	app      *db.App
	connPriv string // the shared circlehub connection secret every prospective member holds
	svc      *nip47Service
	ts       *tests.TestService
	hubXOnly string
}

func newJoinHub(t *testing.T, allowed ...string) *joinHub {
	t.Helper()
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(ts.Remove)
	app, secret, err := ts.AppsService.CreateCircleHub("Circle Hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "Circle Hub", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 7200, PerWalletMaxMloki: 1_000_000_000})
	require.NoError(t, err)
	tests.FundApp(ts, app.ID, 100_000_000, "fundtx")
	set := map[string]bool{}
	for _, p := range allowed {
		set[p] = true
	}
	return &joinHub{
		app: app, connPriv: secret, ts: ts, hubXOnly: strings.Repeat("ab", 32),
		svc: NewNip47Service(ts.DB, ts.Cfg, ts.Keys, ts.EventPublisher, &joinAllowlistCache{allowed: set}),
	}
}

// joinParams builds a join exactly as nmilat's client does, with a fresh
// member-generated connection key (NIP-CW §Request).
func joinParams(t *testing.T, memberPriv, hubWalletPubkey string, maxAmount uint64) string {
	t.Helper()
	connection, err := nipcw.NewConnectionKey()
	require.NoError(t, err)
	req, err := nipcw.CreateCircleWalletParams{
		Credential:      nipcw.BySigning(memberPriv),
		Connection:      connection,
		MaxAmountMillis: maxAmount,
		Expiry:          time.Hour,
	}.Request(hubWalletPubkey)
	require.NoError(t, err)
	b, err := json.Marshal(req)
	require.NoError(t, err)
	return string(b)
}

func (h *joinHub) serve(t *testing.T, id, params, itemSignerPriv, nonce string) (transport.Result, bool) {
	t.Helper()
	notAfter := time.Now().Add(time.Minute).Unix()
	item := buildItem(t, id, *h.app.WalletPubkey, constants.NIP47MethodCreateCircleWallet, params,
		itemSignerPriv, h.connPriv, h.hubXOnly, nonce, notAfter)
	return h.svc.ServePrivateItem(context.TODO(), h.ts.LNClient, item,
		PrivateItemBinding{HubXOnly: h.hubXOnly, Nonce: nonce, NotAfter: notAfter})
}

func (h *joinHub) children(t *testing.T) []db.App {
	var out []db.App
	h.ts.DB.Where("parent_app_id = ? AND kind = ?", h.app.ID, db.AppKindCircleWallet).Find(&out)
	return out
}

func TestPrivateJoin_HonestJoinIsServed(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	h := newJoinHub(t, memberPub)

	res, served := h.serve(t, "j1", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, strings.Repeat("01", 32))
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var body map[string]any
	require.NoError(t, json.Unmarshal(res.Result, &body))
	assert.Len(t, body, 1, "only encrypted_details may travel")
	assert.NotEmpty(t, body["encrypted_details"])
}

func TestPrivateJoin_BatchEvaluatedPerItem(t *testing.T) {
	a, b, c := nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey()
	aPub, _ := nostr.GetPublicKey(a)
	cPub, _ := nostr.GetPublicKey(c)
	h := newJoinHub(t, aPub, cPub) // b is NOT a member
	nonce := strings.Repeat("02", 32)
	ra, sa := h.serve(t, "a", joinParams(t, a, *h.app.WalletPubkey, 10_000), a, nonce)
	rb, sb := h.serve(t, "b", joinParams(t, b, *h.app.WalletPubkey, 10_000), b, nonce)
	rc, sc := h.serve(t, "c", joinParams(t, c, *h.app.WalletPubkey, 10_000), c, nonce)
	require.True(t, sa && sb && sc)
	assert.Nil(t, ra.Error)
	require.NotNil(t, rb.Error)
	assert.Nil(t, rc.Error)
	assert.Len(t, h.children(t), 2)
}

// NIP-CW §Private Join: the item proof's signer MUST equal the request's pubkey. An
// envelope assembler keeps the member's own 23199 proof, raises max_amount and signs
// the 23192 with its own key; the join must be refused and no wallet issued.
func TestPrivateJoin_ItemProofSignedByAnotherKey_Refused(t *testing.T) {
	member, assembler := nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	h := newJoinHub(t, memberPub)

	var req nipcw.CreateCircleWalletRequest
	require.NoError(t, json.Unmarshal([]byte(joinParams(t, member, *h.app.WalletPubkey, 10_000)), &req))
	req.MaxAmount = 50_000_000
	tampered, err := json.Marshal(req)
	require.NoError(t, err)

	res, served := h.serve(t, "x", string(tampered), assembler, strings.Repeat("06", 32))
	require.True(t, served, "the sender holds the hub connection, so the refusal is an error")
	require.NotNil(t, res.Error, "a join signed by another key must not be served")
	assert.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)
	assert.Nil(t, res.Result)
	assert.Empty(t, h.children(t), "no wallet is issued")

	// The member's own, correctly signed join still goes through.
	ok, served := h.serve(t, "m", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, strings.Repeat("07", 32))
	require.True(t, served)
	require.Nil(t, ok.Error, "%+v", ok.Error)
	assert.Len(t, h.children(t), 1)
}

// The hub failing to read its own records is temporary: the join gets
// NOT_READY, which NIP-CW clients retry, not a definite refusal, and the same
// member joins once the database recovers.
func TestPrivateJoin_ClaimsLookupFailure_IsNotReady(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	h := newJoinHub(t, memberPub)

	failing := true
	require.NoError(t, h.ts.DB.Callback().Query().Before("gorm:query").Register("test:fail_claims", func(tx *gorm.DB) {
		if failing && tx.Statement.Table == "cash_wallet_claims" {
			_ = tx.AddError(errors.New("database is locked"))
		}
	}))

	res, served := h.serve(t, "j1", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, strings.Repeat("07", 32))
	require.True(t, served, "the sender proved it holds the hub connection, so it gets an answer")
	require.NotNil(t, res.Error)
	assert.Equal(t, nipcw.ErrNotReady, res.Error.Code)
	assert.Empty(t, h.children(t))

	failing = false
	res, served = h.serve(t, "j2", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, strings.Repeat("08", 32))
	require.True(t, served)
	assert.Nil(t, res.Error, "%+v", res.Error)
	assert.Len(t, h.children(t), 1)
}

// One envelope carrying the same member's join twice issues one wallet: the
// second item is refused with WALLET_EXISTS, so batching is no way around
// one wallet per member.
func TestPrivateJoin_SameIdentityTwiceInOneBatch_OneWallet(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	h := newJoinHub(t, memberPub)
	nonce := strings.Repeat("0a", 32)

	first, served := h.serve(t, "a", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, nonce)
	require.True(t, served)
	require.Nil(t, first.Error, "%+v", first.Error)
	second, served := h.serve(t, "b", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, nonce)
	require.True(t, served)
	require.NotNil(t, second.Error)
	assert.Equal(t, nipcw.ErrWalletExists, second.Error.Code)
	assert.Len(t, h.children(t), 1)
}
