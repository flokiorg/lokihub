// Package mcp exposes lokihub's operations to an AI assistant over the Model
// Context Protocol (MCP). This file defines the capability-group / scope model
// that gates every tool.
//
// The permission model is deliberately simple (see
// cash-e2e/design/lokihub-mcp-2026-11-20/ARCHITECTURE.md): an MCP connection
// operates the hub like a human, bounded only by the capability groups the
// owner grants (Off / Read / Read+Write) and a fixed hard-exclusion list. There
// is no budget and no per-action approval, with one carve-out: an irreversible
// class (on-chain redeem to a foreign address, channel open/close) carries a
// residual guard.
//
// These mcp:* scopes are intentionally a separate vocabulary from the NIP-47
// wallet scopes in nip47/permissions — the admin surface they cover has no
// scope concept in the REST/NWC layer, so the gate here is the sole authority.
package mcp

// Group is one of the six capability groups an owner grants on a connection.
type Group string

const (
	// GroupWallet — "Wallet & payments": balances, pay, receive, on-chain.
	GroupWallet Group = "wallet"
	// GroupCashCircles — "Cash & circles": cash hubs/wallets, orders, circles.
	GroupCashCircles Group = "cash_circles"
	// GroupChannels — "Network & liquidity": peers, channels, LSP.
	GroupChannels Group = "channels"
	// GroupListings — "Listings & discoverability".
	GroupListings Group = "listings"
	// GroupNode — "Node info & hub settings".
	GroupNode Group = "node"
	// GroupConnections — "App connections" + identity authorities.
	GroupConnections Group = "connections"
)

// AllGroups is every capability group, in display order.
var AllGroups = []Group{
	GroupWallet,
	GroupCashCircles,
	GroupChannels,
	GroupListings,
	GroupNode,
	GroupConnections,
}

// Valid reports whether g is a known group.
func (g Group) Valid() bool {
	for _, x := range AllGroups {
		if x == g {
			return true
		}
	}
	return false
}

// Mode is the access level a tool needs within its group.
type Mode string

const (
	// ModeRead — no state change.
	ModeRead Mode = "read"
	// ModeWrite — mutates or moves.
	ModeWrite Mode = "write"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModeRead || m == ModeWrite }

// ScopePrefix namespaces every MCP scope, keeping them disjoint from the
// nip47/permissions wallet scopes.
const ScopePrefix = "mcp:"

// Scope is the scope string a connection must hold to use a tool in this group
// at this mode, e.g. GroupWallet.Scope(ModeRead) == "mcp:wallet:read". A
// Read+Write grant holds both the :read and :write scope for its group.
func (g Group) Scope(m Mode) string {
	return ScopePrefix + string(g) + ":" + string(m)
}

// AllScopes returns every mcp:* scope across all groups and modes.
func AllScopes() []string {
	out := make([]string, 0, len(AllGroups)*2)
	for _, g := range AllGroups {
		out = append(out, g.Scope(ModeRead), g.Scope(ModeWrite))
	}
	return out
}
