package notifications

import (
	"context"
	"testing"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func alwaysOkTransport() *stubTransport {
	return &stubTransport{
		transportType: TransportTypePush,
		sendFunc:      func(context.Context, string, string, interface{}) error { return nil },
	}
}

func TestNotifyWebhookRegistered_SendsToTheGivenWebhookOnly(t *testing.T) {
	store := newFakeOpeningStore()
	transport := alwaysOkTransport()
	svc := NewNotificationService(store, nil, newFakeNotifStore())
	svc.AddTransport(transport)

	webhook := &core.Lsps5Webhook{ClientNodeId: "peer1", AppName: "wallet-a", Url: "https://a.example", Transport: "push"}

	notified := svc.NotifyWebhookRegistered("peer1", webhook)
	assert.True(t, notified)
	assert.EqualValues(t, 1, transport.callCount())
}

func TestNotifyExpirySoon_DispatchesToAllRegisteredWebhooks(t *testing.T) {
	store := newFakeOpeningStore()
	store.webhooks["peer1"] = []*core.Lsps5Webhook{
		{ClientNodeId: "peer1", AppName: "a", Url: "https://a.example", Transport: "push"},
		{ClientNodeId: "peer1", AppName: "b", Url: "https://b.example", Transport: "push"},
	}
	transport := alwaysOkTransport()
	svc := NewNotificationService(store, nil, newFakeNotifStore())
	svc.AddTransport(transport)

	notified, err := svc.NotifyExpirySoon("peer1", 42, nil, "hash1")
	require.NoError(t, err)
	assert.True(t, notified)
	assert.EqualValues(t, 2, transport.callCount())
}

func TestNotifyExpirySoon_NoWebhooksReturnsFalse(t *testing.T) {
	store := newFakeOpeningStore()
	svc := NewNotificationService(store, nil, newFakeNotifStore())

	notified, err := svc.NotifyExpirySoon("unknown-peer", 42, nil, "hash1")
	require.NoError(t, err)
	assert.False(t, notified)
}

func TestNotifyExpirySoon_StoreErrorPropagates(t *testing.T) {
	store := newFakeOpeningStore()
	store.getWebhooksErr = assert.AnError
	svc := NewNotificationService(store, nil, newFakeNotifStore())

	notified, err := svc.NotifyExpirySoon("peer1", 42, nil, "hash1")
	assert.ErrorIs(t, err, assert.AnError)
	assert.False(t, notified)
}

func TestNotifyLiquidityManagementRequest_DispatchesToAllRegisteredWebhooks(t *testing.T) {
	store := newFakeOpeningStore()
	store.webhooks["peer1"] = []*core.Lsps5Webhook{
		{ClientNodeId: "peer1", AppName: "a", Url: "https://a.example", Transport: "push"},
	}
	transport := alwaysOkTransport()
	svc := NewNotificationService(store, nil, newFakeNotifStore())
	svc.AddTransport(transport)

	notified, err := svc.NotifyLiquidityManagementRequest("peer1", nil)
	require.NoError(t, err)
	assert.True(t, notified)
	assert.EqualValues(t, 1, transport.callCount())
}

func TestNotifyOrderStateChanged_DispatchesWithCorrectParams(t *testing.T) {
	store := newFakeOpeningStore()
	store.webhooks["peer1"] = []*core.Lsps5Webhook{
		{ClientNodeId: "peer1", AppName: "a", Url: "https://a.example", Transport: "push"},
	}

	var capturedParams interface{}
	transport := &stubTransport{
		transportType: TransportTypePush,
		sendFunc: func(_ context.Context, _ string, _ string, params interface{}) error {
			capturedParams = params
			return nil
		},
	}
	svc := NewNotificationService(store, nil, newFakeNotifStore())
	svc.AddTransport(transport)

	notified, err := svc.NotifyOrderStateChanged("peer1", "order-123", "COMPLETED", "txid:0", "")
	require.NoError(t, err)
	assert.True(t, notified)

	params, ok := capturedParams.(*OrderStateChangedParams)
	require.True(t, ok)
	assert.Equal(t, "order-123", params.OrderId)
	assert.Equal(t, "COMPLETED", params.State)
	assert.Equal(t, "txid:0", params.ChannelPoint)
}

func TestNotifyOrderStateChanged_StoreErrorPropagates(t *testing.T) {
	store := newFakeOpeningStore()
	store.getWebhooksErr = assert.AnError
	svc := NewNotificationService(store, nil, newFakeNotifStore())

	notified, err := svc.NotifyOrderStateChanged("peer1", "order-123", "COMPLETED", "", "")
	assert.ErrorIs(t, err, assert.AnError)
	assert.False(t, notified)
}

func TestNotifyOnionMessageIncoming_DispatchesToAllRegisteredWebhooks(t *testing.T) {
	store := newFakeOpeningStore()
	store.webhooks["peer1"] = []*core.Lsps5Webhook{
		{ClientNodeId: "peer1", AppName: "a", Url: "https://a.example", Transport: "push"},
		{ClientNodeId: "peer1", AppName: "b", Url: "https://b.example", Transport: "push"},
	}
	transport := alwaysOkTransport()
	svc := NewNotificationService(store, nil, newFakeNotifStore())
	svc.AddTransport(transport)

	notified, err := svc.NotifyOnionMessageIncoming("peer1", nil)
	require.NoError(t, err)
	assert.True(t, notified)
	assert.EqualValues(t, 2, transport.callCount())
}
