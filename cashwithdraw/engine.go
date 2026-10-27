package cashwithdraw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// Engine stages at which EngineHooks.Before runs (plan §4.2).
const (
	EngineAfterFund        = "AfterFund"
	EngineAfterAboutToSign = "AfterAboutToSign"
	EngineAfterFinalize    = "AfterFinalize"
	EngineAfterStore       = "AfterStore"
	EngineAfterPublish     = "AfterPublish"
	EngineAfterChainUpdate = "AfterChainUpdate"
)

// EngineStages are the engine's stages, in a pass's order.
var EngineStages = []string{EngineAfterFund, EngineAfterAboutToSign, EngineAfterFinalize, EngineAfterStore,
	EngineAfterPublish, EngineAfterChainUpdate}

// ErrEngineStopped is what a hook returns to stop a pass where it stands, as a
// crash would (the next engine resumes from the database).
var ErrEngineStopped = errors.New("payout engine stopped by a test hook")

// EngineHooks are the engine's test seams: Before runs at every stage (an error
// ends the pass there), and the holds stop signing, broadcasting or bumping.
// nil in production; the regtest dev build drives the holds over HTTP.
type EngineHooks struct {
	Before        func(stage string) error
	HoldSign      atomic.Bool
	HoldBroadcast atomic.Bool
	HoldBump      atomic.Bool
}

// NoticeSender publishes a state notice on a relay.
type NoticeSender interface {
	SendNotice(ctx context.Context, relay string, ev *nip01.Event) error
}

// Engine pays committed payouts from the node's on-chain wallet (NIP-CASH
// §Paying a Payout, §Cancelling a Payout, §State Notice): one engine per node,
// one pass at a time, every step recorded in the database before the node is
// asked to act, so a crash at any point resumes without a second version that
// could confirm. It is the commit's Funds.
type Engine struct {
	S       *Service
	Hooks   *EngineHooks
	Notices NoticeSender
	// Rand draws notice delays; nil is crypto/rand.
	Rand io.Reader

	ready    atomic.Bool
	floorSat atomic.Int64
	wake     chan struct{}
	passMu   sync.Mutex

	leaseMu sync.Mutex
	leaseID *[32]byte

	// Metrics, read by the status endpoint and the tests.
	Bumps, Reorgs, Invalidations, Vanished, InvariantFailures atomic.Int64
}

// NewEngine is the payout engine of s; it becomes s's Funds.
func NewEngine(s *Service) *Engine {
	e := &Engine{S: s, wake: make(chan struct{}, 1)}
	s.Funds = e
	return e
}

func (e *Engine) env() config.WithdrawEnv { return e.S.env() }

func (e *Engine) before(stage string) error {
	if e.Hooks == nil || e.Hooks.Before == nil {
		return nil
	}
	return e.Hooks.Before(stage)
}

func (e *Engine) held(f func(h *EngineHooks) bool) bool { return e.Hooks != nil && f(e.Hooks) }

func (e *Engine) rand() io.Reader {
	if e.Rand != nil {
		return e.Rand
	}
	return rand.Reader
}

// --- Funds ---

// Ready reports the startup reconcile done: until then nothing is offered.
func (e *Engine) Ready() bool { return e.ready.Load() }

// Signal wakes the engine for a pass.
func (e *Engine) Signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Snapshot reads the confirmed on-chain balance.
func (e *Engine) Snapshot(ctx context.Context) (FundsSnapshot, error) {
	p := e.S.payer()
	if p == nil {
		return FundsSnapshot{}, errors.New("no on-chain capability")
	}
	bal, err := p.ConfirmedBalance(ctx)
	if err != nil {
		return FundsSnapshot{}, err
	}
	return FundsSnapshot{ConfirmedSat: bal}, nil
}

