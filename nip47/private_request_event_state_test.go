package nip47

// A private item's request-event row records how its handler ended, as the standard
// path's publishResponse does, instead of staying "executing" forever (#138).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func requestEventState(t *testing.T, gormDB *gorm.DB, nonce, itemID string) string {
	t.Helper()
	var row db.RequestEvent
	require.NoError(t, gormDB.Where("nostr_id = ?", nonce+":"+itemID).First(&row).Error)
	return row.State
}

func TestServePrivateItem_AnsweredItemIsMarkedExecuted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("ef", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	item := buildItem(t, "s1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		recipientPriv, connPriv, hubXOnly, nonce, notAfter)

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.Nil(t, result.Error)
	assert.Equal(t, db.REQUEST_EVENT_STATE_HANDLER_EXECUTED, requestEventState(t, svc.DB, nonce, "s1"))
}

// A refusal the handler answered is still an executed handler, exactly as on the
// standard path, where any published response marks the row executed.
func TestServePrivateItem_HandlerRefusalIsMarkedExecuted(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	h := newJoinHub(t) // nobody is a member

	nonce := strings.Repeat("02", 32)
	res, served := h.serve(t, "j1", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, nonce)
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code)
	assert.Equal(t, db.REQUEST_EVENT_STATE_HANDLER_EXECUTED, requestEventState(t, h.ts.DB, nonce, "j1"))
}
