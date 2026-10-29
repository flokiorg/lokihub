package nip47

// NIP-CW §Recovering a Lost Connection: once due, the old key is refused at once and cannot cancel the rotation.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func TestRotation_OldKeyRefusedOnceDue(t *testing.T) {
	r := newRotationCircle(t)
	newKey, _ := nipcw.NewConnectionKey()
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), time.Now()).Code)
	// Its completes_at has just come; the periodic job has not run.
	require.NoError(t, r.svc.DB.Model(&db.CircleRotation{}).Where("wallet_app_id = ?", r.w.app.ID).
		Update("completes_at", time.Now().Unix()).Error)

	clientPub, err := nostr.GetPublicKey(r.w.secret)
	require.NoError(t, err)
	raw, _ := json.Marshal(map[string]interface{}{"method": models.GET_BALANCE_METHOD})
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, r.w.walletPubkey, r.w.secret)
	require.NoError(t, err)
	content, err := c.Encrypt(string(raw))
	require.NoError(t, err)
	ev := &nostr.Event{Kind: models.REQUEST_KIND, PubKey: clientPub, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2}, {"p", r.w.walletPubkey}}, Content: content}
	require.NoError(t, ev.Sign(r.w.secret))
	pool := tests.NewMockSimplePool()
	r.nip47svc.HandleEvent(context.TODO(), pool, ev, r.svc.LNClient)

	assert.Empty(t, pool.PublishedEvents, "the old key is not served once the rotation is due")
	var rotation db.CircleRotation
	require.NoError(t, r.svc.DB.First(&rotation).Error)
	assert.Equal(t, db.CircleRotationCompleted, rotation.State, "the old key's request cannot cancel a due rotation")
	assert.Equal(t, newKey.Pubkey(), r.wallet(t).AppPubkey)
}
