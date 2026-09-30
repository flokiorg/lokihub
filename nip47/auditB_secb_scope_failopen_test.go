package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// auditBBill builds a bill with an arbitrary claim set, returning what a caller needs.
func auditBBill(t *testing.T, svc *tests.TestService, hubID uint, claims []db.CashWalletClaim) (walletPubkey, connPriv string, walletID uint) {
	t.Helper()
	wallet := db.App{
		Name: "auditb-bill-" + tests.RandomHex32()[:8], Kind: db.AppKindCashWallet,
		ParentAppID: &hubID, ParentKind: db.ParentKindCash,
		AppPubkey: tests.RandomHex32(),
	}
	require.NoError(t, svc.DB.Create(&wallet).Error)

	walletKey, err := svc.Keys.GetAppWalletKey(wallet.ID)
	require.NoError(t, err)
	walletPubkey, err = nostr.GetPublicKey(walletKey)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&wallet).Update("wallet_pubkey", walletPubkey).Error)

	connPriv, err = svc.Keys.GetCashPairingKey(wallet.ID)
	require.NoError(t, err)
	connPub, err := nostr.GetPublicKey(connPriv)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&wallet).Update("app_pubkey", connPub).Error)

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, claims))
	for _, scope := range []string{
		constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE,
		constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE,
	} {
		require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: wallet.ID, Scope: scope}).Error)
	}
	return walletPubkey, connPriv, wallet.ID
}

// TestAuditB_ConnectionKeyBill_TokenPossessorWithNoSliceReadsTheWholeRoster
//
// THREAT: an attacker who possesses a bill's token (its connection string, i.e. the
// pairing secret) but holds NO slice on that bill. Per NIP-CASH §Scoping the Roster a
// caller whose item proof verifies but who holds no slice "receives no rows" and MUST be
// answered NOT_FOUND. Demonstrated here: on a bill carrying any connection_key claim,
// they instead receive every recipient's row.
//
// CONTROL (required by this round's rules): the same attacker, same key, against a bill
// whose claims are all pubkey-mode, is correctly told NOT_FOUND — proving the hub is
// answering and the gate does work when it believes it can decide. Plus a legitimate
// recipient of the SAME connection_key bill is served across the attempt, proving the
// bill is live throughout.
func TestAuditB_ConnectionKeyBill_TokenPossessorWithNoSliceReadsTheWholeRoster(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	alicePriv := nostr.GeneratePrivateKey()
	alicePub, err := nostr.GetPublicKey(alicePriv)
	require.NoError(t, err)

	// The bill: Alice holds a pubkey slice, Bob holds a connection_key slice.
	mixedPubkey, mixedConn, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 1000},
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: strings.Repeat("11", 32),
			IAPubkey: strings.Repeat("22", 32), AmountMloki: 7000},
	})

	// A bill with the SAME shape but all-pubkey claims — the control.
	purePubkey, pureConn, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: strings.Repeat("33", 32), AmountMloki: 7000},
	})

	hubXOnly := strings.Repeat("ab", 32)
	notAfter := time.Now().Add(time.Minute).Unix()

	ask := func(nonce, target, conn, signerPriv, params string) (transport.Result, bool) {
		binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}
		item := buildItem(t, "i1", target, constants.NIP47MethodCashStatus, params,
			signerPriv, conn, hubXOnly, nonce, notAfter)
		return nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	}
	roster := func(r transport.Result) nipcash.CashStatusResult {
		var out nipcash.CashStatusResult
		require.NoError(t, json.Unmarshal(r.Result, &out))
		return out
	}

	// --- CONTROL 1: the bill is live and the hub answers its real recipient. ---
	res, served := ask(strings.Repeat("c1", 32), mixedPubkey, mixedConn, alicePriv, `{"scope":"mine"}`)
	require.True(t, served, "control: the hub must answer a real recipient of the mixed bill")
	require.Nil(t, res.Error, "control: %v", res.Error)
	require.Len(t, roster(res).Recipients, 1, "control: scope=mine gives Alice exactly her own row")

	// --- CONTROL 2: an outsider with a token but no slice on an ALL-PUBKEY bill. ---
	attackerPriv := nostr.GeneratePrivateKey()
	res, served = ask(strings.Repeat("c2", 32), purePubkey, pureConn, attackerPriv, `{"scope":"all"}`)
	require.True(t, served, "control: an item with a valid bill proof is answered, not omitted")
	require.NotNil(t, res.Error, "control: a no-slice caller on an all-pubkey bill must get an error")
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code,
		"control: the design's answer is NOT_FOUND")
	t.Logf("CONTROL all-pubkey bill, no slice, scope=all -> %s %q", res.Error.Code, res.Error.Message)

	// --- ATTACK: the same outsider, same key, on the connection_key bill. ---
	// The bill's composition must make no difference. Before the fix it made all the
	// difference: a connection_key claim set decidableHere=false, the gate deferred to "the
	// controller authorizes" — true of the three spend methods, FALSE of cash_status, whose
	// controller performs no identity check at all — and the whole roster came back.
	res, served = ask(strings.Repeat("a1", 32), mixedPubkey, mixedConn, attackerPriv, `{"scope":"all"}`)
	require.True(t, served, "an item with a valid bill proof is answered, not omitted")
	if res.Error == nil {
		out := roster(res)
		for _, r := range out.Recipients {
			t.Logf("  DISCLOSED: type=%s value=%s amount=%d claimed=%v fee=%d",
				r.IdentityType, r.IdentityValue, r.AmountMillis, r.Claimed, r.RedeemFeeMillis)
		}
		require.FailNowf(t, "AUDITB-B1 BUG PRESENT",
			"a token possessor holding NO slice read %d roster row(s) off a bill merely because it "+
				"carries a connection_key recipient; the same key correctly got NOT_FOUND on the "+
				"all-pubkey bill above. §Scoping the Roster makes NOT_FOUND a MUST for a no-slice "+
				"caller, unconditional on identity mode", len(out.Recipients))
	}
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code,
		"a no-slice caller must get the SAME answer whatever the bill's identity modes are")
	t.Logf("ATTACK connection_key bill, no slice, scope=all -> %s %q (identical to the control)",
		res.Error.Code, res.Error.Message)

	// --- CONTROL 3: the bill was still live AFTER the attempt. ---
	res2, served2 := ask(strings.Repeat("c3", 32), mixedPubkey, mixedConn, alicePriv, `{"scope":"mine"}`)
	require.True(t, served2)
	require.Nil(t, res2.Error)
	require.Len(t, roster(res2).Recipients, 1, "control: bill still live and still scoped after the attempt")

	// Control 3 runs AFTER the attack deliberately: it proves the bill was live and still
	// correctly scoped throughout, so the NOT_FOUND above cannot be explained by the bill
	// having gone away mid-test.
}

