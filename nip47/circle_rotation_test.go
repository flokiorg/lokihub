package nip47

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// rotationCircle is a circle serving rotation with a 100-second delay and one
// member's wallet.
type rotationCircle struct {
	*freezeCircle
	memberPriv string
	w          *memberWallet
	cfg        *db.CircleHubConfig
}

func newRotationCircle(t *testing.T) *rotationCircle {
	t.Helper()
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	require.NoError(t, c.svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", c.hub.ID).
		Updates(map[string]interface{}{"rotation_enabled": true, "rotation_delay_secs": 100}).Error)
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	c.add(memberPub)
	w := c.join(member)
	cfg, err := c.svc.AppsService.GetCircleHubConfig(c.hub.ID)
	require.NoError(t, err)
	return &rotationCircle{freezeCircle: c, memberPriv: member, w: w, cfg: cfg}
}

func (r *rotationCircle) request(t *testing.T, signer string, action nipcw.MemberAction, conn string) *nip01.Event {
	t.Helper()
	ev, err := nipcw.NewMemberRequest(signer, *r.hub.WalletPubkey, action, r.w.walletPubkey, conn)
	require.NoError(t, err)
	return ev
}

func (r *rotationCircle) submit(t *testing.T, ev *nip01.Event, now time.Time) circlelife.MemberRequestResult {
	t.Helper()
	res, err := circlelife.ApplyMemberRequest(r.svc.DB, r.hub, r.cfg, ev, now)
	require.NoError(t, err)
	return res
}

func (r *rotationCircle) wallet(t *testing.T) db.App {
	t.Helper()
	var w db.App
	require.NoError(t, r.svc.DB.First(&w, r.w.app.ID).Error)
	return w
}

// NIP-CW §Recovering a Lost Connection, the member request's checks in order.
func TestMemberRequest_ChecksInOrder(t *testing.T) {
	r := newRotationCircle(t)
	now := time.Now()
	newKey, _ := nipcw.NewConnectionKey()

	// 2: not signed by anyone it claims (tampered).
	bad := r.request(t, r.memberPriv, nipcw.ActionFreeze, "")
	bad.Sig = strings.Repeat("00", 64)
	assert.Equal(t, nipcw.ErrUnauthorized, r.submit(t, bad, now).Code)

	// 3: single use; a request refused later stays consumed.
	other := nostr.GeneratePrivateKey()
	stranger := r.request(t, other, nipcw.ActionFreeze, "")
	assert.Equal(t, nipcw.ErrUnauthorized, r.submit(t, stranger, now).Code, "5: not the join's pubkey")
	assert.Equal(t, nipcw.ErrRequestReused, r.submit(t, stranger, now).Code)

	// 4: a wallet the Hub does not hold.
	unknown, err := nipcw.NewMemberRequest(r.memberPriv, *r.hub.WalletPubkey, nipcw.ActionFreeze, strings.Repeat("ab", 32), "")
	require.NoError(t, err)
	assert.Equal(t, nipcw.ErrNotFound, r.submit(t, unknown, now).Code)

	// 7: a rotate to a key in use on the node.
	inUse := r.request(t, r.memberPriv, nipcw.ActionRotate, r.hub.AppPubkey)
	assert.Equal(t, nipcw.ErrRestricted, r.submit(t, inUse, now).Code)

	ok := r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), now)
	require.Empty(t, ok.Code, ok.Message)
	require.NotNil(t, ok.CompletesAt)
	assert.Equal(t, now.Unix()+100, *ok.CompletesAt)
	require.Len(t, ok.Notices, 1)
	assert.Equal(t, circlelife.NoticeRotationPending, ok.Notices[0].Type)

	another, _ := nipcw.NewConnectionKey()
	assert.Equal(t, nipcw.ErrRotationPending, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, another.Pubkey()), now).Code)

	// 1: a Hub that serves no rotation, with no wallet issued with a delay,
	// refuses everything first.
	r.cfg.RotationEnabled = false
	require.NoError(t, r.svc.DB.Model(&db.App{}).Where("id = ?", r.w.app.ID).Update("circle_rotation_delay_secs", nil).Error)
	assert.Equal(t, nipcw.ErrNotImplemented, r.submit(t, unknown, now).Code, "before the wallet is even looked up")

	// U-18.8: member request ids are kept for 24 hours and a minute.
	var proof db.CircleWalletIdentityProof
	require.NoError(t, r.svc.DB.Where("event_id = ?", stranger.ID).First(&proof).Error)
	assert.Equal(t, db.CircleProofMember, proof.Kind)
	assert.Equal(t, int64(stranger.CreatedAt)+86460, proof.RetainUntil) //nolint:gosec // a unix time
}

