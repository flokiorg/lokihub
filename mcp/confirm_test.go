package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// payAPI counts real payment executions.
type payAPI struct {
	testAPI
	pays      *int32
	transfers *int32
}

func (p payAPI) SendPayment(context.Context, string, *uint64, *uint, map[string]interface{}) (*api.SendPaymentResponse, error) {
	atomic.AddInt32(p.pays, 1)
	return &api.SendPaymentResponse{}, nil
}

func (p payAPI) Transfer(context.Context, *uint, *uint, uint64) error {
	atomic.AddInt32(p.transfers, 1)
	return nil
}

func stepOf(t *testing.T, sc any) PaymentStep[json.RawMessage] {
	t.Helper()
	b, _ := json.Marshal(sc)
	var st PaymentStep[json.RawMessage]
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("decode step: %v (%s)", err, b)
	}
	return st
}

// TestPaymentNeedsTwoStepConfirmation: the owner's rule — every payment tool
// carries the warning, the first call never pays, and only a second call with
// the code issued for THE SAME payment executes, exactly once.
func TestPaymentNeedsTwoStepConfirmation(t *testing.T) {
	var pays, transfers int32
	s := NewServer(payAPI{pays: &pays, transfers: &transfers}, &fakeAuth{})
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: []string{GroupWallet.Scope(ModeWrite)}})
	defer done()

	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range lt.Tools {
		if (tl.Name == "send_payment" || tl.Name == "transfer") && !strings.Contains(tl.Description, "cannot be undone") {
			t.Errorf("%s description lacks the payment warning", tl.Name)
		}
	}

	inv, _ := tests.SignedInvoice(21_000, time.Now(), time.Hour)
	other, _ := tests.SignedInvoice(9_999_000, time.Now(), time.Hour)

	// Step 1: preview — nothing paid; amount comes from the invoice itself.
	res := callTool(t, cs, "send_payment", map[string]any{"idempotency_key": "p1", "invoice": inv})
	if res.IsError {
		t.Fatalf("preview errored: %s", resultText(res))
	}
	st := stepOf(t, res.StructuredContent)
	if st.Status != "needs_confirmation" || st.ConfirmCode == "" || st.Preview == nil || st.Warning == "" {
		t.Fatalf("bad preview: %+v", st)
	}
	if st.Preview.AmountMloki != 21_000 || st.Preview.AmountLoki != "21" {
		t.Errorf("preview amount = %d (%s), want the invoice's 21000 mloki", st.Preview.AmountMloki, st.Preview.AmountLoki)
	}
	if atomic.LoadInt32(&pays) != 0 {
		t.Fatal("preview paid")
	}

	// The code cannot be used for a different invoice (injected swap) — and the
	// attempt burns it.
	if r := callTool(t, cs, "send_payment", map[string]any{"idempotency_key": "p1", "invoice": other, "confirm_code": st.ConfirmCode}); !r.IsError {
		t.Error("confirm code accepted for a different invoice")
	}
	if r := callTool(t, cs, "send_payment", map[string]any{"idempotency_key": "p1", "invoice": inv, "confirm_code": st.ConfirmCode}); !r.IsError {
		t.Error("burned confirm code was accepted")
	}
	if atomic.LoadInt32(&pays) != 0 {
		t.Fatal("paid with a mismatched/burned code")
	}

	// Fresh preview, then confirm → pays once; a retry does not pay again.
	st = stepOf(t, callTool(t, cs, "send_payment", map[string]any{"idempotency_key": "p2", "invoice": inv}).StructuredContent)
	confirm := map[string]any{"idempotency_key": "p2", "invoice": inv, "confirm_code": st.ConfirmCode}
	if r := callTool(t, cs, "send_payment", confirm); r.IsError || stepOf(t, r.StructuredContent).Status != "executed" {
		t.Fatalf("confirmed payment failed: %s", resultText(r))
	}
	callTool(t, cs, "send_payment", confirm)
	if got := atomic.LoadInt32(&pays); got != 1 {
		t.Fatalf("payments = %d, want exactly 1", got)
	}
	// The used code cannot pay again under a new idempotency key.
	if r := callTool(t, cs, "send_payment", map[string]any{"idempotency_key": "p3", "invoice": inv, "confirm_code": st.ConfirmCode}); !r.IsError {
		t.Error("confirm code reused for a second payment")
	}

	// transfer follows the same two steps.
	tr := map[string]any{"idempotency_key": "t1", "amount_mloki": float64(5000)}
	st = stepOf(t, callTool(t, cs, "transfer", tr).StructuredContent)
	if st.Status != "needs_confirmation" || atomic.LoadInt32(&transfers) != 0 {
		t.Fatalf("transfer executed without confirmation: %+v", st)
	}
	tr["confirm_code"] = st.ConfirmCode
	callTool(t, cs, "transfer", tr)
	if got := atomic.LoadInt32(&transfers); got != 1 {
		t.Fatalf("transfers = %d, want 1", got)
	}
}

