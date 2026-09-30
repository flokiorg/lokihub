package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/tests"
)

// The kind-23193 BILL proof: possession of the bill's token, as distinct from the
// kind-23192 slice proof's "I am identity K".
//
// This gate is the one the standard transport had for free — a request there was
// encrypted to the bill's own wallet pubkey, so sending one proved possession — and
// which vanished when the envelope became addressed to the hub's inbox instead.
// Everything the hub is now willing to CONFIRM about a bill rests on it, so the
// attack surface is tested directly rather than inferred from the happy path.
//
// The stakes if it is wrong: a wallet pubkey is public and guessable, so a hub that
// answers without it becomes an oracle for every bill it ever minted.

// billProofBinding rebuilds the binding the hub computes for itself, so a test can
// forge a proof over any part of it.
func billProofBinding(t *testing.T, item transport.Item, hubXOnly, nonce string, notAfter int64) transport.ProofBinding {
	t.Helper()
	hash, err := transport.CanonicalParamsHash(item.Params)
	require.NoError(t, err)
	return transport.ProofBinding{
		Target: item.Target, HubXOnly: hubXOnly, Method: item.Method,
		ParamsHash: hash, Nonce: nonce, NotAfter: notAfter,
	}
}

// TestBillProof_ForgeriesAreAllOmitted is the attack surface, item by item.
//
// Every case here is a caller who does NOT hold the bill trying to look like one,
// and every one must be met with an omission — not an error, because an error
// confirms the bill exists and that is precisely what possession is meant to gate.
//
// Omission is also why these have to be enumerated. A hub that accepted any of them
// would leak silently: there is no error to notice, no log a client sees, and the
// only symptom is that guessing a wallet pubkey starts working.
func TestBillProof_ForgeriesAreAllOmitted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	attackerPriv := nostr.GeneratePrivateKey()

	// good is a fully valid item; each case below breaks exactly one thing about its
	// bill proof, so a failure names the specific protection that stopped working.
	good := func(id string) transport.Item {
		return buildItem(t, id, walletPubkey, constants.NIP47MethodCashStatus, `{}`,
			recipientPriv, connPriv, hubXOnly, nonce, notAfter)
	}

	for _, tc := range []struct {
		name string
		mut  func(transport.Item) transport.Item
		why  string
	}{
		{
			name: "signed by an attacker's own key",
			mut: func(it transport.Item) transport.Item {
				p, err := transport.BuildBillProof(attackerPriv,
					billProofBinding(t, it, hubXOnly, nonce, notAfter))
				require.NoError(t, err)
				it.BillProof = p
				return it
			},
			why: "anyone can generate a key; only the token's holder has THIS key",
		},
		{
			name: "signed by the recipient's identity key",
			mut: func(it transport.Item) transport.Item {
				p, err := transport.BuildBillProof(recipientPriv,
					billProofBinding(t, it, hubXOnly, nonce, notAfter))
				require.NoError(t, err)
				it.BillProof = p
				return it
			},
			why: "being a recipient is not the same as holding the token; the two keys are different",
		},
		{
			name: "the slice proof reused as the bill proof",
			mut: func(it transport.Item) transport.Item {
				it.BillProof = it.Proof
				return it
			},
			why: "the KIND is the only thing separating the two proofs",
		},
		{
			name: "a bill proof for a different bill",
			mut: func(it transport.Item) transport.Item {
				b := billProofBinding(t, it, hubXOnly, nonce, notAfter)
				b.Target = strings.Repeat("ef", 32)
				p, err := transport.BuildBillProof(connPriv, b)
				require.NoError(t, err)
				it.BillProof = p
				return it
			},
			why: "a proof must not be liftable onto another bill",
		},
		{
			name: "a bill proof for a different method",
			mut: func(it transport.Item) transport.Item {
				b := billProofBinding(t, it, hubXOnly, nonce, notAfter)
				b.Method = constants.NIP47MethodCashRedeem
				p, err := transport.BuildBillProof(connPriv, b)
				require.NoError(t, err)
				it.BillProof = p
				return it
			},
			why: "a status proof must not authorise a redeem",
		},
		{
			name: "a bill proof over different params",
			mut: func(it transport.Item) transport.Item {
				b := billProofBinding(t, it, hubXOnly, nonce, notAfter)
				b.ParamsHash = strings.Repeat("11", 32)
				p, err := transport.BuildBillProof(connPriv, b)
				require.NoError(t, err)
				it.BillProof = p
				return it
			},
			why: "an aggregator must not be able to re-point an item's params",
		},
		{
			name: "a bill proof from another envelope",
			mut: func(it transport.Item) transport.Item {
				b := billProofBinding(t, it, hubXOnly, nonce, notAfter)
				b.Nonce = strings.Repeat("77", 32)
				p, err := transport.BuildBillProof(connPriv, b)
				require.NoError(t, err)
				it.BillProof = p
				return it
			},
			why: "a banked proof must not be replayable later",
		},
		{
			name: "a bill proof bound to another hub",
			mut: func(it transport.Item) transport.Item {
				b := billProofBinding(t, it, hubXOnly, nonce, notAfter)
				b.HubXOnly = strings.Repeat("99", 32)
				p, err := transport.BuildBillProof(connPriv, b)
				require.NoError(t, err)
				it.BillProof = p
				return it
			},
			why: "an item must not be replayable at a different hub",
		},
		{
			name: "no bill proof at all",
			mut: func(it transport.Item) transport.Item {
				it.BillProof = nil
				return it
			},
			why: "absent must not read as satisfied",
		},
		{
			name: "an explicit JSON null bill proof",
			mut: func(it transport.Item) transport.Item {
				it.BillProof = json.RawMessage(`null`)
				return it
			},
			why: "null decodes to four bytes, so a length check would read it as present",
		},
		{
			name: "an empty-object bill proof",
			mut: func(it transport.Item) transport.Item {
				it.BillProof = json.RawMessage(`{}`)
				return it
			},
			why: "a well-formed but unsigned proof must not pass",
		},
		{
			name: "a tampered signature",
			mut: func(it transport.Item) transport.Item {
				var ev map[string]any
				require.NoError(t, json.Unmarshal(it.BillProof, &ev))
				sig, _ := ev["sig"].(string)
				require.NotEmpty(t, sig)
				// Flip one hex character of the signature.
				flipped := "0"
				if sig[0] == '0' {
					flipped = "1"
				}
				ev["sig"] = flipped + sig[1:]
				raw, err := json.Marshal(ev)
				require.NoError(t, err)
				it.BillProof = raw
				return it
			},
			why: "the signature must actually be verified, not merely present",
		},
		{
			name: "a stale bill proof",
			mut: func(it transport.Item) transport.Item {
				var ev map[string]any
				require.NoError(t, json.Unmarshal(it.BillProof, &ev))
				// Well outside transport.ProofFreshnessPast. The signature no longer
				// matches either, which is itself correct: created_at is signed.
				ev["created_at"] = time.Now().Add(-24 * time.Hour).Unix()
				raw, err := json.Marshal(ev)
				require.NoError(t, err)
				it.BillProof = raw
				return it
			},
			why: "a proof captured long ago must not stay usable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := tc.mut(good("f1"))
			result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
			assert.False(t, served, "must be OMITTED: %s", tc.why)
			assert.Empty(t, result.ID, "an omitted item must produce no result at all")
		})
	}
}

