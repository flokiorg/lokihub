package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

type consolidateFixture struct {
	svc       *tests.TestService
	hub       *db.App
	ownerPriv string
	ownerPub  string
	s1, s2    *db.App
	newPub    string
}

func newConsolidateFixture(t *testing.T) *consolidateFixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	priv := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(priv)
	s1 := guardSource(t, svc, hub, pub, 3000, 0, 0)
	tests.FundApp(svc, s1.ID, 3000, "s1-fund")
	s2 := guardSource(t, svc, hub, pub, 2000, 0, 0)
	tests.FundApp(svc, s2.ID, 2000, "s2-fund")
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	return &consolidateFixture{svc: svc, hub: hub, ownerPriv: priv, ownerPub: pub, s1: s1, s2: s2, newPub: newPub}
}

// sources are both sources, authorized to merge into the fixture's new pubkey,
// as cash_consolidate proofs.
func (f *consolidateFixture) sources(t *testing.T) []consolidateSourceParam {
	t.Helper()
	return asConsolidateProofs(t, []consolidateSourceParam{
		guardParam(t, f.s1, f.ownerPriv, f.ownerPub, f.newPub, 3000),
		guardParam(t, f.s2, f.ownerPriv, f.ownerPub, f.newPub, 2000),
	})
}

func (f *consolidateFixture) params(t *testing.T) cashConsolidateParams {
	return cashConsolidateParams{
		Sources:     f.sources(t),
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: f.newPub},
		RequestID:   testRequestID(),
	}
}

// Step 2's refusal is one code and one message whatever failed: a source not
// held, no slice for the identity, a proof by another key, a proof bound to
// another amount. Nothing is marked.
func TestConsolidate_UniformRefusal(t *testing.T) {
	variants := map[string]func(t *testing.T, f *consolidateFixture, p *cashConsolidateParams){
		"a source not held": func(t *testing.T, f *consolidateFixture, p *cashConsolidateParams) {
			p.Sources[1].WalletPubkey = tests.RandomHex32()
		},
		"no slice for the identity": func(t *testing.T, f *consolidateFixture, p *cashConsolidateParams) {
			p.Sources[1].IdentityValue = tests.RandomHex32()
		},
		"a proof by another key": func(t *testing.T, f *consolidateFixture, p *cashConsolidateParams) {
			stranger := nostr.GeneratePrivateKey()
			p.Sources[1] = guardParam(t, f.s2, stranger, f.ownerPub, f.newPub, 2000)
		},
		"a proof bound to another amount": func(t *testing.T, f *consolidateFixture, p *cashConsolidateParams) {
			p.Sources[1] = guardParam(t, f.s2, f.ownerPriv, f.ownerPub, f.newPub, 1999)
		},
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			f := newConsolidateFixture(t)
			p := f.params(t)
			mutate(t, f, &p)
			resp := handleCashConsolidateFor(t, f.svc, NewTestNip47Controller(f.svc), f.s1, p)
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ERROR_UNAUTHORIZED, resp.Error.Code)
			assert.Equal(t, "sources not authorized", resp.Error.Message)
			assert.False(t, isUsed(t, f.svc, p.RequestID), "an unauthorized call leaves its id unused")
		})
	}
}

// The item proof must be sources[0]'s claim signer's.
func TestConsolidate_ItemSignerMustBeTheFirstSourcesSigner(t *testing.T) {
	f := newConsolidateFixture(t)
	p := f.params(t)
	call := testPrivateCall(p.Sources[0].IdentityEvent)
	call.ItemSigner, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	resp := consolidateAs(t, f.svc, NewTestNip47Controller(f.svc), f.s1, p, call)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, resp.Error.Code)
	assert.False(t, isUsed(t, f.svc, p.RequestID))
}

// The item's target is sources[0]: an item on another bill is BAD_REQUEST,
// before anything about the sources is looked at.
func TestConsolidate_TargetIsTheFirstSource(t *testing.T) {
	f := newConsolidateFixture(t)
	p := f.params(t)
	resp := consolidateAs(t, f.svc, NewTestNip47Controller(f.svc), f.s2, p, testPrivateCall(p.Sources[0].IdentityEvent))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "first source")
}

