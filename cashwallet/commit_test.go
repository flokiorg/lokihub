package cashwallet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// These are the crash-point tests of the one commit (NIP-CASH §Atomicity — One
// Commit): a fault at any stage of a mint, an in-place transfer, a full or
// partial split, or a consolidate of 2 or 5 sources leaves nothing behind.

const commitHubFunds = 100_000_000

type commitWorld struct {
	svc  *tests.TestService
	deps Deps
	hub  *db.App
}

func newCommitWorld(t *testing.T) *commitWorld {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := tests.CreateCashHub(t, svc, 10_000_000, 3600)
	tests.FundApp(svc, hub.ID, commitHubFunds, "hubfund")
	return &commitWorld{svc: svc, deps: newTestDeps(svc), hub: hub}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// mintBill mints a bill with one pubkey slice per amount, without hooks.
func (w *commitWorld) mintBill(t *testing.T, amounts ...uint64) (*db.App, []apps.SliceRef) {
	t.Helper()
	recipients := make([]RecipientInput, len(amounts))
	for i, a := range amounts {
		pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
		recipients[i] = RecipientInput{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: a}
	}
	deps := w.deps
	deps.Hooks = nil
	res, err := Create(context.TODO(), deps, Params{HubApp: w.hub, Recipients: recipients, ExpirySecs: 1800})
	require.NoError(t, err)
	var claims []db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", res.WalletApp.ID).Order("id").Find(&claims).Error)
	refs := make([]apps.SliceRef, len(claims))
	for i, c := range claims {
		refs[i] = apps.SliceRef{ClaimID: c.ID, IdentityType: c.IdentityType, IdentityValue: c.IdentityValue, TransferCount: c.TransferCount}
	}
	return res.WalletApp, refs
}

func (w *commitWorld) record(t *testing.T, method string, deadline int64) *db.CashRequestRecord {
	return &db.CashRequestRecord{
		RequestID: randomHex(t, 32), Method: method, HubAppID: w.hub.ID,
		TargetWalletPubkey: randomHex(t, 32), TargetConnPubkey: randomHex(t, 32),
		AuthorizerKind: "pubkey", Authorizer: randomHex(t, 32), Deadline: deadline, ResultMeta: "{}",
	}
}

func (w *commitWorld) proof(t *testing.T, method string) ClaimProof {
	return ClaimProof{EventID: randomHex(t, 32), CreatedAt: time.Now().Unix(), Method: method}
}

// commitOp is one kind of commit, prepared on a world and run with its deps.
type commitOp struct {
	name    string
	prepare func(t *testing.T, w *commitWorld) func(deps Deps) error
}

func farDeadline() int64 { return time.Now().Add(time.Hour).Unix() }

func newPubkey() string {
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	return pk
}

var commitOps = []commitOp{
	{"mint", func(t *testing.T, w *commitWorld) func(Deps) error {
		resolved, err := Resolve(context.TODO(), w.deps, Params{HubApp: w.hub, Recipients: onePubkeyRecipient(4_000), ExpirySecs: 1800})
		require.NoError(t, err)
		rec := w.record(t, "mint_cash", farDeadline())
		return func(deps Deps) error { _, err := Commit(context.TODO(), deps, resolved, rec); return err }
	}},
	{"in-place transfer", func(t *testing.T, w *commitWorld) func(Deps) error {
		bill, refs := w.mintBill(t, 3_000)
		rec := w.record(t, "cash_transfer", farDeadline())
		proof := w.proof(t, "cash_transfer")
		return func(deps Deps) error {
			return Reassign(context.TODO(), deps, ReassignParams{WalletApp: bill, Source: refs[0],
				NewIdentityType: db.CashIdentityPubkey, NewIdentityValue: newPubkey(), Proofs: []ClaimProof{proof}, Record: rec})
		}
	}},
	{"full spin-off", func(t *testing.T, w *commitWorld) func(Deps) error {
		bill, refs := w.mintBill(t, 3_000, 1_000)
		rec := w.record(t, "cash_transfer", farDeadline())
		proof := w.proof(t, "cash_transfer")
		return func(deps Deps) error {
			_, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{HubApp: w.hub, SourceWalletApp: bill, Source: refs[0],
				CarvedIdentityType: db.CashIdentityPubkey, CarvedIdentityValue: newPubkey(), CarvedAmountMloki: 3_000,
				ExpiresAt: bill.ExpiresAt, Proofs: []ClaimProof{proof}, Record: rec})
			return err
		}
	}},
	{"partial split", func(t *testing.T, w *commitWorld) func(Deps) error {
		bill, refs := w.mintBill(t, 5_000)
		rec := w.record(t, "cash_transfer", farDeadline())
		proof := w.proof(t, "cash_transfer")
		return func(deps Deps) error {
			_, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{HubApp: w.hub, SourceWalletApp: bill, Source: refs[0],
				CarvedIdentityType: db.CashIdentityPubkey, CarvedIdentityValue: newPubkey(), CarvedAmountMloki: 2_000,
				RemainderIdentityType: refs[0].IdentityType, RemainderIdentityValue: refs[0].IdentityValue, RemainderAmountMloki: 3_000,
				ExpiresAt: bill.ExpiresAt, Proofs: []ClaimProof{proof}, Record: rec})
			return err
		}
	}},
	{"consolidate 2", consolidateOp(2)},
	{"consolidate 5", consolidateOp(5)},
}

