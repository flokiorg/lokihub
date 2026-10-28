package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// countingLimiter allows or refuses every call and counts the calls it saw.
type countingLimiter struct {
	allow bool
	calls int
}

func (l *countingLimiter) Allow(string, int) bool { l.calls++; return l.allow }

// mintStepCase is a mint_cash request that fails at two adjacent steps of NIP-CASH
// §Minting Cash, Processing Algorithm; want is the step that must win.
type mintStepCase struct {
	name string
	// params builds the request's params from a fresh request_id and fixtures.
	params func(f mintFixtures) map[string]any
	// meta is the call's arrival and deadline; nil is a valid deadline.
	meta func() models.CallMeta
	// setup runs before the call: a held Hub lock, a used id, an exhausted limiter.
	setup func(t *testing.T, f *mintFixtures)
	// hubFunds is the Hub's balance; 0 is plenty.
	hubFunds   uint64
	wantCode   string
	wantMsg    string
	wantMarked bool
}

type mintFixtures struct {
	svc       *tests.TestService
	hub       *db.App
	requestID string
	pubkey    string
	trustedIA string
	limiter   *countingLimiter
}

func recipient(identityType, value, ia string, amount uint64) map[string]any {
	r := map[string]any{"identity_type": identityType, "identity_value": value, "amount_millis": amount}
	if ia != "" {
		r["ia_pubkey"] = ia
	}
	return r
}

func goodRecipients(f mintFixtures) []any {
	return []any{recipient(db.CashIdentityPubkey, f.pubkey, "", 1_000)}
}

func manyRecipients(n int) []any {
	out := make([]any, n)
	for i := range out {
		pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
		out[i] = recipient(db.CashIdentityPubkey, pk, "", 1)
	}
	return out
}

func untrustedIA() string {
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	return pk
}

