package nip47

// NIP-CASH §Lost Replies (request_id reuse, cash_lookup authorization) and
// §Consolidating Tokens / §Transferring and Splitting a Slice (one spend per slice).

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/archive"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

func lookupStatusOf(t *testing.T, res transport.Result) (string, nipcash.CashLookupResult) {
	t.Helper()
	if res.Error != nil {
		return res.Error.Code, nipcash.CashLookupResult{}
	}
	var answer nipcash.CashLookupResult
	require.NoError(t, json.Unmarshal(res.Result, &answer))
	return "", answer
}

// A record's lookup must name the recorded target: the same signer looking the
// call up through another bill it holds is UNAUTHORIZED.
func TestPrivateLookup_OnlyTheRecordedTargetAnswers(t *testing.T) {
	f := newLookupFixture(t)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	rid := tests.RandomHex32()
	split := uint64(400)
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashTransferParams{Credential: nipcash.BySigning(f.ownerPriv), To: nipcash.Pubkey(newPub),
			CurrentAmount: 1000, SplitAmount: &split, RequestID: rid}.Item("t", f.walletPubkey, f.connPriv, b)
	})
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)

	var hub db.App
	require.NoError(t, f.svc.DB.Where("kind = ?", db.AppKindCashHub).First(&hub).Error)
	otherPubkey, otherConn, _ := auditBBill(t, f.svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: f.ownerPub, AmountMloki: 1000},
	})
	look, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(f.ownerPriv),
			Request: nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Unix()}}.Item("l", otherPubkey, otherConn, b)
	})
	require.True(t, served)
	code, _ := lookupStatusOf(t, look)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, code)

	look, served = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(f.ownerPriv),
			Request: nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Unix()}}.Item("l", f.walletPubkey, f.connPriv, b)
	})
	require.True(t, served)
	code, answer := lookupStatusOf(t, look)
	require.Empty(t, code)
	assert.Equal(t, nipcash.LookupDone, answer.Status, "the recorded target answers")
}

// A call authorized by a cash secret is looked up only with that secret, never
// with a signed proof, and its tokens come back in the clear exactly once it matches.
func TestPrivateLookup_CashAuthorizedRecordNeedsItsSecret(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")
	src, err := nipcash.NewCashTarget()
	require.NoError(t, err)
	walletPubkey, connPriv, walletID := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: src.Commitment(), AmountMloki: 1000},
	})
	tests.FundApp(svc, walletID, 1000, "bill-fund")
	f := &lookupFixture{svc: svc, nip47svc: NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil),
		walletPubkey: walletPubkey, connPriv: connPriv, walletID: walletID, hubXOnly: strings.Repeat("ab", 32)}

	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	rem, err := nipcash.NewCashTarget()
	require.NoError(t, err)
	rid := tests.RandomHex32()
	split := uint64(300)
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashTransferParams{Credential: nipcash.BySecret(src.Secret()), To: nipcash.Pubkey(newPub),
			CurrentAmount: 1000, SplitAmount: &split, RemainderTarget: rem, RequestID: rid}.Item("t", walletPubkey, connPriv, b)
	})
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var original map[string]any
	require.NoError(t, json.Unmarshal(res.Result, &original))

	lookup := func(cred nipcash.Credential, secret string) string {
		look, served := f.lookup(t, cred, nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Unix(), CashSecret: secret})
		require.True(t, served)
		code, answer := lookupStatusOf(t, look)
		if code != "" {
			return code
		}
		require.Equal(t, nipcash.LookupDone, answer.Status)
		var rebuilt map[string]any
		require.NoError(t, json.Unmarshal(answer.Result, &rebuilt))
		assert.Equal(t, original["new_wallet_token"], rebuilt["new_wallet_token"], "a clear token, byte for byte")
		assert.Equal(t, original["remainder_wallet_token"], rebuilt["remainder_wallet_token"])
		return "done"
	}
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, lookup(nipcash.BySigning(nostr.GeneratePrivateKey()), ""), "a proof instead of the secret")
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, lookup(nipcash.BySecret(src.Secret()), ""), "no secret")
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, lookup(nipcash.BySecret(src.Secret()), rem.Secret()), "another secret")
	assert.Equal(t, "done", lookup(nipcash.BySecret(src.Secret()), src.Secret()))
}