func consolidateOp(n int) func(t *testing.T, w *commitWorld) func(Deps) error {
	return func(t *testing.T, w *commitWorld) func(Deps) error {
		sources := make([]ConsolidateSource, n)
		proofs := make([]ClaimProof, n)
		for i := range sources {
			bill, refs := w.mintBill(t, uint64(1_000*(i+1)))                                                      //nolint:gosec // small test amounts
			sources[i] = ConsolidateSource{WalletApp: bill, Slice: refs[0], AmountMloki: uint64(1_000 * (i + 1))} //nolint:gosec // small test amounts
			proofs[i] = w.proof(t, "cash_consolidate")
		}
		rec := w.record(t, "cash_consolidate", farDeadline())
		return func(deps Deps) error {
			_, err := Consolidate(context.TODO(), deps, ConsolidateParams{HubApp: w.hub, Sources: sources,
				NewIdentityType: db.CashIdentityPubkey, NewIdentityValue: newPubkey(), Proofs: proofs, Record: rec})
			return err
		}
	}
}

// commitSnapshot is everything a commit may write.
type commitSnapshot struct {
	Apps        int64
	Claims      []string
	Txs         int64
	Records     int64
	RecordBills int64
	Proofs      int64
	Balances    map[uint]int64
}

func snapshotOf(t *testing.T, w *commitWorld) commitSnapshot {
	t.Helper()
	d := w.svc.DB
	var s commitSnapshot
	require.NoError(t, d.Model(&db.App{}).Count(&s.Apps).Error)
	require.NoError(t, d.Model(&db.Transaction{}).Count(&s.Txs).Error)
	require.NoError(t, d.Model(&db.CashRequestRecord{}).Count(&s.Records).Error)
	require.NoError(t, d.Model(&db.CashRequestRecordBill{}).Count(&s.RecordBills).Error)
	require.NoError(t, d.Model(&db.CashConsumedProof{}).Count(&s.Proofs).Error)
	var claims []db.CashWalletClaim
	require.NoError(t, d.Order("id").Find(&claims).Error)
	for _, c := range claims {
		s.Claims = append(s.Claims, fmt.Sprintf("%d:%s:%s:%v:%d:%v", c.ID, c.IdentityType, c.IdentityValue,
			c.ClaimedAt != nil, c.TransferCount, c.SpunOffToWalletAppID != nil))
	}
	var all []db.App
	require.NoError(t, d.Find(&all).Error)
	s.Balances = map[uint]int64{}
	for _, a := range all {
		s.Balances[a.ID] = queries.GetIsolatedBalance(d, a.ID)
	}
	return s
}

// stagesOf runs op once with a recording hook and returns the stages it passed.
func stagesOf(t *testing.T, op commitOp) []string {
	t.Helper()
	w := newCommitWorld(t)
	run := op.prepare(t, w)
	var stages []string
	deps := w.deps
	deps.Hooks = &TestHooks{Before: func(stage string) error { stages = append(stages, stage); return nil }}
	require.NoError(t, run(deps))
	return stages
}

