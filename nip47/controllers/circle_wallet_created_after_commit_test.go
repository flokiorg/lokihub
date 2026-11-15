package controllers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// lookupOnCreate does what createAppConsumer does when nwc_app_created arrives:
// it looks the new app up over its own connection, right away.
type lookupOnCreate struct {
	events.EventPublisher
	db    *gorm.DB
	mu    sync.Mutex
	found []bool
}

func (p *lookupOnCreate) Publish(event *events.Event) {
	if event.Event == "nwc_app_created" {
		id, _ := event.Properties.(map[string]interface{})["id"].(uint)
		var count int64
		p.db.Model(&db.App{}).Where("id = ?", id).Count(&count)
		p.mu.Lock()
		p.found = append(p.found, count == 1)
		p.mu.Unlock()
	}
	p.EventPublisher.Publish(event)
}

// NIP-CW §Creating a Circle Wallet: the new wallet's relay subscription is set
// up from nwc_app_created, which looks the app up over another connection. If
// the event fires before the creating transaction commits, the lookup misses,
// the subscription is never made, and the wallet never receives a request.
func TestCreateCircleWallet_CreatedEventOnlyAfterCommit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	publisher := &lookupOnCreate{EventPublisher: svc.EventPublisher, db: svc.DB}
	appsService := apps.NewAppsService(svc.DB, publisher, svc.Keys, svc.Cfg)
	controller := NewNip47Controller(svc.LNClient, svc.DB, publisher,
		permissions.NewPermissionsService(svc.DB, publisher), transactions.NewTransactionsService(svc.DB, publisher),
		appsService, svc.Keys, &mockSocialCache{authorized: true}, NewRateLimiter(), NewRateLimiter(), NewRateLimiter(), svc.Cfg,
		apps.NewIdentityAuthorityManager(svc.DB))

	req := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, 100_000, 3600)), req))
	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var response *models.Response
	controller.testJoin(t, context.TODO(), req, ev.ID, provider, func(r *models.Response, _ nostr.Tags) {
		response = r
	})
	require.NotNil(t, response)
	require.Nil(t, response.Error, "%+v", response.Error)

	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	require.Len(t, publisher.found, 1, "nwc_app_created is published once for the new wallet")
	assert.True(t, publisher.found[0], "when nwc_app_created fires, the new wallet must already be visible to other connections")
}
