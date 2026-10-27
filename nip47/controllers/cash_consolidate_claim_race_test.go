package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// raceLosingApps lets a concurrent request claim one wallet's slice just after
// this request has read it: the window between consolidate resolving its sources
// and its commit claiming them.
type raceLosingApps struct {
	apps.AppsService
	lostWalletID uint
}

func (a raceLosingApps) GetCashWalletClaim(walletAppID uint, identityType, identityValue string) (*db.CashWalletClaim, error) {
	claim, err := a.AppsService.GetCashWalletClaim(walletAppID, identityType, identityValue)
	if err == nil && claim != nil && walletAppID == a.lostWalletID {
		if _, err := a.AppsService.ClaimCashSlice(walletAppID, identityType, identityValue); err != nil {
			return nil, err
		}
	}
	return claim, err
}

// A consolidate that loses the race for one source is refused RESTRICTED (NIP-CASH:
// a slice already claimed by a concurrent call), and its one commit leaves every
// other source unclaimed, the winner's claim alone, no merged bill and no used
// proof.
func TestConsolidate_LosesClaimRace_RestoresOwnClaimsOnly(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	_, secretHash, err := tests.GenerateCashSecret()
	require.NoError(t, err)

	s1 := guardSource(t, svc, hub, ownerPub, 2_000, 0, 0)
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")
	s2 := guardSource(t, svc, hub, ownerPub, 1_000, 0, 0)
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	controller := NewNip47Controller(svc.LNClient, svc.DB, svc.EventPublisher,
		permissions.NewPermissionsService(svc.DB, svc.EventPublisher), transactions.NewTransactionsService(svc.DB, svc.EventPublisher),
		raceLosingApps{AppsService: svc.AppsService, lostWalletID: s2.ID}, svc.Keys, nil,
		NewRateLimiter(), NewRateLimiter(), NewRateLimiter(), svc.Cfg, apps.NewIdentityAuthorityManager(svc.DB))

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityCash, secretHash, "", 2_000, nil, time.Now())
	proof2 := buildTransferProofEvent(t, ownerPriv, *s2.WalletPubkey, db.CashIdentityCash, secretHash, "", 1_000, nil, time.Now())
	resp := handleCashConsolidateFor(t, svc, controller, caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof2)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: secretHash},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)

	assert.Nil(t, cashWalletClaimByIdentity(t, svc, s1.ID, db.CashIdentityPubkey, ownerPub).ClaimedAt,
		"the other source is untouched: its claim rolled back with the commit")
	assert.NotNil(t, cashWalletClaimByIdentity(t, svc, s2.ID, db.CashIdentityPubkey, ownerPub).ClaimedAt,
		"the concurrent winner's claim is left alone")

	var merged int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ? AND id NOT IN ?",
		hub.ID, db.AppKindCashWallet, []uint{caller.ID, s1.ID, s2.ID}).Count(&merged).Error)
	assert.Zero(t, merged, "no merged bill is created")
	var burned int64
	require.NoError(t, svc.DB.Model(&db.CashConsumedProof{}).Count(&burned).Error)
	assert.Zero(t, burned, "no proof is used up by a request that failed")
}