// A wallet issued without a rotation delay cannot be rotated.
func TestMemberRequest_WalletWithoutDelay(t *testing.T) {
	r := newRotationCircle(t)
	require.NoError(t, r.svc.DB.Model(&db.App{}).Where("id = ?", r.w.app.ID).Update("circle_rotation_delay_secs", nil).Error)
	assert.Equal(t, nipcw.ErrNotImplemented, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionFreeze, ""), time.Now()).Code)
}

// U-18.9, U-18.4: a frozen wallet's key is refused everything with FROZEN, and
// its requests cancel nothing; freezing is idempotent; unfreeze lifts it.
func TestFrozenWallet(t *testing.T) {
	r := newRotationCircle(t)
	now := time.Now()
	freeze := r.submit(t, r.request(t, r.memberPriv, nipcw.ActionFreeze, ""), now)
	require.Empty(t, freeze.Code)
	require.Len(t, freeze.Notices, 1)
	assert.Equal(t, circlelife.NoticeWalletFrozen, freeze.Notices[0].Type)
	time.Sleep(1100 * time.Millisecond) // a new created_at, so a new request id
	again := r.submit(t, r.request(t, r.memberPriv, nipcw.ActionFreeze, ""), now)
	assert.Empty(t, again.Code)
	assert.Empty(t, again.Notices, "a freeze of a frozen wallet changes nothing")

	newKey, _ := nipcw.NewConnectionKey()
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), now).Code)

	for _, method := range []string{models.GET_BALANCE_METHOD, models.GET_INFO_METHOD, models.PAY_INVOICE_METHOD, constants.NIP47MethodDeleteCircleWallet} {
		resp := r.w.call(method, map[string]interface{}{"invoice": tests.MockInvoice})
		assert.Equal(t, nipcw.ErrFrozen, errorCode(resp), method)
	}
	var pending int64
	require.NoError(t, r.svc.DB.Model(&db.CircleRotation{}).Where("state = ?", db.CircleRotationPending).Count(&pending).Error)
	assert.EqualValues(t, 1, pending, "a frozen key's request cancels nothing")

	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionUnfreeze, ""), now).Code)
	assert.Nil(t, r.w.call(models.GET_BALANCE_METHOD, nil).Error)
}

// U-18.3: any request by the current (not frozen) key cancels a pending
// rotation, and is then served.
func TestRotation_CancelledByTheOldKey(t *testing.T) {
	r := newRotationCircle(t)
	newKey, _ := nipcw.NewConnectionKey()
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), time.Now()).Code)

	assert.Nil(t, r.w.call(models.GET_BALANCE_METHOD, nil).Error, "served as usual")
	var rotation db.CircleRotation
	require.NoError(t, r.svc.DB.First(&rotation).Error)
	assert.Equal(t, db.CircleRotationCancelled, rotation.State)
	assert.NotNil(t, rotation.EndedAt)
}

