package controllers

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// --- fixtures for the cash_consolidate guard table ---

func guardCashWallet(t *testing.T, svc *tests.TestService, hub *db.App) *db.App {
	t.Helper()
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	return wallet
}

// guardSource creates a cash_wallet under hub holding a single pubkey slice for
// callerPub, with the given inherited terms.
func guardSource(t *testing.T, svc *tests.TestService, hub *db.App, callerPub string, amount uint64, minTransfer int64, redeemFee int) *db.App {
	t.Helper()
	w := guardCashWallet(t, svc, hub)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(w.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: callerPub, AmountMloki: int64(amount), MinTransferMloki: minTransfer, RedeemFeePpm: redeemFee}, //nolint:gosec
	}))
	return w
}

// guardParam builds one source param with a proof bound to (sourceWallet, newPub, proofAmount).
func guardParam(t *testing.T, w *db.App, callerPriv, callerPub, newPub string, proofAmount uint64) consolidateSourceParam {
	t.Helper()
	proof := buildTransferProofEvent(t, callerPriv, *w.WalletPubkey, db.CashIdentityPubkey, newPub, "", proofAmount, nil, time.Now())
	return consolidateSourceParam{
		WalletPubkey:  *w.WalletPubkey,
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: callerPub,
		IdentityEvent: mustMarshal(t, proof),
	}
}

