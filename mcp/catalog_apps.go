package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/cash/cashorder"
	"github.com/flokiorg/lokihub/db"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AppResolver loads an App by id (nil when it does not exist). The hub wires it
// to apps.AppsService.GetAppById — the same lookup the REST handlers use
// (getAppByIDParam), so the MCP path is never weaker than REST.
type AppResolver func(id uint) *db.App

// ErrAppNotFound mirrors REST's 404 for an unknown app id.
var ErrAppNotFound = errors.New("mcp: app not found")

// SetAppResolver installs the app lookup used by the *db.App-taking tools. Until
// it is set those tools are not registered (they could not run).
func (s *Server) SetAppResolver(r AppResolver) { s.appByID = r }

// resolveApp loads an app and, when wantKind is non-empty, requires that kind.
func (s *Server) resolveApp(id uint, wantKind string) (*db.App, error) {
	app := s.appByID(id)
	if app == nil {
		return nil, ErrAppNotFound
	}
	if wantKind != "" && app.Kind != wantKind {
		return nil, fmt.Errorf("mcp: app %d is a %s, not a %s", id, app.Kind, wantKind)
	}
	return app, nil
}

type (
	pubkeysArgs struct {
		IdempotencyKey string   `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID          uint     `json:"app_id"`
		Pubkeys        []string `json:"pubkeys"`
		ConfirmCode    string   `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	appPubkeyArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID          uint   `json:"app_id"`
		Pubkey         string `json:"pubkey"`
		ConfirmCode    string `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	appWriteArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID          uint   `json:"app_id"`
	}
	appDeleteArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID          uint   `json:"app_id"`
		ConfirmCode    string `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	deleteAppArgs struct {
		IdempotencyKey  string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID           uint   `json:"app_id"`
		ConfirmWatchEnd bool   `json:"confirm_watch_end,omitempty" jsonschema:"confirm ending the watch on a cash hub's paid on-chain order addresses"`
		ConfirmCode     string `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	deleteCircleArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID          uint   `json:"app_id"`
		Mode           string `json:"mode" jsonschema:"how member balances are settled, as in the hub UI"`
		ConfirmCode    string `json:"confirm_code,omitempty" jsonschema:"omit on the first call to get a preview; call again with the code only after the user explicitly approves it"`
	}
	acceptingArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID          uint   `json:"app_id"`
		Accepting      bool   `json:"accepting"`
	}
	orderConnArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID          uint   `json:"app_id"`
		Label          string `json:"label"`
	}
	retireArgs struct {
		IdempotencyKey string `json:"idempotency_key" jsonschema:"a unique key identifying this write; retrying with the same key will not repeat the effect"`
		AppID          uint   `json:"app_id"`
		ConnAppID      uint   `json:"connection_app_id"`
	}
	childrenPageArgs struct {
		AppID  uint   `json:"app_id"`
		Limit  uint64 `json:"limit,omitempty" jsonschema:"page size (max 100)"`
		Offset uint64 `json:"offset,omitempty"`
	}
	pubkeyList struct {
		Pubkeys []string `json:"pubkeys"`
	}
	childrenPage struct {
		Children []api.CircleChildBalance `json:"children"`
		Total    uint64                   `json:"total"`
	}
	orderConns struct {
		Connections []orderConnView `json:"connections"`
	}
)

func (a pubkeysArgs) key() string      { return a.IdempotencyKey }
func (a appPubkeyArgs) key() string    { return a.IdempotencyKey }
func (a appWriteArgs) key() string     { return a.IdempotencyKey }
func (a appDeleteArgs) key() string    { return a.IdempotencyKey }
func (a deleteAppArgs) key() string    { return a.IdempotencyKey }
func (a deleteCircleArgs) key() string { return a.IdempotencyKey }
func (a acceptingArgs) key() string    { return a.IdempotencyKey }
func (a orderConnArgs) key() string    { return a.IdempotencyKey }
func (a retireArgs) key() string       { return a.IdempotencyKey }

// registerAppTools wires the tools whose api.API method takes a loaded *db.App.
func (s *Server) registerAppTools(srv *mcpsdk.Server, conn *Conn) {
	if s.appByID == nil {
		return
	}
	// App connections.
	readToolIn(s, srv, conn, "GetApp", "get_app",
		"Full detail for one connected app, cash hub or circle.",
		func(ctx context.Context, a api.API, in appIDArgs) (*api.App, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return nil, err
			}
			v := a.GetApp(ctx, app)
			if v != nil {
				cp := *v
				cp.Metadata = nil // raw metadata never reaches an assistant (redact.go)
				v = &cp
			}
			return v, nil
		})
	confirmTool(s, srv, conn, "DeleteApp", "delete_app",
		"Delete (revoke) a connected app. If it holds its own balance, that balance is affected.", keyOf[appDeleteArgs],
		func(in appDeleteArgs) string { return in.ConfirmCode },
		func(in appDeleteArgs) any { return in.AppID },
		func(ctx context.Context, a api.API, in appDeleteArgs) (*PaymentPreview, error) {
			if app, err := s.resolveApp(in.AppID, ""); err != nil {
				return nil, err
			} else if err := s.guardAssistantDelete(conn, app); err != nil {
				return nil, err
			}
			return s.appFundsPreview(ctx, a, in.AppID, "Delete app", "the hub (per the app's kind; see the Lokihub app for details)")
		},
		func(_ context.Context, a api.API, in appDeleteArgs) (okResult, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return okResult{}, err
			}
			if err := s.guardAssistantDelete(conn, app); err != nil {
				return okResult{}, err
			}
			return okResult{OK: true}, a.DeleteApp(app)
		})
	confirmTool(s, srv, conn, "DeleteAppWithOptions", "delete_app_confirmed",
		"Delete an app, confirming side effects (e.g. ending a cash hub's on-chain order watch).", keyOf[deleteAppArgs],
		func(in deleteAppArgs) string { return in.ConfirmCode },
		func(in deleteAppArgs) any { return [2]any{in.AppID, in.ConfirmWatchEnd} },
		func(ctx context.Context, a api.API, in deleteAppArgs) (*PaymentPreview, error) {
			if in.ConfirmWatchEnd {
				return nil, errInvalidArgs("ending a cash hub's order-address watch must be confirmed by the owner in the Lokihub app")
			}
			if app, err := s.resolveApp(in.AppID, ""); err != nil {
				return nil, err
			} else if err := s.guardAssistantDelete(conn, app); err != nil {
				return nil, err
			}
			return s.appFundsPreview(ctx, a, in.AppID, "Delete app", "the hub (per the app's kind; see the Lokihub app for details)")
		},
		func(_ context.Context, a api.API, in deleteAppArgs) (okResult, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return okResult{}, err
			}
			if err := s.guardAssistantDelete(conn, app); err != nil {
				return okResult{}, err
			}
			if in.ConfirmWatchEnd {
				// A human confirmation (ending the watch on paid on-chain order
				// addresses can miss customer payments) — never self-asserted
				// by the assistant (audit MED-5).
				return okResult{}, errInvalidArgs("ending a cash hub's order-address watch must be confirmed by the owner in the Lokihub app")
			}
			return okResult{OK: true}, a.DeleteAppWithOptions(app, api.DeleteAppOptions{})
		})

	// Circles.
	readToolIn(s, srv, conn, "ListCircleAllowlist", "list_circle_allowlist",
		"A circle's member allowlist.",
		func(_ context.Context, a api.API, in appIDArgs) (pubkeyList, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return pubkeyList{}, err
			}
			p, err := a.ListCircleAllowlist(app)
			return pubkeyList{Pubkeys: p}, err
		})
	readToolIn(s, srv, conn, "ListCircleChildrenBalances", "list_circle_children",
		"A circle's member wallets and balances (paged, max 100).",
		func(_ context.Context, a api.API, in childrenPageArgs) (childrenPage, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return childrenPage{}, err
			}
			c, total, err := a.ListCircleChildrenBalances(app, clampLimit(in.Limit), in.Offset)
			return childrenPage{Children: c, Total: total}, err
		})
	// Membership edits suspend members at once (pentest F F-6): two-step,
	// with the exact added/removed counts in the preview.
	confirmToolWarn(s, srv, conn, "ReplaceCircleAllowlist", "replace_circle_allowlist",
		"Replace a circle's member allowlist. Members not in the new list are suspended.", accessWarning, keyOf[pubkeysArgs],
		func(in pubkeysArgs) string { return in.ConfirmCode },
		func(in pubkeysArgs) any { return [2]any{in.AppID, in.Pubkeys} },
		func(_ context.Context, a api.API, in pubkeysArgs) (*PaymentPreview, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return nil, err
			}
			cur, err := a.ListCircleAllowlist(app)
			if err != nil {
				return nil, err
			}
			added, removed := setDiff(cur, in.Pubkeys)
			return &PaymentPreview{
				Action:    fmt.Sprintf("Replace the member list of circle %q (#%d)", app.Name, app.ID),
				Recipient: fmt.Sprintf("%d member(s) after the change", len(in.Pubkeys)),
				Details: []PreviewDetail{
					{Label: "members added", Value: strconv.Itoa(len(added))},
					{Label: "members removed (suspended)", Value: strconv.Itoa(len(removed))},
				},
			}, nil
		},
		func(_ context.Context, a api.API, in pubkeysArgs) (okResult, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return okResult{}, err
			}
			return okResult{OK: true}, a.ReplaceCircleAllowlist(app, in.Pubkeys)
		})
	confirmToolWarn(s, srv, conn, "RemoveCircleAllowedPubkey", "remove_circle_member",
		"Remove one pubkey from a circle's allowlist (the member is suspended).", accessWarning, keyOf[appPubkeyArgs],
		func(in appPubkeyArgs) string { return in.ConfirmCode },
		func(in appPubkeyArgs) any { return [2]any{in.AppID, in.Pubkey} },
		func(_ context.Context, _ api.API, in appPubkeyArgs) (*PaymentPreview, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return nil, err
			}
			return &PaymentPreview{
				Action:    fmt.Sprintf("Remove a member from circle %q (#%d)", app.Name, app.ID),
				Recipient: in.Pubkey,
			}, nil
		},
		func(_ context.Context, a api.API, in appPubkeyArgs) (okResult, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return okResult{}, err
			}
			return okResult{OK: true}, a.RemoveCircleAllowedPubkey(app, in.Pubkey)
		})
	writeTool(s, srv, conn, "RefreshCircleFollowSet", "refresh_circle_follow_set",
		"Re-fetch a follow-set circle's members from relays.", keyOf[appWriteArgs],
		func(ctx context.Context, a api.API, in appWriteArgs) (okResult, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return okResult{}, err
			}
			return okResult{OK: true}, a.RefreshCircleFollowSet(ctx, app)
		})
	writeTool(s, srv, conn, "SetCircleAccepting", "set_circle_accepting",
		"Open or close a circle to new members.", keyOf[acceptingArgs],
		func(_ context.Context, a api.API, in acceptingArgs) (okResult, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return okResult{}, err
			}
			return okResult{OK: true}, a.SetCircleAccepting(app, in.Accepting)
		})
	confirmTool(s, srv, conn, "DeleteCircleHub", "delete_circle_hub",
		"Tear down a whole circle; mode decides how member balances are settled.", keyOf[deleteCircleArgs],
		func(in deleteCircleArgs) string { return in.ConfirmCode },
		func(in deleteCircleArgs) any { return [2]any{in.AppID, in.Mode} },
		func(ctx context.Context, a api.API, in deleteCircleArgs) (*PaymentPreview, error) {
			if _, err := s.resolveApp(in.AppID, db.AppKindCircleHub); err != nil {
				return nil, err
			}
			if in.Mode != db.CircleDeleteModeAll && in.Mode != db.CircleDeleteModeEmptyOnly {
				return nil, errInvalidArgs("mode must be " + strconv.Quote(db.CircleDeleteModeAll) + " or " + strconv.Quote(db.CircleDeleteModeEmptyOnly))
			}
			return s.appFundsPreview(ctx, a, in.AppID, "Tear down circle", "member balances settled by mode \""+in.Mode+"\"")
		},
		func(_ context.Context, a api.API, in deleteCircleArgs) (*api.DeleteCircleHubResult, error) {
			// Same kind check the REST handler applies before this call.
			app, err := s.resolveApp(in.AppID, db.AppKindCircleHub)
			if err != nil {
				return nil, err
			}
			return a.DeleteCircleHub(app, in.Mode)
		})

	// Cash orders (selling).
	readToolIn(s, srv, conn, "ListOrderConnections", "list_order_connections",
		"A cash hub's order (selling) connections.",
		func(_ context.Context, a api.API, in appIDArgs) (orderConns, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return orderConns{}, err
			}
			c, err := a.ListOrderConnections(app)
			views := make([]orderConnView, 0, len(c))
			for _, x := range c {
				views = append(views, toOrderConnView(x))
			}
			return orderConns{Connections: views}, err
		})
	readToolIn(s, srv, conn, "GetOrderTerms", "get_order_terms",
		"A cash hub's selling terms.",
		func(_ context.Context, a api.API, in appIDArgs) (*cashorder.Settings, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return nil, err
			}
			return a.GetOrderTerms(app)
		})
	readToolIn(s, srv, conn, "ListOrders", "list_orders",
		"A cash hub's placed orders.",
		func(_ context.Context, a api.API, in appIDArgs) (*cashorder.OrdersView, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return nil, err
			}
			return a.ListOrders(app)
		})
	writeTool(s, srv, conn, "CreateOrderConnection", "create_order_connection",
		"Create a selling (order) connection for a cash hub.", keyOf[orderConnArgs],
		func(_ context.Context, a api.API, in orderConnArgs) (orderConnView, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return orderConnView{}, err
			}
			c, err := a.CreateOrderConnection(app, in.Label)
			if err != nil || c == nil {
				return orderConnView{}, err
			}
			return toOrderConnView(*c), nil
		})
	writeTool(s, srv, conn, "RetireOrderConnection", "retire_order_connection",
		"Retire a cash hub's selling connection.", keyOf[retireArgs],
		func(_ context.Context, a api.API, in retireArgs) (okResult, error) {
			app, err := s.resolveApp(in.AppID, "")
			if err != nil {
				return okResult{}, err
			}
			return okResult{OK: true}, a.RetireOrderConnection(app, in.ConnAppID)
		})
}

// guardAssistantDelete: an assistant may revoke its own connection or ordinary
// apps, but never another AI-assistant connection (audit MED-5) — one assistant
// must not be able to cut the owner's other assistants off.
func (s *Server) guardAssistantDelete(conn *Conn, app *db.App) error {
	if app.Kind == AppKindMCP && app.ID != conn.AppID {
		return errInvalidArgs("an assistant cannot revoke another AI assistant's connection")
	}
	return nil
}

// setDiff returns what next adds to and removes from cur.
func setDiff(cur, next []string) (added, removed []string) {
	in := map[string]bool{}
	for _, c := range cur {
		in[c] = true
	}
	nx := map[string]bool{}
	for _, n := range next {
		nx[n] = true
		if !in[n] {
			added = append(added, n)
		}
	}
	for _, c := range cur {
		if !nx[c] {
			removed = append(removed, c)
		}
	}
	return added, removed
}
