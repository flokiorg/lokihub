//go:build integration

package integration

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// numGeneratedUnauthorizedIdentities is how many freshly-generated,
// never-provisioned keypairs the unauthorized-rejection test exercises. Any
// of these is unauthorized by construction (they were never allowlisted or
// followed), so there's no need to hand-provision one - generating several
// here gives broader coverage than a single fixed identity would (e.g.
// rules out any bug that only manifests for one specific pubkey value).
const numGeneratedUnauthorizedIdentities = 5

func TestCircleHubs(t *testing.T) {
	cfg := requireConfig(t)
	for _, policy := range []string{circlePolicyAllowlist, circlePolicyFollowing} {
		t.Run(policy, func(t *testing.T) {
			testCircleHub(t, cfg, policy)
		})
	}
}

func testCircleHub(t *testing.T, cfg *Config, policy string) {
	member0Priv := newTestPrivkey(t)
	member0Pub := mustPubkey(t, member0Priv)
	member1Priv := newTestPrivkey(t)
	member1Pub := mustPubkey(t, member1Priv)

	// Both members are named upfront (authorizedPubkeys), not added later -
	// required for circlePolicyFollowing, whose membership is fixed by the
	// provider's kind:3 list published before the hub itself is created (see
	// createEphemeralCircleHub's own doc comment); kept the same shape for
	// circlePolicyAllowlist too rather than branching this test's own setup.
	hub, _, _ := createEphemeralCircleHub(t, cfg, "circle-hub-"+policy, policy,
		[]string{member0Priv, member1Priv},
		ephemeralCircleHubOpts{MinBudgetRenewal: constants.BUDGET_RENEWAL_MONTHLY})
	hubClient := mustConnect(t, hub.Connection)

	// Every identity/validation scenario below is rejected before the
	// controller ever consults the following-vs-allowlist authorization check
	// (see create_circle_wallet_controller.go's step ordering), so they run
	// unconditionally against both policies below - the goal is zero skipped
	// tests in a normal suite run (see integration/README.md's "Running
	// everything, with zero skips" section), and re-running them here is
	// cheap since it's still the same allowlist-vs-following hub already
	// created above, not an extra one. isAllowlist is kept only for the one
	// remaining case below (the omitted-expiry/omitted-budget_renewal default
	// check) that's still deliberately exercised once rather than twice.
	isAllowlist := policy == circlePolicyAllowlist

	// These two validation-focused subtests deliberately run BEFORE
	// CreateWallet_AuthorizedMember_HappyPath below: circle wallets cap at
	// one active wallet per (hub, identity) - see
	// create_circle_wallet_controller.go - so once the happy-path subtest
	// successfully mints a wallet for member0Pub, any further
	// create_circle_wallet call for that same identity would be rejected by
	// that cap instead of reaching the validation rule each of these
	// subtests actually means to exercise. Every other validation-only
	// subtest below is likewise placed before the happy path, for the same
	// reason.
	t.Run("CreateWallet_MaxAmountExceedsPerWalletCap", func(t *testing.T) {
		identityEvent := distinctCircleWalletIdentityEvent(t, member0Priv, hubClient.ClientPubkey(), "MaxAmountExceedsPerWalletCap")
		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     clearlyTooLargeAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, identityEvent),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_QUOTA_EXCEEDED)
	})

	t.Run("CreateWallet_BudgetRenewalTighterThanFloor", func(t *testing.T) {
		// "daily" is the tightest possible rank, so it is rejected against
		// this hub's own min_budget_renewal floor (BUDGET_RENEWAL_MONTHLY,
		// set explicitly above).
		identityEvent := distinctCircleWalletIdentityEvent(t, member0Priv, hubClient.ClientPubkey(), "BudgetRenewalTighterThanFloor")
		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			BudgetRenewal: constants.BUDGET_RENEWAL_DAILY,
			IdentityEvent: eventJSON(t, identityEvent),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_IdentityEvent_Missing_Rejected", func(t *testing.T) {
		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:    member0Pub,
			MaxAmount: happyPathAmountMloki,
			Expiry:    happyPathExpirySecs,
			// IdentityEvent intentionally omitted.
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_IdentityEvent_MalformedJSON_Rejected", func(t *testing.T) {
		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: "not valid json",
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	// The direct regression test for the impersonation gap the identity-proof
	// requirement closes: an attacker who merely knows a victim's pubkey (but
	// not their private key) cannot forge a proof on the victim's behalf.
	t.Run("CreateWallet_IdentityEvent_WrongSigner_Rejected", func(t *testing.T) {
		attackerPriv := newTestPrivkey(t)
		forged := buildCircleWalletIdentityEvent(t, attackerPriv, hubClient.ClientPubkey())

		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, forged),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	// A proof validly signed by the requester but bound (via its d-tag) to a
	// different app pubkey than this hub's own must not be usable here - e.g.
	// a proof captured from another circle_hub connection the requester also
	// holds.
	t.Run("CreateWallet_IdentityEvent_WrongHub_Rejected", func(t *testing.T) {
		wrongDTag := mustPubkey(t, newTestPrivkey(t)) // any pubkey other than this hub's own AppPubkey
		ev := buildCircleWalletIdentityEventCustom(t, member0Priv, wrongDTag, time.Now())

		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, ev),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_IdentityEvent_Stale_Rejected", func(t *testing.T) {
		ev := buildCircleWalletIdentityEventCustom(t, member0Priv, hubClient.ClientPubkey(), time.Now().Add(-10*time.Minute))

		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, ev),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("CreateWallet_IdentityEvent_FutureTimestamp_Rejected", func(t *testing.T) {
		ev := buildCircleWalletIdentityEventCustom(t, member0Priv, hubClient.ClientPubkey(), time.Now().Add(10*time.Minute))

		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, ev),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	// CRITICAL regression test: go-nostr's CheckSignature() verifies the
	// signature against a hash it recomputes from the event's own fields - it
	// never checks that the client-supplied `id` field actually equals that
	// hash (only CheckID() does). Since the replay guard trusts the event id
	// as its unique key, an attacker holding one captured, validly-signed
	// proof could otherwise resubmit it indefinitely by mutating only the
	// `id` field.
	t.Run("CreateWallet_IdentityEvent_TamperedID_Rejected", func(t *testing.T) {
		genuine := buildCircleWalletIdentityEvent(t, member0Priv, hubClient.ClientPubkey())
		tamperedJSON := eventJSONWithTamperedID(t, genuine)

		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: tamperedJSON,
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	// A captured proof (the circle_hub connection is shared/public, so anyone
	// holding it can decrypt every request sent over it, including this one)
	// must not be resubmittable. The first call is deliberately rejected for
	// an unrelated reason (amount too large) so the identity proof itself is
	// verified and recorded by the replay guard without minting a wallet;
	// the second call, with a valid amount but the identical identity_event,
	// must still be rejected - specifically because the proof was already
	// used, not because of anything amount-related.
	t.Run("CreateWallet_IdentityEvent_Replayed_Rejected", func(t *testing.T) {
		ev := distinctCircleWalletIdentityEvent(t, member0Priv, hubClient.ClientPubkey(), "IdentityEventReplayed")
		evJSON := eventJSON(t, ev)

		var first CreateCircleWalletResult
		err1 := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     clearlyTooLargeAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: evJSON,
		}, &first)
		requireNWCErrorCode(t, err1, constants.ERROR_QUOTA_EXCEEDED)

		var second CreateCircleWalletResult
		err2 := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: evJSON,
		}, &second)
		requireNWCErrorCode(t, err2, constants.ERROR_BAD_REQUEST)
	})

	// A malformed requester pubkey is rejected before the identity_event is
	// even parsed (create_circle_wallet_controller.go's step 0), so the
	// identity_event field is irrelevant here and left empty.
	t.Run("CreateWallet_RequesterPubkey_Malformed_Rejected", func(t *testing.T) {
		cases := map[string]string{
			"too_short": member0Pub[:63],
			"uppercase": strings.ToUpper(member0Pub),
			"non_hex":   "zz" + member0Pub[2:],
		}
		for name, badPubkey := range cases {
			t.Run(name, func(t *testing.T) {
				var result CreateCircleWalletResult
				err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
					Pubkey:    badPubkey,
					MaxAmount: happyPathAmountMloki,
					Expiry:    happyPathExpirySecs,
				}, &result)
				requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
			})
		}
	})

	t.Run("CreateWallet_BudgetRenewal_InvalidValue_Rejected", func(t *testing.T) {
		ev := distinctCircleWalletIdentityEvent(t, member0Priv, hubClient.ClientPubkey(), "BudgetRenewalInvalidValue")

		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			BudgetRenewal: "fortnightly",
			IdentityEvent: eventJSON(t, ev),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	// A max_amount large enough to overflow the int64 cast used against the
	// commitment/balance check must be rejected unconditionally, independent
	// of any configured per-wallet cap.
	t.Run("CreateWallet_MaxAmount_Overflow_Rejected", func(t *testing.T) {
		ev := distinctCircleWalletIdentityEvent(t, member0Priv, hubClient.ClientPubkey(), "MaxAmountOverflow")

		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     math.MaxUint64,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, ev),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	// Proves an attacker can't distinguish "this pubkey is an authorized
	// member" from "it isn't" via the returned error code: a forged proof
	// (signed by the attacker, but claiming a target pubkey the attacker
	// doesn't control) is always rejected by the identity-signature check
	// BEFORE the authorization/allowlist lookup ever runs, regardless of
	// whether the impersonated target actually is a member. If this ever
	// regressed to check authorization first, an attacker could probe
	// allowlist membership for arbitrary pubkeys by the difference between
	// BAD_REQUEST (not a member) and RESTRICTED (is a member, wrong signer
	// caught downstream) - or vice versa.
	t.Run("CreateWallet_SpoofedIdentity_MembershipOracleClosed", func(t *testing.T) {
		attempt := func(targetPubkey string) *nwcclient.NWCError {
			attackerPriv := newTestPrivkey(t)
			forged := buildCircleWalletIdentityEvent(t, attackerPriv, hubClient.ClientPubkey())
			var result CreateCircleWalletResult
			err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
				Pubkey:        targetPubkey,
				MaxAmount:     happyPathAmountMloki,
				Expiry:        happyPathExpirySecs,
				IdentityEvent: eventJSON(t, forged),
			}, &result)
			require.Error(t, err)
			var nwcErr *nwcclient.NWCError
			require.True(t, errors.As(err, &nwcErr), "expected an *nwcclient.NWCError, got %T: %v", err, err)
			return nwcErr
		}

		errForAuthorizedTarget := attempt(member0Pub)
		errForUnauthorizedTarget := attempt(mustPubkey(t, newTestPrivkey(t)))

		assert.Equal(t, constants.ERROR_BAD_REQUEST, errForAuthorizedTarget.Code)
		assert.Equal(t, errForAuthorizedTarget.Code, errForUnauthorizedTarget.Code,
			"spoofing an authorized vs unauthorized identity must return identical error codes - otherwise the code itself becomes a membership oracle")
	})

	// Only the happy-path subtest below reaches the controller's rate-limit
	// check (every subtest above is rejected earlier in validation, before
	// rate limiting is applied) - see create_circle_wallet_controller.go.
	// The rate limiter is keyed by requester pubkey, and every identity here
	// is freshly generated per test run, so there's no shared-budget concern
	// across suite runs the way a pre-provisioned hub's real identities used
	// to have.
	t.Run("CreateWallet_AuthorizedMember_HappyPath", func(t *testing.T) {
		members := []struct {
			priv, pub string
		}{
			{member0Priv, member0Pub},
			{member1Priv, member1Pub},
		}
		for i, member := range members {
			t.Run(fmt.Sprintf("member_%d", i), func(t *testing.T) {
				identityEvent := distinctCircleWalletIdentityEvent(t, member.priv, hubClient.ClientPubkey(), fmt.Sprintf("HappyPath-member-%d", i))

				params := CreateCircleWalletParams{
					Pubkey:        member.pub,
					MaxAmount:     happyPathAmountMloki,
					Expiry:        happyPathExpirySecs,
					BudgetRenewal: constants.BUDGET_RENEWAL_MONTHLY,
					IdentityEvent: eventJSON(t, identityEvent),
				}
				// Exercise omitted-expiry/omitted-budget_renewal default
				// resolution on a real successful creation, for the second
				// member only - policy-independent, so only worth doing
				// once (isAllowlist), and needs a spare identity so it
				// doesn't also need to be member0 (which the
				// second-request-rejected subtest below relies on using
				// explicit params).
				testingDefaults := isAllowlist && i == len(members)-1
				if testingDefaults {
					params.Expiry = 0
					params.BudgetRenewal = ""
				}

				var result CreateCircleWalletResult
				err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, params, &result)
				require.NoError(t, err)
				require.NotEmpty(t, result.WalletPubkey)
				require.NotEmpty(t, result.EncryptedPairingURI)

				if testingDefaults {
					assert.Equal(t, constants.BUDGET_RENEWAL_NEVER, result.BudgetRenewal,
						"an omitted budget_renewal must default to \"never\"")
					assert.Greater(t, result.ExpiresAt, time.Now().Unix(),
						"an omitted expiry must default to the hub's own max_exp_secs, not produce an already-expired wallet")
				}

				pairingURI, err := nwcclient.DecryptPairingURI(member.priv, result.WalletPubkey, result.EncryptedPairingURI)
				require.NoError(t, err)

				child := mustConnect(t, pairingURI)

				var balance GetBalanceResult
				require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balance))
				require.EqualValues(t, 0, balance.Balance, "circle wallets start unfunded until the member receives payments")

				var info GetInfoResult
				require.NoError(t, child.Call(ctxT(t), "get_info", struct{}{}, &info))
				require.Contains(t, info.Methods, "make_invoice", "circle wallets, unlike JIT wallets, must be able to receive funds")
				require.Contains(t, info.Methods, "pay_invoice")
				require.NotContains(t, info.Methods, constants.NIP47MethodCreateCircleWallet, "a circle_wallet child must not be able to issue its own sub-wallets")
			})
		}
	})

	// member0Pub already has an active wallet from the happy path subtest
	// above - a second request for the same identity must be rejected by
	// the one-active-wallet-per-identity cap, not silently mint a second
	// wallet.
	t.Run("CreateWallet_SecondRequestForActiveIdentity_Rejected", func(t *testing.T) {
		ev := distinctCircleWalletIdentityEvent(t, member0Priv, hubClient.ClientPubkey(), "SecondRequestForActiveIdentity")

		var result CreateCircleWalletResult
		err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        member0Pub,
			MaxAmount:     happyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, ev),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	})

	t.Run("GetInfo_HubAdvertisesCreateCircleWallet", func(t *testing.T) {
		var info GetInfoResult
		require.NoError(t, hubClient.Call(ctxT(t), "get_info", struct{}{}, &info))
		require.Contains(t, info.Methods, constants.NIP47MethodCreateCircleWallet)
	})

	t.Run("CreateWallet_UnauthorizedMember_Rejected", func(t *testing.T) {
		for i := 0; i < numGeneratedUnauthorizedIdentities; i++ {
			t.Run(fmt.Sprintf("identity_%d", i), func(t *testing.T) {
				unauthorizedPriv := newTestPrivkey(t)
				unauthorizedPub := mustPubkey(t, unauthorizedPriv)
				identityEvent := buildCircleWalletIdentityEvent(t, unauthorizedPriv, hubClient.ClientPubkey())

				var result CreateCircleWalletResult
				err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
					Pubkey:        unauthorizedPub,
					MaxAmount:     happyPathAmountMloki,
					Expiry:        happyPathExpirySecs,
					IdentityEvent: eventJSON(t, identityEvent),
				}, &result)
				requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
			})
		}
	})
}