// After authorization the id is used even when the sources' state then refuses
// the call (RESTRICTED); a resend is REQUEST_REUSED.
func TestConsolidate_MarkedOnceAuthorized(t *testing.T) {
	f := newConsolidateFixture(t)
	require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ?", f.s2.ID).
		Update("min_transfer_mloki", 7).Error)
	p := f.params(t)
	resp := handleCashConsolidateFor(t, f.svc, NewTestNip47Controller(f.svc), f.s1, p)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
	assert.True(t, isUsed(t, f.svc, p.RequestID))

	resp = handleCashConsolidateFor(t, f.svc, NewTestNip47Controller(f.svc), f.s1, p)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_REQUEST_REUSED, resp.Error.Code)
}

// The record commits with the merge: its target is sources[0], its authorizer
// and delivery key sources[0]'s signer for a pubkey target (none for a cash
// one, whose token travels in the clear), one merged bill; every proof is
// consumed.
func TestConsolidate_Record(t *testing.T) {
	for _, cashTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "pubkey target", true: "cash target"}[cashTarget], func(t *testing.T) {
			f := newConsolidateFixture(t)
			newType, newValue := db.CashIdentityPubkey, f.newPub
			if cashTarget {
				_, newValue = cashSecretAndHash(t)
				newType = db.CashIdentityCash
			}
			proof := func(w *db.App, amount uint64) consolidateSourceParam {
				ev := buildTransferProofEvent(t, f.ownerPriv, *w.WalletPubkey, newType, newValue, "", amount, nil, time.Now())
				return consolidateSourceParam{WalletPubkey: *w.WalletPubkey, IdentityType: db.CashIdentityPubkey,
					IdentityValue: f.ownerPub, IdentityEvent: mustMarshal(t, ev)}
			}
			p := cashConsolidateParams{
				Sources:     []consolidateSourceParam{proof(f.s1, 3000), proof(f.s2, 2000)},
				NewIdentity: cashTransferNewIdentityParam{IdentityType: newType, IdentityValue: newValue},
				RequestID:   testRequestID(),
			}
			resp := handleCashConsolidateFor(t, f.svc, NewTestNip47Controller(f.svc), f.s1, p)
			require.Nil(t, resp.Error)
			result := resp.Result.(cashConsolidateResponse)

			rec, err := cashrequest.New(f.svc.DB).Get(p.RequestID)
			require.NoError(t, err)
			require.NotNil(t, rec)
			assert.Equal(t, constants.NIP47MethodCashConsolidate, rec.Method)
			assert.Equal(t, *f.s1.WalletPubkey, rec.TargetWalletPubkey)
			assert.Equal(t, f.s1.AppPubkey, rec.TargetConnPubkey)
			assert.Equal(t, "pubkey", rec.AuthorizerKind)
			assert.Equal(t, f.ownerPub, rec.Authorizer)
			if cashTarget {
				assert.Nil(t, rec.DeliveryPubkey)
			} else {
				require.NotNil(t, rec.DeliveryPubkey)
				assert.Equal(t, f.ownerPub, *rec.DeliveryPubkey)
			}
			bills, err := cashrequest.New(f.svc.DB).Bills(rec.ID)
			require.NoError(t, err)
			require.Len(t, bills, 1)
			assert.Equal(t, db.CashRecordBillMerged, bills[0].Role)
			assert.Equal(t, result.NewWalletPubkey, bills[0].WalletPubkey)
			var consumed int64
			require.NoError(t, f.svc.DB.Model(&db.CashConsumedProof{}).Count(&consumed).Error)
			assert.EqualValues(t, 2, consumed)
		})
	}
}

// The live-bill cap bounds a consolidate too (QUOTA_EXCEEDED).
func TestConsolidate_LiveBillCap(t *testing.T) {
	f := newConsolidateFixture(t)
	var live int64
	require.NoError(t, f.svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", f.hub.ID, db.AppKindCashWallet).Count(&live).Error)
	f.svc.Cfg.GetEnv().CashMaxLiveBillsPerHub = int(live)
	p := f.params(t)
	resp := handleCashConsolidateFor(t, f.svc, NewTestNip47Controller(f.svc), f.s1, p)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, resp.Error.Code)
	assert.Nil(t, cashWalletClaimByIdentity(t, f.svc, f.s1.ID, db.CashIdentityPubkey, f.ownerPub).ClaimedAt)
}