// UnconfirmedValueTx is Σ payout_millis of payouts queued or broadcast (a
// reorganised confirmed payout is broadcast again, so it counts again).
func UnconfirmedValueTx(tx *gorm.DB) (uint64, error) {
	var sum int64
	err := tx.Model(&db.CashWithdrawal{}).Where("state IN ?", []string{db.CashWithdrawQueued, db.CashWithdrawBroadcast}).
		Select("COALESCE(SUM(payout_millis), 0)").Scan(&sum).Error
	return uint64(max(sum, 0)), err //nolint:gosec // non-negative
}

// AtUnconfirmedBoundTx: the unconfirmed payouts plus this one exceed
// CASH_WITHDRAW_UNCONFIRMED_PPM of the confirmed balance.
func (e *Engine) AtUnconfirmedBoundTx(tx *gorm.DB, snap FundsSnapshot, payoutMillis uint64) (bool, error) {
	v, err := UnconfirmedValueTx(tx)
	if err != nil {
		return false, err
	}
	bound := uint64(max(snap.ConfirmedSat, 0)) * 1000 / 1_000_000 * uint64(e.env().UnconfirmedPpm()) //nolint:gosec // non-negative
	return v+payoutMillis > bound, nil
}

// needSat is what a payout sets aside: its payout plus three times its quoted
// network fee (bump headroom), in base units.
func needSat(payoutMillis, networkFeeMillis uint64) int64 {
	return int64((payoutMillis+999)/1000 + 3*((networkFeeMillis+999)/1000)) //nolint:gosec // bounded
}

// CanSetAsideTx: the coins neither in a version nor below the floor, less what
// is already set aside, cover need.
func (e *Engine) CanSetAsideTx(tx *gorm.DB, payoutMillis, networkFeeMillis uint64) (bool, error) {
	var coins, aside int64
	if err := tx.Model(&db.CashWithdrawCoin{}).Where("state IN ?", []string{db.CashCoinAvailable, db.CashCoinSetAside}).
		Select("COALESCE(SUM(value_sat), 0)").Scan(&coins).Error; err != nil {
		return false, err
	}
	if err := tx.Model(&db.CashWithdrawSetAside{}).Select("COALESCE(SUM(amount_sat), 0)").Scan(&aside).Error; err != nil {
		return false, err
	}
	return coins-e.floorSat.Load()-aside >= needSat(payoutMillis, networkFeeMillis), nil
}

// SetAsideTx records the payout's set-aside, durably and without expiry, and
// assigns it coins, largest first. The coins are leased at the node by the next
// pass; this table is the source of truth.
func (e *Engine) SetAsideTx(tx *gorm.DB, withdrawalID uint, payoutMillis, networkFeeMillis uint64) error {
	need := needSat(payoutMillis, networkFeeMillis)
	if err := tx.Create(&db.CashWithdrawSetAside{WithdrawalID: withdrawalID, AmountSat: need, CreatedAt: e.S.now().Unix()}).Error; err != nil {
		return err
	}
	return assignCoinsTx(tx, withdrawalID, need)
}

// assignCoinsTx assigns available coins to a payout until they cover need.
func assignCoinsTx(tx *gorm.DB, withdrawalID uint, need int64) error {
	var have int64
	if err := tx.Model(&db.CashWithdrawCoin{}).Where("state = ? AND withdrawal_id = ?", db.CashCoinSetAside, withdrawalID).
		Select("COALESCE(SUM(value_sat), 0)").Scan(&have).Error; err != nil {
		return err
	}
	if have >= need {
		return nil
	}
	var free []db.CashWithdrawCoin
	if err := tx.Where("state = ?", db.CashCoinAvailable).Order("value_sat DESC, outpoint").Find(&free).Error; err != nil {
		return err
	}
	for _, c := range free {
		if have >= need {
			break
		}
		res := tx.Model(&db.CashWithdrawCoin{}).Where("outpoint = ? AND state = ?", c.Outpoint, db.CashCoinAvailable).
			Updates(map[string]interface{}{"state": db.CashCoinSetAside, "withdrawal_id": withdrawalID})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			have += c.ValueSat
		}
	}
	return nil
}

