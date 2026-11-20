package mcp

import (
	"context"
	"fmt"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/lnclient"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// deferredTools are tool-classified api.API methods that intentionally have no
// adapter yet, each with the reason. The catalogue ratchet test fails if a
// tool-classified method is neither wired nor listed here, so nothing silently
// drops off (or onto) the surface. Shrink this list; never grow it without a
// reason.
var deferredTools = map[string]string{
	"SubmitCircleMemberRequest": "excluded by design: it takes a member-signed join event — an assistant must never forge or relay a member's signature",
	"LSPS5SetWebhook": "excluded: replaces this hub's single hub-wide LSPS5 registration (keyed by app name) with a caller-chosen URL — " +
		"it would redirect the LSP's order/payment notifications to an arbitrary endpoint (exfiltration/SSRF-by-proxy) and silently " +
		"stop order-state updates; the hub registers its own webhook when it orders a channel, so there is no operator need",
	"LSPS5RemoveWebhook": "excluded: removes this hub's hub-wide LSPS5 registration (the URL argument is ignored), silently stopping " +
		"order-state notifications for every order; no operator need through an assistant",
}

type (
	anyResult struct {
		Data any `json:"data"`
	}
	transferArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		FromAppID      *uint  `json:"from_app_id,omitempty" jsonschema:"source sub-wallet id; omit for the main wallet"`
		ToAppID        *uint  `json:"to_app_id,omitempty" jsonschema:"destination sub-wallet id; omit for the main wallet"`
		AmountMloki    uint64 `json:"amount_mloki"`
		ConfirmCode    string `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	updateChannelArgs struct {
		IdempotencyKey                      string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		ChannelID                           string `json:"channel_id"`
		NodeID                              string `json:"node_id"`
		ForwardingFeeBaseMloki              uint32 `json:"forwarding_fee_base_mloki"`
		ForwardingFeeProportionalMillionths uint32 `json:"forwarding_fee_proportional_millionths"`
	}
	stringArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		Value          string `json:"value"`
	}
	relaysArgs struct {
		IdempotencyKey string   `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		Relays         []string `json:"relays"`
	}
	idArgs struct {
		ID uint `json:"id"`
	}
	hubChildArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		HubAppID       uint   `json:"hub_app_id"`
		ChildAppID     uint   `json:"child_app_id"`
		ConfirmCode    string `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	claimArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		HubAppID       uint   `json:"hub_app_id"`
		WalletAppID    uint   `json:"wallet_app_id"`
		ClaimID        uint   `json:"claim_id"`
		ConfirmCode    string `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	idWriteArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		ID             uint   `json:"id"`
	}
	authorityArgs struct {
		IdempotencyKey string   `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		Pubkey         string   `json:"pubkey"`
		Name           string   `json:"name"`
		RelayURLs      []string `json:"relay_urls,omitempty"`
	}
	claimsPageArgs struct {
		AppID  uint   `json:"app_id"`
		Limit  uint64 `json:"limit,omitempty" jsonschema:"page size (max 100)"`
		Offset uint64 `json:"offset,omitempty"`
		Status string `json:"status,omitempty"`
	}
	claimsPage struct {
		Claims []claimView               `json:"claims"`
		Total  uint64                    `json:"total"`
		Counts api.CashWalletClaimCounts `json:"counts"`
	}
	appsPage struct {
		Limit  uint64 `json:"limit,omitempty" jsonschema:"page size (max 100)"`
		Offset uint64 `json:"offset,omitempty"`
		Name   string `json:"name,omitempty"`
	}
	recipients struct {
		Recipients []claimView `json:"recipients"`
	}
)

func keyOf[T interface{ key() string }](in T) string { return in.key() }

func (a transferArgs) key() string      { return a.IdempotencyKey }
func (a updateChannelArgs) key() string { return a.IdempotencyKey }
func (a stringArgs) key() string        { return a.IdempotencyKey }
func (a relaysArgs) key() string        { return a.IdempotencyKey }
func (a hubChildArgs) key() string      { return a.IdempotencyKey }
func (a claimArgs) key() string         { return a.IdempotencyKey }
func (a idWriteArgs) key() string       { return a.IdempotencyKey }
func (a authorityArgs) key() string     { return a.IdempotencyKey }
func (a keyOnlyArgs) key() string       { return a.IdempotencyKey }

// registerCatalogMore wires the second batch of the catalogue.
func (s *Server) registerCatalogMore(srv *mcpsdk.Server, conn *Conn) {
	// Node info & hub settings.
	readTool(s, srv, conn, "GetNodeConnectionInfo", "node_connection_info",
		"The node's public pubkey@host:port.",
		func(ctx context.Context, a api.API) (*lnclient.NodeConnectionInfo, error) {
			return a.GetNodeConnectionInfo(ctx)
		})
	readToolIn(s, srv, conn, "GetNetworkGraph", "network_graph",
		"Network graph info for the given node ids.",
		func(ctx context.Context, a api.API, in struct {
			NodeIDs []string `json:"node_ids"`
		}) (anyResult, error) {
			g, err := a.GetNetworkGraph(ctx, in.NodeIDs)
			return anyResult{Data: g}, err
		})
	readTool(s, srv, conn, "GetServices", "get_services",
		"The hub's configured services catalogue (explorers, relays).",
		func(ctx context.Context, a api.API) (anyResult, error) {
			v, err := a.GetServices(ctx)
			return anyResult{Data: v}, err
		})
	writeTool(s, srv, conn, "SetFlokicoinDisplayFormat", "set_display_format",
		"Set how amounts are displayed.", keyOf[stringArgs],
		func(_ context.Context, a api.API, in stringArgs) (okResult, error) {
			return okResult{OK: true}, a.SetFlokicoinDisplayFormat(in.Value)
		})

	// Wallet & payments.
	confirmTool(s, srv, conn, "Transfer", "transfer",
		"Move funds between the main wallet and sub-wallets.", keyOf[transferArgs],
		func(in transferArgs) string { return in.ConfirmCode },
		func(in transferArgs) any { return [3]any{in.FromAppID, in.ToAppID, in.AmountMloki} },
		func(_ context.Context, _ api.API, in transferArgs) (*PaymentPreview, error) {
			if in.AmountMloki == 0 {
				return nil, errInvalidArgs("amount_mloki must be greater than zero")
			}
			return &PaymentPreview{
				Action:      "Move funds from " + walletLabel(in.FromAppID) + " to " + walletLabel(in.ToAppID),
				AmountLoki:  formatLoki(in.AmountMloki),
				AmountMloki: in.AmountMloki,
				Recipient:   walletLabel(in.ToAppID),
			}, nil
		},
		func(ctx context.Context, a api.API, in transferArgs) (okResult, error) {
			return okResult{OK: true}, a.Transfer(ctx, in.FromAppID, in.ToAppID, in.AmountMloki)
		})
	writeTool(s, srv, conn, "SyncWallet", "sync_wallet",
		"Resync the on-chain wallet.", keyOf[keyOnlyArgs],
		func(_ context.Context, a api.API, _ keyOnlyArgs) (okResult, error) {
			return okResult{OK: true}, a.SyncWallet()
		})

	// Network & liquidity.
	writeTool(s, srv, conn, "UpdateChannel", "update_channel",
		"Update a channel's forwarding fee policy.", keyOf[updateChannelArgs],
		func(ctx context.Context, a api.API, in updateChannelArgs) (okResult, error) {
			return okResult{OK: true}, a.UpdateChannel(ctx, &api.UpdateChannelRequest{
				ChannelId: in.ChannelID, NodeId: in.NodeID,
				ForwardingFeeBaseMloki:              in.ForwardingFeeBaseMloki,
				ForwardingFeeProportionalMillionths: in.ForwardingFeeProportionalMillionths,
			})
		})

	// Listings & discoverability.
	writeTool(s, srv, conn, "SetListingRelays", "set_listing_relays",
		"Set the relays listings are published to.", keyOf[relaysArgs],
		func(ctx context.Context, a api.API, in relaysArgs) (*api.ListingRelaysResponse, error) {
			return a.SetListingRelays(ctx, in.Relays)
		})

	// Cash & circles.
	readToolIn(s, srv, conn, "ListCashWalletClaims", "list_cash_wallet_claims",
		"A cash hub's recipient slices (paged, max 100).",
		func(_ context.Context, a api.API, in claimsPageArgs) (claimsPage, error) {
			rows, total, counts, err := a.ListCashWalletClaims(in.AppID, clampLimit(in.Limit), in.Offset, in.Status)
			return claimsPage{Claims: toClaimViews(rows), Total: total, Counts: counts}, err
		})
	readToolIn(s, srv, conn, "GetCashWalletRecipients", "cash_wallet_recipients",
		"Recipients served by one cash wallet.",
		func(_ context.Context, a api.API, in appIDArgs) (recipients, error) {
			r, err := a.GetCashWalletRecipients(in.AppID)
			return recipients{Recipients: toClaimViews(r)}, err
		})
	readToolIn(s, srv, conn, "GetCircleIdentity", "get_circle_identity",
		"Details of one circle identity.",
		func(ctx context.Context, a api.API, in idArgs) (*api.CircleIdentityResponse, error) {
			return a.GetCircleIdentity(ctx, in.ID)
		})
	readToolIn(s, srv, conn, "GetCircleIdentityFollowSet", "get_circle_identity_follow_set",
		"A follow-set identity's members as this hub reads them.",
		func(ctx context.Context, a api.API, in idArgs) (*api.FollowSetResponse, error) {
			return a.GetCircleIdentityFollowSet(ctx, in.ID)
		})
	confirmTool(s, srv, conn, "DeleteCashWallet", "delete_cash_wallet",
		"Reclaim a cash wallet's remaining balance to its hub and delete it. Bills issued from it stop working.", keyOf[hubChildArgs],
		func(in hubChildArgs) string { return in.ConfirmCode },
		func(in hubChildArgs) any { return [2]uint{in.HubAppID, in.ChildAppID} },
		func(ctx context.Context, a api.API, in hubChildArgs) (*PaymentPreview, error) {
			return s.appFundsPreview(ctx, a, in.ChildAppID, "Reclaim and delete cash wallet", fmt.Sprintf("its cash hub #%d — anyone still holding its cash loses it", in.HubAppID))
		},
		func(_ context.Context, a api.API, in hubChildArgs) (okResult, error) {
			return okResult{OK: true}, a.DeleteCashWallet(in.HubAppID, in.ChildAppID)
		})
	confirmTool(s, srv, conn, "DeleteCashClaim", "delete_cash_claim",
		"Remove an unclaimed cash slice, sweeping it back to the hub. Its recipient can no longer claim it.", keyOf[claimArgs],
		func(in claimArgs) string { return in.ConfirmCode },
		func(in claimArgs) any { return [3]uint{in.HubAppID, in.WalletAppID, in.ClaimID} },
		func(ctx context.Context, a api.API, in claimArgs) (*PaymentPreview, error) {
			p, err := s.appFundsPreview(ctx, a, in.WalletAppID, fmt.Sprintf("Take back unclaimed cash slice #%d from wallet", in.ClaimID), fmt.Sprintf("cash hub #%d — the slice's recipient can no longer claim it", in.HubAppID))
			if p != nil {
				p.AmountLoki, p.AmountMloki = "", 0 // the slice amount, not the whole wallet's balance
			}
			return p, err
		},
		func(_ context.Context, a api.API, in claimArgs) (okResult, error) {
			return okResult{OK: true}, a.DeleteCashClaim(in.HubAppID, in.WalletAppID, in.ClaimID)
		})
	confirmTool(s, srv, conn, "DeleteCircleWalletChild", "delete_circle_child",
		"Remove one member wallet from a circle (it closes after its grace period; the member's balance is not swept).", keyOf[hubChildArgs],
		func(in hubChildArgs) string { return in.ConfirmCode },
		func(in hubChildArgs) any { return [2]uint{in.HubAppID, in.ChildAppID} },
		func(ctx context.Context, a api.API, in hubChildArgs) (*PaymentPreview, error) {
			return s.appFundsPreview(ctx, a, in.ChildAppID, "Close circle member wallet", "stays with the member until the wallet closes")
		},
		func(_ context.Context, a api.API, in hubChildArgs) (okResult, error) {
			return okResult{OK: true}, a.DeleteCircleWalletChild(in.HubAppID, in.ChildAppID)
		})
	writeTool(s, srv, conn, "DeleteCircleIdentity", "delete_circle_identity",
		"Delete a circle identity no circle references.", keyOf[idWriteArgs],
		func(_ context.Context, a api.API, in idWriteArgs) (okResult, error) {
			return okResult{OK: true}, a.DeleteCircleIdentity(in.ID)
		})

	// App connections & identity.
	readToolIn(s, srv, conn, "ListApps", "list_apps",
		"Connected apps (paged, max 100).",
		func(_ context.Context, a api.API, in appsPage) (*api.ListAppsResponse, error) {
			return a.ListApps(clampLimit(in.Limit), in.Offset, api.ListAppsFilters{Name: in.Name}, "")
		})
	writeTool(s, srv, conn, "AddIdentityAuthority", "add_identity_authority",
		"Trust a new identity authority.", keyOf[authorityArgs],
		func(_ context.Context, a api.API, in authorityArgs) (*api.IdentityAuthorityResponse, error) {
			return a.AddIdentityAuthority(&api.AddIdentityAuthorityRequest{Pubkey: in.Pubkey, Name: in.Name, RelayURLs: in.RelayURLs})
		})
	writeTool(s, srv, conn, "DeleteIdentityAuthority", "delete_identity_authority",
		"Remove a trusted identity authority.", keyOf[stringArgs],
		func(_ context.Context, a api.API, in stringArgs) (okResult, error) {
			return okResult{OK: true}, a.DeleteIdentityAuthority(in.Value)
		})
}

func walletLabel(id *uint) string {
	if id == nil {
		return "the main wallet"
	}
	return fmt.Sprintf("sub-wallet #%d", *id)
}