// A used request_id is refused whatever the params or the method, and the
// refused call moves nothing.
func TestRequestID_ReuseWithOtherParamsIsRefused(t *testing.T) {
	f := newLookupFixture(t)
	var hub db.App
	require.NoError(t, f.svc.DB.Where("kind = ?", db.AppKindCashHub).First(&hub).Error)
	otherPubkey, otherConn, otherID := auditBBill(t, f.svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: f.ownerPub, AmountMloki: 1000},
	})
	tests.FundApp(f.svc, otherID, 1000, "other-fund")

	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	rid := tests.RandomHex32()
	split := uint64(400)
	res, _ := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashTransferParams{Credential: nipcash.BySigning(f.ownerPriv), To: nipcash.Pubkey(newPub),
			CurrentAmount: 1000, SplitAmount: &split, RequestID: rid}.Item("t", f.walletPubkey, f.connPriv, b)
	})
	require.Nil(t, res.Error, "%+v", res.Error)

	var apps0 int64
	require.NoError(t, f.svc.DB.Model(&db.App{}).Count(&apps0).Error)
	otherSplit := uint64(100)
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashTransferParams{Credential: nipcash.BySigning(f.ownerPriv), To: nipcash.Pubkey(f.ownerPub),
			CurrentAmount: 1000, SplitAmount: &otherSplit, RequestID: rid}.Item("t2", otherPubkey, otherConn, b)
	})
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_REQUEST_REUSED, res.Error.Code, "another bill, another amount")
	assert.Empty(t, res.Result, "the refusal carries no result")

	res, served = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashConsolidateParams{Sources: []nipcash.Source{
			{WalletPubkey: otherPubkey, Amount: 1000, Credential: nipcash.BySigning(f.ownerPriv)},
			{WalletPubkey: f.walletPubkey, Amount: 1000, Credential: nipcash.BySigning(f.ownerPriv)},
		}, To: nipcash.Pubkey(f.ownerPub), RequestID: rid}.Item("c", otherPubkey, otherConn, b)
	})
	require.True(t, served)
	require.NotNil(t, res.Error)
	assert.Equal(t, constants.ERROR_REQUEST_REUSED, res.Error.Code, "another method")

	var apps1 int64
	require.NoError(t, f.svc.DB.Model(&db.App{}).Count(&apps1).Error)
	assert.Equal(t, apps0, apps1, "no bill created")
	assert.EqualValues(t, 1000, queries.GetIsolatedBalance(f.svc.DB, otherID), "nothing moved")
}

// A split of bill A and a consolidate of A with B race: exactly one spends A,
// and every unit stays accounted for.
func TestTransferRacingConsolidate_ExactlyOneSpends(t *testing.T) {
	for round := 0; round < 4; round++ {
		f := newLookupFixture(t)
		var hub db.App
		require.NoError(t, f.svc.DB.Where("kind = ?", db.AppKindCashHub).First(&hub).Error)
		bPubkey, bConn, bID := auditBBill(t, f.svc, hub.ID, []db.CashWalletClaim{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: f.ownerPub, AmountMloki: 1000},
		})
		tests.FundApp(f.svc, bID, 1000, "b-fund")
		newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
		split := uint64(400)

		var wg sync.WaitGroup
		results := make([]transport.Result, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			results[0], _ = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
				return nipcash.CashTransferParams{Credential: nipcash.BySigning(f.ownerPriv), To: nipcash.Pubkey(newPub),
					CurrentAmount: 1000, SplitAmount: &split, RequestID: tests.RandomHex32()}.Item("t", f.walletPubkey, f.connPriv, b)
			})
		}()
		go func() {
			defer wg.Done()
			results[1], _ = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
				return nipcash.CashConsolidateParams{Sources: []nipcash.Source{
					{WalletPubkey: bPubkey, Amount: 1000, Credential: nipcash.BySigning(f.ownerPriv)},
					{WalletPubkey: f.walletPubkey, Amount: 1000, Credential: nipcash.BySigning(f.ownerPriv)},
				}, To: nipcash.Pubkey(f.ownerPub), RequestID: tests.RandomHex32()}.Item("c", bPubkey, bConn, b)
			})
		}()
		wg.Wait()
		ok := 0
		for _, r := range results {
			if r.Error == nil && len(r.Result) > 0 {
				ok++
			}
		}
		assert.Equal(t, 1, ok, "round %d: exactly one spends bill A: %+v / %+v", round, results[0].Error, results[1].Error)

		var bills []db.App
		require.NoError(t, f.svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&bills).Error)
		var total int64
		var live int64
		for _, b := range bills {
			total += queries.GetIsolatedBalance(f.svc.DB, b.ID)
			var claims []db.CashWalletClaim
			require.NoError(t, f.svc.DB.Where("wallet_app_id = ? AND claimed_at IS NULL", b.ID).Find(&claims).Error)
			for _, c := range claims {
				live += c.AmountMloki
			}
		}
		assert.EqualValues(t, 2000, total, "round %d: value conserved across bills", round)
		assert.EqualValues(t, 2000, live, "round %d: live slices back exactly the value", round)
	}
}