// U-4.1 and U-4.2: an error or a panic at every stage of every commit leaves no
// new app, no slice change, no transfer, no record and no consumed proof.
func TestCommit_FaultAtEveryStageLeavesNothing(t *testing.T) {
	for _, op := range commitOps {
		stages := stagesOf(t, op)
		require.Contains(t, stages, StageBeforeCommit, op.name)
		for _, stage := range stages {
			for _, mode := range []string{"error", "panic"} {
				t.Run(op.name+"/"+stage+"/"+mode, func(t *testing.T) {
					w := newCommitWorld(t)
					run := op.prepare(t, w)
					before := snapshotOf(t, w)

					injected := errors.New("injected at " + stage)
					deps := w.deps
					deps.Hooks = &TestHooks{Before: func(s string) error {
						if s != stage {
							return nil
						}
						if mode == "panic" {
							panic(injected)
						}
						return injected
					}}
					err := func() (err error) {
						defer func() {
							if r := recover(); r != nil {
								err = fmt.Errorf("panicked: %v", r)
							}
						}()
						return run(deps)
					}()
					require.Error(t, err)
					assert.Contains(t, err.Error(), "injected at "+stage)
					assert.Equal(t, before, snapshotOf(t, w), "a commit that failed at %s left something behind", stage)

					// The database is not left locked: a later commit goes through.
					_, _ = w.mintBill(t, 1_000)
				})
			}
		}
	}
}

// U-4.3: a call that committed is all there, and its record holds what a lookup
// needs to rebuild every token byte for byte (the secret is re-derived).
func TestCommit_RecordRebuildsEveryTokenByteForByte(t *testing.T) {
	w := newCommitWorld(t)
	bill, refs := w.mintBill(t, 5_000)
	rec := w.record(t, "cash_transfer", farDeadline())
	res, err := SplitInTwo(context.TODO(), w.deps, SplitInTwoParams{HubApp: w.hub, SourceWalletApp: bill, Source: refs[0],
		CarvedIdentityType: db.CashIdentityCash, CarvedIdentityValue: randomHex(t, 32), CarvedAmountMloki: 2_000,
		RemainderIdentityType: refs[0].IdentityType, RemainderIdentityValue: refs[0].IdentityValue, RemainderAmountMloki: 3_000,
		ExpiresAt: bill.ExpiresAt, Record: rec})
	require.NoError(t, err)

	stored, err := cashrequest.New(w.svc.DB).Get(rec.RequestID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	bills, err := cashrequest.New(w.svc.DB).Bills(stored.ID)
	require.NoError(t, err)
	require.Len(t, bills, 2)
	assert.Equal(t, db.CashRecordBillCarved, bills[0].Role)
	assert.Equal(t, db.CashRecordBillRemainder, bills[1].Role)

	for i, want := range []string{res.Carved.CashToken, res.Remainder.CashToken} {
		b := bills[i]
		secret, err := w.svc.Keys.GetCashPairingKey(b.WalletAppID)
		require.NoError(t, err)
		var relays []string
		require.NoError(t, json.Unmarshal([]byte(b.RelayHints), &relays))
		sig, err := hex.DecodeString(b.Signature)
		require.NoError(t, err)
		amount := uint64(b.AmountMillis) //nolint:gosec // positive
		tok := nipcash.Token{HRP: constants.CashTokenHRP, WalletPubkey: b.WalletPubkey, Secret: secret, RelayURLs: relays,
			MintSignature: sig, AttestedAmountMillis: &amount, Scheme: nipcash.SignatureScheme(b.Scheme)}
		if b.IdentityRequired != nil {
			v := *b.IdentityRequired == 1
			tok.IdentityRequired = &v
		}
		if b.HubGroup != nil {
			g, err := hex.DecodeString(*b.HubGroup)
			require.NoError(t, err)
			tok.HubGroup = g
		}
		rebuilt, err := nipcash.Encode(tok)
		require.NoError(t, err)
		assert.Equal(t, want, rebuilt, "bill %d", i)
	}

	// The source is claimed terminal and points at the carved bill; both new bills
	// name it as their source.
	var source db.CashWalletClaim
	require.NoError(t, w.svc.DB.First(&source, refs[0].ClaimID).Error)
	require.NotNil(t, source.ClaimedAt)
	require.NotNil(t, source.SpunOffToWalletAppID)
	assert.Equal(t, res.Carved.WalletApp.ID, *source.SpunOffToWalletAppID)
	for _, b := range []*SplitResult{res.Carved, res.Remainder} {
		var app db.App
		require.NoError(t, w.svc.DB.First(&app, b.WalletApp.ID).Error)
		require.NotNil(t, app.SplitFromWalletAppID)
		assert.Equal(t, bill.ID, *app.SplitFromWalletAppID)
		assert.Equal(t, b.CashToken, app.CashToken, "the token is stored in the same commit")
	}
}

// U-4.4: the deadline is checked as the last statement before COMMIT: a call
// reaching it at D commits, one reaching it at D+1 is refused with nothing written.
func TestCommit_DeadlineCrossing(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late=%v", late), func(t *testing.T) {
			w := newCommitWorld(t)
			resolved, err := Resolve(context.TODO(), w.deps, Params{HubApp: w.hub, Recipients: onePubkeyRecipient(4_000), ExpirySecs: 1800})
			require.NoError(t, err)
			d := time.Now().Add(time.Minute).Truncate(time.Second)
			rec := w.record(t, "mint_cash", d.Unix())
			before := snapshotOf(t, w)

			clock := time.Now()
			var mu sync.Mutex
			deps := w.deps
			deps.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
			deps.Hooks = &TestHooks{Before: func(stage string) error {
				if stage == StageBeforeCommit {
					mu.Lock()
					clock = d
					if late {
						clock = d.Add(time.Second)
					}
					mu.Unlock()
				}
				return nil
			}}
			_, err = Commit(context.TODO(), deps, resolved, rec)
			if !late {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, cashrequest.ErrDeadlinePassed)
			assert.Equal(t, before, snapshotOf(t, w))
		})
	}
}

