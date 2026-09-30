package nip47

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// AUDIT-B SEC-A / F3 — a possessor who holds NO slice reads the whole roster.
//
// The round's question is "can a possessor enumerate co-recipients?". Yes, and the
// route is not the widened NOT_FOUND oracle — it is the gate that is supposed to
// produce that NOT_FOUND abstaining.
//
// privateItemIsAuthorized (private_dispatch.go:~440-465) computes `decidableHere`
// and, if ANY claim on the bill is neither `cash` nor `pubkey`, returns
// authorized=true WITHOUT matching the signer against anything — deliberately, so
// connection_key slices stay redeemable (its comment says so). But "defer to the
// controller" is only safe if the controller authorizes, and for cash_status the
// controller's only narrowing is the `scope` VIEW, which the caller chooses.
// scope=all skips claimsForCaller entirely (cash_status_controller.go:96-98).
//
// So: one connection_key recipient anywhere on a bill turns cash_status into a
// full-roster read for anyone who holds the bill's token, whether or not they hold
// a slice of it. A FORMER recipient is the natural attacker — a token's connection
// secret is derived from the app id and cannot be rotated (round 1's B-2), so
// losing a slice does not lose possession.
func TestAuditBSecA_NoSlicePossessorReadsTheWholeRoster(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, recipientPub, connPriv := privateDispatchFixture(t, svc)

	// Make the bill mixed-mode, which is the only precondition: one connection_key
	// recipient alongside the pubkey one the fixture already created.
	var wallet db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", walletPubkey).First(&wallet).Error)
	require.NoError(t, svc.DB.Create(&db.CashWalletClaim{
		WalletAppID:   wallet.ID,
		IdentityType:  db.CashIdentityConnectionKey,
		IdentityValue: "9f" + tests.RandomHex32()[2:], // hex(sha256(platform:externalID))
		AmountMloki:   2500,
	}).Error)

	hubXOnly := tests.RandomHex32()
	ctx := context.Background()

	// The attacker: holds the bill's TOKEN (so its connection secret, hence a valid
	// kind-23193 bill proof) and holds NO slice. The slice proof is signed with a key
	// they minted seconds ago, which matches no claim on this bill.
	strangerPriv := nostr.GeneratePrivateKey()
	strangerPub, err := nostr.GetPublicKey(strangerPriv)
	require.NoError(t, err)
	require.NotEqual(t, recipientPub, strangerPub)

	ask := func(t *testing.T, scope string, slicePriv string) (transport.Result, bool) {
		nonce, err := transport.NewNonce()
		require.NoError(t, err)
		params := json.RawMessage(`{}`)
		if scope != "" {
			params = json.RawMessage(`{"scope":"` + scope + `"}`)
		}
		item := transport.Item{
			ID: "1", Target: walletPubkey, Method: nipcash.MethodCashStatus, Params: params,
		}
		notAfter := time.Now().Add(60 * time.Second).Unix()
		b := billProofBinding(t, item, hubXOnly, nonce, notAfter)
		item.Proof, err = transport.BuildItemProof(slicePriv, b)
		require.NoError(t, err)
		item.BillProof, err = transport.BuildBillProof(connPriv, b) // possession: real
		require.NoError(t, err)
		return nip47svc.ServePrivateItem(ctx, nil, item, PrivateItemBinding{
			HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter,
		})
	}

	rosterOf := func(t *testing.T, res transport.Result) []nipcash.RecipientStatus {
		t.Helper()
		require.Nil(t, res.Error, "expected a result, got error %+v", res.Error)
		var out nipcash.CashStatusResult
		require.NoError(t, json.Unmarshal(res.Result, &out))
		return out.Recipients
	}

	// CONTROL A — scope=mine discloses nothing to the stranger.
	//
	// Updated for the fix, and the change is itself the point. Pre-fix this returned a
	// SUCCESS with an empty roster, because the gate abstained and the controller's scope
	// filter simply matched no row. That asymmetry — empty success here, NOT_FOUND on an
	// all-pubkey bill — was a composition oracle in its own right: it told a no-slice token
	// holder whether the bill carried a connection_key recipient, i.e. which tokens were
	// worth a scope=all probe.
	//
	// Post-fix the answer is NOT_FOUND whatever the scope, so the oracle closes along with
	// the disclosure.
	mine, served := ask(t, nipcash.ScopeMine, strangerPriv)
	require.True(t, served)
	require.NotNil(t, mine.Error, "CONTROL A: a no-slice signer must be refused, not answered emptily")
	require.Equal(t, constants.ERROR_NOT_FOUND, mine.Error.Code)
	t.Logf("AUDITB-SECA-F3 CONTROL A: scope=mine -> %s (same as scope=all, so the answer shape "+
		"no longer reveals the bill's identity modes)", mine.Error.Code)

	// THE FINDING, now a regression test — the same stranger, same self-minted key,
	// asks for scope=all. Before the fix this returned both rows: every co-recipient's
	// identity, entitled amount and claimed state, to a caller holding no slice.
	all, served := ask(t, nipcash.ScopeAll, strangerPriv)
	require.True(t, served, "an item with a valid bill proof is answered, not omitted")
	if all.Error == nil {
		rows := rosterOf(t, all)
		for _, r := range rows {
			t.Logf("AUDITB-SECA-F3 DISCLOSED: identity_type=%s identity_value=%s amount_millis=%d "+
				"claimed=%v net_redeemable=%d min_transfer=%d",
				r.IdentityType, r.IdentityValue, r.AmountMillis, r.Claimed,
				r.NetRedeemableMillis, r.MinTransferMillis)
		}
		require.FailNowf(t, "AUDITB-SECA-F3 BUG PRESENT",
			"signer %s… holds no claim on this bill, yet scope=all returned all %d rows — the "+
				"identity of every co-recipient, their entitled amounts, and whether each has "+
				"been claimed", strangerPub[:8], len(rows))
	}
	require.Equal(t, constants.ERROR_NOT_FOUND, all.Error.Code,
		"a no-slice possessor must be refused whatever identity modes the bill carries")
	t.Logf("AUDITB-SECA-F3: scope=all -> %s %q for a signer holding no claim",
		all.Error.Code, all.Error.Message)

	// CONTROL B — the gate is NOT simply open. Remove the connection_key claim and
	// the same request is refused NOT_FOUND, which proves the disclosure is caused
	// specifically by `decidableHere` abstaining and not by the absence of any check.
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_type = ?",
		wallet.ID, db.CashIdentityConnectionKey).Delete(&db.CashWalletClaim{}).Error)
	refused, served := ask(t, nipcash.ScopeAll, strangerPriv)
	require.True(t, served)
	require.NotNil(t, refused.Error,
		"CONTROL B: with only pubkey claims the stranger must be refused")
	t.Logf("AUDITB-SECA-F3 CONTROL B: with the connection_key claim removed the SAME request is "+
		"refused %s/%q — so one connection_key recipient anywhere on the bill is the whole "+
		"precondition", refused.Error.Code, refused.Error.Message)

	// CONTROL C — the hub is still answering the bill's REAL recipient about this
	// same bill across the whole attempt, so none of the above is an outage, a
	// deregistered pubkey or an aged-out bill (this round's mandatory control).
	legit, served := ask(t, nipcash.ScopeMine, recipientPriv)
	require.True(t, served)
	require.Len(t, rosterOf(t, legit), 1,
		"CONTROL C: the bill's real pubkey recipient must still get their own row")
	t.Log("AUDITB-SECA-F3 CONTROL C: the bill's real recipient is served throughout, " +
		"so the results above are the gate's behaviour and not a dead fixture")
}

