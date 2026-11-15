package config

import (
	"fmt"
)

// AbsoluteMinChannelCapacityLoki is the hard floor for channel size enforced by LSP.
const AbsoluteMinChannelCapacityLoki = 20000

// Validate performs a full validation of the NodeConfig.
func (cfg *NodeConfig) Validate() error {
	// 0. Expand any tiered-model blocks (opening_fee, routing_fee,
	// channel_size, profile) into the flat fields the checks below and
	// every downstream consumer already read. Must run first so those
	// checks see fully-resolved values regardless of which style (flat or
	// tiered) the operator used.
	if err := cfg.applyTieredConfig(); err != nil {
		return err
	}

	// 1. Basic Capacity Checks
	if cfg.MinChannelCapacityLoki > 0 && cfg.MinChannelCapacityLoki < AbsoluteMinChannelCapacityLoki {
		return fmt.Errorf("minChannelCapacityLoki must be >= %d (LSP Limit), got %d", AbsoluteMinChannelCapacityLoki, cfg.MinChannelCapacityLoki)
	}
	if cfg.MinChannelCapacityLoki == 0 {
		cfg.MinChannelCapacityLoki = AbsoluteMinChannelCapacityLoki
	}

	if cfg.AdditionalChannelCapacity < 0 {
		return fmt.Errorf("additionalChannelCapacity (%d) cannot be negative", cfg.AdditionalChannelCapacity)
	}

	// 2. Financial Sanity Checks (Prevent extreme fee settings)
	if cfg.FeeRate > 0.1 { // Max 10% fee rate
		return fmt.Errorf("feeRate (%f) is too high (max 0.1)", cfg.FeeRate)
	}
	if cfg.BaseFeeMloki > 100000 { // Max 100 Loki base fee
		return fmt.Errorf("baseFeeMloki (%d) is too high (max 100000)", cfg.BaseFeeMloki)
	}

	// Phase 7, F-18: a conservative default so IsCurrentChainFeeCheaper has
	// something real to compare against even if an operator never sets
	// this explicitly. Deliberately not derived from ChannelMinimumFeeMloki
	// automatically -- that's a business fee floor in mloki-per-payment,
	// not an on-chain feerate, and conflating the two units would be
	// exactly the kind of silent, wrong "looks calibrated but isn't"
	// mistake this fix exists to remove.
	if cfg.ReferenceFeeratePerVByte <= 0 {
		cfg.ReferenceFeeratePerVByte = 20.0
	}

	// 3. LSPS1 Validation
	if err := cfg.ValidateLsps1(); err != nil {
		return fmt.Errorf("LSPS1 config validation failed: %w", err)
	}

	// 3. JIT Validation
	if err := cfg.ValidateJit(); err != nil {
		return fmt.Errorf("JIT config validation failed: %w", err)
	}

	// 4. NIP-LSP announce block (T8-2).
	if err := cfg.Announce.Validate(); err != nil {
		return err
	}

	return nil
}