// IsChangeScriptTx: script is a change output of one of our versions.
func (e *Engine) IsChangeScriptTx(tx *gorm.DB, script []byte) (bool, error) {
	want := hex.EncodeToString(script)
	var versions []db.CashWithdrawVersion
	if err := tx.Select("outputs").Find(&versions).Error; err != nil {
		return false, err
	}
	for _, v := range versions {
		for _, o := range outputsOf(&v) {
			if o.WithdrawalID == nil && o.Script == want {
				return true, nil
			}
		}
	}
	return false, nil
}

// PayoutTxHexTx is the raw transaction a payout reports: the confirmed version
// paying it, else the last signed one.
func (e *Engine) PayoutTxHexTx(tx *gorm.DB, withdrawalID uint) (string, error) {
	var vs []db.CashWithdrawVersion
	if err := tx.Joins("JOIN cash_withdraw_version_outputs o ON o.version_id = cash_withdraw_versions.id").
		Where("o.withdrawal_id = ? AND cash_withdraw_versions.raw_tx IS NOT NULL", withdrawalID).
		Order("cash_withdraw_versions.id DESC").Find(&vs).Error; err != nil {
		return "", err
	}
	for _, v := range vs {
		if v.State == db.CashVersionConfirmed || v.State == db.CashVersionFinal {
			return *v.RawTx, nil
		}
	}
	if len(vs) > 0 {
		return *vs[0].RawTx, nil
	}
	return "", nil
}

// DropVersionsTx deletes a payout's version rows with its record. A version
// shared with payouts still kept stays.
func (e *Engine) DropVersionsTx(tx *gorm.DB, withdrawalID uint) error {
	return tx.Where("withdrawal_id = ?", withdrawalID).Delete(&db.CashWithdrawVersionOutput{}).Error
}

// --- the lease id ---

const leaseIDKey = "CashWithdrawLeaseID"

// lease is the engine's lease id at the node, 32 random bytes kept in
// user_configs.
func (e *Engine) lease() ([32]byte, error) {
	e.leaseMu.Lock()
	defer e.leaseMu.Unlock()
	if e.leaseID != nil {
		return *e.leaseID, nil
	}
	var id [32]byte
	var row db.UserConfig
	err := e.S.DB.Where("key = ?", leaseIDKey).Limit(1).Find(&row).Error
	if err != nil {
		return id, err
	}
	if row.Value == "" {
		if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
			return id, err
		}
		if err := e.S.DB.Create(&db.UserConfig{Key: leaseIDKey, Value: hex.EncodeToString(id[:])}).Error; err != nil {
			return id, err
		}
	} else {
		b, err := hex.DecodeString(row.Value)
		if err != nil || len(b) != 32 {
			return id, fmt.Errorf("stored lease id is malformed")
		}
		copy(id[:], b)
	}
	e.leaseID = &id
	return id, nil
}

// --- running ---

// passInterval is how often a pass runs without a signal.
const passInterval = 30 * time.Second

// Run runs the engine until ctx ends: the leader lock, the startup reconcile,
// then a pass every 30 s, on every block and on every signal.
func (e *Engine) Run(ctx context.Context) {
	release, err := e.lead(ctx)
	if err != nil {
		return
	}
	defer release()
	var blocks <-chan lnclient.BlockEpoch
	for ctx.Err() == nil {
		if e.S.payer() == nil {
			// No node, or no on-chain capability: nothing is offered and
			// nothing can be paid; wait for the node.
			select {
			case <-ctx.Done():
			case <-time.After(passInterval):
			}
			continue
		}
		if !e.ready.Load() {
			if err := e.Reconcile(ctx); err != nil {
				logger.Logger.Warn().Err(err).Msg("Payout engine startup reconcile failed; retrying")
			}
		}
		if e.ready.Load() {
			if blocks == nil {
				if p := e.S.payer(); p != nil {
					blocks, _ = p.SubscribeBlocks(ctx)
				}
			}
			if err := e.Pass(ctx); err != nil && !errors.Is(err, ErrEngineStopped) {
				logger.Logger.Error().Err(err).Msg("Payout engine pass failed; retried next pass")
			}
		}
		select {
		case <-ctx.Done():
		case <-e.wake:
		case <-blocks:
		case <-time.After(passInterval):
		}
	}
}