// stallingSigner is a node whose signer never answers.
type stallingSigner struct{ lnclient.LNClient }

func (stallingSigner) SignMessage(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// U-4.5: a signer that stalls past its timeout fails the call with nothing
// committed, and other writers are not held past their own busy timeout.
func TestCommit_StalledSignerFailsWithinTheWriteLock(t *testing.T) {
	w := newCommitWorld(t)
	resolved, err := Resolve(context.TODO(), w.deps, Params{HubApp: w.hub, Recipients: onePubkeyRecipient(4_000), ExpirySecs: 1800})
	require.NoError(t, err)
	before := snapshotOf(t, w)

	deps := w.deps
	deps.LNClient = stallingSigner{w.deps.LNClient}
	signing := make(chan struct{})
	deps.Hooks = &TestHooks{Before: func(stage string) error {
		if stage == StageAfterCreateApp {
			close(signing)
		}
		return nil
	}}

	var probeErr error
	var probeTook time.Duration
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-signing
		start := time.Now()
		probeErr = w.svc.DB.Create(&db.CashUsedRequestID{RequestID: randomHex(t, 32), Method: "probe", UsedAt: 1}).Error
		probeTook = time.Since(start)
	}()

	_, err = Commit(context.TODO(), deps, resolved, nil)
	require.Error(t, err)
	<-done
	require.NoError(t, probeErr, "a writer waiting behind the stalled commit must get through")
	assert.Less(t, probeTook, 5*time.Second)

	after := snapshotOf(t, w)
	assert.Equal(t, before, after)
}

// notifySpy records each NotifyAppCreated and whether the app was already
// visible outside the commit's transaction.
type notifySpy struct {
	apps.AppsService
	w        *commitWorld
	notified []uint
	visible  []bool
}

func (s *notifySpy) NotifyAppCreated(app *db.App) {
	var n int64
	s.w.svc.DB.Model(&db.App{}).Where("id = ?", app.ID).Count(&n)
	s.notified = append(s.notified, app.ID)
	s.visible = append(s.visible, n == 1)
	s.AppsService.NotifyAppCreated(app)
}

// U-4.6: a new bill is announced only after it committed, and never when its
// commit rolled back.
func TestCommit_AnnouncesOnlyCommittedBills(t *testing.T) {
	w := newCommitWorld(t)
	bill, refs := w.mintBill(t, 5_000)
	spy := &notifySpy{AppsService: w.deps.AppsService, w: w}
	deps := w.deps
	deps.AppsService = spy

	params := SplitInTwoParams{HubApp: w.hub, SourceWalletApp: bill, Source: refs[0],
		CarvedIdentityType: db.CashIdentityPubkey, CarvedIdentityValue: newPubkey(), CarvedAmountMloki: 2_000,
		RemainderIdentityType: refs[0].IdentityType, RemainderIdentityValue: refs[0].IdentityValue, RemainderAmountMloki: 3_000,
		ExpiresAt: bill.ExpiresAt}

	failing := deps
	failing.Hooks = &TestHooks{Before: func(stage string) error {
		if stage == StageBeforeCommit {
			return errors.New("injected")
		}
		return nil
	}}
	_, err := SplitInTwo(context.TODO(), failing, params)
	require.Error(t, err)
	assert.Empty(t, spy.notified, "a rolled-back bill is never announced")

	res, err := SplitInTwo(context.TODO(), deps, params)
	require.NoError(t, err)
	assert.Equal(t, []uint{res.Carved.WalletApp.ID, res.Remainder.WalletApp.ID}, spy.notified)
	assert.Equal(t, []bool{true, true}, spy.visible, "each bill is announced only once it is committed")
}