func (cfg *NodeConfig) ValidateLsps1() error {
	// Helper to ensure field is set or use default
	setUint16 := func(ptr **uint16, def uint16) uint16 {
		if *ptr == nil {
			*ptr = &def
		}
		return **ptr
	}
	setUint32 := func(ptr **uint32, def uint32) uint32 {
		if *ptr == nil {
			*ptr = &def
		}
		return **ptr
	}
	setUint64 := func(ptr **uint64, def uint64) uint64 {
		if *ptr == nil {
			*ptr = &def
		}
		return **ptr
	}
	setBool := func(ptr **bool, def bool) bool {
		if *ptr == nil {
			*ptr = &def
		}
		return **ptr
	}

	// 1. Set Defaults for Minimal Config
	setUint16(&cfg.Lsps1MinRequiredChannelConfirmations, 0)
	setUint16(&cfg.Lsps1MinFundingConfirmsWithinBlocks, 1)
	setBool(&cfg.Lsps1SupportsZeroChannelReserve, true)
	setUint32(&cfg.Lsps1MaxChannelExpiryBlocks, 201600) // ~140 days
	setUint32(&cfg.Lsps1OrderExpirySeconds, 86400)      // 24h

	minClient := setUint64(&cfg.Lsps1MinInitialClientBalanceLoki, 0)
	maxClient := setUint64(&cfg.Lsps1MaxInitialClientBalanceLoki, 100_000_000)
	minLsp := setUint64(&cfg.Lsps1MinInitialLspBalanceLoki, 0)
	maxLsp := setUint64(&cfg.Lsps1MaxInitialLspBalanceLoki, 100_000_000)
	minChan := setUint64(&cfg.Lsps1MinChannelBalanceLoki, cfg.MinChannelCapacityLoki)
	maxChan := setUint64(&cfg.Lsps1MaxChannelBalanceLoki, 100_000_000)

	// 2. Check Logical Constraints (Min <= Max)
	if minClient > maxClient {
		return fmt.Errorf("lsps1MinInitialClientBalanceLoki (%d) > lsps1MaxInitialClientBalanceLoki (%d)", minClient, maxClient)
	}
	if minLsp > maxLsp {
		return fmt.Errorf("lsps1MinInitialLspBalanceLoki (%d) > lsps1MaxInitialLspBalanceLoki (%d)", minLsp, maxLsp)
	}
	if minChan > maxChan {
		return fmt.Errorf("lsps1MinChannelBalanceLoki (%d) > lsps1MaxChannelBalanceLoki (%d)", minChan, maxChan)
	}

	// 3. Network Compatibility Check
	padding := uint64(cfg.AdditionalChannelCapacity)
	if minChan+padding < cfg.MinChannelCapacityLoki {
		return fmt.Errorf("LSPS1 incompatibility: minChannelBalanceLoki (%d) + padding (%d) < network floor (%d)", minChan, padding, cfg.MinChannelCapacityLoki)
	}

	// 4. Business Floor vs Physical Floor
	if minChan < cfg.MinChannelCapacityLoki {
		return fmt.Errorf("LSPS1 config error: lsps1MinChannelBalanceLoki (%d) cannot be lower than network floor (%d)", minChan, cfg.MinChannelCapacityLoki)
	}

	return nil
}

func (cfg *NodeConfig) ValidateJit() error {
	if !cfg.JitEnabled {
		return nil
	}

	// 1. Check Max Client Delay
	if cfg.JitMaxClientToSelfDelay == 0 {
		cfg.JitMaxClientToSelfDelay = 10080 // DefaultMaxClientToSelfDelay
	}

	// 2. Check Min Payment Size vs Floors
	padding := uint64(cfg.AdditionalChannelCapacity)
	minPaymentLoki := cfg.MinPaymentSizeMloki / 1000

	// Check against Physical Network Floor
	if minPaymentLoki+padding < cfg.MinChannelCapacityLoki {
		return fmt.Errorf("JIT incompatibility: minPaymentSizeMloki (%d loki) + padding (%d) < network floor (%d)", minPaymentLoki, padding, cfg.MinChannelCapacityLoki)
	}

	// Check against Business Floor
	if cfg.JitMinChannelBalanceLoki != nil {
		if minPaymentLoki+padding < *cfg.JitMinChannelBalanceLoki {
			return fmt.Errorf("JIT business violation: minPaymentSizeMloki (%d loki) + padding (%d) < jitMinChannelBalanceLoki (%d)", minPaymentLoki, padding, *cfg.JitMinChannelBalanceLoki)
		}

		// Check that Business Floor is at least Physical Network Floor
		if *cfg.JitMinChannelBalanceLoki < cfg.MinChannelCapacityLoki {
			return fmt.Errorf("JIT configuration error: jitMinChannelBalanceLoki (%d) cannot be lower than network floor (%d)", *cfg.JitMinChannelBalanceLoki, cfg.MinChannelCapacityLoki)
		}
	}

	// 3. Check Min <= Max
	if cfg.JitMaxPaymentSizeMloki > 0 && cfg.MinPaymentSizeMloki > cfg.JitMaxPaymentSizeMloki {
		return fmt.Errorf("JIT logical error: minPaymentSizeMloki (%d) > jitMaxPaymentSizeMloki (%d)", cfg.MinPaymentSizeMloki, cfg.JitMaxPaymentSizeMloki)
	}

	return nil
}
