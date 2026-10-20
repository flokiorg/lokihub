package nip47

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One member sending two joins in the same envelope gets one wallet: items are
// served one after another, and the second hits the one-wallet rule.
func TestPrivateJoin_SameMemberTwiceInOneBatch_OneWallet(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	h := newJoinHub(t, memberPub)
	nonce := strings.Repeat("07", 32)

	r1, s1 := h.serve(t, "first", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, nonce)
	r2, s2 := h.serve(t, "second", joinParams(t, member, *h.app.WalletPubkey, 10_000), member, nonce)
	require.True(t, s1 && s2)
	assert.Nil(t, r1.Error)
	require.NotNil(t, r2.Error)
	assert.Equal(t, nipcw.ErrWalletExists, r2.Error.Code)
	assert.Len(t, h.children(t), 1)
}