// TestBillProof_ValidProofButNoSliceIsARefusal is the behaviour the bill proof was
// added to make possible.
//
// The caller holds the bill — they proved it — but holds no slice of it. That is the
// single most common real mistake: being handed a token that names someone else.
//
// Before the bill proof this was an omission, which was correct when possession
// could not be proved and yet cost real information: "this bill is not addressed to
// you" became indistinguishable from "the hub is unreachable", and clients reported
// it as retryable, so they retried forever a token that would never be theirs.
func TestBillProof_ValidProofButNoSliceIsARefusal(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, _, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	// A stranger's SLICE proof — they hold no slice — with a VALID bill proof.
	strangerPriv := nostr.GeneratePrivateKey()
	item := buildItem(t, "n1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		strangerPriv, connPriv, hubXOnly, nonce, notAfter)

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	require.True(t, served, "possession was proved, so the hub must answer rather than go silent")
	require.NotNil(t, result.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, result.Error.Code)
	assert.Equal(t, "n1", result.ID)
}

// TestBillProof_MissingSliceProofOnIdentityBoundBillIsARefusal separates the other
// reason authorization can fail once possession is proved: the item was built wrong.
//
// Distinct from the case above because the two call for different actions — fix your
// client, versus this bill is not yours — and a single code for both would tell a
// caller neither.
func TestBillProof_MissingSliceProofOnIdentityBoundBillIsARefusal(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	item := buildItem(t, "b1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		recipientPriv, connPriv, hubXOnly, nonce, notAfter)
	item.Proof = nil // a bill cannot dodge its slice proof by looking bearer

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	require.True(t, served)
	require.NotNil(t, result.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, result.Error.Code)
}

// seedDestroyedBillWithConnKey extends seedDestroyedBill with the bill's connection
// key, which is what a private-transport tombstone request has to sign with.
//
// Nothing is stored to make that possible: the archive keeps the bill's app id, and
// the pairing key is deterministic from it, so a destroyed bill stays verifiable
// without retaining any secret.
func seedDestroyedBillWithConnKey(t *testing.T, svc *tests.TestService, endedAt time.Time, retentionSecs int) (walletPubkey, connPriv string) {
	t.Helper()
	walletAppID, walletPubkey := seedDestroyedBill(t, svc, endedAt, retentionSecs)
	connPriv, err := svc.Keys.GetCashPairingKey(walletAppID)
	require.NoError(t, err)
	return walletPubkey, connPriv
}

// TestPrivateTombstone_HolderInsideRetentionGetsSpent restores the three-outcome
// answer model on the private transport.
//
// Silence cannot be told apart from a hub that is slow or unreachable, which forces
// every client to pick a wrong default: treat it as retryable and a genuinely spent
// bill retries forever, or treat it as gone and an outage tells someone their funds
// are lost. The standard transport has answered this since tombstones existed; the
// private transport could not, because it had no way to verify the holder.
func TestPrivateTombstone_HolderInsideRetentionGetsSpent(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	endedAt := time.Now().Add(-1 * time.Minute)
	walletPubkey, connPriv := seedDestroyedBillWithConnKey(t, svc, endedAt, 3600)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	// No slice proof: the bill is gone, so there are no slices and no identity to
	// prove. Possession is the whole authorization here.
	item := buildItem(t, "t1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		nostr.GeneratePrivateKey(), connPriv, hubXOnly, nonce, notAfter)

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	require.True(t, served, "a destroyed bill inside retention must answer its holder definitively")
	require.Nil(t, result.Error, "a tombstone travels as a RESULT, not a transport error")

	var status nipcash.CashStatusResult
	require.NoError(t, json.Unmarshal(result.Result, &status))
	assert.Equal(t, nipcash.ErrorSpent, status.Error)
	require.NotNil(t, status.RetainedUntil, "a tombstone must say when the hub will stop answering")
	assert.Equal(t, endedAt.Add(3600*time.Second).Unix(), *status.RetainedUntil)
	assert.Empty(t, status.Recipients, "a destroyed bill has no roster to disclose")
}

// TestPrivateTombstone_NonHolderGetsSilence is the gate that keeps the tombstone
// from becoming the existence oracle that deleting the bill exists to remove.
func TestPrivateTombstone_NonHolderGetsSilence(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, _ := seedDestroyedBillWithConnKey(t, svc, time.Now().Add(-1*time.Minute), 3600)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	// Someone who guessed the wallet pubkey, signing with their own key.
	item := buildItem(t, "t2", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey(), hubXOnly, nonce, notAfter)

	_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	assert.False(t, served, "a tombstone for a guessed wallet pubkey would confirm the bill existed")
}

// TestPrivateTombstone_PastRetentionGetsSilence: retention is how long the hub is
// willing to keep talking about a destroyed bill. Past it, even its holder gets
// nothing — otherwise the hub would keep a permanently answerable record of every
// bill it ever minted.
func TestPrivateTombstone_PastRetentionGetsSilence(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	// Ended two hours ago with a one-hour window: the window has closed.
	walletPubkey, connPriv := seedDestroyedBillWithConnKey(t, svc, time.Now().Add(-2*time.Hour), 3600)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	item := buildItem(t, "t3", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		nostr.GeneratePrivateKey(), connPriv, hubXOnly, nonce, notAfter)

	_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	assert.False(t, served, "past retention the hub falls silent again, holder or not")
}

// TestPrivateTombstone_OnlyStatusIsAnswered: a redeem or transfer naming a destroyed
// bill keeps its silence even from the holder.
//
// Answering those would return a result_type that does not match the request, and
// would widen the disclosure past the three-outcome design agreed for the STATUS
// method alone. Same rule the standard transport applies.
func TestPrivateTombstone_OnlyStatusIsAnswered(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, connPriv := seedDestroyedBillWithConnKey(t, svc, time.Now().Add(-1*time.Minute), 3600)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	for _, method := range []string{
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer,
		constants.NIP47MethodCashConsolidate,
	} {
		t.Run(method, func(t *testing.T) {
			item := buildItem(t, "t4", walletPubkey, method, `{}`,
				nostr.GeneratePrivateKey(), connPriv, hubXOnly, nonce, notAfter)
			_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
			assert.False(t, served, "%s on a destroyed bill must stay silent", method)
		})
	}
}
