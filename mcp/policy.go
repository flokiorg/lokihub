package mcp

// This file is the authoritative classification of every api.API method into
// exactly one of: a tool (with its capability group + mode), hard-excluded
// (crown jewels — never a tool), or not-applicable (internal plumbing). The
// ratchet test (policy_test.go) fails the build if any api.API method is left
// unclassified, so adding a core method forces a conscious decision here. This
// is the single source the tool registry and the scope gate both derive from.
//
// Tool *names* (snake_case, per CONTRACTS.md) and handlers are wired in later
// milestones; M0 only fixes the group/mode/exclusion policy and proves it is
// complete and sound.

// ClassKind is how an api.API method is treated by the MCP surface.
type ClassKind int

const (
	// ClassTool — exposed as an MCP tool, gated by Group+Mode.
	ClassTool ClassKind = iota
	// ClassExcluded — a crown jewel (secret, backup, lifecycle, raw node
	// control). Never registered as a tool; unreachable regardless of scope.
	ClassExcluded
	// ClassNotApplicable — internal plumbing / telemetry / webhook callbacks
	// that are not operator operations and are intentionally not exposed.
	ClassNotApplicable
)

// Classification is the policy decision for one api.API method.
type Classification struct {
	Kind    ClassKind
	Group   Group  // set when Kind == ClassTool
	Mode    Mode   // set when Kind == ClassTool
	Guarded bool   // set when Kind == ClassTool: irreversible, needs the residual guard
	Reason  string // set when Kind == ClassExcluded or ClassNotApplicable
}

func tool(g Group, m Mode) Classification {
	return Classification{Kind: ClassTool, Group: g, Mode: m}
}

// guarded marks an irreversible write (on-chain redeem to a foreign address,
// channel open/close) that requires the residual guard (owner confirmation or a
// GUI-preset allow-list) before it executes. See ARCHITECTURE.md §7.
func guarded(g Group, m Mode) Classification {
	return Classification{Kind: ClassTool, Group: g, Mode: m, Guarded: true}
}

func excluded(reason string) Classification {
	return Classification{Kind: ClassExcluded, Reason: reason}
}

func na(reason string) Classification {
	return Classification{Kind: ClassNotApplicable, Reason: reason}
}

