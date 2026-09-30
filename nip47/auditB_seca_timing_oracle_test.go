package nip47

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// AUDIT-B SEC-A / F2 — is the OMISSION information-free?
//
// ServePrivateItem omits an item in both of these cases, and the design says the
// two must be indistinguishable (private_dispatch.go:36-40, 96-98):
//
//	(a) target names no bill this hub has          -> gate 2 misses, omit
//	(b) target names a real bill, proof is foreign  -> gate 2.5 fails, omit
//
// The CONTENT is identical: nothing is emitted either way. The COST is not. Path
// (b) reaches transport.VerifyBillProof, whose own doc comment measures the
// secp256k1 verification at ~373us and deliberately puts it LAST so a failing
// binding is cheap. An attacker who supplies a structurally PERFECT proof signed
// with their own key makes every structural comparison pass, so the signature is
// verified in full and only then does signer == app.AppPubkey fail.
//
// Path (a) verifies nothing at all.
//
// There is no rate limit and no proof-of-work anywhere before gate 2.5
// (acceptsPrivateEvent: kind, p-tag, minimum length; unwrap: decrypt, decode,
// freshness, nonce), so this measurement can be repeated without bound, and
// MaxItems=32 lets one envelope carry 32 copies of it.
func TestAuditBSecA_OmissionLeaksBillExistenceByTiming(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, _, _, connPriv := privateDispatchFixture(t, svc)
	absent := tests.RandomHex32() // a wallet pubkey this hub has never served

	hubXOnly := tests.RandomHex32()
	attackerPriv := nostr.GeneratePrivateKey() // NOT the bill's connection key
	ctx := context.Background()

	// oneProbe builds a structurally flawless item for `target`, signed by the
	// attacker, and times the hub's refusal.
	oneProbe := func(t *testing.T, target string) (time.Duration, bool) {
		nonce, err := transport.NewNonce()
		require.NoError(t, err)
		item := transport.Item{
			ID: "1", Target: target, Method: nipcash.MethodCashStatus,
			Params: json.RawMessage(`{}`),
		}
		notAfter := time.Now().Add(60 * time.Second).Unix()
		b := billProofBinding(t, item, hubXOnly, nonce, notAfter)
		item.Proof, err = transport.BuildItemProof(attackerPriv, b)
		require.NoError(t, err)
		item.BillProof, err = transport.BuildBillProof(attackerPriv, b)
		require.NoError(t, err)

		start := time.Now()
		_, served := nip47svc.ServePrivateItem(ctx, nil, item, PrivateItemBinding{
			HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter,
		})
		return time.Since(start), served
	}

	// CONTROL 1 — both probes really are omissions. If either answered, the whole
	// premise (that content is identical) would be wrong and this would not be a
	// timing finding at all.
	dPresent, servedPresent := oneProbe(t, walletPubkey)
	dAbsent, servedAbsent := oneProbe(t, absent)
	require.False(t, servedPresent, "CONTROL 1: a foreign proof on a REAL bill must be omitted")
	require.False(t, servedAbsent, "CONTROL 1: an unknown target must be omitted")
	t.Logf("AUDITB-SECA-F2 CONTROL 1: both probes omitted (served=false), so nothing on the "+
		"wire distinguishes them. first samples: present=%s absent=%s", dPresent, dAbsent)

	// CONTROL 2 — the hub is still answering the RIGHT holder about that same bill
	// across the whole attempt (this round's mandatory control). Silence is not
	// because the bill aged out or the backend stopped.
	{
		nonce, err := transport.NewNonce()
		require.NoError(t, err)
		item := transport.Item{
			ID: "ctl", Target: walletPubkey, Method: nipcash.MethodCashStatus,
			Params: json.RawMessage(`{}`),
		}
		notAfter := time.Now().Add(60 * time.Second).Unix()
		b := billProofBinding(t, item, hubXOnly, nonce, notAfter)
		item.Proof, err = transport.BuildItemProof(attackerPriv, b)
		require.NoError(t, err)
		item.BillProof, err = transport.BuildBillProof(connPriv, b) // the REAL connection key
		require.NoError(t, err)
		_, served := nip47svc.ServePrivateItem(ctx, nil, item, PrivateItemBinding{
			HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter,
		})
		require.True(t, served,
			"CONTROL 2: the hub must still be answering the bill's real holder about this same bill")
		t.Log("AUDITB-SECA-F2 CONTROL 2: the bill is live and the hub answers its real holder, " +
			"so the omissions above are the gate and not an outage")
	}

	// Interleaved so clock drift and cache warming hit both arms equally.
	const samples = 150
	present := make([]time.Duration, 0, samples)
	absentD := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		d, _ := oneProbe(t, walletPubkey)
		present = append(present, d)
		d, _ = oneProbe(t, absent)
		absentD = append(absentD, d)
	}

	pMed, aMed := median(present), median(absentD)
	t.Logf("AUDITB-SECA-F2 MEASURED over %d interleaved samples:\n"+
		"    real bill,   foreign proof (gate 2.5, verifies a signature): median %s\n"+
		"    unknown target            (gate 2, no crypto at all):       median %s\n"+
		"    per-item delta %s   |   x32 items in one envelope: %s",
		samples, pMed, aMed, pMed-aMed, 32*(pMed-aMed))

	// Asserted, in BOTH directions, and on the ABSOLUTE difference.
	//
	// The original version of this check only logged, and only considered
	// pMed > aMed — so it could not fail, and would have reported "not demonstrated"
	// even if the absent path had become wildly slower. Either sign is a
	// distinguisher; an attacker does not care which way the clock leans, only that
	// it leans reliably.
	//
	// The bound is relative because absolute microseconds are machine-specific. 25%
	// of the smaller median is comfortably above the residual this hub now shows
	// (~13%, from the absent path running two queries — the lookup miss plus the
	// tombstone lookup — against the present path's single hit) and far below what
	// the defect produced (~97%, when only the present path paid for a signature
	// verification).
	//
	// This is explicitly NOT a claim of constant time. Making a database hit and a
	// miss cost the same is not achievable here; what is achievable, and what this
	// pins, is that the dominant term — the ~373us secp256k1 verification — no longer
	// falls on only one of the two paths.
	delta := pMed - aMed
	if delta < 0 {
		delta = -delta
	}
	smaller := pMed
	if aMed < smaller {
		smaller = aMed
	}
	ratio := float64(delta) / float64(smaller)
	t.Logf("AUDITB-SECA-F2 absolute skew %s = %.1f%% of the faster path", delta, ratio*100)

	require.Less(t, ratio, 0.25,
		"the two information-free omissions differ by %s (%.1f%% of the faster path), which is "+
			"an existence oracle: an outsider who can merely NAME a wallet pubkey learns whether "+
			"this hub serves it, unmetered and repeatable. present=%s absent=%s",
		delta, ratio*100, pMed, aMed)
}

func median(ds []time.Duration) time.Duration {
	c := append([]time.Duration(nil), ds...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2]
}