// TestConfirmStoreBinding: codes are bound to connection, tool and arguments,
// single-use, and expire.
func TestConfirmStoreBinding(t *testing.T) {
	c := NewConfirmStore()
	a, b := ArgsHash("a"), ArgsHash("b")
	code, _ := c.Issue(1, "send_payment", a)
	if err := c.Redeem(code, 2, "send_payment", a); !errors.Is(err, ErrConfirmInvalid) {
		t.Error("another connection redeemed the code")
	}
	code, _ = c.Issue(1, "send_payment", a)
	if err := c.Redeem(code, 1, "transfer", a); err == nil {
		t.Error("code redeemed by another tool")
	}
	code, _ = c.Issue(1, "send_payment", a)
	if err := c.Redeem(code, 1, "send_payment", b); err == nil {
		t.Error("code redeemed for different args")
	}
	code, _ = c.Issue(1, "send_payment", a)
	c.now = func() time.Time { return time.Now().Add(confirmTTL + time.Second) }
	if err := c.Redeem(code, 1, "send_payment", a); err == nil {
		t.Error("expired code accepted")
	}
	c.now = time.Now
	for i := 0; i < maxOpenConfirms; i++ {
		if _, err := c.Issue(7, "send_payment", a); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	if _, err := c.Issue(7, "send_payment", a); !errors.Is(err, ErrTooManyConfirms) {
		t.Error("unbounded previews per connection")
	}
}

// TestMoneyToolsAllWarnAndConfirm is the ratchet for the owner's rule: every
// tool that can move, take back, lock or strand money carries the warning, and
// every non-approval one is two-step (no effect on the first call).
func TestMoneyToolsAllWarnAndConfirm(t *testing.T) {
	twoStep := []string{"send_payment", "transfer", "delete_app", "delete_app_confirmed",
		"delete_cash_wallet", "delete_cash_claim", "delete_circle_child", "delete_circle_hub",
		// Formerly deferred: LSP purchases and trust, cash minting, selling
		// terms, and irreversible public publishes under the node identity.
		"create_cash_wallet", "lsp_order_channel", "lsp_buy_jit_channel", "set_lsp_trust", "put_order_terms",
		"create_listing", "update_listing", "listing_action",
		"publish_node_profile", "publish_node_note", "publish_circle_follow_set",
		// Back-office: LSP fees/limits, withdrawal terms and switch, LSP announcement.
		"put_lsp_config", "set_withdraw_terms", "set_withdraw_node", "set_lsp_announce_state"}
	ownerApproved := []string{"redeem_onchain_funds", "open_channel", "close_channel"}

	s := NewServer(testAPI{}, &fakeAuth{})
	s.SetAppResolver(func(id uint) *db.App { return &db.App{ID: id, Kind: db.AppKindCircleHub} })
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	schema := map[string]string{}
	for _, tl := range lt.Tools {
		byName[tl.Name] = tl.Description
		b, _ := json.Marshal(tl.InputSchema)
		schema[tl.Name] = string(b)
	}
	for _, n := range append(append([]string{}, twoStep...), ownerApproved...) {
		d, ok := byName[n]
		if !ok {
			t.Errorf("%s not advertised", n)
			continue
		}
		if !strings.Contains(d, "cannot be undone") || !strings.Contains(d, "untrusted") {
			t.Errorf("%s lacks the money warning", n)
		}
	}
	for _, n := range twoStep {
		if !strings.Contains(schema[n], "confirm_code") {
			t.Errorf("%s is not two-step (no confirm_code)", n)
		}
	}
	// Access / publish changes (pentest D PD-10, F F-1, F-6): two-step with an
	// untrusted-text warning, even though they move no funds.
	for _, n := range []string{"set_listing_relays", "delete_identity_authority", "replace_circle_allowlist", "remove_circle_member"} {
		if !strings.Contains(schema[n], "confirm_code") || !strings.Contains(byName[n], "untrusted") {
			t.Errorf("%s is not two-step with a warning", n)
		}
	}
}

type limitControls struct{ limit uint64 }

func (limitControls) Paused() bool                 { return false }
func (l limitControls) ApprovalLimitMloki() uint64 { return l.limit }

// TestPaymentAboveLimitNeedsOwner (owner decision D22): below the owner's
// limit the assistant's confirmation pays; above it the payment waits in the
// owner's approval queue and runs exactly once on approval.
func TestPaymentAboveLimitNeedsOwner(t *testing.T) {
	var pays, transfers int32
	s := NewServer(payAPI{pays: &pays, transfers: &transfers}, &fakeAuth{})
	s.SetControls(limitControls{limit: 50_000}) // 50 loki
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: []string{GroupWallet.Scope(ModeWrite)}})
	defer done()

	pay := func(key string, mloki int64) PaymentStep[json.RawMessage] {
		inv, _ := tests.SignedInvoice(mloki, time.Now(), time.Hour)
		args := map[string]any{"idempotency_key": key, "invoice": inv}
		st := stepOf(t, callTool(t, cs, "send_payment", args).StructuredContent)
		args["confirm_code"] = st.ConfirmCode
		return stepOf(t, callTool(t, cs, "send_payment", args).StructuredContent)
	}
	if st := pay("small", 21_000); st.Status != "executed" || atomic.LoadInt32(&pays) != 1 {
		t.Fatalf("payment under the limit: %+v pays=%d", st, pays)
	}
	big := pay("big", 900_000)
	if big.Status != "pending_approval" || big.ActionID == "" || atomic.LoadInt32(&pays) != 1 {
		t.Fatalf("payment over the limit was not held for the owner: %+v pays=%d", big, pays)
	}
	if _, err := s.Pending().Approve(big.ActionID); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&pays) != 2 {
		t.Fatalf("approved payment did not run exactly once: %d", pays)
	}
	// The owner sees hub facts only — never the invoice memo.
	for _, a := range s.Pending().ListAll() {
		if a.ID == big.ActionID && (!strings.Contains(a.Summary, "900 loki") || strings.Contains(a.Summary, "test")) {
			t.Errorf("owner summary = %q", a.Summary)
		}
	}
	// Default (no limit configured) is 10,000 loki.
	if NewServer(testAPI{}, &fakeAuth{}).approvalLimit() != DefaultApprovalLimitMloki {
		t.Error("default limit not applied")
	}
}
