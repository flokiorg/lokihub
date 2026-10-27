package cashwithdraw

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/chainutil/psbt"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

var (
	// ErrSpendSetAside refuses an operator spend that would take coins set
	// aside for payouts.
	ErrSpendSetAside = errors.New("this spend would take on-chain coins set aside for withdrawals")
	errInvariant     = errors.New("version invariant failed")
	errNoCoins       = errors.New("not enough coins for this version")
)

// sequenceRBF is every input's nSequence: opt-in replaceable (version 3 is not
// relayed on this network, XQ-24).
const sequenceRBF = 0xfffffffd

// inputVB is the vsize of one wallet input (a key-hash witness spend).
const inputVB = 68

func estimateVsize(nIn int, outs []vout) int64 {
	vs := int64(12 + inputVB*nIn)
	for _, o := range outs {
		vs += 8 + 1 + int64(len(o.Script)/2)
	}
	return vs
}

// versionSpec is a version to build: its batch (0 for a new one), kind, the
// payouts it pays, the payouts it must not pay (an invalidation's), the inputs it
// must spend and the version it replaces (fee rules).
type versionSpec struct {
	batchID  uint
	kind     string
	payouts  []db.CashWithdrawal
	excluded map[uint]bool
	inputs   []string
	prev     *db.CashWithdrawVersion
	hubIDs   []uint
}

func parseOutpoint(s string) (*wire.OutPoint, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return nil, fmt.Errorf("outpoint %q", s)
	}
	h, err := chainhash.NewHashFromStr(s[:i])
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(s[i+1:], 10, 32)
	if err != nil {
		return nil, err
	}
	return wire.NewOutPoint(h, uint32(n)), nil
}

// coinValues are the values of outpoints from the coin table.
func (e *Engine) coinValues(tx *gorm.DB, ops []string) (map[string]int64, error) {
	var coins []db.CashWithdrawCoin
	if err := tx.Where("outpoint IN ?", ops).Find(&coins).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(coins))
	for _, c := range coins {
		out[c.Outpoint] = c.ValueSat
	}
	return out, nil
}

// batchVersions are a batch's versions in order.
func (e *Engine) batchVersions(tx *gorm.DB, batchID uint) ([]db.CashWithdrawVersion, error) {
	var vs []db.CashWithdrawVersion
	err := tx.Where("batch_id = ?", batchID).Order("seq").Find(&vs).Error
	return vs, err
}

// possiblySigned are the versions that may have been signed and can still
// confirm: every recorded version but an invalid one.
func possiblySigned(vs []db.CashWithdrawVersion) [][]string {
	var out [][]string
	for i := range vs {
		if vs[i].State != db.CashVersionInvalid && vs[i].Kind != db.CashVersionChild {
			out = append(out, inputsOf(&vs[i]))
		}
	}
	return out
}

