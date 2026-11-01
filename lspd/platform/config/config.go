package config

type NodeConfig struct {
	// Name of the LSP. If empty, the node's alias will be taken instead. The
	// name is returned in the ChannelInformation api endpoint as a friendly
	// name.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// The public key of the lightning node. If empty, the node's pubkey will be
	// taken instead. The pubkey is returned in the ChannelInformation api
	// endpoint, s
	NodePubkey string `json:"nodePubkey,omitempty" yaml:"nodePubkey,omitempty"`

	// Hex encoded private key of the LSP. This is used to decrypt traffic from
	// clients. Note this is not the private key of the node. Provide it
	// inline, or as {from_env: VAR} / {from_file: /path} — see SecretRef.
	FlspdPrivateKey SecretRef `json:"flspdPrivateKey" yaml:"flspdPrivateKey"`

	// Tokens used to authenticate to flspd. These tokens must be unique for each
	// configured node, so it's obvious which node an rpc call is meant for.
	Tokens []string `json:"tokens" yaml:"tokens"`

	// The network location of the lightning node, e.g. `12.34.56.78:9012` or
	// `localhost:10011`. The host is returned in the ChannelInformation api
	// endpoint so clients can connect to the node.
	Host string `json:"host" yaml:"host"`

	// Public channel amount is a reserved amount for public channels. If a
	// zero conf channel is opened, it will never have this exact amount.
	PublicChannelAmount int64 `json:"publicChannelAmount,string,omitempty" yaml:"publicChannelAmount,omitempty"`

	// Number of blocks after which an opened channel is considered confirmed.
	TargetConf uint32 `json:"targetConf,string" yaml:"targetConf"`

	// ReferenceFeeratePerVByte (loki/vbyte) is the on-chain feerate this
	// node's opening-fee floor (ChannelMinimumFeeMloki / the fee menu's
	// MinFeeMloki) is calibrated to safely absorb (Phase 7, F-18). Used
	// only to decide whether to honor an EXPIRED LSPS2 opening_fee_params
	// quote: the current live feerate must be at or below this reference
	// for the LSP to still treat honoring the stale quote as safe. Not a
	// hard cap on channel-open fees in general -- openChannel's own
	// TargetConf-based estimate is what actually prices the transaction.
	ReferenceFeeratePerVByte float64 `json:"referenceFeeratePerVByte,string,omitempty" yaml:"referenceFeeratePerVByte,omitempty"`

	// Minimum number of confirmations inputs for zero conf channel opens should
	// have.
	MinConfs *uint32 `json:"minConfs,string" yaml:"minConfs"`

	// Smallest htlc amount routed over channels a channel. It is configured on
	// the node itself, but this value is returned in the ChannelInformation rpc.
	MinHtlcMloki uint64 `json:"minHtlcMloki,string" yaml:"minHtlcMloki"`

	// The base fee for routing payments over the channel. It is configured on
	// the node itself, but this value is returned in the ChannelInformation rpc.
	BaseFeeMloki uint64 `json:"baseFeeMloki,string" yaml:"baseFeeMloki"`

	// The fee rate for routing payments over the channel. It is configured on
	// the node itself, but this value is returned in the ChannelInformation rpc.
	FeeRate float64 `json:"feeRate,string" yaml:"feeRate"`

	// Minimum timelock delta required for opening a zero conf channel.
	TimeLockDelta uint32 `json:"timeLockDelta,string" yaml:"timeLockDelta"`

	// Fee for opening a zero conf channel in loki per 10000 loki based
	// on the incoming payment amount.
	// This field is for legacy support for clients that don't support
	// OpeningFeeParams.
	ChannelFeePermyriad int64 `json:"channelFeePermyriad,string" yaml:"channelFeePermyriad"`

	// Minimum fee for opening a zero conf channel in milliloki. If the fee
	// using ChannelFeePermyriad is less than this amount, this amount is the
	// actual fee to be paid.
	// This field is for legacy support for clients that don't support
	// OpeningFeeParams.
	ChannelMinimumFeeMloki int64 `json:"channelMinimumFeeMloki,string" yaml:"channelMinimumFeeMloki"`

	// Channel capacity that is added on top of the incoming payment amount
	// when a new zero conf channel is opened. In loki.
	AdditionalChannelCapacity int64 `json:"additionalChannelCapacity,string" yaml:"additionalChannelCapacity"`

	// Minimum channel capacity in loki. Must be >= 20000 (LND minimum).
	MinChannelCapacityLoki uint64 `json:"minChannelCapacityLoki,string,omitempty" yaml:"minChannelCapacityLoki,omitempty"`

	// The channel can be closed if not used this duration in seconds. This is
	// not enforced by flspd, but gives an indication to clients.
	// This field is for legacy support for clients that don't support
	// OpeningFeeParams.
	MaxInactiveDuration uint64 `json:"maxInactiveDuration,string" yaml:"maxInactiveDuration"`

	// The maximum time to hold a htlc after sending a notification when the
	// peer is offline. Defaults to 1m.
	NotificationTimeout string `json:"notificationTimeout,string" yaml:"notificationTimeout"`

	// Minimum payment size in mloki for JIT channels.
	MinPaymentSizeMloki uint64 `json:"minPaymentSizeMloki,string,omitempty" yaml:"minPaymentSizeMloki,omitempty"`

	// Maximum payment size in mloki for JIT channels.
	JitMaxPaymentSizeMloki uint64 `json:"jitMaxPaymentSizeMloki,string,omitempty" yaml:"jitMaxPaymentSizeMloki,omitempty"`

	// If true, enables JIT channels to override MinConfs to 0 (implicit zero conf).
	JitEnabled bool `json:"jitEnabled,omitempty" yaml:"jitEnabled,omitempty"`

	// JIT Channels Configuration
	// Maximum ClientToSelfDelay for JIT channel opening fee params.
	JitMaxClientToSelfDelay uint32 `json:"jitMaxClientToSelfDelay,string,omitempty" yaml:"jitMaxClientToSelfDelay,omitempty"`

	// JIT Valid Until duration in seconds.
	JitValidUntilSeconds uint32 `json:"jitValidUntilSeconds,string,omitempty" yaml:"jitValidUntilSeconds,omitempty"`

	// Minimum channel balance in Loki for JIT channels (LSPS2).
	JitMinChannelBalanceLoki *uint64 `json:"jitMinChannelBalanceLoki,string,omitempty" yaml:"jitMinChannelBalanceLoki,omitempty"`

	// LSPS1 Configuration
	// Minimum required channel confirmations for LSPS1 orders.
	Lsps1MinRequiredChannelConfirmations *uint16 `json:"lsps1MinRequiredChannelConfirmations,string,omitempty" yaml:"lsps1MinRequiredChannelConfirmations,omitempty"`

	// Minimum funding confirms within blocks for LSPS1 orders.
	Lsps1MinFundingConfirmsWithinBlocks *uint16 `json:"lsps1MinFundingConfirmsWithinBlocks,string,omitempty" yaml:"lsps1MinFundingConfirmsWithinBlocks,omitempty"`

	// Supports zero channel reserve.
	Lsps1SupportsZeroChannelReserve *bool `json:"lsps1SupportsZeroChannelReserve,omitempty" yaml:"lsps1SupportsZeroChannelReserve,omitempty"`

	// Maximum channel expiry blocks for LSPS1 orders.
	Lsps1MaxChannelExpiryBlocks *uint32 `json:"lsps1MaxChannelExpiryBlocks,string,omitempty" yaml:"lsps1MaxChannelExpiryBlocks,omitempty"`

	// Duration in seconds before an LSPS1 order expires if not funded.
	Lsps1OrderExpirySeconds *uint32 `json:"lsps1OrderExpirySeconds,string,omitempty" yaml:"lsps1OrderExpirySeconds,omitempty"`

	// Minimum initial client balance in Loki for LSPS1 orders.
	Lsps1MinInitialClientBalanceLoki *uint64 `json:"lsps1MinInitialClientBalanceLoki,string,omitempty" yaml:"lsps1MinInitialClientBalanceLoki,omitempty"`

	// Maximum initial client balance in Loki for LSPS1 orders.
	Lsps1MaxInitialClientBalanceLoki *uint64 `json:"lsps1MaxInitialClientBalanceLoki,string,omitempty" yaml:"lsps1MaxInitialClientBalanceLoki,omitempty"`

	// Minimum initial LSP balance in Loki for LSPS1 orders.
	Lsps1MinInitialLspBalanceLoki *uint64 `json:"lsps1MinInitialLspBalanceLoki,string,omitempty" yaml:"lsps1MinInitialLspBalanceLoki,omitempty"`

	// Maximum initial LSP balance in Loki for LSPS1 orders.
	Lsps1MaxInitialLspBalanceLoki *uint64 `json:"lsps1MaxInitialLspBalanceLoki,string,omitempty" yaml:"lsps1MaxInitialLspBalanceLoki,omitempty"`

	// Minimum channel balance in Loki for LSPS1 orders.
	Lsps1MinChannelBalanceLoki *uint64 `json:"lsps1MinChannelBalanceLoki,string,omitempty" yaml:"lsps1MinChannelBalanceLoki,omitempty"`

	// Maximum channel balance in Loki for LSPS1 orders.
	Lsps1MaxChannelBalanceLoki *uint64 `json:"lsps1MaxChannelBalanceLoki,string,omitempty" yaml:"lsps1MaxChannelBalanceLoki,omitempty"`

	// --- Tiered config model (T5-2..T5-5) ---
	// These are newer, unit-consistent alternatives to several of the
	// fields above. When present, each overwrites the flat field(s) it
	// corresponds to — see tiered.go's applyTieredConfig for the exact
	// mapping and precedence rules. Optional; every field above remains
	// fully supported.

	// Risk-tolerance preset (conservative|balanced|aggressive) expanding
	// to confirmation/CLTV-delta/expiry defaults. See tiered.go.
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`

	// Unified channel-opening fee, replacing channelFeePermyriad +
	// channelMinimumFeeMloki.
	OpeningFee *OpeningFeeConfig `json:"opening_fee,omitempty" yaml:"opening_fee,omitempty"`

	// Advertised routing fee, replacing feeRate + baseFeeMloki.
	RoutingFee *RoutingFeeConfig `json:"routing_fee,omitempty" yaml:"routing_fee,omitempty"`

	// Unified channel-size bounds, replacing minChannelCapacityLoki,
	// additionalChannelCapacity, and the LSPS1/JIT min/max balance fields.
	ChannelSize *ChannelSizeConfig `json:"channel_size,omitempty" yaml:"channel_size,omitempty"`

	// NIP-LSP announcement settings (T8-2). Nil or enabled: false means this
	// node publishes nothing. See announce.go.
	Announce *AnnounceConfig `json:"announce,omitempty" yaml:"announce,omitempty"`

	// Set this field to connect to an LND node.
	Lnd *LndConfig `json:"flnd,omitempty" yaml:"flnd,omitempty"`
}

type LndConfig struct {
	// Address to the grpc api.
	Address string `json:"address" yaml:"address"`

	// tls cert for the grpc api. Provide it inline (hex-encoded PEM), or as
	// {from_env: VAR} / {from_file: /path} — see SecretRef. A from_file ref
	// can point directly at flnd's own tls.cert.
	Cert SecretRef `json:"cert" yaml:"cert"`

	// macaroon to use. Provide it inline (hex-encoded), or as
	// {from_env: VAR} / {from_file: /path} — see SecretRef. A from_file ref
	// can point directly at flnd's own *.macaroon file.
	Macaroon SecretRef `json:"macaroon" yaml:"macaroon"`

	// DisableTls disables TLS verification for the LND connection.
	// This is useful for testing or internal networks (e.g. docker).
	DisableTls bool `json:"disableTls,omitempty" yaml:"disableTls,omitempty"`
}
