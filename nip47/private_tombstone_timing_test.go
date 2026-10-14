package nip47

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Archival on Deletion: a request naming an archived wallet's pubkey MUST be
// met with silence identical to one naming a pubkey the Hub never served. Identical in
// cost too: a bill destroyed inside its retention window must not take measurably longer
// to refuse a foreign proof than an unknown pubkey does. Same 25% bound as
// TestAuditBSecA_OmissionLeaksBillExistenceByTiming applies to live bills.
func TestPrivateTombstone_DestroyedBillOmissionIsTimingIndistinguishable(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	destroyed, _ := seedDestroyedBillWithConnKey(t, svc, time.Now().Add(-1*time.Minute), 3600)
	absent := tests.RandomHex32()

	hubXOnly := strings.Repeat("ab", 32)
	attacker := nostr.GeneratePrivateKey()
	probe := func(target string) (time.Duration, bool) {
		nonce := tests.RandomHex32()
		notAfter := time.Now().Add(time.Minute).Unix()
		item := buildItem(t, "x", target, constants.NIP47MethodCashStatus, `{}`,
			attacker, attacker, hubXOnly, nonce, notAfter)
		start := time.Now()
		_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
			PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
		return time.Since(start), served
	}

	_, s1 := probe(destroyed)
	_, s2 := probe(absent)
	require.False(t, s1, "control: a foreign proof on a destroyed bill is omitted")
	require.False(t, s2, "control: a never-served pubkey is omitted")

	const samples = 150
	var d, a []time.Duration
	for i := 0; i < samples; i++ {
		x, _ := probe(destroyed)
		d = append(d, x)
		y, _ := probe(absent)
		a = append(a, y)
	}
	dMed, aMed := median(d), median(a)
	delta := dMed - aMed
	if delta < 0 {
		delta = -delta
	}
	ratio := float64(delta) / float64(min(dMed, aMed))
	t.Logf("destroyed-in-retention median %s, never-served median %s, skew %s (%.1f%%)", dMed, aMed, delta, ratio*100)
	require.Less(t, ratio, 0.25,
		"a destroyed bill inside retention is distinguishable from a never-served pubkey by timing (%s vs %s)", dMed, aMed)
}