// build makes a version's transaction: version 2, every input opt-in
// replaceable, locktime the tip; inputs as given plus coins when needed;
// outputs one per payout at exactly payout_millis ÷ 1000 to its script, and a
// Hub change output; fee rate the estimate (at least min relay + 1) and, for a
// replacement, the replaced rate plus the incremental rate with the absolute
// fee raised by incremental × vsize.
func (e *Engine) build(ctx context.Context, p lnclient.OnchainPayer, tip int64, rates Rates, spec versionSpec) (*wire.MsgTx, []vout, []string, int64, error) {
	inputs := append([]string(nil), spec.inputs...)
	sort.Strings(inputs)
	inputs = dedupe(inputs)
	// A coin someone else spent can never be an input again.
	var vanished []string
	if err := e.S.DB.Model(&db.CashWithdrawCoin{}).Where("state = ? AND outpoint IN ?", db.CashCoinVanished, append(inputs, "")).
		Pluck("outpoint", &vanished).Error; err != nil {
		return nil, nil, nil, 0, err
	}
	if len(vanished) > 0 {
		skip := map[string]bool{}
		for _, v := range vanished {
			skip[v] = true
		}
		kept := inputs[:0]
		for _, in := range inputs {
			if !skip[in] {
				kept = append(kept, in)
			}
		}
		inputs = kept
	}
	var outs []vout
	sort.Slice(spec.payouts, func(i, j int) bool { return spec.payouts[i].ID < spec.payouts[j].ID })
	for _, w := range spec.payouts {
		if spec.excluded[w.ID] {
			continue
		}
		id := w.ID
		outs = append(outs, vout{Script: w.OutputScript, Value: w.PayoutMillis / 1000, WithdrawalID: &id})
	}
	change, err := p.NextChangeScript(ctx)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	changeHex := hex.EncodeToString(change)
	dust := int64(DustMillis(rates, nipcash.ScriptType{Version: 0, ProgramLen: uint8(max(len(change)-2, 0))}) / 1000) //nolint:gosec // small
	rate := rates.SatPerVB
	var minFee int64
	if spec.prev != nil && spec.prev.FeeRateSatKvb != nil && spec.prev.FeeSat != nil {
		rate = max(rate, (*spec.prev.FeeRateSatKvb+999)/1000+1)
	}
	values, err := e.coinValues(e.S.DB, inputs)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	for {
		var in, pay int64
		for _, o := range inputs {
			v, ok := values[o]
			if !ok {
				return nil, nil, nil, 0, fmt.Errorf("input %s is not a known coin", o)
			}
			in += v
		}
		for _, o := range outs {
			pay += o.Value
		}
		withChange := append(append([]vout(nil), outs...), vout{Script: changeHex})
		vs := estimateVsize(len(inputs), withChange)
		fee := rate * vs
		if spec.prev != nil && spec.prev.FeeSat != nil {
			minFee = *spec.prev.FeeSat + vs
			fee = max(fee, minFee)
		}
		if ch := in - pay - fee; ch >= dust {
			withChange[len(withChange)-1].Value = ch
			outs = withChange
			break
		}
		// Change would be dust: one more coin.
		var extra db.CashWithdrawCoin
		if err := e.S.DB.Where("state = ? AND outpoint NOT IN ?", db.CashCoinAvailable, append(inputs, "")).
			Order("value_sat DESC").Limit(1).Find(&extra).Error; err != nil {
			return nil, nil, nil, 0, err
		}
		if extra.Outpoint == "" {
			return nil, nil, nil, 0, errNoCoins
		}
		inputs = append(inputs, extra.Outpoint)
		values[extra.Outpoint] = extra.ValueSat
	}
	tx := wire.NewMsgTx(2)
	tx.LockTime = uint32(max(tip, 0)) //nolint:gosec // a block height
	for _, o := range inputs {
		op, err := parseOutpoint(o)
		if err != nil {
			return nil, nil, nil, 0, err
		}
		in := wire.NewTxIn(op, nil, nil)
		in.Sequence = sequenceRBF
		tx.AddTxIn(in)
	}
	for _, o := range outs {
		script, _ := hex.DecodeString(o.Script)
		tx.AddTxOut(wire.NewTxOut(o.Value, script))
	}
	return tx, outs, inputs, dust, nil
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// assertVersionInvariants refuses a version that could let two payouts of one
// payout confirm, or pay what it should not (NIP-CASH §Paying a Payout): it
// must spend at least one input of every possibly signed version of its batch;
// keep, with the same script and value, every payout any signed version paid
// (unless it is being invalidated); pay no payout of another batch; stay
// within the output cap; and keep a change output above dust.
func (e *Engine) assertVersionInvariants(tx *gorm.DB, spec versionSpec, inputs []string, outs []vout, dust int64) error {
	fail := func(format string, a ...any) error {
		e.InvariantFailures.Add(1)
		return fmt.Errorf("%w: "+format, append([]any{errInvariant}, a...)...)
	}
	var vs []db.CashWithdrawVersion
	if spec.batchID != 0 {
		var err error
		if vs, err = e.batchVersions(tx, spec.batchID); err != nil {
			return err
		}
	}
	if !nipcash.SpendsFromEach(possiblySigned(vs), inputs) {
		return fail("the version does not spend from every earlier version of its batch")
	}
	paid := map[uint]vout{}
	nPay := 0
	var change *vout
	for i := range outs {
		if outs[i].WithdrawalID == nil {
			change = &outs[i]
			continue
		}
		nPay++
		paid[*outs[i].WithdrawalID] = outs[i]
	}
	for i := range vs {
		if vs[i].Kind == db.CashVersionChild {
			continue
		}
		for _, o := range outputsOf(&vs[i]) {
			if o.WithdrawalID == nil || spec.excluded[*o.WithdrawalID] {
				continue
			}
			got, ok := paid[*o.WithdrawalID]
			if !ok || got.Script != o.Script || got.Value != o.Value {
				return fail("payout %d of a signed version is dropped or changed", *o.WithdrawalID)
			}
		}
	}
	for id := range paid {
		if spec.excluded[id] {
			return fail("the version pays payout %d it invalidates", id)
		}
		var w db.CashWithdrawal
		if err := tx.Where("id = ?", id).Limit(1).Find(&w).Error; err != nil {
			return err
		}
		if w.BatchID != nil && (spec.batchID == 0 || *w.BatchID != spec.batchID) {
			return fail("payout %d belongs to another batch", id)
		}
		if strconv.FormatInt(w.PayoutMillis/1000, 10) != strconv.FormatInt(paid[id].Value, 10) || w.OutputScript != paid[id].Script {
			return fail("payout %d is not paid exactly", id)
		}
	}
	if nPay > e.env().MaxOutputs() {
		return fail("%d payout outputs exceed the cap", nPay)
	}
	if change == nil || change.Value < dust {
		return fail("no change output above dust")
	}
	return nil
}

// signVersion is step 5: the node locks and decorates the inputs; the
// about-to-sign record (inputs, outputs, PSBT; signed and batch_id on its
// payouts; coins in the version) is committed; then, and only then, the node
// signs; the raw transaction is checked against the record and stored before
// anything broadcasts it.
func (e *Engine) signVersion(ctx context.Context, p lnclient.OnchainPayer, spec versionSpec, mtx *wire.MsgTx, inputs []string, outs []vout, parent *uint) (*db.CashWithdrawVersion, error) {
	id, err := e.lease()
	if err != nil {
		return nil, err
	}
	packet, err := psbt.NewFromUnsignedTx(mtx)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := packet.Serialize(&buf); err != nil {
		return nil, err
	}
	funded, err := p.FundPsbtFixed(ctx, buf.Bytes(), id, e.env().LeaseSecs())
	if err != nil {
		return nil, err
	}
	if err := e.before(EngineAfterFund); err != nil {
		return nil, err
	}
	if e.held(func(h *EngineHooks) bool { return h.HoldSign.Load() }) {
		return nil, ErrEngineStopped
	}

	inJSON, _ := json.Marshal(inputs)
	outJSON, _ := json.Marshal(outs)
	v := &db.CashWithdrawVersion{Kind: spec.kind, State: db.CashVersionAboutToSign, Inputs: string(inJSON),
		Outputs: string(outJSON), Psbt: hex.EncodeToString(funded), ParentID: parent, CreatedAt: e.S.now().Unix()}
	err = e.S.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockTx(tx, spec.hubIDs...); err != nil {
			return err
		}
		if spec.batchID == 0 {
			b := db.CashWithdrawBatch{CreatedAt: e.S.now().Unix(), State: db.CashBatchOpen}
			if err := tx.Create(&b).Error; err != nil {
				return err
			}
			spec.batchID = b.ID
		}
		var seq int
		if err := tx.Model(&db.CashWithdrawVersion{}).Where("batch_id = ?", spec.batchID).
			Select("COALESCE(MAX(seq), 0)").Scan(&seq).Error; err != nil {
			return err
		}
		v.BatchID, v.Seq = spec.batchID, seq+1
		if err := tx.Create(v).Error; err != nil {
			return err
		}
		for i, o := range outs {
			if o.WithdrawalID == nil {
				continue
			}
			// signed is set once and never reset; batch_id is set once and
			// never changes.
			res := tx.Model(&db.CashWithdrawal{}).Where("id = ? AND (batch_id IS NULL OR batch_id = ?)", *o.WithdrawalID, spec.batchID).
				Updates(map[string]interface{}{"signed": true, "batch_id": spec.batchID})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return fmt.Errorf("%w: payout %d already belongs to another batch", errInvariant, *o.WithdrawalID)
			}
			if err := tx.Create(&db.CashWithdrawVersionOutput{VersionID: v.ID, WithdrawalID: *o.WithdrawalID, Vout: i}).Error; err != nil {
				return err
			}
		}
		// The payouts this version invalidates keep their about-to-sign mark.
		for wid := range spec.excluded {
			if err := tx.Model(&db.CashWithdrawal{}).Where("id = ?", wid).Update("signed", true).Error; err != nil {
				return err
			}
		}
		return tx.Model(&db.CashWithdrawCoin{}).Where("outpoint IN ?", inputs).
			Updates(map[string]interface{}{"state": db.CashCoinInVersion, "batch_id": spec.batchID}).Error
	})
	if err != nil {
		return nil, err
	}
	if err := e.before(EngineAfterAboutToSign); err != nil {
		return nil, err
	}
	if err := e.finalizeAndStore(ctx, p, v); err != nil {
		return nil, err
	}
	e.versionsTotal(spec.kind)
	return v, nil
}