// Sources must agree on redeem_fee_base, and ordered and not-ordered sources are
// never merged: both are RESTRICTED and move nothing (NIP-CASH §Consolidating Tokens).
func TestConsolidate_SourceTermsMustMatch(t *testing.T) {
	for _, c := range []struct {
		name  string
		claim db.CashWalletClaim
	}{
		{"redeem_fee_base differs", db.CashWalletClaim{RedeemFeeBaseMloki: 5}},
		{"ordered with not-ordered", db.CashWalletClaim{Ordered: true}},
		{"same terms", db.CashWalletClaim{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLookupFixture(t)
			var hub db.App
			require.NoError(t, f.svc.DB.Where("kind = ?", db.AppKindCashHub).First(&hub).Error)
			claim := c.claim
			claim.IdentityType, claim.IdentityValue, claim.AmountMloki = db.CashIdentityPubkey, f.ownerPub, 1000
			bPubkey, bConn, bID := auditBBill(t, f.svc, hub.ID, []db.CashWalletClaim{claim})
			tests.FundApp(f.svc, bID, 1000, "b-fund")
			res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
				return nipcash.CashConsolidateParams{Sources: []nipcash.Source{
					{WalletPubkey: bPubkey, Amount: 1000, Credential: nipcash.BySigning(f.ownerPriv), Ordered: claim.Ordered},
					{WalletPubkey: f.walletPubkey, Amount: 1000, Credential: nipcash.BySigning(f.ownerPriv)},
				}, To: nipcash.Pubkey(f.ownerPub), RequestID: tests.RandomHex32()}.Item("c", bPubkey, bConn, b)
			})
			require.True(t, served)
			if c.name == "same terms" {
				require.Nil(t, res.Error, "%+v", res.Error)
				assert.EqualValues(t, 0, queries.GetIsolatedBalance(f.svc.DB, bID), "a merge of equal terms drains its sources")
				return
			}
			require.NotNil(t, res.Error, "a merge across terms must be refused")
			assert.Equal(t, constants.ERROR_RESTRICTED, res.Error.Code)
			assert.EqualValues(t, 1000, queries.GetIsolatedBalance(f.svc.DB, bID))
			assert.EqualValues(t, 1000, queries.GetIsolatedBalance(f.svc.DB, f.walletID))
		})
	}
}

// A new wallet made from an ordered slice is ordered, carved and remainder alike
// (NIP-CASH §Spinning a Slice Off Into a Dedicated Wallet, Eligibility and Limits).
func TestSplit_OrderedMarkCarries(t *testing.T) {
	f := newLookupFixture(t)
	var hub db.App
	require.NoError(t, f.svc.DB.Where("kind = ?", db.AppKindCashHub).First(&hub).Error)
	bPubkey, bConn, bID := auditBBill(t, f.svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: f.ownerPub, AmountMloki: 1000, Ordered: true},
	})
	tests.FundApp(f.svc, bID, 1000, "b-fund")
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	split := uint64(400)
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashTransferParams{Credential: nipcash.BySigning(f.ownerPriv), To: nipcash.Pubkey(newPub),
			CurrentAmount: 1000, SplitAmount: &split, RequestID: tests.RandomHex32()}.Item("t", bPubkey, bConn, b)
	})
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var claims []db.CashWalletClaim
	require.NoError(t, f.svc.DB.Joins("JOIN apps ON apps.id = cash_wallet_claims.wallet_app_id").
		Where("apps.split_from_wallet_app_id = ?", bID).Find(&claims).Error)
	require.Len(t, claims, 2)
	var sum int64
	for _, c := range claims {
		assert.True(t, c.Ordered, "slice %d keeps the ordered mark", c.ID)
		sum += c.AmountMloki
		assert.EqualValues(t, c.AmountMloki, queries.GetIsolatedBalance(f.svc.DB, c.WalletAppID), "each new bill is funded with exactly its amount")
	}
	assert.EqualValues(t, 1000, sum)
	assert.EqualValues(t, 0, queries.GetIsolatedBalance(f.svc.DB, bID), "the source is drained")
}