// methodClass classifies every method of the api.API interface (direct methods
// plus the embedded ListingsAPI, LSPCatalogueAPI, WithdrawAPI and LSPOperatorAPI). The ratchet test asserts
// this map's keys exactly cover api.API's method set.
var methodClass = map[string]Classification{
	// ---- Wallet & payments ----
	"GetBalances":             tool(GroupWallet, ModeRead),
	"GetUnusedOnchainAddress": tool(GroupWallet, ModeRead),
	"GetWalletCapabilities":   tool(GroupWallet, ModeRead),
	"ListTransactions":        tool(GroupWallet, ModeRead),
	"ListOnchainTransactions": tool(GroupWallet, ModeRead),
	"LookupInvoice":           tool(GroupWallet, ModeRead),
	"EstimateInvoiceFee":      tool(GroupWallet, ModeRead),
	"CreateInvoice":           tool(GroupWallet, ModeWrite),
	"SendPayment":             tool(GroupWallet, ModeWrite),
	"Transfer":                tool(GroupWallet, ModeWrite),
	"GetNewOnchainAddress":    tool(GroupWallet, ModeWrite),
	"SignMessage":             tool(GroupWallet, ModeWrite),
	"SyncWallet":              tool(GroupWallet, ModeWrite),
	"RedeemOnchainFunds":      guarded(GroupWallet, ModeWrite), // foreign-address sweep, irreversible

	// ---- Cash & circles ----
	"ListCashWalletClaims":           tool(GroupCashCircles, ModeRead),
	"GetCashHubStats":                tool(GroupCashCircles, ModeRead),
	"GetAllCashHubStats":             tool(GroupCashCircles, ModeRead),
	"GetCircleHubStats":              tool(GroupCashCircles, ModeRead),
	"GetCashWalletRecipients":        tool(GroupCashCircles, ModeRead),
	"ListOrderConnections":           tool(GroupCashCircles, ModeRead),
	"GetOrderTerms":                  tool(GroupCashCircles, ModeRead),
	"ListOrders":                     tool(GroupCashCircles, ModeRead),
	"ListCircleAllowlist":            tool(GroupCashCircles, ModeRead),
	"ListCircleChildrenBalances":     tool(GroupCashCircles, ModeRead),
	"ListCircleIdentities":           tool(GroupCashCircles, ModeRead),
	"GetCircleIdentity":              tool(GroupCashCircles, ModeRead),
	"GetCircleIdentityFollowSet":     tool(GroupCashCircles, ModeRead),
	"CreateCashWallet":               tool(GroupCashCircles, ModeWrite),
	"DeleteCashWallet":               tool(GroupCashCircles, ModeWrite),
	"DeleteCashClaim":                tool(GroupCashCircles, ModeWrite),
	"CreateOrderConnection":          tool(GroupCashCircles, ModeWrite),
	"RetireOrderConnection":          tool(GroupCashCircles, ModeWrite),
	"PutOrderTerms":                  tool(GroupCashCircles, ModeWrite),
	"ReplaceCircleAllowlist":         tool(GroupCashCircles, ModeWrite),
	"RemoveCircleAllowedPubkey":      tool(GroupCashCircles, ModeWrite),
	"RefreshCircleFollowSet":         tool(GroupCashCircles, ModeWrite),
	"SetCircleAccepting":             tool(GroupCashCircles, ModeWrite),
	"SubmitCircleMemberRequest":      tool(GroupCashCircles, ModeWrite),
	"PublishCircleIdentityFollowSet": tool(GroupCashCircles, ModeWrite),
	"DeleteCircleWalletChild":        tool(GroupCashCircles, ModeWrite),
	"DeleteCircleHub":                tool(GroupCashCircles, ModeWrite),
	"DeleteCircleIdentity":           tool(GroupCashCircles, ModeWrite),

	// ---- Channels & liquidity (peers, channels, LSP client/catalogue) ----
	"ListChannels":       tool(GroupChannels, ModeRead),
	"ListPeers":          tool(GroupChannels, ModeRead),
	"GetForwards":        tool(GroupChannels, ModeRead),
	"ConnectPeer":        tool(GroupChannels, ModeWrite),
	"DisconnectPeer":     tool(GroupChannels, ModeWrite),
	"UpdateChannel":      tool(GroupChannels, ModeWrite),
	"OpenChannel":        guarded(GroupChannels, ModeWrite), // commits on-chain funds, irreversible
	"CloseChannel":       guarded(GroupChannels, ModeWrite), // force-close storm risk, irreversible
	"LSPS0ListProtocols": tool(GroupChannels, ModeRead),
	"LSPS1GetInfo":       tool(GroupChannels, ModeRead),
	"LSPS1GetOrder":      tool(GroupChannels, ModeRead),
	"LSPS1ListOrders":    tool(GroupChannels, ModeRead),
	"LSPS2GetInfo":       tool(GroupChannels, ModeRead),
	"LSPS5ListWebhooks":  tool(GroupChannels, ModeRead),
	"LSPS1CreateOrder":   tool(GroupChannels, ModeWrite),
	"LSPS2Buy":           tool(GroupChannels, ModeWrite),
	"LSPS5SetWebhook":    tool(GroupChannels, ModeWrite),
	"LSPS5RemoveWebhook": tool(GroupChannels, ModeWrite),
	"ListLSPs":           tool(GroupChannels, ModeRead),
	"HandleListLSPs":     tool(GroupChannels, ModeRead),
	"HandleAddLSP":       tool(GroupChannels, ModeWrite),
	"HandleTrustLSP":     tool(GroupChannels, ModeWrite),
	"HandleLSPLiveCheck": tool(GroupChannels, ModeWrite),

	// ---- Channels & liquidity: LSP-operator back-office (lspd, D10) ----
	"LSPOperatorStatus":              tool(GroupChannels, ModeRead),
	"LSPOperatorSummary":             tool(GroupChannels, ModeRead),
	"LSPOperatorOrders":              tool(GroupChannels, ModeRead),
	"LSPOperatorOrder":               tool(GroupChannels, ModeRead),
	"LSPOperatorPeers":               tool(GroupChannels, ModeRead),
	"LSPOperatorJitChannels":         tool(GroupChannels, ModeRead),
	"LSPOperatorNodes":               tool(GroupChannels, ModeRead),
	"LSPOperatorAnalytics":           tool(GroupChannels, ModeRead),
	"LSPOperatorConfig":              tool(GroupChannels, ModeRead),
	"LSPOperatorAnnounceState":       tool(GroupChannels, ModeRead),
	"LSPOperatorUpdateConfig":        tool(GroupChannels, ModeWrite), // fees/limits peers pay: two-step + owner (D22)
	"LSPOperatorSetAnnounceState":    tool(GroupChannels, ModeWrite), // public NIP-LSP listing: two-step
	"LSPOperatorRecheckAnnouncement": tool(GroupChannels, ModeWrite),

	// ---- Listings & discoverability (embedded ListingsAPI) ----
	"ListListings":     tool(GroupListings, ModeRead),
	"PreviewListing":   tool(GroupListings, ModeRead),
	"GetListingRelays": tool(GroupListings, ModeRead),
	"CreateListing":    tool(GroupListings, ModeWrite),
	"UpdateListing":    tool(GroupListings, ModeWrite),
	"ListingAction":    tool(GroupListings, ModeWrite), // pause/resume/unlist/reopen; the revoke-connection action is excluded at the tool layer
	"SetListingRelays": tool(GroupListings, ModeWrite),

	// ---- Node info & hub settings ----
	"GetInfo":                   tool(GroupNode, ModeRead),
	"GetNodeConnectionInfo":     tool(GroupNode, ModeRead),
	"GetNodeStatus":             tool(GroupNode, ModeRead),
	"GetNetworkGraph":           tool(GroupNode, ModeRead),
	"Health":                    tool(GroupNode, ModeRead),
	"GetServices":               tool(GroupNode, ModeRead),
	"RequestMempoolApi":         tool(GroupNode, ModeRead),
	"SetNodeAlias":              tool(GroupNode, ModeWrite),
	"UpdateSettings":            tool(GroupNode, ModeWrite),
	"SetCurrency":               tool(GroupNode, ModeWrite),
	"SetFlokicoinDisplayFormat": tool(GroupNode, ModeWrite),
	"PublishNodeProfile":        tool(GroupNode, ModeWrite),
	"PublishNodeNote":           tool(GroupNode, ModeWrite),

	// ---- Node & settings: on-chain cash withdrawals (WithdrawAPI, CONTRACTS Group 5) ----
	"GetCashHubWithdrawTerms":    tool(GroupNode, ModeRead),
	"ListCashHubPayouts":         tool(GroupNode, ModeRead),
	"GetCashHubPayout":           tool(GroupNode, ModeRead),
	"CashWithdrawStatus":         tool(GroupNode, ModeRead),
	"CashWithdrawNode":           tool(GroupNode, ModeRead),
	"SetCashHubWithdrawTerms":    tool(GroupNode, ModeWrite), // what holders pay to withdraw: two-step + owner (D22)
	"SetCashWithdrawNodeEnabled": tool(GroupNode, ModeWrite), // node pays cash out on-chain: two-step + owner (D22)

	// ---- App connections & identity authorities ----
	"ListApps":                tool(GroupConnections, ModeRead),
	"GetApp":                  tool(GroupConnections, ModeRead),
	"ListIdentityAuthorities": tool(GroupConnections, ModeRead),
	"DeleteApp":               tool(GroupConnections, ModeWrite), // DoS-class (revokes other connections), not drain
	"DeleteAppWithOptions":    tool(GroupConnections, ModeWrite),
	"AddIdentityAuthority":    tool(GroupConnections, ModeWrite),
	"DeleteIdentityAuthority": tool(GroupConnections, ModeWrite),

	// ---- Hard exclusions (never a tool, any scope) ----
	"CreateApp":                   excluded("mints a connection and returns a live pairing secret — a full exclusion bypass (audit S-CRIT-1)"),
	"UpdateApp":                   excluded("can alter a connection's scopes/secret (audit S-CRIT-1)"),
	"RegenerateCashHubConnection": excluded("rotates a cash-hub connection secret, silently breaking holders (audit S-HIGH-1)"),
	"GetCashWalletConnection":     excluded("reveals a connection secret"),
	"GetMnemonic":                 excluded("reveals the seed phrase"),
	"CreateBackup":                excluded("exports the full wallet"),
	"RestoreBackup":               excluded("restores/overwrites the wallet"),
	"GetLogOutput":                excluded("logs may contain bearer tokens / secrets"),
	"ExecuteCustomNodeCommand":    excluded("arbitrary node command"),
	"ChangeUnlockPassword":        excluded("changes the unlock password"),
	"SetAutoUnlockPassword":       excluded("stores the unlock password"),
	"RotatePrivateTransportInbox": excluded("rotates private-transport infra keys"),
	"ResetRouter":                 excluded("wipes routing data (destructive)"),
	"MigrateNodeStorage":          excluded("destructive storage migration"),
	"Stop":                        excluded("shuts the node down (lifecycle)"),
	"Start":                       excluded("node lifecycle / unlock"),
	"Setup":                       excluded("first-run wallet setup"),
	"SetupLocal":                  excluded("first-run wallet setup"),
	"SetupManual":                 excluded("first-run wallet setup"),

	// ---- Not applicable (internal plumbing, not an operator operation) ----
	"GetSetupStatus":               na("setup-flow status; not useful to an assistant"),
	"SetNextBackupReminder":        na("internal reminder bookkeeping"),
	"SendPaymentProbes":            na("routing diagnostic; not exposed"),
	"SendSpontaneousPaymentProbes": na("routing diagnostic; not exposed"),
	"GetCustomNodeCommands":        na("lists raw node commands; the node-command surface is not exposed"),
	"SendEvent":                    na("telemetry sink"),
	"UpdateLSPS1OrderState":        na("called by an inbound LSPS5 webhook notification, not an operator"),
}

// classify returns the policy for an api.API method name, and whether it was
// found. An unknown method is a classification gap the ratchet test must catch.
func classify(method string) (Classification, bool) {
	c, ok := methodClass[method]
	return c, ok
}

// GuardedMethods returns the api.API methods flagged as the irreversible class
// that requires the residual guard.
func GuardedMethods() []string {
	var out []string
	for name, c := range methodClass {
		if c.Kind == ClassTool && c.Guarded {
			out = append(out, name)
		}
	}
	return out
}