func (e *Engine) versionsTotal(kind string) {
	logger.Logger.Info().Str("kind", kind).Msg("cash_withdraw_versions_total")
}

// finalizeAndStore is step 5 (c) and (d) for a recorded version: sign its stored
// PSBT, check the transaction is the recorded one, store it.
func (e *Engine) finalizeAndStore(ctx context.Context, p lnclient.OnchainPayer, v *db.CashWithdrawVersion) error {
	funded, err := hex.DecodeString(v.Psbt)
	if err != nil {
		return err
	}
	raw, err := p.FinalizePsbt(ctx, funded)
	if err != nil {
		return err
	}
	if err := e.before(EngineAfterFinalize); err != nil {
		return err
	}
	var mtx wire.MsgTx
	if err := mtx.Deserialize(bytes.NewReader(raw)); err != nil {
		return err
	}
	if !matchesRecord(&mtx, v) {
		e.InvariantFailures.Add(1)
		logger.Logger.Error().Uint("version_id", v.ID).Msg("A signed version differs from its record; kept as possibly signed, never broadcast")
		return e.S.DB.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).Update("state", db.CashVersionInvalid).Error
	}
	values, err := e.coinValues(e.S.DB, inputsOf(v))
	if err != nil {
		return err
	}
	var in, out int64
	for _, o := range inputsOf(v) {
		in += values[o]
	}
	for _, o := range mtx.TxOut {
		out += o.Value
	}
	fee := in - out
	vs := txVsize(&mtx)
	rate := fee * 1000 / max(vs, 1)
	txid := mtx.TxHash().String()
	rawHex := hex.EncodeToString(raw)
	now := e.S.now().Unix()
	vsize := int(vs)
	if err := e.S.DB.Model(&db.CashWithdrawVersion{}).Where("id = ?", v.ID).Updates(map[string]interface{}{
		"raw_tx": rawHex, "txid": txid, "vsize": vsize, "fee_sat": fee, "fee_rate_sat_kvb": rate,
		"state": db.CashVersionSigned, "signed_at": now,
	}).Error; err != nil {
		return err
	}
	v.RawTx, v.Txid, v.Vsize, v.FeeSat, v.FeeRateSatKvb, v.State = &rawHex, &txid, &vsize, &fee, &rate, db.CashVersionSigned
	return e.before(EngineAfterStore)
}

