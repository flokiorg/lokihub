package service

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/flokiorg/lokihub/nodesign"
	"github.com/flokiorg/lokihub/tests"
)

// U-12.RC3 (NIP-CASH §Mint Provenance): the first start records the node
// identity's network; the same network starts again; an identity recorded
// under another network refuses cash serving. The mock node runs testnet.
func TestCheckCashIdentityNetwork_RefusesAnIdentityRecordedOnAnotherNetwork(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	svc := &service{cfg: ts.Cfg, keys: ts.Keys, db: ts.DB, lnClient: ts.LNClient}
	identity, err := nipcash.MinterXOnly(tests.MockNodePubkey)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, svc.checkCashIdentityNetwork(ctx), "first start")
	recorded, err := ts.Cfg.Get(nodesign.IdentityNetworksKey, "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"`+identity+`":"testnet"}`, recorded)
	require.NoError(t, svc.checkCashIdentityNetwork(ctx), "same network")

	require.NoError(t, ts.Cfg.SetUpdate(nodesign.IdentityNetworksKey, `{"`+identity+`":"regtest"}`, ""))
	assert.ErrorIs(t, svc.checkCashIdentityNetwork(ctx), nodesign.ErrIdentityOnAnotherNetwork)

	// The private transport does not start, and no cash payload is signed.
	t.Cleanup(func() { nodesign.Guard.Set(nil) })
	nodesign.Guard.Set(svc.checkCashIdentityNetwork)
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	group, gctx := errgroup.WithContext(gctx)
	err = svc.startPrivateTransport(gctx, nostr.NewSimplePool(gctx), group)
	assert.ErrorIs(t, err, nodesign.ErrIdentityOnAnotherNetwork)
	_, err = nodesign.Signer(ctx, ts.LNClient)("payload")
	assert.ErrorIs(t, err, nodesign.ErrIdentityOnAnotherNetwork)

	require.NoError(t, ts.Cfg.SetUpdate(nodesign.IdentityNetworksKey, `{"`+identity+`":"testnet"}`, ""))
	_, err = nodesign.Signer(ctx, ts.LNClient)("payload")
	assert.NoError(t, err, "signs once the record matches the node's network")
}
