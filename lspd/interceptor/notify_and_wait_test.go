package interceptor

import (
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newNotifyTestInterceptor(cfg *config.NodeConfig, client *MockLightningClient, notifier *MockNotifier) *Interceptor {
	return &Interceptor{
		config:              config.NewNodeConfigRef(cfg),
		client:              client,
		notificationService: notifier,
	}
}

func TestNotifyAndWait_NotifyErrorResumesHtlc(t *testing.T) {
	notifier := new(MockNotifier)
	notifier.On("Notify", mock.Anything, mock.Anything, mock.Anything).Return(false, assert.AnError)
	client := new(MockLightningClient)
	i := newNotifyTestInterceptor(&config.NodeConfig{}, client, notifier)

	result := i.notifyAndWait("hash", []byte{1}, false)
	require.NotNil(t, result)
	assert.Equal(t, core.INTERCEPT_RESUME, result.Action)
	client.AssertNotCalled(t, "WaitOnline", mock.Anything, mock.Anything)
}

func TestNotifyAndWait_ClientNotNotifiedResumesHtlc(t *testing.T) {
	notifier := new(MockNotifier)
	notifier.On("Notify", mock.Anything, mock.Anything, mock.Anything).Return(false, nil)
	client := new(MockLightningClient)
	i := newNotifyTestInterceptor(&config.NodeConfig{}, client, notifier)

	result := i.notifyAndWait("hash", []byte{1}, false)
	require.NotNil(t, result)
	assert.Equal(t, core.INTERCEPT_RESUME, result.Action)
}

func TestNotifyAndWait_WaitOnlineErrorResumesHtlc(t *testing.T) {
	notifier := new(MockNotifier)
	notifier.On("Notify", mock.Anything, mock.Anything, mock.Anything).Return(true, nil)
	client := new(MockLightningClient)
	client.On("WaitOnline", mock.Anything, mock.Anything).Return(assert.AnError)
	i := newNotifyTestInterceptor(&config.NodeConfig{NotificationTimeout: "10ms"}, client, notifier)

	result := i.notifyAndWait("hash", []byte{1}, false)
	require.NotNil(t, result)
	assert.Equal(t, core.INTERCEPT_RESUME, result.Action)
	client.AssertNotCalled(t, "WaitChannelActive", mock.Anything, mock.Anything)
}

func TestNotifyAndWait_UnregisteredWaitsForChannelActive(t *testing.T) {
	notifier := new(MockNotifier)
	notifier.On("Notify", mock.Anything, mock.Anything, mock.Anything).Return(true, nil)
	client := new(MockLightningClient)
	client.On("WaitOnline", mock.Anything, mock.Anything).Return(nil)
	client.On("WaitChannelActive", mock.Anything, mock.Anything).Return(nil)
	i := newNotifyTestInterceptor(&config.NodeConfig{NotificationTimeout: "10ms"}, client, notifier)

	result := i.notifyAndWait("hash", []byte{1}, false) // isRegistered=false -> channel partner
	assert.Nil(t, result, "nil result signals the caller should proceed with forwarding")
	client.AssertExpectations(t)
}

func TestNotifyAndWait_UnregisteredChannelNeverActiveResumesHtlc(t *testing.T) {
	notifier := new(MockNotifier)
	notifier.On("Notify", mock.Anything, mock.Anything, mock.Anything).Return(true, nil)
	client := new(MockLightningClient)
	client.On("WaitOnline", mock.Anything, mock.Anything).Return(nil)
	client.On("WaitChannelActive", mock.Anything, mock.Anything).Return(assert.AnError)
	i := newNotifyTestInterceptor(&config.NodeConfig{NotificationTimeout: "10ms"}, client, notifier)

	result := i.notifyAndWait("hash", []byte{1}, false)
	require.NotNil(t, result)
	assert.Equal(t, core.INTERCEPT_RESUME, result.Action)
}

func TestNotifyAndWait_RegisteredPaymentSkipsChannelActiveWait(t *testing.T) {
	notifier := new(MockNotifier)
	notifier.On("Notify", mock.Anything, mock.Anything, mock.Anything).Return(true, nil)
	client := new(MockLightningClient)
	client.On("WaitOnline", mock.Anything, mock.Anything).Return(nil)
	i := newNotifyTestInterceptor(&config.NodeConfig{NotificationTimeout: "10ms"}, client, notifier)

	result := i.notifyAndWait("hash", []byte{1}, true) // isRegistered=true -> LSPS2/webhook payment
	assert.Nil(t, result)
	client.AssertNotCalled(t, "WaitChannelActive", mock.Anything, mock.Anything)
}

func TestNotifyAndWait_InvalidTimeoutFallsBackToOneMinute(t *testing.T) {
	notifier := new(MockNotifier)
	notifier.On("Notify", mock.Anything, mock.Anything, mock.Anything).Return(true, nil)
	client := new(MockLightningClient)
	client.On("WaitOnline", mock.Anything, mock.MatchedBy(func(deadline time.Time) bool {
		// Roughly one minute out, not the empty/zero default duration.
		return time.Until(deadline) > 50*time.Second
	})).Return(nil)
	client.On("WaitChannelActive", mock.Anything, mock.Anything).Return(nil)
	i := newNotifyTestInterceptor(&config.NodeConfig{NotificationTimeout: ""}, client, notifier)

	result := i.notifyAndWait("hash", []byte{1}, false)
	assert.Nil(t, result)
	client.AssertExpectations(t)
}