func txVsize(tx *wire.MsgTx) int64 {
	base := int64(tx.SerializeSizeStripped())
	total := int64(tx.SerializeSize())
	return (base*3 + total + 3) / 4
}

// matchesRecord: the signed transaction spends exactly the recorded inputs and
// pays exactly the recorded outputs.
func matchesRecord(tx *wire.MsgTx, v *db.CashWithdrawVersion) bool {
	ins, outs := inputsOf(v), outputsOf(v)
	if len(tx.TxIn) != len(ins) || len(tx.TxOut) != len(outs) {
		return false
	}
	for i, in := range tx.TxIn {
		if in.PreviousOutPoint.String() != ins[i] {
			return false
		}
	}
	for i, o := range tx.TxOut {
		if hex.EncodeToString(o.PkScript) != outs[i].Script || o.Value != outs[i].Value {
			return false
		}
	}
	return true
}

// buildStep is step 4 with 5: queued payouts in no version are batched once
// the batch window closes (oldest + window, or earliest sent_by − 1800): into
// the newest batch whose last version is unconfirmed, never confirmed, and has
// room (by replacement), else a new batch on their own set-aside coins.
func (e *Engine) buildStep(ctx context.Context, p lnclient.OnchainPayer, tip int64, rates Rates) error {
	var ws []db.CashWithdrawal
	if err := e.S.DB.Where("state = ? AND batch_id IS NULL AND cancel_reason IS NULL", db.CashWithdrawQueued).
		Order("id").Find(&ws).Error; err != nil {
		return err
	}
	if len(ws) == 0 {
		return nil
	}
	now := e.S.now().Unix()
	oldest, earliest := ws[0].CommittedAt, ws[0].SentBy
	for _, w := range ws {
		oldest, earliest = min(oldest, w.CommittedAt), min(earliest, w.SentBy)
	}
	if now < min(oldest+e.env().BatchWindowSecs(), earliest-1800) && len(ws) < e.env().MaxOutputs() {
		return nil
	}
	if len(ws) > e.env().MaxOutputs() {
		ws = ws[:e.env().MaxOutputs()]
	}
	spec := versionSpec{kind: db.CashVersionPayout}
	if target, last, err := e.joinableBatch(len(ws)); err != nil {
		return err
	} else if target != 0 {
		spec.batchID, spec.prev = target, last
		vs, err := e.batchVersions(e.S.DB, target)
		if err != nil {
			return err
		}
		for _, set := range possiblySigned(vs) {
			spec.inputs = append(spec.inputs, set...)
		}
		var existing []db.CashWithdrawal
		if err := e.S.DB.Where("batch_id = ?", target).Find(&existing).Error; err != nil {
			return err
		}
		spec.payouts = append(spec.payouts, existing...)
	}
	spec.payouts = append(spec.payouts, ws...)
	for _, w := range spec.payouts {
		spec.hubIDs = append(spec.hubIDs, w.HubAppID)
	}
	for _, w := range ws {
		var coins []string
		if err := e.S.DB.Model(&db.CashWithdrawCoin{}).Where("state = ? AND withdrawal_id = ?", db.CashCoinSetAside, w.ID).
			Pluck("outpoint", &coins).Error; err != nil {
			return err
		}
		spec.inputs = append(spec.inputs, coins...)
	}
	return e.buildAndSign(ctx, p, tip, rates, spec)
}

