package controllers

// nmilat migration (PR #90): verifyClaimAttestationEvent (kind 35522, IA
// attestation) delegates its structural checks to nipIC.ParseAttestation.
// Equivalence with the previous hand-rolled check was verified first as a
// Phase 0 spike before the swap landed; these tests now serve as permanent
// regression coverage for that swap, including the two deliberate
// hardenings it brought along (event-ID integrity, evidence-must-be-a-JSON-
// object) - see verifyClaimAttestationEvent's own doc comment for the full
// reasoning.

import (
	"strconv"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nipIC"

	"github.com/flokiorg/lokihub/tests"
)

// TestNmilatEquivalence_Attestation_StructuralChecks mirrors
// TestVerifyClaimAttestationEvent_PlatformEvidenceTags's own table (see that
// test's doc comment) and confirms verifyClaimAttestationEvent (now backed by
// nipIC.ParseAttestation) and a direct nipIC.ParseAttestation call reach the
// same accept/reject verdict on the structural layer both actually check:
// platform/evidence tag presence and evidence JSON-object validity. d/p-tag
// binding to a specific connectionKey/claimant and mandatory-unexpired
// expiration are lokihub-specific POLICY layered on top of the parse (nipIC
// itself doesn't enforce either - expiration is optional there because
// NIP-IC has real per-attestation revocation to fall back on), so this test
// doesn't assert equivalence on those, only on what the parse itself covers.
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
		name           string
		tags           nostr.Tags
		lokihubWantErr string // substring, "" means accepted
		nipICWantErr   string // substring, "" means accepted
	}{
		{
			name:           "missing platform tag rejected",
			tags:           append(baseTags(), nostr.Tag{"evidence", `{"version":1}`}),
			lokihubWantErr: "#platform tag is required",
			nipICWantErr:   "#platform tag is required",
		},
		{
			name:           "empty platform tag rejected",
			tags:           append(baseTags(), nostr.Tag{"platform", ""}, nostr.Tag{"evidence", `{"version":1}`}),
			lokihubWantErr: "#platform tag is required",
			nipICWantErr:   "#platform tag is required",
		},
		{
			name:           "missing evidence tag rejected",
			tags:           append(baseTags(), nostr.Tag{"platform", "discord"}),
			lokihubWantErr: "#evidence tag is required",
			nipICWantErr:   "#evidence tag is required",
		},
		{
			name:           "malformed evidence tag rejected",
			tags:           append(baseTags(), nostr.Tag{"platform", "discord"}, nostr.Tag{"evidence", "not valid json"}),
			lokihubWantErr: "evidence tag is not valid JSON",
			nipICWantErr:   "evidence tag is not valid JSON",
		},
		{
			name:           "evidence that's valid JSON but not an object is rejected (hardening #2)",
			tags:           append(baseTags(), nostr.Tag{"platform", "discord"}, nostr.Tag{"evidence", `"just-a-bare-json-string"`}),
			lokihubWantErr: "evidence tag is not valid JSON",
			nipICWantErr:   "evidence tag is not valid JSON",
		},
		{
			name:           "platform and evidence present, accepted",
			tags:           append(baseTags(), nostr.Tag{"platform", "discord"}, nostr.Tag{"evidence", `{"version":1,"platform":"discord"}`}),
			lokihubWantErr: "",
			nipICWantErr:   "",
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

			nip01Ev, convErr := toNip01Event(ev)
			require.NoError(t, convErr)
			parsed, nipICErr := nipIC.ParseAttestation(nip01Ev)
			if tc.nipICWantErr == "" {
				require.NoError(t, nipICErr)
				require.NotNil(t, parsed)
				assert.Equal(t, connectionKey, string(parsed.ConnectionKey), "nipIC should extract the same connection_key from the d-tag")
				assert.Equal(t, claimantPubkey, parsed.UserPubkey, "nipIC should extract the same claimant pubkey from the p-tag")
				assert.Equal(t, "discord", string(parsed.Platform))
			} else {
				require.Error(t, nipICErr)
				assert.Contains(t, nipICErr.Error(), tc.nipICWantErr)
			}
		})
	}
}

// TestVerifyClaimAttestationEvent_IDIntegrityHardening is the regression test
// for hardening #1 documented on verifyClaimAttestationEvent: a
// structurally-valid, validly-signed attestation whose `id` field has been
// tampered with (content/tags/sig untouched) must now be rejected, closing
// the previously-inert gap where go-nostr's CheckSignature() alone ignores
// the stored ID field entirely (it recomputes the hash from content and
// verifies against that, never checking the stored ID matches).
func TestVerifyClaimAttestationEvent_IDIntegrityHardening(t *testing.T) {
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

	// Sanity: the untampered event is accepted.
	require.NoError(t, verifyClaimAttestationEvent(ev, iaPubkey, claimantPubkey, connectionKey))

	// Tamper the ID only - content, tags, and sig are untouched, so
	// CheckSignature (which recomputes the hash and ignores the stored ID)
	// would still report valid; ID integrity is what must catch this now.
	tampered := *ev
	tampered.ID = "1111111111111111111111111111111111111111111111111111111111111111"
	require.NotEqual(t, ev.ID, tampered.ID)

	valid, sigErr := tampered.CheckSignature()
	require.NoError(t, sigErr)
	require.True(t, valid, "go-nostr CheckSignature ignores the stored ID field entirely - this is exactly the gap the ID-integrity hardening closes")

	err := verifyClaimAttestationEvent(&tampered, iaPubkey, claimantPubkey, connectionKey)
	require.Error(t, err, "a tampered-ID attestation must now be rejected")
	assert.Contains(t, err.Error(), "event ID mismatch")
}
