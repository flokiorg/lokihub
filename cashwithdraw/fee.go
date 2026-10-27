package cashwithdraw

import (
	"context"
	"errors"
	"time"

	"github.com/flokiorg/go-flokicoin/chainutil"
	"github.com/flokiorg/go-flokicoin/mempool"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/lnclient"
)

// feeCacheTTL is how long a node fee estimate is reused.
const feeCacheTTL = 60 * time.Second

// shareVB is one payout's share of a one-payout transaction beyond its own
// output: one input, the change output and the overhead (batching savings are
// the Hub's).
const shareVB = 110

type feeCacheEntry struct {
	at   time.Time
	conf int
	est  lnclient.FeeEstimate
}

// Rates are the fee rates a quote and a version use, in base units per vbyte:
// the estimate at the target, floored at the minimum relay rate plus one, and
// the minimum relay rate itself (for dust and replacements).
type Rates struct {
	SatPerVB         int64
	MinRelaySatPerVB int64
	// MinRelaySatPerKvB is the minimum relay fee as the mempool's policy reads it.
	MinRelaySatPerKvB int64
}

// RatesFrom converts a node estimate (sat per 1000 weight units).
func RatesFrom(est lnclient.FeeEstimate) Rates {
	ceilDiv := func(a, b int64) int64 { return (a + b - 1) / b }
	minRelay := max(ceilDiv(est.MinRelaySatPerKw*4, 1000), 1)
	rate := max(ceilDiv(est.SatPerKw*4, 1000), minRelay+1)
	return Rates{SatPerVB: rate, MinRelaySatPerVB: minRelay, MinRelaySatPerKvB: max(est.MinRelaySatPerKw*4, 1000)}
}

// Rates are the node's current rates at CASH_WITHDRAW_FEE_TARGET_CONF, cached
// for a minute.
func (s *Service) Rates(ctx context.Context) (Rates, error) {
	p := s.payer()
	if p == nil {
		return Rates{}, errors.New("this node cannot estimate on-chain fees")
	}
	conf := s.env().FeeTargetConf()
	s.feeMu.Lock()
	c := s.feeCache
	s.feeMu.Unlock()
	if !c.at.IsZero() && c.conf == conf && s.now().Sub(c.at) < feeCacheTTL {
		return RatesFrom(c.est), nil
	}
	est, err := p.EstimateFee(ctx, conf)
	if err != nil {
		return Rates{}, err
	}
	s.feeMu.Lock()
	s.feeCache = feeCacheEntry{at: s.now(), conf: conf, est: est}
	s.feeMu.Unlock()
	return RatesFrom(est), nil
}

// OutputVB is the vsize of one output paying st: value, script length, the
// version opcode and push, and the program.
func OutputVB(st nipcash.ScriptType) int64 { return 8 + 1 + 2 + int64(st.ProgramLen) }

// NetworkFee is a payout's network fee for st at r, in millis: 1000 × rate ×
// (its output + its share of one input, the change and the overhead). It is the
// Hub's live estimate, never a setting.
func NetworkFee(r Rates, st nipcash.ScriptType) uint64 {
	return uint64(1000 * r.SatPerVB * (OutputVB(st) + shareVB)) //nolint:gosec // small positive
}

// DustMillis is the smallest payout for st that the network's nodes relay at r
// (go-flokicoin's mempool.IsDust), in millis.
func DustMillis(r Rates, st nipcash.ScriptType) uint64 {
	script := nipcash.WitnessScript(st.Version, make([]byte, st.ProgramLen))
	minRelay := chainutil.Amount(r.MinRelaySatPerKvB)
	lo, hi := int64(1), int64(1)
	for mempool.IsDust(wire.NewTxOut(hi, script), minRelay) {
		lo, hi = hi, hi*2
	}
	for lo < hi {
		mid := lo + (hi-lo)/2
		if mempool.IsDust(wire.NewTxOut(mid, script), minRelay) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return uint64(lo) * 1000 //nolint:gosec // positive
}