// U-4.7: a claim proof stays usable when its call is refused or rolls back, and
// is consumed when its call commits; a second call on it is ErrProofUsed.
func TestCommit_ProofConsumedOnlyByACommit(t *testing.T) {
	w := newCommitWorld(t)
	bill, refs := w.mintBill(t, 3_000)
	proof := w.proof(t, "cash_transfer")
	store := cashrequest.New(w.svc.DB)

	failing := w.deps
	failing.Hooks = &TestHooks{Before: func(stage string) error {
		if strings.HasPrefix(stage, StageAfterProof) {
			return errors.New("injected")
		}
		return nil
	}}
	params := ReassignParams{WalletApp: bill, Source: refs[0], NewIdentityType: db.CashIdentityPubkey,
		NewIdentityValue: newPubkey(), Proofs: []ClaimProof{proof}}
	require.Error(t, Reassign(context.TODO(), failing, params))
	used, err := store.IsProofConsumed(proof.EventID)
	require.NoError(t, err)
	assert.False(t, used, "a rolled-back call leaves its proof usable")

	require.NoError(t, Reassign(context.TODO(), w.deps, params))
	used, err = store.IsProofConsumed(proof.EventID)
	require.NoError(t, err)
	assert.True(t, used)

	// The same proof on another call, racing past any pre-check, loses in the commit.
	other, otherRefs := w.mintBill(t, 1_000)
	err = Reassign(context.TODO(), w.deps, ReassignParams{WalletApp: other, Source: otherRefs[0],
		NewIdentityType: db.CashIdentityPubkey, NewIdentityValue: newPubkey(), Proofs: []ClaimProof{proof}})
	require.ErrorIs(t, err, ErrProofUsed)
	var still db.CashWalletClaim
	require.NoError(t, w.svc.DB.First(&still, otherRefs[0].ClaimID).Error)
	assert.Equal(t, otherRefs[0].IdentityValue, still.IdentityValue, "the losing call changed nothing")
}

// A slice claimed or transferred by another call after this one read it fails
// the commit with apps.ErrSliceClaimed and nothing written.
func TestCommit_StaleSliceIsRefused(t *testing.T) {
	w := newCommitWorld(t)
	bill, refs := w.mintBill(t, 3_000)
	require.NoError(t, Reassign(context.TODO(), w.deps, ReassignParams{WalletApp: bill, Source: refs[0],
		NewIdentityType: db.CashIdentityPubkey, NewIdentityValue: newPubkey()}))
	before := snapshotOf(t, w)
	_, err := SplitInTwo(context.TODO(), w.deps, SplitInTwoParams{HubApp: w.hub, SourceWalletApp: bill, Source: refs[0],
		CarvedIdentityType: db.CashIdentityPubkey, CarvedIdentityValue: newPubkey(), CarvedAmountMloki: 3_000})
	require.ErrorIs(t, err, apps.ErrSliceClaimed)
	assert.Equal(t, before, snapshotOf(t, w))
}

