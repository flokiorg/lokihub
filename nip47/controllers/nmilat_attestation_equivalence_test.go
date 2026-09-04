package controllers

// Phase 0 verification spike for the nmilat migration plan (PR #90): before
// verifyClaimAttestationEvent (kind 35522, IA attestation) is ever rewired to
// delegate its structural checks to nipIC.ParseAttestation, this file proves
// exactly where the two agree and exactly where they deliberately diverge -
// both are real findings this test records, not something to paper over.

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipIC"

	"github.com/flokiorg/lokihub/tests"
)

// toNip01Event converts a signed go-nostr Event into nmilat's own nip01.Event
// via a plain JSON round-trip. Both packages use the identical NIP-01 wire
// field names (id/pubkey/created_at/kind/tags/content/sig), so this is a real
// cross-implementation interop check, not just a type-shape convenience: if
// the two disagreed on any field's JSON tag or encoding, this round-trip
// itself would fail before either side's own verification ever ran.
func toNip01Event(t *testing.T, ev *nostr.Event) *nip01.Event {
	t.Helper()
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	var out nip01.Event
	require.NoError(t, json.Unmarshal(raw, &out))
	return &out
}

// TestNmilatEquivalence_Attestation_StructuralChecks mirrors
// TestVerifyClaimAttestationEvent_PlatformEvidenceTags's own table (see that
// test's doc comment) against nipIC.ParseAttestation, on the dimension both
// functions actually check: platform/evidence tag presence and evidence JSON
// validity. d/p-tag binding to a specific connectionKey/claimant and
// mandatory-unexpired expiration are lokihub-specific POLICY on top of the
// wire shape - nipIC intentionally doesn't enforce either (expiration is
// optional there because NIP-IC has real per-attestation revocation to fall
// back on; see verifyClaimAttestationEvent's own doc comment) - so this test
// does not assert equivalence on those, only on the structural layer a
// migration would actually delegate.
func TestNmilatEquivalence_Attestation_StructuralChecks(t *testing.T) {
	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	connectionKey := tests.RandomHex32()
	validExpiration := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)

	baseTags := func() nostr.Tags {
		return nostr.Tags{
			{"d", connectionKey},
			{"p", claimantPubkey},
			{"expiration", validExpiration},
		}
	}
	buildWithTags := func(t *testing.T, tags nostr.Tags) *nostr.Event {
		t.Helper()
		ev := &nostr.Event{Kind: nostrKindIAAttestation, CreatedAt: nostr.Now(), Tags: tags}
		require.NoError(t, ev.Sign(iaPrivkey))
		return ev
	}

	cases := []struct {
		name              string
		tags              nostr.Tags
		lokihubWantErr    string // substring, "" means lokihub accepts
		nipICWantAccepted bool
	}{
		{
			name:              "missing platform tag rejected by both",
			tags:              append(baseTags(), nostr.Tag{"evidence", `{"version":1}`}),
			lokihubWantErr:    "missing a required platform tag",
			nipICWantAccepted: false,
		},
		{
			name:              "empty platform tag rejected by both",
			tags:              append(baseTags(), nostr.Tag{"platform", ""}, nostr.Tag{"evidence", `{"version":1}`}),
			lokihubWantErr:    "missing a required platform tag",
			nipICWantAccepted: false,
		},
		{
			name:              "missing evidence tag rejected by both",
			tags:              append(baseTags(), nostr.Tag{"platform", "discord"}),
			lokihubWantErr:    "missing a required evidence tag",
			nipICWantAccepted: false,
		},
		{
			name:              "malformed evidence tag rejected by both",
			tags:              append(baseTags(), nostr.Tag{"platform", "discord"}, nostr.Tag{"evidence", "not valid json"}),
			lokihubWantErr:    "evidence tag is not valid JSON",
			nipICWantAccepted: false,
		},
		{
			name:              "platform and evidence present, both accept structurally",
			tags:              append(baseTags(), nostr.Tag{"platform", "discord"}, nostr.Tag{"evidence", `{"version":1,"platform":"discord"}`}),
			lokihubWantErr:    "",
			nipICWantAccepted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := buildWithTags(t, tc.tags)

			lokihubErr := verifyClaimAttestationEvent(ev, iaPubkey, claimantPubkey, connectionKey)
			if tc.lokihubWantErr == "" {
				require.NoError(t, lokihubErr)
			} else {
				require.Error(t, lokihubErr)
				assert.Contains(t, lokihubErr.Error(), tc.lokihubWantErr)
			}

			nip01Ev := toNip01Event(t, ev)
			parsed, nipICErr := nipIC.ParseAttestation(nip01Ev)
			if tc.nipICWantAccepted {
				require.NoError(t, nipICErr, "nipIC.ParseAttestation should structurally accept this attestation")
				require.NotNil(t, parsed)
				assert.Equal(t, connectionKey, string(parsed.ConnectionKey), "nipIC should extract the same connection_key from the d-tag")
				assert.Equal(t, claimantPubkey, parsed.UserPubkey, "nipIC should extract the same claimant pubkey from the p-tag")
				assert.Equal(t, "discord", string(parsed.Platform))
			} else {
				assert.Error(t, nipICErr, "nipIC.ParseAttestation should reject this attestation too")
			}
		})
	}
}