// AUDIT-B SEC-A / F4 — cash_status publishes sha256(cash_secret).
//
// A cash-mode claim's db IdentityValue is "a one-way SHA-256 commitment of the
// slice's secret" (db/models.go:182-187), and cashwallet/create.go:639-644 calls it
// "an internal storage detail, never meant for the wire response".
//
// cash_status_controller.go:110-111 copies c.IdentityValue onto every
// RecipientStatus unconditionally, cash rows included — while that same file's
// header comment (lines 27-32) states the opposite: "a cash-mode slice's
// identity_value (always "") is now omitted from the response entirely". The
// omitempty tag only hides an EMPTY value; the value is not empty.
//
// Why it matters: cash_redeem_controller.go:100-105 says the per-connection claim
// throttle is "the ONLY throttle standing between a cash-mode slice and an attacker
// who's guessing at its secret, since a cash-mode redemption has no signature to
// forge — only a secret to guess." Handing out the commitment moves that guessing
// OFFLINE, where no throttle applies. mint_cash's own secrets are 32 random bytes
// (cashwallet/create.go:428-445) and safe; a cash_transfer cash-mode target's
// commitment is SUPPLIED BY THE CALLER, so its preimage entropy is not something
// the hub can know or enforce.
func TestAuditBSecA_CashStatusPublishesTheCashSecretCommitment(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, _, _, connPriv := privateDispatchFixture(t, svc)

	var wallet db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", walletPubkey).First(&wallet).Error)

	// A caller-chosen commitment, exactly as a cash_transfer cash-mode target
	// supplies one. Deliberately weak so the point is unmistakable.
	secret := "weak-passphrase"
	sum := sha256.Sum256([]byte(secret))
	commitment := hex.EncodeToString(sum[:])
	require.NoError(t, svc.DB.Create(&db.CashWalletClaim{
		WalletAppID:   wallet.ID,
		IdentityType:  db.CashIdentityCash,
		IdentityValue: commitment,
		AmountMloki:   4200,
	}).Error)

	hubXOnly := tests.RandomHex32()
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	item := transport.Item{
		ID: "1", Target: walletPubkey, Method: nipcash.MethodCashStatus,
		Params: json.RawMessage(`{"scope":"all"}`),
	}
	notAfter := time.Now().Add(60 * time.Second).Unix()
	b := billProofBinding(t, item, hubXOnly, nonce, notAfter)
	// Possession only: the bill proof. No slice proof, which is what a caller who
	// holds the TOKEN but not the SECRET has (cashctl's "no_embedded_secret" case).
	item.BillProof, err = transport.BuildBillProof(connPriv, b)
	require.NoError(t, err)

	res, served := nip47svc.ServePrivateItem(context.Background(), nil, item, PrivateItemBinding{
		HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter,
	})
	require.True(t, served)
	require.Nil(t, res.Error, "got %+v", res.Error)
	var out nipcash.CashStatusResult
	require.NoError(t, json.Unmarshal(res.Result, &out))

	var leaked string
	for _, r := range out.Recipients {
		if r.IdentityType == db.CashIdentityCash {
			leaked = r.IdentityValue
		}
	}
	require.Equal(t, commitment, leaked,
		"BUG: the cash slice's secret commitment was returned on the wire")
	t.Logf("AUDITB-SECA-F4 BUG PRESENT: cash_status returned identity_value=%s for the cash row, "+
		"which is sha256(%q). The controller's own header comment claims this field is always "+
		"empty for a cash row.", leaked, secret)

	// CONTROL — this is not the test inventing a field. An offline guess against the
	// disclosed value confirms the secret with no hub involvement at all, so the
	// per-connection claim throttle is not in the loop.
	guess := sha256.Sum256([]byte("weak-passphrase"))
	require.Equal(t, leaked, hex.EncodeToString(guess[:]),
		"CONTROL: the disclosed value is verifiable offline against a guessed secret")
	wrong := sha256.Sum256([]byte("weak-passphras"))
	require.NotEqual(t, leaked, hex.EncodeToString(wrong[:]),
		"CONTROL: and a wrong guess does not match, so it is a real oracle rather than a constant")
	t.Log("AUDITB-SECA-F4 CONTROL: the disclosed commitment is an offline oracle for the cash " +
		"secret — right guess matches, wrong guess does not, hub never contacted")
}