// U-18.5, U-18.6: at completes_at the new key replaces the old one (same
// wallet, unfrozen), or the rotation is cancelled when the key got used.
func TestRotation_Completion(t *testing.T) {
	r := newRotationCircle(t)
	now := time.Now()
	newKey, _ := nipcw.NewConnectionKey()
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), now).Code)
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionFreeze, ""), now).Code)

	notices, swapped, err := circlelife.CompleteDueRotations(r.svc.DB, now.Add(99*time.Second), nil)
	require.NoError(t, err)
	assert.Empty(t, notices, "not due yet")
	assert.Empty(t, swapped)

	notices, swapped, err = circlelife.CompleteDueRotations(r.svc.DB, now.Add(100*time.Second), nil)
	require.NoError(t, err)
	assert.Equal(t, []uint{r.w.app.ID}, swapped)
	require.Len(t, notices, 1)
	assert.Equal(t, circlelife.NoticeRotationCompleted, notices[0].Type)
	w := r.wallet(t)
	assert.Equal(t, newKey.Pubkey(), w.AppPubkey)
	assert.False(t, w.CircleFrozen, "a completed rotation ends the freeze")

	// The new key is served, the old one no longer.
	served := sendOverStandardTransport(t, r.svc, r.nip47svc, r.w.walletPubkey, newKey.Secret(),
		map[string]interface{}{"method": models.GET_BALANCE_METHOD})
	assert.Nil(t, served.Error)

	// A second rotation whose key another app took by then is cancelled.
	third, _ := nipcw.NewConnectionKey()
	later := now.Add(time.Hour)
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, third.Pubkey()), later).Code)
	require.NoError(t, r.svc.DB.Create(&db.App{Name: "taker", AppPubkey: third.Pubkey(), Kind: db.AppKindStandard}).Error)
	notices, swapped, err = circlelife.CompleteDueRotations(r.svc.DB, later.Add(100*time.Second), nil)
	require.NoError(t, err)
	assert.Empty(t, swapped)
	require.Len(t, notices, 1)
	assert.Equal(t, circlelife.NoticeRotationCancelled, notices[0].Type)
	assert.Equal(t, newKey.Pubkey(), r.wallet(t).AppPubkey)
}

// A join cannot take a key a pending rotation names (NIP-CW step 9).
func TestJoin_KeyPendingInARotationRestricted(t *testing.T) {
	r := newRotationCircle(t)
	newKey, _ := nipcw.NewConnectionKey()
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), time.Now()).Code)

	other := nostr.GeneratePrivateKey()
	otherPub, _ := nostr.GetPublicKey(other)
	r.add(otherPub)
	prepared, err := nipcw.CreateCircleWalletParams{Credential: nipcw.BySigning(other), Connection: newKey,
		MaxAmountMillis: 100_000, MaxReceiveMillis: 100_000, Expiry: time.Hour}.Prepare(*r.hub.WalletPubkey, time.Now())
	require.NoError(t, err)
	params, err := json.Marshal(prepared.Request)
	require.NoError(t, err)
	nonce := tests.RandomHex32()
	notAfter := time.Now().Add(time.Minute).Unix()
	hubXOnly := strings.Repeat("ab", 32)
	item := buildItem(t, "join", *r.hub.WalletPubkey, constants.NIP47MethodCreateCircleWallet, string(params), other, r.hubSecret, hubXOnly, nonce, notAfter)
	res, served := r.nip47svc.ServePrivateItem(t.Context(), r.svc.LNClient, item, PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code)
}