// TestNmilatEquivalence_Attestation_IDTamperDivergence documents, with a real
// test rather than a claim in prose, the one confirmed behavioral difference
// found during this equivalence spike: verifyClaimAttestationEvent
// deliberately never calls ev.CheckID() for the kind-35522 attestation (see
// its own doc comment - the attestation's client-supplied ID is never used as
// a trust anchor anywhere in this codebase, only as an informational e-tag
// citation from the claim proof), while nipIC.ParseAttestation's underlying
// nip01.Event.Verify() DOES recompute and compare the event ID as part of
// full NIP-01 verification. A structurally-valid, validly-signed attestation
// whose `id` field has been tampered with (independent of content/tags/sig)
// is therefore accepted by lokihub's current check today, and would start
// being rejected if verifyClaimAttestationEvent were ever swapped to call
// nipIC.ParseAttestation as-is.
//
// This is NOT a defect in nmilat - it's NIP-01 correctness lokihub itself
// applies elsewhere (verifyClaimIdentityEvent, verifyCircleWalletIdentityEvent
// both call ev.CheckID() explicitly, exactly because THEIR replay guards do
// trust the event ID). It genuinely is a behavior change relative to
// lokihub's current, deliberately lenient attestation check, and per the
// migration plan's own "never combine a behavior change with a swap" rule,
// adopting nipIC.ParseAttestation here needs an explicit decision, not a
// silent tightening bundled into a refactor.
func TestNmilatEquivalence_Attestation_IDTamperDivergence(t *testing.T) {
	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	connectionKey := tests.RandomHex32()

	ev := &nostr.Event{
		Kind:      nostrKindIAAttestation,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", connectionKey},
			{"p", claimantPubkey},
			{"platform", "discord"},
			{"evidence", `{"version":1,"platform":"discord"}`},
			{"expiration", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)},
		},
	}
	require.NoError(t, ev.Sign(iaPrivkey))

	// Sanity: the untampered event is accepted by both, establishing the
	// baseline before tampering.
	require.NoError(t, verifyClaimAttestationEvent(ev, iaPubkey, claimantPubkey, connectionKey))
	_, err := nipIC.ParseAttestation(toNip01Event(t, ev))
	require.NoError(t, err)

	// Tamper the ID only - content, tags, and sig are untouched, so
	// CheckSignature (which recomputes the hash and ignores the stored ID)
	// still reports valid.
	tampered := *ev
	tampered.ID = "1111111111111111111111111111111111111111111111111111111111111111"
	require.NotEqual(t, ev.ID, tampered.ID)

	valid, sigErr := tampered.CheckSignature()
	require.NoError(t, sigErr)
	require.True(t, valid, "go-nostr CheckSignature ignores the stored ID field entirely - this is the documented root cause")

	lokihubErr := verifyClaimAttestationEvent(&tampered, iaPubkey, claimantPubkey, connectionKey)
	nip01Tampered := toNip01Event(t, &tampered)
	_, nipICErr := nipIC.ParseAttestation(nip01Tampered)

	t.Logf("lokihub verifyClaimAttestationEvent on tampered-ID attestation: err=%v", lokihubErr)
	t.Logf("nipIC.ParseAttestation on tampered-ID attestation: err=%v", nipICErr)

	assert.NoError(t, lokihubErr, "confirms current documented behavior: lokihub does not check attestation ID integrity")
	assert.Error(t, nipICErr, "confirms nipIC.ParseAttestation DOES reject a tampered ID via nip01.Event.Verify()'s recompute-and-compare check")
}
