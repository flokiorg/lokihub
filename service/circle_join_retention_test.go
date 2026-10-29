package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Data Model: the single-use record keeps a join proof's id until its
// retention ends, and only then may the cleanup forget it.
func TestPruneCircleIdentityProofs_KeepsUntilRetention(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	now := time.Now().Unix()
	live := db.CircleWalletIdentityProof{AppID: 1, EventID: "live", Kind: db.CircleProofJoin, RetainUntil: now + 5}
	edge := db.CircleWalletIdentityProof{AppID: 1, EventID: "edge", Kind: db.CircleProofJoin, RetainUntil: now}
	gone := db.CircleWalletIdentityProof{AppID: 1, EventID: "gone", Kind: db.CircleProofJoin, RetainUntil: now - 2}
	require.NoError(t, svc.DB.Create(&live).Error)
	require.NoError(t, svc.DB.Create(&edge).Error)
	require.NoError(t, svc.DB.Create(&gone).Error)

	pruneStaleCircleWalletIdentityProofs(svc.DB)

	var ids []string
	require.NoError(t, svc.DB.Model(&db.CircleWalletIdentityProof{}).Order("event_id").Pluck("event_id", &ids).Error)
	assert.Equal(t, []string{"edge", "live"}, ids)
}

// NIP-CW §Lost Join Reply: a join's record is kept while its wallet exists and,
// once the wallet is gone, until its deadline plus 6 minutes.
func TestPruneCircleJoinRecords_KeepsLiveWalletsAndTheWindow(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	live, _, err := svc.AppsService.CreateApp("live", "", 0, "never", nil, []string{"get_info"}, db.AppKindCircleWallet, nil, "", nil)
	require.NoError(t, err)
	now := time.Now()
	old := now.Unix() - 3600
	rec := func(name string, walletID uint, deadline int64) db.CircleJoinRecord {
		return db.CircleJoinRecord{CircleHubAppID: 1, HubWalletPubkey: "h", HubConnPubkey: "c", AuthorizerPubkey: name,
			ConnectionPubkey: name, ProofCreatedAt: deadline - 300, Deadline: deadline, EncryptedDetails: "x", WalletAppID: walletID}
	}
	for _, r := range []db.CircleJoinRecord{
		rec("live-old", live.ID, old),               // wallet exists: kept however old
		rec("gone-window", 999_999, now.Unix()-360), // wallet gone, now = deadline + 6 min: kept
		rec("gone-past", 999_998, now.Unix()-361),   // wallet gone, past the window: dropped
	} {
		require.NoError(t, svc.DB.Create(&r).Error)
	}

	pruneCircleJoinRecords(svc.DB, now)

	var kept []string
	require.NoError(t, svc.DB.Model(&db.CircleJoinRecord{}).Order("authorizer_pubkey").Pluck("authorizer_pubkey", &kept).Error)
	assert.Equal(t, []string{"gone-window", "live-old"}, kept)
}
