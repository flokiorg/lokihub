package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ohstr/nmilat/niplsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/lsps5"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/lsps/transport"
)

const gateLSP = "03cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

// gateManager is a manager over a fresh catalogue holding gateLSP, as edit
// leaves it, with a node that records the LSPS messages sent.
func gateManager(t *testing.T, edit func(l *persist.LSP)) (*LiquidityManager, *mockLNClientJIT, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&persist.LSP{}))
	mockLN := &mockLNClientJIT{msgChan: make(chan lnclient.CustomMessage, 10), errChan: make(chan error),
		balances: &lnclient.BalancesResponse{}}
	m := &LiquidityManager{
		cfg:             &ManagerConfig{LNClient: mockLN, LSPManager: NewLSPManager(db)},
		transport:       transport.NewFLNDTransport(mockLN),
		eventQueue:      events.NewEventQueue(10),
		listeners:       make(map[string]chan events.Event),
		unclaimedEvents: make(map[string]events.Event),
		nostrPubkeys:    make(map[string]string),
	}
	m.lsps0Client = lsps0.NewClientHandler(m.transport, m.eventQueue)
	m.lsps1Client = lsps1.NewClientHandler(m.transport, m.eventQueue)
	m.lsps2Client = lsps2.NewClientHandler(m.transport, m.eventQueue)
	m.lsps5Client = lsps5.NewClientHandler(m.transport, m.eventQueue)
	l := persist.LSP{NodeKey: NodeKeyOf(gateLSP), Pubkey: gateLSP, Host: "10.0.0.1:9735", Name: "gate",
		Trusted: true, Usable: true, Status: "open"}
	announce(&l, announcedRow)
	if edit != nil {
		edit(&l)
	}
	require.NoError(t, db.Create(&l).Error)
	return m, mockLN, db
}

// announcedRow is K-LSP-1's announced LSPS2 fee row.
var announcedRow = niplsp.FeeRow{MinFeeMillis: 2000000, Proportional: 4000, MinLifetime: 4320,
	MaxClientToSelfDelay: 2016, MinPaymentSizeMillis: 10000000, MaxPaymentSizeMillis: 4000000000}

func sent(m *mockLNClientJIT) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sentMsgs)
}

// R2-S1: a purchase from an LSP that is untrusted, paused, closed or whose
// newest announcement is unusable is refused before any message is sent.
func TestPurchaseRefusedWithoutTrustOrOpenStatus(t *testing.T) {
	cases := map[string]func(l *persist.LSP){
		"untrusted": func(l *persist.LSP) { l.Trusted = false },
		"unusable":  func(l *persist.LSP) { l.Usable = false },
		"paused":    func(l *persist.LSP) { l.Status = "paused" },
		"closed":    func(l *persist.LSP) { l.Status = "closed" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			m, ln, _ := gateManager(t, edit)
			// A purchase let through would wait for an answer: fail fast.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			params := lsps2.OpeningFeeParams{MinFeeMloki: 1}
			_, err := m.BuyLiquidity(ctx, gateLSP, 100_000, &params, nil)
			assert.ErrorIs(t, err, ErrLSPNotUsable)
			_, err = m.CreateLSPS1Order(ctx, gateLSP, lsps1.OrderParams{LspBalanceLoki: 100_000}, nil, nil)
			assert.ErrorIs(t, err, ErrLSPNotUsable)
			assert.Zero(t, sent(ln), "nothing is sent to the LSP")
		})
	}
	// Paused is still contactable (existing orders, info); closed is not.
	m, _, _ := gateManager(t, func(l *persist.LSP) { l.Status = "paused" })
	assert.NoError(t, m.CheckContact(gateLSP))
}

// R2-S1: each purchase runs the live check first; one that finds an announced
// protocol missing refuses the purchase, and an automatic one also refuses
// live terms worse than announced.
func TestPurchaseRunsTheLiveCheck(t *testing.T) {
	m, ln, _ := gateManager(t, nil)
	var checks int
	result := &LiveCheck{Usable: false, Error: "list_protocols lacks 2"}
	m.SetLiveChecker(func(_ context.Context, key string) (*LiveCheck, error) {
		checks++
		assert.Equal(t, NodeKeyOf(gateLSP), key)
		return result, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	params := lsps2.OpeningFeeParams{MinFeeMloki: 1}
	_, err := m.BuyLiquidity(ctx, gateLSP, 100_000, &params, nil)
	assert.ErrorIs(t, err, ErrLSPNotUsable)
	assert.Equal(t, 1, checks)
	assert.Zero(t, sent(ln))

	result = &LiveCheck{Usable: true, Worse: true}
	assert.ErrorIs(t, m.CheckPurchase(context.TODO(), gateLSP, true), ErrLSPNotUsable, "automatic: no one to ask again")
	assert.NoError(t, m.CheckPurchase(context.TODO(), gateLSP, false), "manual: the user chose these terms")
	assert.Equal(t, 3, checks)
}

// R2-S1: automatic JIT skips an LSP whose stored live check found it unusable,
// and buys from none when no LSP passes.
func TestJITHonoursStoredLiveCheck(t *testing.T) {
	stored, _ := json.Marshal(map[string]any{"usable": false, "missingProtocol": []int{2}})
	m, ln, _ := gateManager(t, func(l *persist.LSP) { l.LastCheckResult = string(stored) })
	_, err := m.EnsureInboundLiquidity(context.TODO(), 100_000)
	assert.ErrorIs(t, err, ErrLSPNotUsable)
	assert.Zero(t, sent(ln), "no purchase message to an LSP its live check refused")
}

// NIP-LSP §Client Behavior: a closed announcement has stopped selling, so the
// node sends it no LSPS message at all, even when the user still trusts it;
// an open or paused one is still contactable.
func TestClosedLSPIsNotContactable(t *testing.T) {
	m, ln, _ := gateManager(t, func(l *persist.LSP) { l.Status = "closed" })
	assert.ErrorIs(t, m.CheckContact(gateLSP), ErrLSPNotUsable)
	_, err := m.LSPS2Terms(gateLSP, lsps2.OpeningFeeParams{})
	assert.ErrorIs(t, err, ErrLSPNotUsable)
	assert.Zero(t, sent(ln))
	for _, status := range []string{"open", "paused"} {
		t.Run(status, func(t *testing.T) {
			m, _, _ := gateManager(t, func(l *persist.LSP) { l.Status = status })
			assert.NoError(t, m.CheckContact(gateLSP))
		})
	}
}

// NIP-LSP §Client Behavior step 4: an announcement that expired since the
// catalogue's last sweep makes its LSP not contactable at once; one still
// before its expiration is contactable.
func TestExpiredAnnouncementIsNotContactable(t *testing.T) {
	m, ln, _ := gateManager(t, func(l *persist.LSP) { l.ExpiresAt = time.Now().Unix() - 1 })
	assert.ErrorIs(t, m.CheckContact(gateLSP), ErrLSPNotUsable)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := m.EnsureInboundLiquidity(ctx, 100_000)
	assert.ErrorIs(t, err, ErrLSPNotUsable)
	assert.Zero(t, sent(ln))

	t.Run("unexpired", func(t *testing.T) {
		m, _, _ := gateManager(t, func(l *persist.LSP) { l.ExpiresAt = time.Now().Unix() + 60 })
		assert.NoError(t, m.CheckContact(gateLSP))
	})
}
