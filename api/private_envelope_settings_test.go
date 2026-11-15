package api

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests/mocks"
)

// Clients learn the envelope limits only from the transport announcement, which
// is published when Nostr starts, so changing them must reload Nostr: otherwise
// clients keep the old limits and the Hub refuses what they size to them.
func TestUpdateSettings_PrivateEnvelopeLimits_RepublishesTheAnnouncement(t *testing.T) {
	cfg := mocks.NewMockConfig(t)
	svc := mocks.NewMockService(t)
	cfg.EXPECT().SetPrivateEnvelopeLimits(mock.Anything).Return(nil).Once()
	svc.EXPECT().ReloadNostr().Return(nil).Once()

	theAPI := &api{svc: svc, cfg: cfg}
	require.NoError(t, theAPI.UpdateSettings(&UpdateSettingsRequest{
		PrivateEnvelope: &PrivateEnvelopeInfo{MaxBytes: 57344, MaxItems: 32, MaxConsolidateSources: 2, PadBucketBytes: 8192, MaxVerifyBudget: 200},
	}))
}
