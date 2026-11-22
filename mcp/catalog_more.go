package mcp

import (
	"context"
	"fmt"
	"github.com/flokiorg/lokihub/listing"
	"strconv"
	"strings"

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
		ConfirmCode    string   `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	authorityDelArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		Pubkey         string `json:"pubkey" jsonschema:"the identity authority's pubkey (64 hex characters)"`
		ConfirmCode    string `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
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
func (a authorityDelArgs) key() string  { return a.IdempotencyKey }
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
			NodeIDs []string `json:"node_ids" jsonschema:"1-50 node ids (66 hex characters)"`
		}) (anyResult, error) {
			// Each call walks the whole graph per id: bounded (pentest D PD-8).
			if len(in.NodeIDs) == 0 || len(in.NodeIDs) > 50 {
				return anyResult{}, errInvalidArgs("give 1 to 50 node ids")
			}
			for _, id := range in.NodeIDs {
				if !isNodePubkey(id) {
					return anyResult{}, errInvalidArgs("node ids must be 66 hex characters")
				}
			}
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
		func(ctx context.Context, a api.API, in transferArgs) (*PaymentPreview, error) {
			if in.AmountMloki == 0 {
				return nil, errInvalidArgs("amount_mloki must be greater than zero")
			}
			// Name every wallet from the hub's own records — an id the user
			// cannot recognise is not something they can approve (pentest D PD-5).
			from, err := s.walletDesc(ctx, a, in.FromAppID)
			if err != nil {
				return nil, err
			}
			to, err := s.walletDesc(ctx, a, in.ToAppID)
			if err != nil {
				return nil, err
			}
			return &PaymentPreview{
				Action:      "Move funds from " + from + " to " + to,
				AmountLoki:  formatLoki(in.AmountMloki),
				AmountMloki: in.AmountMloki,
				Recipient:   to,
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
	// The node signs and publishes its relay list (NIP-65) on change, and every
	// listing is republished there: a public, identity-level action (pentest F
	// F-1). Two-step, and only public wss:// relays (no SSRF into the LAN).
	confirmToolWarn(s, srv, conn, "SetListingRelays", "set_listing_relays",
		"Set the relays listings are published to. The node publishes its new relay list publicly.", publishWarning, keyOf[relaysArgs],
		func(in relaysArgs) string { return in.ConfirmCode },
		func(in relaysArgs) any { return in.Relays },
		func(_ context.Context, a api.API, in relaysArgs) (*PaymentPreview, error) {
			if len(in.Relays) == 0 || len(in.Relays) > 10 {
				return nil, errInvalidArgs("give 1 to 10 relays")
			}
			for _, r := range in.Relays {
				if !listing.IsPublicRelay(r) {
					return nil, errInvalidArgs("relays must be public wss:// URLs (no local or private addresses): " + strconv.QuoteToASCII(r))
				}
			}
			cur := a.GetListingRelays()
			current := "(default)"
			if cur != nil && len(cur.Configured) > 0 {
				current = strings.Join(cur.Configured, ", ")
			}
			return &PaymentPreview{
				Action:    "Replace the node's listing relays and publish the new relay list",
				Recipient: strings.Join(in.Relays, ", "),
				Details:   []PreviewDetail{{Label: "current relays", Value: current}, {Label: "new relays", Value: strings.Join(in.Relays, ", ")}},
			}, nil
		},
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
			if err != nil {
				return nil, err
			}
			// Show the slice's own amount and recipient (pentest D PD-6).
			rows, err := a.GetCashWalletRecipients(in.WalletAppID)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				if r.ID != in.ClaimID {
					continue
				}
				amt := uint64(0)
				if r.AmountMloki > 0 {
					amt = uint64(r.AmountMloki)
				}
				p.AmountMloki, p.AmountLoki = amt, formatLoki(amt)
				p.Details = append(p.Details, PreviewDetail{Label: "slice recipient", Value: r.IdentityType + " " + r.IdentityValue})
				return p, nil
			}
			return nil, errInvalidArgs(fmt.Sprintf("cash slice #%d not found in wallet #%d", in.ClaimID, in.WalletAppID))
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
			r, err := a.ListApps(clampLimit(in.Limit), in.Offset, api.ListAppsFilters{Name: in.Name}, "")
			if r != nil {
				for i := range r.Apps {
					r.Apps[i].Metadata = nil // raw metadata never reaches an assistant (redact.go)
				}
			}
			return r, err
		})
	writeTool(s, srv, conn, "AddIdentityAuthority", "add_identity_authority",
		"Trust a new identity authority.", keyOf[authorityArgs],
		func(_ context.Context, a api.API, in authorityArgs) (*api.IdentityAuthorityResponse, error) {
			return a.AddIdentityAuthority(&api.AddIdentityAuthorityRequest{Pubkey: in.Pubkey, Name: in.Name, RelayURLs: in.RelayURLs})
		})
	// Removing an authority strands every unclaimed slice it vouched for
	// (pentest D PD-10): two-step, with that count in the preview.
	confirmToolWarn(s, srv, conn, "DeleteIdentityAuthority", "delete_identity_authority",
		"Remove a trusted identity authority. Unclaimed cash slices it vouched for can no longer be redeemed.", accessWarning, keyOf[authorityDelArgs],
		func(in authorityDelArgs) string { return in.ConfirmCode },
		func(in authorityDelArgs) any { return in.Pubkey },
		func(_ context.Context, a api.API, in authorityDelArgs) (*PaymentPreview, error) {
			list, err := a.ListIdentityAuthorities()
			if err != nil {
				return nil, err
			}
			for _, ia := range list {
				if ia.Pubkey == in.Pubkey {
					return &PaymentPreview{
						Action:    fmt.Sprintf("Remove identity authority %q (%s)", ia.Name, ia.Pubkey),
						Recipient: "nobody — slices it vouched for become unredeemable until it is re-added",
						Details:   []PreviewDetail{{Label: "unclaimed slices stranded", Value: strconv.Itoa(ia.UnredeemedSliceCount)}},
					}, nil
				}
			}
			return nil, errInvalidArgs("no identity authority with that pubkey")
		},
		func(_ context.Context, a api.API, in authorityDelArgs) (okResult, error) {
			return okResult{OK: true}, a.DeleteIdentityAuthority(in.Pubkey)
		})
}

func walletLabel(id *uint) string {
	if id == nil {
		return "the main wallet"
	}
	return fmt.Sprintf("sub-wallet #%d", *id)
}

// walletDesc names a wallet for a preview from the hub's records: name, kind
// and balance; nil = the main wallet. An unknown id is refused.
func (s *Server) walletDesc(ctx context.Context, a api.API, id *uint) (string, error) {
	if id == nil {
		return "the main wallet", nil
	}
	app, err := s.resolveApp(*id, "")
	if err != nil {
		return "", err
	}
	v := a.GetApp(ctx, app)
	if v == nil {
		return fmt.Sprintf("wallet #%d", *id), nil
	}
	bal := uint64(0)
	if v.Balance > 0 {
		bal = uint64(v.Balance)
	}
	return fmt.Sprintf("%q (#%d, %s, balance %s loki)", v.Name, *id, v.Kind, formatLoki(bal)), nil
}