// U-14.RD1: a join lookup by the member naming the new key reads the rotation:
// pending until completes_at, then done with the details re-encrypted for the
// new key, writing nothing.
func TestJoinLookup_ReadsTheRotation(t *testing.T) {
	r := newRotationCircle(t)
	newKey, _ := nipcw.NewConnectionKey()
	ok := r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), time.Now().Add(-50*time.Second))
	require.Empty(t, ok.Code)

	lookup := func() (*nipcash.CashLookupResult, *string) {
		params := fmt.Sprintf(`{"connection_pubkey":%q,"deadline":%d}`, newKey.Pubkey(), time.Now().Add(time.Minute).Unix())
		nonce := tests.RandomHex32()
		notAfter := time.Now().Add(time.Minute).Unix()
		hubXOnly := strings.Repeat("ab", 32)
		item := buildItem(t, "l", *r.hub.WalletPubkey, constants.NIP47MethodCashLookup, params, r.memberPriv, r.hubSecret, hubXOnly, nonce, notAfter)
		res, served := r.nip47svc.ServePrivateItem(t.Context(), r.svc.LNClient, item, PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
		require.True(t, served)
		if res.Error != nil {
			return nil, &res.Error.Code
		}
		var out nipcash.CashLookupResult
		require.NoError(t, json.Unmarshal(res.Result, &out))
		return &out, nil
	}
	pending, code := lookup()
	require.Nil(t, code)
	assert.Equal(t, nipcash.LookupPending, pending.Status)
	assert.Equal(t, *ok.CompletesAt, pending.RetryAfter)

	// Due, the job not run: done, and nothing written.
	require.NoError(t, r.svc.DB.Model(&db.CircleRotation{}).Where("1 = 1").Update("completes_at", time.Now().Unix()-1).Error)
	before := r.wallet(t).AppPubkey
	done, code := lookup()
	require.Nil(t, code)
	require.Equal(t, nipcash.LookupDone, done.Status)
	assert.Equal(t, before, r.wallet(t).AppPubkey, "the lookup changes no wallet")
	var rotation db.CircleRotation
	require.NoError(t, r.svc.DB.First(&rotation).Error)
	assert.Equal(t, db.CircleRotationPending, rotation.State, "nor the rotation")

	var body struct {
		EncryptedDetails string `json:"encrypted_details"`
	}
	require.NoError(t, json.Unmarshal(done.Result, &body))
	details := decryptDetails(t, r.memberPriv, *r.hub.WalletPubkey, body.EncryptedDetails)
	assert.Equal(t, newKey.Pubkey(), details.ConnectionPubkey)
	assert.Equal(t, r.w.walletPubkey, details.WalletPubkey)
	require.NotNil(t, details.RotationDelaySecs)
	assert.Equal(t, uint64(100), *details.RotationDelaySecs)
}

// decryptDetails opens a join's encrypted_details as the member does.
func decryptDetails(t *testing.T, memberPriv, hubPub, encrypted string) nipcw.CircleWalletDetails {
	t.Helper()
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, hubPub, memberPriv)
	require.NoError(t, err)
	plain, err := c.Decrypt(encrypted)
	require.NoError(t, err)
	var d nipcw.CircleWalletDetails
	require.NoError(t, json.Unmarshal([]byte(plain), &d))
	return d
}

// NIP-CW §Recovering a Lost Connection: each wallet's delay is fixed for its
// life, so a Hub that stops offering rotation still serves the member requests
// of the wallets it issued with one; a wallet issued after has none and is
// refused at step 6.
func TestMemberRequest_RotationTurnedOffKeepsIssuedWalletsRecoverable(t *testing.T) {
	r := newRotationCircle(t)
	require.NoError(t, r.svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", r.hub.ID).Update("rotation_enabled", false).Error)
	cfg, err := r.svc.AppsService.GetCircleHubConfig(r.hub.ID)
	require.NoError(t, err)
	r.cfg = cfg

	newKey, _ := nipcw.NewConnectionKey()
	res := r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), time.Now())
	require.Empty(t, res.Code, res.Message)
	assert.Equal(t, int64(100), *r.wallet(t).CircleRotationDelaySecs, "the delay it was issued with")

	late := nostr.GeneratePrivateKey()
	latePub, _ := nostr.GetPublicKey(late)
	r.add(latePub)
	lateWallet := r.join(late)
	var stored db.App
	require.NoError(t, r.svc.DB.First(&stored, lateWallet.app.ID).Error)
	assert.Nil(t, stored.CircleRotationDelaySecs, "issued after: no delay")
	ev, err := nipcw.NewMemberRequest(late, *r.hub.WalletPubkey, nipcw.ActionFreeze, lateWallet.walletPubkey, "")
	require.NoError(t, err)
	assert.Equal(t, nipcw.ErrNotImplemented, r.submit(t, ev, time.Now()).Code)
}
