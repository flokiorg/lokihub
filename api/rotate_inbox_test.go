package api

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/service"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// A rotation that stands is answered with its new inbox, and its failed
// announcement as information; one that did not happen is an error.
func TestRotatePrivateTransportInbox_AnswersTheNewInbox(t *testing.T) {
	svc := mocks.NewMockService(t)
	theAPI := &api{svc: svc}

	svc.On("RotatePrivateTransportInbox").Return("aa", nil).Once()
	resp, err := theAPI.RotatePrivateTransportInbox()
	require.NoError(t, err)
	assert.Equal(t, &RotateInboxResponse{InboxPubkey: "aa"}, resp)

	svc.On("RotatePrivateTransportInbox").Return("bb", errors.New("no relay accepted it")).Once()
	resp, err = theAPI.RotatePrivateTransportInbox()
	require.NoError(t, err)
	assert.Equal(t, "bb", resp.InboxPubkey)
	assert.Contains(t, resp.AnnounceError, "no relay accepted it")

	svc.On("RotatePrivateTransportInbox").Return("", service.ErrPrivateTransportNotRunning).Once()
	_, err = theAPI.RotatePrivateTransportInbox()
	assert.ErrorIs(t, err, service.ErrPrivateTransportNotRunning)
}