// TestConsolidate_GuardRejections is the adversarial guard table for
// cash_consolidate: every case constructs a request that must be rejected at a
// specific guard BEFORE any funds move, and asserts both the error code and the
// reason — so a regression that silently drops a guard (and lets an attacker
// combine wallets they don't own, span hubs, bypass the ceiling, replay a
// proof, or restate an amount) fails loudly here.
func TestConsolidate_GuardRejections(t *testing.T) {
	type built struct {
		caller *db.App
		params cashConsolidateParams
	}
	cases := []struct {
		name     string
		build    func(t *testing.T, svc *tests.TestService) built
		wantCode string
		wantMsg  string
	}{
		{
			name: "caller is not a cash_wallet",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return built{caller: hub, params: cashConsolidateParams{
					Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_RESTRICTED,
			wantMsg:  "cash_wallet",
		},
		{
			name: "fewer than two sources",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return built{caller: caller, params: cashConsolidateParams{
					Sources:     []consolidateSourceParam{{WalletPubkey: *caller.WalletPubkey, CashSecret: "00"}},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "at least two sources",
		},
		{
			name: "new_identity is an unrecognized type",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				return built{caller: caller, params: cashConsolidateParams{
					Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: "carrier-pigeon", IdentityValue: "deadbeef"},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "must be",
		},
		{
			name: "new_identity value missing",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				return built{caller: caller, params: cashConsolidateParams{
					Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "64-character lowercase hex string",
		},
		{
			name: "new_identity cash-mode target missing identity_value",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				return built{caller: caller, params: cashConsolidateParams{
					Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "required for a cash-mode target",
		},
		{
			name: "new_identity cash-mode target carries ia_pubkey",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				iaPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
					NewIdentity: cashTransferNewIdentityParam{
						IdentityType: db.CashIdentityCash, IdentityValue: tests.RandomHex32(), IAPubkey: iaPub,
					},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "must not carry ia_pubkey",
		},
		{
			name: "new_identity connection_key target untrusted IA",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				untrustedIA, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
					NewIdentity: cashTransferNewIdentityParam{
						IdentityType: db.CashIdentityConnectionKey, IdentityValue: tests.RandomHex32(), IAPubkey: untrustedIA,
					},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "not a trusted Identity Authority",
		},
		{
			name: "connection_key source missing attestation_event",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						{WalletPubkey: *caller.WalletPubkey, IdentityType: db.CashIdentityConnectionKey, IdentityValue: "ck", IdentityEvent: "{}"},
						{WalletPubkey: "b", IdentityType: db.CashIdentityConnectionKey, IdentityValue: "ck2", IdentityEvent: "{}"},
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "attestation_event is required",
		},
		{
			name: "source not custodied by this node",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						{WalletPubkey: "deadbeef"},
						{WalletPubkey: "cafebabe"},
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_NOT_FOUND,
			wantMsg:  "custodies",
		},
		{
			name: "a hub (non-cash_wallet) app referenced as a source is not custodied",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				// hub.WalletPubkey exists but its kind is cash_hub, not cash_wallet.
				hubPubkey := "00"
				if hub.WalletPubkey != nil {
					hubPubkey = *hub.WalletPubkey
				}
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						{WalletPubkey: hubPubkey},
						{WalletPubkey: *caller.WalletPubkey},
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_NOT_FOUND,
			wantMsg:  "custodies",
		},
		{
			name: "unauthorized proof (signed by a stranger)",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hub, ownerPub, 3000, 0, 0)
				s2 := guardSource(t, svc, hub, ownerPub, 2000, 0, 0)
				// Attacker signs the proof for s1 with THEIR OWN key, not ownerPriv.
				attackerPriv := nostr.GeneratePrivateKey()
				bad := guardParam(t, s1, attackerPriv, ownerPub, newPub, 3000)
				good := guardParam(t, s2, ownerPriv, ownerPub, newPub, 2000)
				return built{caller: caller, params: cashConsolidateParams{
					Sources:     []consolidateSourceParam{bad, good},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "source 0",
		},
		{
			name: "proof bound to the wrong amount",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hub, ownerPub, 3000, 0, 0)
				s2 := guardSource(t, svc, hub, ownerPub, 2000, 0, 0)
				// s1's slice is 3000 but the proof commits to 9999.
				bad := guardParam(t, s1, ownerPriv, ownerPub, newPub, 9999)
				good := guardParam(t, s2, ownerPriv, ownerPub, newPub, 2000)
				return built{caller: caller, params: cashConsolidateParams{
					Sources:     []consolidateSourceParam{bad, good},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "source 0",
		},
		{
			name: "duplicate source wallet",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hub, ownerPub, 3000, 0, 0)
				p := guardParam(t, s1, ownerPriv, ownerPub, newPub, 3000)
				return built{caller: caller, params: cashConsolidateParams{
					Sources:     []consolidateSourceParam{p, p},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "twice",
		},
		{
			name: "sources span different hubs",
			build: func(t *testing.T, svc *tests.TestService) built {
				hubA := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				hubB := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hubA)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hubA, ownerPub, 3000, 0, 0)
				s2 := guardSource(t, svc, hubB, ownerPub, 2000, 0, 0)
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						guardParam(t, s1, ownerPriv, ownerPub, newPub, 3000),
						guardParam(t, s2, ownerPriv, ownerPub, newPub, 2000),
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "same Cash Hub",
		},
		{
			name: "sources disagree on min_transfer_millis",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hub, ownerPub, 3000, 100, 0)
				s2 := guardSource(t, svc, hub, ownerPub, 2000, 500, 0) // different floor
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						guardParam(t, s1, ownerPriv, ownerPub, newPub, 3000),
						guardParam(t, s2, ownerPriv, ownerPub, newPub, 2000),
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "disagree",
		},
		{
			name: "sources disagree on redeem_fee_ppm",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hub, ownerPub, 3000, 0, 10)
				s2 := guardSource(t, svc, hub, ownerPub, 2000, 0, 50) // different fee
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						guardParam(t, s1, ownerPriv, ownerPub, newPub, 3000),
						guardParam(t, s2, ownerPriv, ownerPub, newPub, 2000),
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "disagree",
		},
		{
			name: "summed amount exceeds the hub's per-wallet ceiling",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 4000, 3600) // ceiling 4000
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hub, ownerPub, 3000, 0, 0)
				s2 := guardSource(t, svc, hub, ownerPub, 2000, 0, 0) // sum 5000 > 4000
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						guardParam(t, s1, ownerPriv, ownerPub, newPub, 3000),
						guardParam(t, s2, ownerPriv, ownerPub, newPub, 2000),
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "ceiling",
		},
		{
			name: "summed amount overflows uint64 (must not wrap past the ceiling)",
			build: func(t *testing.T, svc *tests.TestService) built {
				// The overflow guard fires DURING the per-source summing loop,
				// before the after-loop ceiling check ever runs — so any positive
				// ceiling is fine; three MaxInt64 slices must be caught as an
				// overflow (a wrapped sum could otherwise slip under the ceiling).
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				const huge = uint64(1)<<63 - 1 // MaxInt64; 3 of these sum past uint64 max -> wraps
				s1 := guardSource(t, svc, hub, ownerPub, huge, 0, 0)
				s2 := guardSource(t, svc, hub, ownerPub, huge, 0, 0)
				s3 := guardSource(t, svc, hub, ownerPub, huge, 0, 0)
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						guardParam(t, s1, ownerPriv, ownerPub, newPub, huge),
						guardParam(t, s2, ownerPriv, ownerPub, newPub, huge),
						guardParam(t, s3, ownerPriv, ownerPub, newPub, huge),
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "overflow",
		},
		{
			// Regression for the independent financial-review finding on the
			// per-source summation guard: a sum landing strictly between
			// MaxInt64 and MaxUint64 does NOT trip a uint64-only overflow
			// check (nowhere near uint64's real ceiling), yet Consolidate
			// casts the final total to int64 for CashWalletClaim.AmountMloki
			// (a DB int64 column). The overflow guard fires DURING the
			// per-source loop, before the after-loop ceiling check ever runs
			// — so this specific hub's ceiling (necessarily <= MaxInt64,
			// since PerWalletMaxMloki is a Go int and is validated strictly
			// positive at both hub creation and update, apps/cash_hub_service.go)
			// would also eventually reject this sum, but only AFTER the loop
			// finishes; asserting "overflow" (not "ceiling") as the rejection
			// reason proves THIS guard is what actually fires. Kept as
			// defense-in-depth mirroring mint_cash's identical guard
			// (cashwallet/create.go) rather than as a currently-reachable
			// wrap: PerWalletMaxMloki has no "unlimited" mode today, but this
			// codebase's established 0-means-unlimited convention (already
			// used for MaxExpSecs/MinTransferMloki/RedeemFeePpm) makes that a
			// plausible future addition this guard shouldn't depend on.
			name: "summed amount exceeds MaxInt64",
			build: func(t *testing.T, svc *tests.TestService) built {
				const maxInt64 = uint64(1)<<63 - 1
				hub := tests.CreateCashHub(t, svc, 1<<62, 3600) // large but valid positive ceiling
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hub, ownerPub, maxInt64, 0, 0)
				s2 := guardSource(t, svc, hub, ownerPub, 2, 0, 0) // pushes the running sum just past MaxInt64
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						guardParam(t, s1, ownerPriv, ownerPub, newPub, maxInt64),
						guardParam(t, s2, ownerPriv, ownerPub, newPub, 2),
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "overflow",
		},
		{
			name: "already-claimed source slice is no longer consolidatable",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				ownerPriv := nostr.GeneratePrivateKey()
				ownerPub, _ := nostr.GetPublicKey(ownerPriv)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				s1 := guardSource(t, svc, hub, ownerPub, 3000, 0, 0)
				s2 := guardSource(t, svc, hub, ownerPub, 2000, 0, 0)
				// Consume s1's slice out from under the consolidation.
				_, err := svc.AppsService.ClaimCashSlice(s1.ID, db.CashIdentityPubkey, ownerPub)
				require.NoError(t, err)
				return built{caller: caller, params: cashConsolidateParams{
					Sources: []consolidateSourceParam{
						guardParam(t, s1, ownerPriv, ownerPub, newPub, 3000),
						guardParam(t, s2, ownerPriv, ownerPub, newPub, 2000),
					},
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_NOT_FOUND,
			wantMsg:  "no unclaimed slice",
		},
		{
			// Mirrors mint_cash's maxRecipientsPerWallet cap: without an upper
			// bound, one rate-limited request could bundle an unbounded number of
			// independent cash-secret guesses or custody/proof-verification
			// work. Every source here is deliberately bogus (never resolves) —
			// this guard must fire before any per-source lookup, on count alone.
			name: "more than maxConsolidateSources sources rejected",
			build: func(t *testing.T, svc *tests.TestService) built {
				hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
				caller := guardCashWallet(t, svc, hub)
				newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				sources := make([]consolidateSourceParam, maxConsolidateSources+1)
				for i := range sources {
					sources[i] = consolidateSourceParam{WalletPubkey: fmt.Sprintf("bogus-%d", i), CashSecret: "00"}
				}
				return built{caller: caller, params: cashConsolidateParams{
					Sources:     sources,
					NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST,
			wantMsg:  "at most",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			b := tc.build(t, svc)
			resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), b.caller, b.params)
			require.NotNil(t, resp.Error, "expected a rejection")
			assert.Equal(t, tc.wantCode, resp.Error.Code)
			assert.Contains(t, resp.Error.Message, tc.wantMsg)
		})
	}
}

// TestConsolidate_RejectedRequest_ReleasesOwnProofsButNotOthers: when source 2's
// proof was already used by an earlier call, the consolidate is refused
// UNAUTHORIZED (NIP-CASH: a proof already used). Source 1's proof is left unused,
// so the caller can retry with it; the earlier call's mark stays (no replay
// reopened); every source is left unclaimed.
func TestConsolidate_RejectedRequest_ReleasesOwnProofsButNotOthers(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	s1 := guardSource(t, svc, hub, ownerPub, 3000, 0, 0)
	s2 := guardSource(t, svc, hub, ownerPub, 2000, 0, 0)

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityPubkey, newPub, "", 3000, nil, time.Now())
	proof2 := buildTransferProofEvent(t, ownerPriv, *s2.WalletPubkey, db.CashIdentityPubkey, newPub, "", 2000, nil, time.Now())

	// Simulate proof2 already consumed by an earlier, legitimate request.
	require.NoError(t, svc.DB.Create(&db.CashConsumedProof{EventID: proof2.ID, ProofCreatedAt: int64(proof2.CreatedAt), ConsumedAt: time.Now().Unix(), Method: "cash_consolidate"}).Error)

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof2)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "already used")

	var count1, count2 int64
	require.NoError(t, svc.DB.Model(&db.CashConsumedProof{}).Where("event_id = ?", proof1.ID).Count(&count1).Error)
	assert.EqualValues(t, 0, count1, "a rejected consolidate must release the proof it consumed, so the caller can retry")
	require.NoError(t, svc.DB.Model(&db.CashConsumedProof{}).Where("event_id = ?", proof2.ID).Count(&count2).Error)
	assert.EqualValues(t, 1, count2, "must NOT un-burn a proof another request legitimately consumed (no replay reopened)")

	// Both sources must be left unclaimed and unchanged.
	for _, s := range []*db.App{s1, s2} {
		claim := cashWalletClaimByIdentity(t, svc, s.ID, db.CashIdentityPubkey, ownerPub)
		require.NotNil(t, claim)
		assert.Nil(t, claim.ClaimedAt, "a rejected consolidate must leave every source unclaimed")
	}
}

// TestConsolidate_FundingFailure_LeavesEverySourceClaimable: the consolidate is
// one commit, so a failure funding the merged bill from the second source, after
// both sources were claimed and the first one moved inside it, leaves both
// sources unclaimed and funded, no merged bill and no used proof.
func TestConsolidate_FundingFailure_LeavesEverySourceClaimable(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	const moved = uint64(123_000)
	s1 := guardSource(t, svc, hub, ownerPub, moved, 0, 0)
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")
	s2 := guardSource(t, svc, hub, ownerPub, moved, 0, 0)
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	controller := NewTestNip47Controller(svc)
	controller.cashHooks = &cashwallet.TestHooks{Before: func(stage string) error {
		if stage == cashwallet.StageAfterTransfer+"(1)" {
			return errors.New("injected: second funding failed")
		}
		return nil
	}}
	resp := handleCashConsolidateFor(t, svc, controller, caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			guardParam(t, s1, ownerPriv, ownerPub, newPub, moved),
			guardParam(t, s2, ownerPriv, ownerPub, newPub, moved),
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_INTERNAL, resp.Error.Code)

	for _, s := range []*db.App{s1, s2} {
		claim := cashWalletClaimByIdentity(t, svc, s.ID, db.CashIdentityPubkey, ownerPub)
		require.NotNil(t, claim)
		assert.Nil(t, claim.ClaimedAt, "a consolidate that did not commit leaves every source claimable")
		assert.Equal(t, int64(200_000), queries.GetIsolatedBalance(svc.DB, s.ID), "and every source funded")
	}
	var merged, used int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ? AND id NOT IN ?",
		hub.ID, db.AppKindCashWallet, []uint{caller.ID, s1.ID, s2.ID}).Count(&merged).Error)
	assert.Zero(t, merged)
	require.NoError(t, svc.DB.Model(&db.CashConsumedProof{}).Count(&used).Error)
	assert.Zero(t, used)
}

// TestConsolidate_HappyPath_RecordsSourceLineage is the regression for a
// full-review finding: unlike cash_transfer's split path (which records
// SpunOffToWalletAppID on the source claim AND SplitFromWalletAppID on the
// new wallet), cash_consolidate never recorded any lineage at all — an
// operator had no way to trace which source wallets fed a merged wallet, or
// what became of a consumed source. Fixed by calling SetCashSliceSplitTarget
// once per source, all pointing at the merged wallet's own ID (SpunOffToWalletAppID
// isn't unique per target, so this is sufficient for an operator to find every
// source of a given merged wallet via a reverse query — no schema change
// needed, and no single-pointer App.SplitFromWalletAppID field is used since
// consolidate's shape is many-to-one, unlike a split's one-to-two).
//
// Reaches a genuine happy path (not just the guard-rejection paths the rest
// of this file covers) with two REAL, distinct invoices — no fault injection
// or reversal needed since nothing here is meant to fail.
func TestConsolidate_HappyPath_RecordsSourceLineage(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	const amount = uint64(123_000)
	s1 := guardSource(t, svc, hub, ownerPub, amount, 0, 0)
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")
	s2 := guardSource(t, svc, hub, ownerPub, 1_000, 0, 0)
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: int64(amount)},                        // fund merged from s1
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: int64(1_000)}, // fund merged from s2 — distinct hash, no collision
	}

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			guardParam(t, s1, ownerPriv, ownerPub, newPub, amount),
			guardParam(t, s2, ownerPriv, ownerPub, newPub, 1_000),
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})
	require.Nil(t, resp.Error)
	result, ok := resp.Result.(cashConsolidateResponse)
	require.True(t, ok, "unexpected result type %T", resp.Result)

	var mergedApp db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", result.NewWalletPubkey).First(&mergedApp).Error)

	for _, s := range []*db.App{s1, s2} {
		claim := cashWalletClaimByIdentity(t, svc, s.ID, db.CashIdentityPubkey, ownerPub)
		require.NotNil(t, claim)
		require.NotNil(t, claim.SpunOffToWalletAppID, "source %d's claim must record where its value went", s.ID)
		assert.Equal(t, mergedApp.ID, *claim.SpunOffToWalletAppID)
	}

	// The reverse lookup an operator would actually run: every claim that
	// fed this merged wallet, found without knowing the source IDs ahead of
	// time.
	var feeders []db.CashWalletClaim
	require.NoError(t, svc.DB.Where("spun_off_to_wallet_app_id = ?", mergedApp.ID).Find(&feeders).Error)
	assert.Len(t, feeders, 2, "both sources must be discoverable from the merged wallet's ID alone")
}
