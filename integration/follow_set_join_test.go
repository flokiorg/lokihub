//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	cwclient "github.com/ohstr/nmilat/nipcw/client"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireWalletErrorCode asserts err is a refusal carrying code, as nmilat's
// clients report it over either transport.
func requireWalletErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var walletErr *relayclient.WalletError
	require.True(t, errors.As(err, &walletErr), "expected a *relayclient.WalletError, got %T: %v", err, err)
	require.Equal(t, code, walletErr.Code, walletErr.Message)
}

// fetchFollowSet reads the newest version of a follow set from cfg's relay.
func fetchFollowSet(t *testing.T, cfg *Config, provider, d string) nostr.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, cfg.RelayURL)
	require.NoError(t, err)
	defer relay.Close()

	events, err := relay.QuerySync(ctx, nostr.Filter{
		Kinds:   []int{kindFollowSet},
		Authors: []string{provider},
		Tags:    nostr.TagMap{"d": []string{d}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, events, "the hub published the set to %s", cfg.RelayURL)
	newest := events[0]
	for _, ev := range events[1:] {
		if ev.CreatedAt > newest.CreatedAt {
			newest = ev
		}
	}
	return *newest
}

// tagValue is the value of the first tag named name, or "".
func tagValue(tags nostr.Tags, name string) string {
	if tag := tags.Find(name); len(tag) >= 2 {
		return tag[1]
	}
	return ""
}

func joinParams(memberPriv string) nipcw.CreateCircleWalletParams {
	return nipcw.CreateCircleWalletParams{
		Credential:      nipcw.BySigning(memberPriv),
		MaxAmountMillis: nmilatHappyPathAmountMillis,
		Expiry:          nmilatHappyPathExpiry,
	}
}

// TestFollowSetCircle_PublicAndHiddenMemberJoin runs NIP-CW §Membership and
// §Hidden Members end to end: the hub publishes a follow set as its node
// identity with one public and one hidden member, both join, a non-member
// is refused on both transports, and a republished set revokes the member it
// drops.
func TestFollowSetCircle_PublicAndHiddenMemberJoin(t *testing.T) {
	cfg := requireConfig(t)

	publicPriv, hiddenPriv, outsiderPriv := newTestPrivkey(t), newTestPrivkey(t), newTestPrivkey(t)
	publicPub, hiddenPub := mustPubkey(t, publicPriv), mustPubkey(t, hiddenPriv)
	suffix := make([]byte, 4)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	d := "e2e-" + hex.EncodeToString(suffix)

	const title = "E2E Family Circle"
	resp, admin := createEphemeralManagedFollowSetHub(t, cfg, "follow-set-join", d, adminFollowSet{
		Title:         title,
		Description:   "One public and one hidden member",
		PublicMembers: []string{publicPub},
		HiddenMembers: []string{hiddenPub},
	}, ephemeralCircleHubOpts{})

	t.Run("invite carries the set's title", func(t *testing.T) {
		require.NotEmpty(t, resp.CircleHubToken)
		token, err := nipcw.DecodeCircleHubConnection(resp.CircleHubToken)
		require.NoError(t, err)
		assert.Equal(t, title, token.Label)
	})

	var info struct {
		CircleWallet *struct {
			CirclePolicy string `json:"circle_policy"`
			Identity     string `json:"identity"`
		} `json:"circle_wallet"`
	}
	require.NoError(t, mustConnect(t, resp.PairingUri).Call(ctxT(t), "get_info", struct{}{}, &info))
	require.NotNil(t, info.CircleWallet)
	require.Equal(t, circlePolicyFollowSet, info.CircleWallet.CirclePolicy)
	require.NotEmpty(t, info.CircleWallet.Identity, "the hub offers Private Join, which hidden members need")
	node := info.CircleWallet.Identity

	t.Run("published set hides the hidden member", func(t *testing.T) {
		ev := fetchFollowSet(t, cfg, node, d)
		ok, err := ev.CheckSignature()
		require.NoError(t, err)
		require.True(t, ok, "signed by the node identity")
		assert.Equal(t, title, tagValue(ev.Tags, "title"))

		var public []string
		var hidden int
		for _, tag := range ev.Tags {
			switch {
			case len(tag) >= 2 && tag[0] == "p":
				public = append(public, tag[1])
			case len(tag) >= 2 && tag[0] == "hidden_p":
				hidden++
			}
		}
		assert.Equal(t, []string{publicPub}, public)
		assert.Equal(t, 1, hidden)

		raw, err := json.Marshal(ev)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), hiddenPub, "the hidden member's pubkey appears nowhere in the published event")
	})

	client, err := cwclient.Connect(ctxT(t), resp.PairingUri)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	t.Run("hidden member joins privately", func(t *testing.T) {
		joined, err := client.Join(ctxT(t), joinParams(hiddenPriv), cwclient.JoinOptions{RequirePrivate: true})
		require.NoError(t, err)
		assert.True(t, joined.Private)
		assert.NotEmpty(t, joined.WalletPubkey)
	})

	t.Run("public member joins over the standard transport", func(t *testing.T) {
		joined, err := client.CreateCircleWallet(ctxT(t), joinParams(publicPriv))
		require.NoError(t, err)
		assert.NotEmpty(t, joined.WalletPubkey)
	})

	t.Run("non-member is refused on both transports", func(t *testing.T) {
		_, err := client.Join(ctxT(t), joinParams(outsiderPriv), cwclient.JoinOptions{RequirePrivate: true})
		requireWalletErrorCode(t, err, nipcw.ErrRestricted)
		_, err = client.CreateCircleWallet(ctxT(t), joinParams(outsiderPriv))
		requireWalletErrorCode(t, err, nipcw.ErrRestricted)
	})

	children, err := admin.listCircleChildren(resp.ID)
	require.NoError(t, err)
	require.Len(t, children, 2, "one wallet per member")

	t.Run("republishing without a member revokes them", func(t *testing.T) {
		identityID, err := admin.getCircleIdentityID(resp.ID)
		require.NoError(t, err)
		require.NoError(t, admin.publishFollowSet(identityID, adminFollowSet{
			Title:         title,
			PublicMembers: []string{publicPub},
		}))

		ev := fetchFollowSet(t, cfg, node, d)
		for _, tag := range ev.Tags {
			assert.NotEqual(t, "hidden_p", tag[0], "the dropped hidden entry is gone")
		}

		// Membership is checked before the one-wallet rule, so a dropped
		// member is refused even though they still hold a wallet...
		_, err = client.Join(ctxT(t), joinParams(hiddenPriv), cwclient.JoinOptions{RequirePrivate: true})
		requireWalletErrorCode(t, err, nipcw.ErrRestricted)
		// ...while a remaining member gets past it and hits that rule.
		_, err = client.CreateCircleWallet(ctxT(t), joinParams(publicPriv))
		requireWalletErrorCode(t, err, nipcw.ErrWalletExists)
	})
}
