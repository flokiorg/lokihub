package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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
	if edit != nil {
		edit(&l)
	}
	require.NoError(t, db.Create(&l).Error)
	return m, mockLN, db
}

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
			params := lsps2.OpeningFeeParams{MinFeeMloki: 1}
			_, err := m.BuyLiquidity(context.TODO(), gateLSP, 100_000, &params)
			assert.ErrorIs(t, err, ErrLSPNotUsable)
			_, err = m.CreateLSPS1Order(context.TODO(), gateLSP, lsps1.OrderParams{LspBalanceLoki: 100_000}, nil)
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
	params := lsps2.OpeningFeeParams{MinFeeMloki: 1}
	_, err := m.BuyLiquidity(context.TODO(), gateLSP, 100_000, &params)
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