// engineLockKey is the advisory lock the leading engine holds ("cashwdx").
const engineLockKey = 0x63617368_776478

// lead takes the leader lock: on postgres a session advisory lock held on a
// reserved connection for the engine's life (a second instance waits and
// retries every 30 s); sqlite runs one process.
func (e *Engine) lead(ctx context.Context) (func(), error) {
	if e.S.DB.Name() != "postgres" {
		return func() {}, nil
	}
	sqlDB, err := e.S.DB.DB()
	if err != nil {
		return nil, err
	}
	for {
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			return nil, err
		}
		var got bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", int64(engineLockKey)).Scan(&got); err == nil && got {
			return func() {
				_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", int64(engineLockKey))
				_ = conn.Close()
			}, nil
		}
		_ = conn.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(passInterval):
		}
	}
}

// TryLead is lead without waiting: ok false when another engine leads.
func (e *Engine) TryLead(ctx context.Context) (release func(), ok bool, err error) {
	if e.S.DB.Name() != "postgres" {
		return func() {}, true, nil
	}
	sqlDB, err := e.S.DB.DB()
	if err != nil {
		return nil, false, err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", int64(engineLockKey)).Scan(&got); err != nil || !got {
		_ = conn.Close()
		return nil, false, err
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", int64(engineLockKey))
		_ = conn.Close()
	}, true, nil
}

// Pass runs steps 2 to 9 once.
func (e *Engine) Pass(ctx context.Context) error {
	e.passMu.Lock()
	defer e.passMu.Unlock()
	p := e.S.payer()
	if p == nil {
		return errors.New("no on-chain capability")
	}
	best, err := p.BestBlock(ctx)
	if err != nil {
		return err
	}
	if err := e.saveTip(best); err != nil {
		return err
	}
	if err := e.chainStep(ctx, p, best.Height); err != nil {
		return err
	}
	if err := e.coinStep(ctx, p, best.Height); err != nil {
		return err
	}
	if err := e.invalidateGone(best.Height); err != nil {
		return err
	}
	rates, err := e.S.Rates(ctx)
	if err != nil {
		return err
	}
	if err := e.cancelStep(ctx, p, best.Height, rates); err != nil {
		return err
	}
	if err := e.buildStep(ctx, p, best.Height, rates); err != nil {
		return err
	}
	if err := e.broadcastStep(ctx, p, best.Height); err != nil {
		return err
	}
	if err := e.bumpStep(ctx, p, best.Height, rates); err != nil {
		return err
	}
	if err := e.broadcastStep(ctx, p, best.Height); err != nil {
		return err
	}
	return e.noticeStep(ctx)
}

const (
	tipHeightKey = "CashWithdrawChainHeight"
	tipHashKey   = "CashWithdrawChainHash"
)

func (e *Engine) saveTip(best lnclient.BlockEpoch) error {
	return e.S.DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range map[string]string{tipHeightKey: fmt.Sprint(best.Height), tipHashKey: best.Hash} {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"})}).
				Create(&db.UserConfig{Key: k, Value: v}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// vout is one output of a version: its script (hex), value, and the payout it
// pays (nil for the change).
type vout struct {
	Script       string `json:"script"`
	Value        int64  `json:"value"`
	WithdrawalID *uint  `json:"withdrawal_id"`
}

func inputsOf(v *db.CashWithdrawVersion) []string {
	var in []string
	_ = json.Unmarshal([]byte(v.Inputs), &in)
	return in
}

func outputsOf(v *db.CashWithdrawVersion) []vout {
	var out []vout
	_ = json.Unmarshal([]byte(v.Outputs), &out)
	return out
}
