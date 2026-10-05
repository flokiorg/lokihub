package service

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// This file covers PublishNodeProfile, reusing the fixtures
// (announcingLN/newAnnouncingLN, newOKRelay) that
// private_announcement_publish_test.go already built for the structurally
// identical announcement-publish path — same LN-backend and relay failure
// modes apply here, just against GetGeneralRelayUrls instead of
// GetRelayUrls.

func TestPublishNodeProfile_RequiresLNClient(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	svc := &service{cfg: ts.Cfg, keys: ts.Keys}
	err = svc.PublishNodeProfile(context.Background(), NodeProfileMetadata{Name: "Hub"})
	require.ErrorContains(t, err, "publishing a node profile needs an LN client")
}

func TestPublishNodeProfile_RequiresTransportSigner(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	// Same default-fixture gap TestPublishTransportAnnouncement_RequiresTransportSigner
	// pins: MockLn's own GetPubkey ("123pubkey") is not a valid compressed
	// key, which would fail at nodeXOnlyIdentity and mask the case this test
	// is for, so give it a real one first.
	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	base.Pubkey = hex.EncodeToString(priv.PubKey().SerializeCompressed())

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ts.LNClient}
	err = svc.PublishNodeProfile(context.Background(), NodeProfileMetadata{Name: "Hub"})
	require.ErrorContains(t, err, "this LN backend cannot sign a node profile")
}

func TestPublishNodeProfile_PropagatesSignerError(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)
	ln.signErr = errors.New("node locked")

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	err = svc.PublishNodeProfile(context.Background(), NodeProfileMetadata{Name: "Hub"})
	require.ErrorContains(t, err, "node refused to sign the profile")
	require.ErrorContains(t, err, "node locked")
}

func TestPublishNodeProfile_NoGeneralRelaysConfiguredIsAnError(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	require.NoError(t, ts.Cfg.SetUpdate("GeneralRelay", "", ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	err = svc.PublishNodeProfile(context.Background(), NodeProfileMetadata{Name: "Hub"})
	require.ErrorContains(t, err, "no general relays configured")
}

// TestPublishNodeProfile_NoRelayAcceptedIsAnError covers General relays
// being configured but all unreachable — distinct from none being
// configured at all.
func TestPublishNodeProfile_NoRelayAcceptedIsAnError(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	require.NoError(t, ts.Cfg.SetUpdate("GeneralRelay", "ws://127.0.0.1:1", ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	err = svc.PublishNodeProfile(context.Background(), NodeProfileMetadata{Name: "Hub"})
	require.ErrorContains(t, err, "no relay accepted the node profile")
}

// TestPublishNodeProfile_SucceedsWhenAtLeastOneRelayAccepts proves one
// unreachable General relay among several configured must not sink the
// publish as long as another accepts it.
func TestPublishNodeProfile_SucceedsWhenAtLeastOneRelayAccepts(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()

	good := newOKRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("GeneralRelay", "ws://127.0.0.1:1,"+good, ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	err = svc.PublishNodeProfile(context.Background(), NodeProfileMetadata{
		Name:  "Hub",
		About: "A lokihub node",
	})
	require.NoError(t, err, "one reachable relay accepting the event must be enough")
}