// U-4.8: across random mints, transfers, splits and consolidates with random
// faults injected at random stages, value is conserved: the Hub plus every bill
// always hold the initial funds, and every bill holds exactly its unclaimed
// slices.
func TestCommit_ConservationUnderRandomFaults(t *testing.T) {
	w := newCommitWorld(t)
	rng := mrand.New(mrand.NewSource(20261027)) //nolint:gosec // reproducible test sequence
	stages := []string{StageAfterLock, StageAfterCreateApp, StageAfterSign, StageAfterClaims,
		StageAfterSourceClaim + "(0)", StageAfterReassign, StageAfterTransfer + "(0)", StageAfterTransfer + "(1)",
		StageAfterProof + "(0)", StageAfterRecord, StageBeforeCommit}

	type live struct {
		bill *db.App
		ref  apps.SliceRef
		amt  uint64
	}
	unclaimed := func() []live {
		var claims []db.CashWalletClaim
		require.NoError(t, w.svc.DB.Where("claimed_at IS NULL").Order("id").Find(&claims).Error)
		out := make([]live, 0, len(claims))
		for _, c := range claims {
			var app db.App
			require.NoError(t, w.svc.DB.First(&app, c.WalletAppID).Error)
			out = append(out, live{bill: &app, amt: uint64(c.AmountMloki), //nolint:gosec // positive
				ref: apps.SliceRef{ClaimID: c.ID, IdentityType: c.IdentityType, IdentityValue: c.IdentityValue, TransferCount: c.TransferCount}})
		}
		return out
	}

	const iterations = 500
	for i := 0; i < iterations; i++ {
		deps := w.deps
		if rng.Intn(3) == 0 {
			fault := stages[rng.Intn(len(stages))]
			deps.Hooks = &TestHooks{Before: func(stage string) error {
				if stage == fault {
					return errors.New("injected")
				}
				return nil
			}}
		}
		slices := unclaimed()
		switch op := rng.Intn(4); {
		case op == 0 || len(slices) < 2:
			n := 1 + rng.Intn(3)
			recipients := make([]RecipientInput, n)
			for j := range recipients {
				recipients[j] = RecipientInput{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey(),
					AmountMloki: uint64(1_000 + rng.Intn(50_000))} //nolint:gosec // small positive
			}
			_, _ = Create(context.TODO(), deps, Params{HubApp: w.hub, Recipients: recipients, ExpirySecs: 1800})
		case op == 1:
			s := slices[rng.Intn(len(slices))]
			_ = Reassign(context.TODO(), deps, ReassignParams{WalletApp: s.bill, Source: s.ref,
				NewIdentityType: db.CashIdentityPubkey, NewIdentityValue: newPubkey()})
		case op == 2:
			s := slices[rng.Intn(len(slices))]
			carved := s.amt
			p := SplitInTwoParams{HubApp: w.hub, SourceWalletApp: s.bill, Source: s.ref,
				CarvedIdentityType: db.CashIdentityPubkey, CarvedIdentityValue: newPubkey(), ExpiresAt: s.bill.ExpiresAt}
			if s.amt > 1 && rng.Intn(2) == 0 {
				carved = 1 + uint64(rng.Int63n(int64(s.amt-1))) //nolint:gosec // positive
				p.RemainderIdentityType, p.RemainderIdentityValue, p.RemainderAmountMloki = s.ref.IdentityType, s.ref.IdentityValue, s.amt-carved
			}
			p.CarvedAmountMloki = carved
			_, _ = SplitInTwo(context.TODO(), deps, p)
		default:
			rng.Shuffle(len(slices), func(a, b int) { slices[a], slices[b] = slices[b], slices[a] })
			n := 2 + rng.Intn(min(4, len(slices)-1))
			sources := make([]ConsolidateSource, n)
			for j := range sources {
				sources[j] = ConsolidateSource{WalletApp: slices[j].bill, Slice: slices[j].ref, AmountMloki: slices[j].amt}
			}
			_, _ = Consolidate(context.TODO(), deps, ConsolidateParams{HubApp: w.hub, Sources: sources,
				NewIdentityType: db.CashIdentityPubkey, NewIdentityValue: newPubkey()})
		}

		var all []db.App
		require.NoError(t, w.svc.DB.Find(&all).Error)
		var total int64
		for _, a := range all {
			bal := queries.GetIsolatedBalance(w.svc.DB, a.ID)
			total += bal
			if a.Kind == db.AppKindCashWallet {
				var owed int64
				require.NoError(t, w.svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ? AND claimed_at IS NULL", a.ID).
					Select("COALESCE(SUM(amount_mloki), 0)").Scan(&owed).Error)
				require.Equal(t, owed, bal, "iteration %d: bill %d holds exactly its unclaimed slices", i, a.ID)
			}
		}
		require.Equal(t, int64(commitHubFunds), total, "iteration %d: value conserved", i)
	}
}

// U-4.R2a: the compensating saga is gone, with every name it used.
func TestNoSagaSymbolsRemain(t *testing.T) {
	banned := []string{
		"Cash" + "StrandedFund", "CashBillOutcome" + "Void", "CashSliceStatus" + "Void",
		"fund" + "Internal", "FundInternal" + "Override", "deleteIf" + "Drained", "cash_stranded" + "_funds",
	}
	files := goFilesOfModule(t)
	sort.Strings(files)
	for _, f := range files {
		raw := readFile(t, f)
		for _, b := range banned {
			assert.False(t, strings.Contains(raw, b), "%s still names %s", f, b)
		}
	}
}