// TestAuditB_ConnectionKeyBill_ScopeMineIsACompositionOracle
//
// Even without scope=all, the SHAPE of the answer tells a token possessor with no slice
// whether the bill has any non-pubkey (connection_key) recipient: an all-pubkey bill
// answers NOT_FOUND, a bill with a connection_key claim answers SUCCESS with an empty
// roster. Both callers are the same key with the same entitlement (none).
func TestAuditB_ConnectionKeyBill_ScopeMineIsACompositionOracle(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	alicePriv := nostr.GeneratePrivateKey()
	alicePub, err := nostr.GetPublicKey(alicePriv)
	require.NoError(t, err)
	_ = alicePriv

	ckPubkey, ckConn, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 1000},
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: strings.Repeat("44", 32),
			IAPubkey: strings.Repeat("22", 32), AmountMloki: 7000},
	})
	pkPubkey, pkConn, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 1000},
	})

	hubXOnly := strings.Repeat("ab", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	attackerPriv := nostr.GeneratePrivateKey()

	ask := func(nonce, target, conn string) transport.Result {
		binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}
		item := buildItem(t, "i1", target, constants.NIP47MethodCashStatus, `{"scope":"mine"}`,
			attackerPriv, conn, hubXOnly, nonce, notAfter)
		res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
		require.True(t, served, "both probes must be answered, so the difference is the signal")
		return res
	}

	ck := ask(strings.Repeat("d1", 32), ckPubkey, ckConn)
	pk := ask(strings.Repeat("d2", 32), pkPubkey, pkConn)

	t.Logf("connection_key bill, scope=mine, no slice -> err=%v result=%s", ck.Error, string(ck.Result))
	t.Logf("all-pubkey   bill, scope=mine, no slice -> err=%v result=%s", pk.Error, string(pk.Result))

	if (ck.Error == nil) != (pk.Error == nil) {
		t.Fatalf("AUDITB ORACLE: identical no-slice callers get structurally different answers " +
			"depending on whether the bill has a connection_key recipient")
	}
}

// TestAuditB_CashModeBill_ProoflessStatusNeedsNoSecret
//
// A cash-mode bill's money is gated on the cash SECRET. cash_status is not: a proofless
// item carrying only a bill proof (i.e. nothing but possession of the token string) is
// authorized, and returns the slice's amount, claimed flag and claimed_at.
//
// So every party that ever held the token — including a giver who handed token#secret on
// and kept a copy of the token — can watch whether and exactly WHEN the current holder
// redeemed, for as long as the bill lives. Recorded rather than rated: NIP-CASH
// §Archival on Deletion already concedes that a token holder can compose a valid
// cash_status. The point of the test is that NO secret is involved at any point.
func TestAuditB_CashModeBill_ProoflessStatusNeedsNoSecret(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	claimedAt := time.Now().Add(-90 * time.Minute).Truncate(time.Second)
	walletPubkey, connPriv, walletID := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: strings.Repeat("55", 32), AmountMloki: 4200},
	})
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ?", walletID).Update("claimed_at", claimedAt).Error)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("ef", 32)
	notAfter := time.Now().Add(time.Minute).Unix()

	// No slice proof, no cash secret: possession of the token is the whole item.
	hash, err := transport.CanonicalParamsHash(json.RawMessage(`{"scope":"mine"}`))
	require.NoError(t, err)
	pb := transport.ProofBinding{Target: walletPubkey, HubXOnly: hubXOnly,
		Method: constants.NIP47MethodCashStatus, ParamsHash: hash, Nonce: nonce, NotAfter: notAfter}
	billProof, err := transport.BuildBillProof(connPriv, pb)
	require.NoError(t, err)
	item := transport.Item{ID: "p1", Target: walletPubkey, Method: constants.NIP47MethodCashStatus,
		Params: json.RawMessage(`{"scope":"mine"}`), BillProof: billProof}
	require.False(t, item.HasProof(), "the item deliberately carries no slice proof")
	require.False(t, item.IsBearer(), "and no cash secret either")

	res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient,
		item, PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.Nil(t, res.Error, "%v", res.Error)
	var out nipcash.CashStatusResult
	require.NoError(t, json.Unmarshal(res.Result, &out))
	require.Len(t, out.Recipients, 1)
	t.Logf("AUDITB token-only cash_status: amount=%d claimed=%v claimed_at=%v",
		out.Recipients[0].AmountMillis, out.Recipients[0].Claimed, out.Recipients[0].ClaimedAt)
	require.NotNil(t, out.Recipients[0].ClaimedAt,
		"the exact redemption instant is disclosed to anyone holding the token")
}