// U-5.1: mint_cash's step order. Each row fails at two adjacent steps and pins
// which one answers, that nothing is created, and that the id is marked exactly
// when the call got past step 2.
func TestMintCash_StepOrder(t *testing.T) {
	passed := func() models.CallMeta {
		now := time.Now().Unix()
		return models.CallMeta{Transport: models.TransportStandard, ArrivedAt: now - 120, Deadline: now - 1, DeadlinePresent: true}
	}
	noDeadline := func() models.CallMeta {
		return models.CallMeta{Transport: models.TransportStandard, ArrivedAt: time.Now().Unix()}
	}
	cases := []mintStepCase{
		{
			name: "lock beats a malformed request_id",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": "nothex", "recipients": goodRecipients(f)}
			},
			setup: func(t *testing.T, f *mintFixtures) {
				release, ok := cashwallet.LockHub(f.hub.ID)
				require.True(t, ok)
				t.Cleanup(release)
			},
			wantCode: constants.ERROR_RATE_LIMITED,
		},
		{
			name: "a malformed request_id is refused before anything is marked",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": "nothex", "recipients": []any{}}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "request_id",
		},
		{
			name: "a used id beats bad recipients, an untrusted IA, low balance and a passed deadline",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": []any{
					recipient(db.CashIdentityConnectionKey, f.pubkey, untrustedIA(), 0),
				}}
			},
			meta: passed,
			setup: func(t *testing.T, f *mintFixtures) {
				require.NoError(t, cashrequest.New(f.svc.DB).MarkUsed(f.requestID, "cash_transfer"))
			},
			hubFunds: 1, wantCode: constants.ERROR_REQUEST_REUSED, wantMarked: true,
		},
		{
			name: "a missing deadline beats empty recipients, and the id stays used",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": []any{}}
			},
			meta:     noDeadline,
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "expiration", wantMarked: true,
		},
		{
			name: "a passed deadline beats a duplicate recipient",
			params: func(f mintFixtures) map[string]any {
				r := recipient(db.CashIdentityPubkey, f.pubkey, "", 1_000)
				return map[string]any{"request_id": f.requestID, "recipients": []any{r, r}}
			},
			meta:     passed,
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "expiration", wantMarked: true,
		},
		{
			name: "recipients over the cap beat a refused field",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": manyRecipients(101), "idempotency_key": "x"}
			},
			wantCode: constants.ERROR_QUOTA_EXCEEDED, wantMarked: true,
		},
		{
			name: "a duplicate recipient beats an untrusted IA",
			params: func(f mintFixtures) map[string]any {
				ia := untrustedIA()
				r := recipient(db.CashIdentityConnectionKey, f.pubkey, ia, 1_000)
				return map[string]any{"request_id": f.requestID, "recipients": []any{r, r}}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "duplicate", wantMarked: true,
		},
		{
			name: "a cash entry beside another beats a malformed commitment",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": []any{
					recipient(db.CashIdentityCash, "not-a-commitment", "", 1_000),
					recipient(db.CashIdentityPubkey, f.pubkey, "", 1_000),
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "only recipient", wantMarked: true,
		},
		{
			name: "one recipient over the ceiling is QUOTA_EXCEEDED before any sum",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": []any{
					recipient(db.CashIdentityPubkey, f.pubkey, "", 1_000_001),
				}, "idempotency_key": "x"}
			},
			wantCode: constants.ERROR_QUOTA_EXCEEDED, wantMsg: "recipient 0", wantMarked: true,
		},
		{
			name: "a sum over the ceiling beats an untrusted IA",
			params: func(f mintFixtures) map[string]any {
				pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
				return map[string]any{"request_id": f.requestID, "recipients": []any{
					recipient(db.CashIdentityPubkey, f.pubkey, "", 600_000),
					recipient(db.CashIdentityConnectionKey, pk, untrustedIA(), 600_000),
				}}
			},
			wantCode: constants.ERROR_QUOTA_EXCEEDED, wantMsg: "total", wantMarked: true,
		},
		{
			name: "an untrusted IA beats a refused field",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": []any{
					recipient(db.CashIdentityConnectionKey, f.pubkey, untrustedIA(), 1_000),
				}, "min_transfer_millis": 0}
			},
			wantCode: constants.ERROR_RESTRICTED, wantMsg: "Identity Authority", wantMarked: true,
		},
		{
			name: "a malformed commitment beats a refused field",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": []any{
					map[string]any{"identity_type": db.CashIdentityCash, "identity_value": "ABC", "amount_millis": 1_000, "cash_secret": "00"},
				}}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "commitment", wantMarked: true,
		},
		{
			name: "a refused field beats an expiry over the ceiling",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": goodRecipients(f), "expiry": 7200, "idempotency_key": ""}
			},
			wantCode: constants.ERROR_BAD_REQUEST, wantMsg: "idempotency_key", wantMarked: true,
		},
		{
			name: "an expiry over the ceiling beats low balance",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": goodRecipients(f), "expiry": 7200}
			},
			hubFunds: 1, wantCode: constants.ERROR_RESTRICTED, wantMsg: "max_exp_secs", wantMarked: true,
		},
		{
			// BAD_REQUEST is for the bound under a "never" ceiling only (NIP-CASH
			// §Minting Cash); a finite ceiling refuses it like any longer expiry.
			name: "under a finite ceiling an expiry past the representational bound is RESTRICTED",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": goodRecipients(f), "expiry": constants.MAX_EXPIRY_SECS + 1}
			},
			hubFunds: 1, wantCode: constants.ERROR_RESTRICTED, wantMsg: "max_exp_secs", wantMarked: true,
		},
		{
			name: "low balance beats an exhausted rate limit, which is not charged",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": goodRecipients(f)}
			},
			setup:    func(t *testing.T, f *mintFixtures) { f.limiter.allow = false },
			hubFunds: 1, wantCode: constants.ERROR_INSUFFICIENT_BALANCE, wantMarked: true,
		},
		{
			name: "an exhausted rate limit stops the commit",
			params: func(f mintFixtures) map[string]any {
				return map[string]any{"request_id": f.requestID, "recipients": goodRecipients(f)}
			},
			setup:    func(t *testing.T, f *mintFixtures) { f.limiter.allow = false },
			wantCode: constants.ERROR_RATE_LIMITED, wantMarked: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
			funds := tc.hubFunds
			if funds == 0 {
				funds = 100_000_000
			}
			tests.FundApp(svc, hub.ID, funds, "fund")
			pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
			f := mintFixtures{svc: svc, hub: hub, requestID: testRequestID(), pubkey: pk, limiter: &countingLimiter{allow: true}}
			if tc.setup != nil {
				tc.setup(t, &f)
			}

			controller := NewTestNip47Controller(svc)
			controller.cashRateLimiter = f.limiter
			raw, err := json.Marshal(tc.params(f))
			require.NoError(t, err)
			req := &models.Request{Method: constants.NIP47MethodMintCash, Params: raw}
			meta := testMintMeta()
			if tc.meta != nil {
				meta = tc.meta()
			}
			var resp *models.Response
			controller.HandleMintCashEvent(context.TODO(), req, 0, hub, func(r *models.Response, _ nostr.Tags) { resp = r }, meta)

			require.NotNil(t, resp)
			require.NotNil(t, resp.Error, "expected a refusal")
			assert.Equal(t, tc.wantCode, resp.Error.Code, resp.Error.Message)
			if tc.wantMsg != "" {
				assert.Contains(t, resp.Error.Message, tc.wantMsg)
			}

			var bills int64
			require.NoError(t, svc.DB.Model(&db.App{}).Where("kind = ?", db.AppKindCashWallet).Count(&bills).Error)
			assert.Zero(t, bills, "a refused mint creates no bill")
			marked, err := cashrequest.New(svc.DB).IsUsed(f.requestID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMarked, marked, "the id is used exactly when the call passed step 2")
			if tc.wantCode != constants.ERROR_RATE_LIMITED || strings.Contains(resp.Error.Message, "another mint") {
				assert.Zero(t, f.limiter.calls, "a call refused before the rate limit is not charged")
			}
		})
	}
}