func (e *Engine) buildAndSign(ctx context.Context, p lnclient.OnchainPayer, tip int64, rates Rates, spec versionSpec) error {
	mtx, outs, inputs, dust, err := e.build(ctx, p, tip, rates, spec)
	if err != nil {
		if errors.Is(err, errNoCoins) {
			logger.Logger.Warn().Msg("Not enough coins to build a payout version; retried next pass")
			return nil
		}
		return err
	}
	if err := e.assertVersionInvariants(e.S.DB, spec, inputs, outs, dust); err != nil {
		logger.Logger.Error().Err(err).Uint("batch_id", spec.batchID).Msg("A payout version failed its invariants and was not signed")
		return nil
	}
	_, err = e.signVersion(ctx, p, spec, mtx, inputs, outs, nil)
	return err
}

// joinableBatch is the newest batch new payouts can join by replacement: its
// last version unconfirmed, never confirmed, a payout version, with room.
func (e *Engine) joinableBatch(n int) (uint, *db.CashWithdrawVersion, error) {
	var b db.CashWithdrawBatch
	if err := e.S.DB.Where("state = ?", db.CashBatchOpen).Order("id DESC").Limit(1).Find(&b).Error; err != nil || b.ID == 0 {
		return 0, nil, err
	}
	vs, err := e.batchVersions(e.S.DB, b.ID)
	if err != nil || len(vs) == 0 {
		return 0, nil, err
	}
	for _, v := range vs {
		if v.ConfirmedOnce || v.State == db.CashVersionConfirmed || v.State == db.CashVersionFinal || v.Kind == db.CashVersionInvalidation {
			return 0, nil, nil
		}
	}
	last := vs[len(vs)-1]
	if last.State != db.CashVersionSigned && last.State != db.CashVersionBroadcast {
		return 0, nil, nil
	}
	var count int64
	if err := e.S.DB.Model(&db.CashWithdrawal{}).Where("batch_id = ?", b.ID).Count(&count).Error; err != nil {
		return 0, nil, err
	}
	var cancelling int64
	if err := e.S.DB.Model(&db.CashWithdrawal{}).Where("batch_id = ? AND cancel_reason IS NOT NULL", b.ID).Count(&cancelling).Error; err != nil {
		return 0, nil, err
	}
	if cancelling > 0 || int(count)+n > e.env().MaxOutputs() {
		return 0, nil, nil
	}
	return b.ID, &last, nil
}