// With no record, an item proof that is present must still verify with its usual
// bindings: an item proof signed over other params is UNAUTHORIZED, not answered by deadline.
func TestPrivateLookup_NoRecordStillVerifiesTheProof(t *testing.T) {
	f := newLookupFixture(t)
	look, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		item, err := nipcash.CashLookupParams{Credential: nipcash.BySigning(f.ownerPriv),
			Request: nipcash.CashLookupRequest{RequestID: tests.RandomHex32(), Deadline: time.Now().Unix() - 30}}.Item("l", f.walletPubkey, f.connPriv, b)
		if err != nil {
			return item, err
		}
		other, err := nipcash.CashLookupParams{Credential: nipcash.BySigning(f.ownerPriv),
			Request: nipcash.CashLookupRequest{RequestID: tests.RandomHex32(), Deadline: time.Now().Unix() - 30}}.Item("l", f.walletPubkey, f.connPriv, b)
		item.Proof = other.Proof // signed over other params
		return item, err
	})
	require.True(t, served)
	code, _ := lookupStatusOf(t, look)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, code)

	look, served = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(f.ownerPriv),
			Request: nipcash.CashLookupRequest{RequestID: tests.RandomHex32(), Deadline: time.Now().Unix() - 30}}.Item("l", f.walletPubkey, f.connPriv, b)
	})
	require.True(t, served)
	code, answer := lookupStatusOf(t, look)
	require.Empty(t, code, "a proof over its own params verifies")
	assert.Equal(t, nipcash.LookupUnknown, answer.Status)
}

// NIP-CASH §Archival on Deletion (CASH-458): cash_lookup rebuilds a split's
// reply with the new bill's token while the bill lives, and never re-issues a
// token for it once it is deleted.
func TestPrivateLookup_NeverReissuesADeletedBillsToken(t *testing.T) {
	f := newLookupFixture(t)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	rid := tests.RandomHex32()
	split := uint64(400)
	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashTransferParams{
			Credential: nipcash.BySigning(f.ownerPriv), To: nipcash.Pubkey(newPub),
			CurrentAmount: 1000, SplitAmount: &split, RequestID: rid,
		}.Item("t", f.walletPubkey, f.connPriv, b)
	})
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var original map[string]any
	require.NoError(t, json.Unmarshal(res.Result, &original))
	newWallet := original["new_wallet_pubkey"].(string)

	rebuiltToken := func() any {
		look, served := f.lookup(t, nipcash.BySigning(f.ownerPriv), nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Unix()})
		require.True(t, served)
		require.Nil(t, look.Error, "%+v", look.Error)
		var answer nipcash.CashLookupResult
		require.NoError(t, json.Unmarshal(look.Result, &answer))
		var rebuilt map[string]any
		require.NoError(t, json.Unmarshal(answer.Result, &rebuilt))
		return rebuilt["new_wallet_token"]
	}
	live, _ := rebuiltToken().(string)
	assert.NotEmpty(t, live, "the live bill's token is rebuilt")

	var app db.App
	require.NoError(t, f.svc.DB.Where("wallet_pubkey = ?", newWallet).First(&app).Error)
	require.NoError(t, f.svc.DB.Transaction(func(tx *gorm.DB) error {
		return archive.ArchiveAndDeleteCashBillTx(tx, &app, db.CashBillOutcomeDeleted, 0, time.Now())
	}))
	tok, isString := rebuiltToken().(string)
	assert.False(t, isString && tok != "", "cash_lookup handed out a token for the deleted bill %s", newWallet)
}