// U-5.RC1: two recipients whose sum leaves int64 are refused with BAD_REQUEST,
// never wrapped, when the ceiling lets each through.
func TestMintCash_RecipientSumOverflowIsBadRequest(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	require.NoError(t, svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", hub.ID).
		Update("per_wallet_max_mloki", int64(1)<<62).Error)

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk3, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	big := uint64(1) << 62
	raw := []byte(fmt.Sprintf(`{"request_id":%q,"recipients":[
		{"identity_type":"pubkey","identity_value":%q,"amount_millis":%d},
		{"identity_type":"pubkey","identity_value":%q,"amount_millis":%d},
		{"identity_type":"pubkey","identity_value":%q,"amount_millis":%d}]}`,
		testRequestID(), pk1, big, pk2, big, pk3, big))
	var resp *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(context.TODO(),
		&models.Request{Method: constants.NIP47MethodMintCash, Params: raw}, 0, hub,
		func(r *models.Response, _ nostr.Tags) { resp = r }, testMintMeta())
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "overflow")
}

// U-5.11: the mint_cash reply carries exactly wallet_pubkey, cash_token,
// expires_at (when the bill expires) and recipients; cash_token is the bill's
// only credential, so there is no URI form.
func TestMintCash_ReplyKeys(t *testing.T) {
	for _, maxExp := range []int{3600, 0} {
		t.Run(fmt.Sprintf("max_exp_secs=%d", maxExp), func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			hub := tests.CreateCashHub(t, svc, 1_000_000, maxExp)
			tests.FundApp(svc, hub.ID, 10_000_000, "fund")
			pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
			raw, err := json.Marshal(map[string]any{"request_id": testRequestID(),
				"recipients": []any{recipient(db.CashIdentityPubkey, pk, "", 1_000)}})
			require.NoError(t, err)
			var resp *models.Response
			NewTestNip47Controller(svc).HandleMintCashEvent(context.TODO(),
				&models.Request{Method: constants.NIP47MethodMintCash, Params: raw}, 0, hub,
				func(r *models.Response, _ nostr.Tags) { resp = r }, testMintMeta())
			require.NotNil(t, resp)
			require.Nil(t, resp.Error)

			encoded, err := json.Marshal(resp.Result)
			require.NoError(t, err)
			var keys map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &keys))
			want := []string{"cash_token", "recipients", "wallet_pubkey"}
			if maxExp != 0 {
				want = append(want, "expires_at")
			}
			got := make([]string, 0, len(keys))
			for k := range keys {
				got = append(got, k)
			}
			assert.ElementsMatch(t, want, got)
		})
	}
}
