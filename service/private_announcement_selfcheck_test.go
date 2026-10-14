package service

import (
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// TestPublishTransportAnnouncement_RefusesOneThatDoesNotVerify pins the hub's own
// check before publishing: an announcement every client would reject (here, signed
// by a key other than the node's) is never put on a relay.
func TestPublishTransportAnnouncement_RefusesOneThatDoesNotVerify(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	relay := newScriptedRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", relay.url, ""))

	base, ok := ts.LNClient.(*tests.MockLn)
	require.True(t, ok)
	ln := newAnnouncingLN(t, base)
	ln.priv, err = btcec.NewPrivateKey() // signs with a key that is not the node's
	require.NoError(t, err)

	svc := &service{cfg: ts.Cfg, keys: ts.Keys, lnClient: ln}
	pt, err := svc.newPrivateTransport()
	require.NoError(t, err)

	err = svc.publishTransportAnnouncement(context.Background(), nostr.NewSimplePool(context.Background()), pt)
	require.ErrorContains(t, err, "does not verify")
	assert.False(t, svc.GetPrivateTransportStatus().Announced)
	select {
	case ev := <-relay.published:
		t.Fatalf("an announcement that does not verify was published: %s", ev.ID)
	default:
	}
}
