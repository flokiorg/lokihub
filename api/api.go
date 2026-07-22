package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/go-flokicoin/chainutil"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/jitwallet"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lnclient/flnd/wrapper"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/loki"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/transactions"

	"github.com/flokiorg/lokihub/keys"
	permissions "github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/service"
	"github.com/flokiorg/lokihub/swaps"
	"github.com/flokiorg/lokihub/utils"
	"github.com/flokiorg/lokihub/version"
)

const (
	FlndEndpoint = "localhost:10005"
)

type api struct {
	db               *gorm.DB
	appsSvc          apps.AppsService
	cfg              config.Config
	svc              service.Service
	permissionsSvc   permissions.PermissionsService
	keys             keys.Keys
	lokiSvc          loki.LokiService
	startupError     error
	startupErrorTime time.Time
	eventPublisher   events.EventPublisher
	lspManager       *manager.LSPManager
	iaManager        *apps.IdentityAuthorityManager
}

func NewAPI(svc service.Service, gormDB *gorm.DB, config config.Config, keys keys.Keys, lokiSvc loki.LokiService, eventPublisher events.EventPublisher) *api {
	return &api{
		db:             gormDB,
		appsSvc:        apps.NewAppsService(gormDB, eventPublisher, keys, config),
		cfg:            config,
		svc:            svc,
		permissionsSvc: permissions.NewPermissionsService(gormDB, eventPublisher),
		keys:           keys,
		lokiSvc:        lokiSvc,
		eventPublisher: eventPublisher,
		lspManager:     manager.NewLSPManager(gormDB),
		iaManager:      apps.NewIdentityAuthorityManager(gormDB),
	}
}

func (api *api) CreateApp(createAppRequest *CreateAppRequest) (*CreateAppResponse, error) {
	if slices.Contains(createAppRequest.Scopes, constants.SUPERUSER_SCOPE) {
		if !api.cfg.CheckUnlockPassword(createAppRequest.UnlockPassword) {
			return nil, fmt.Errorf(
				"incorrect unlock password to create app with superuser permission")
		}
	}

	expiresAt, err := api.parseExpiresAt(createAppRequest.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("invalid expiresAt: %v", err)
	}

	for _, scope := range createAppRequest.Scopes {
		if !slices.Contains(permissions.AllScopes(), scope) {
			return nil, fmt.Errorf("did not recognize requested scope: %s", scope)
		}
	}

	kind := createAppRequest.Kind
	if kind == "" {
		kind = db.AppKindStandard
	}

	var app *db.App
	var pairingSecretKey string

	switch kind {
	case db.AppKindJITHub:
		app, pairingSecretKey, err = api.appsSvc.CreateJITHub(
			createAppRequest.Name,
			createAppRequest.Pubkey,
			createAppRequest.MaxAmountLoki,
			createAppRequest.BudgetRenewal,
			expiresAt,
			createAppRequest.Scopes,
			createAppRequest.Metadata,
			db.JITHubConfig{
				PerWalletMaxMloki: createAppRequest.JITPerWalletMaxMloki,
				MaxExpSecs:        createAppRequest.JITMaxExpSecs,
			},
		)
	case db.AppKindCircleHub:
		app, pairingSecretKey, err = api.appsSvc.CreateCircleHub(
			createAppRequest.Name,
			createAppRequest.Pubkey,
			createAppRequest.MaxAmountLoki,
			createAppRequest.BudgetRenewal,
			expiresAt,
			createAppRequest.Scopes,
			createAppRequest.Metadata,
			apps.CircleIdentityRef{
				ExistingID:     createAppRequest.CircleIdentityId,
				Name:           createAppRequest.CircleIdentityName,
				Policy:         createAppRequest.CirclePolicy,
				ProviderPubkey: createAppRequest.ProviderPubkey,
			},
			db.CircleHubConfig{
				MaxExpSecs:        createAppRequest.CircleMaxExpSecs,
				FeesPpm:           createAppRequest.CircleFeesPpm,
				PerWalletMaxMloki: createAppRequest.CirclePerWalletMaxMloki,
				MinBudgetRenewal:  createAppRequest.CircleMinBudgetRenewal,
			},
		)
	default:
		app, pairingSecretKey, err = api.appsSvc.CreateApp(
			createAppRequest.Name,
			createAppRequest.Pubkey,
			createAppRequest.MaxAmountLoki,
			createAppRequest.BudgetRenewal,
			expiresAt,
			createAppRequest.Scopes,
			kind,
			nil,
			"",
			createAppRequest.Metadata,
		)
	}

	if err != nil {
		return nil, err
	}

	if kind == db.AppKindCircleHub {
		// Resolve the actual identity attached (whether newly created or reused)
		// to find its real policy/pubkey — createAppRequest's circlePolicy/
		// providerPubkey are only populated on the create-new-identity path.
		if cfg, cfgErr := api.appsSvc.GetCircleHubConfig(app.ID); cfgErr == nil && cfg.CircleIdentity.Policy == db.CirclePolicyFollowing {
			if _, err := api.svc.WarmCircleFollowingCache(context.Background(), cfg.CircleIdentity.ProviderPubkey); err != nil {
				logger.Logger.Warn().Err(err).Uint("app_id", app.ID).
					Msg("Failed to warm following-policy social cache at creation; will self-heal on next periodic refresh")
			}
		}
	}

	relayUrls := api.cfg.GetRelayUrls()

	lightningAddress, err := api.cfg.Get("LightningAddress", "")
	if err != nil {
		return nil, err
	}

	responseBody := &CreateAppResponse{}
	responseBody.Id = app.ID
	responseBody.Name = app.Name
	responseBody.Pubkey = app.AppPubkey
	responseBody.PairingSecret = pairingSecretKey
	responseBody.WalletPubkey = *app.WalletPubkey
	responseBody.RelayUrls = relayUrls
	responseBody.Lud16 = lightningAddress

	if createAppRequest.ReturnTo != "" {
		returnToUrl, err := url.Parse(createAppRequest.ReturnTo)
		if err == nil {
			query := returnToUrl.Query()
			for _, relayUrl := range relayUrls {
				query.Add("relay", relayUrl)
			}
			query.Add("pubkey", *app.WalletPubkey)
			if lightningAddress != "" && !app.IsIsolated() {
				query.Add("lud16", lightningAddress)
			}
			returnToUrl.RawQuery = query.Encode()
			responseBody.ReturnTo = returnToUrl.String()
		}
	}

	var lud16 string
	if lightningAddress != "" && !app.IsIsolated() {
		lud16 = fmt.Sprintf("&lud16=%s", lightningAddress)
	}
	responseBody.PairingUri = fmt.Sprintf("nostr+walletconnect://%s?relay=%s&secret=%s%s", *app.WalletPubkey, strings.Join(relayUrls, "&relay="), pairingSecretKey, lud16)

	return responseBody, nil
}

func (api *api) GetSetupStatus(ctx context.Context) (*SetupStatusResponse, error) {
	address, macaroonHex, certHex, err := api.discoverFlndConfig(ctx)
	if err != nil {
		// If we can't find credentials, the node is not "ready" for us
		return &SetupStatusResponse{
			Active: false,
		}, nil
	}

	err = api.verifyFLNDConnection(ctx, address, certHex, macaroonHex)
	active := err == nil

	return &SetupStatusResponse{
		Active: active,
	}, nil
}

func (api *api) UpdateApp(userApp *db.App, updateAppRequest *UpdateAppRequest) error {
	err := api.db.Transaction(func(tx *gorm.DB) error {
		// Initialize name with current app name, update if provided
		name := userApp.Name

		// Update app name if provided and different
		if updateAppRequest.Name != nil {
			name = *updateAppRequest.Name

			if name == "" {
				return fmt.Errorf("won't update an app to have no name")
			}
			if name != userApp.Name {
				// JIT/circle wallet names are system-generated and carry the
				// identity used to resolve a Nostr profile for display —
				// renaming here would silently break that.
				if db.IsNameImmutableKind(userApp.Kind) {
					return constants.ErrKindImmutable
				}
				err := tx.Model(&db.App{}).Where("id", userApp.ID).Update("name", name).Error
				if err != nil {
					return err
				}
			}
		}

		// Update the app metadata if provided
		if updateAppRequest.Metadata != nil {
			var metadataBytes []byte
			var err error
			metadataBytes, err = json.Marshal(*updateAppRequest.Metadata)
			if err != nil {
				logger.Logger.Error().Err(err).Msg("Failed to serialize metadata")
				return err
			}
			err = tx.Model(&db.App{}).Where("id", userApp.ID).Update("metadata", datatypes.JSON(metadataBytes)).Error
			if err != nil {
				return err
			}
		}

		// Handle permissions updates only if any permission-related field is provided
		if updateAppRequest.Scopes != nil || updateAppRequest.MaxAmountLoki != nil ||
			updateAppRequest.BudgetRenewal != nil || updateAppRequest.ExpiresAt != nil || updateAppRequest.UpdateExpiresAt {

			// Privileged kinds are managed by dedicated APIs; generic UpdateApp must not
			// change their scopes after issuance.
			if updateAppRequest.Scopes != nil && db.IsPrivilegedKind(userApp.Kind) {
				return constants.ErrKindImmutable
			}

			// Some privileged kinds also have system-managed budget/expiry
			// (e.g. circle wallets, JIT allocations); a circle hub's own
			// budget/expiry, however, is user-configurable like a regular app.
			if (updateAppRequest.MaxAmountLoki != nil || updateAppRequest.BudgetRenewal != nil ||
				updateAppRequest.ExpiresAt != nil || updateAppRequest.UpdateExpiresAt) &&
				db.IsBudgetImmutableKind(userApp.Kind) {
				return constants.ErrKindImmutable
			}

			// Get current values or use provided ones
			var maxAmount uint64
			var budgetRenewal string
			var expiresAt *time.Time

			// Get existing permissions to use as defaults
			var existingPermissions []db.AppPermission
			if err := tx.Where("app_id = ?", userApp.ID).Find(&existingPermissions).Error; err != nil {
				return err
			}

			// Use existing values as defaults
			if len(existingPermissions) > 0 {
				// Find the pay-capable permission for budget-related fields
				// (constants.PayCapableScopes, not just pay_invoice alone —
				// jit_wallet apps carry jit_claim_funds instead).
				for _, perm := range existingPermissions {
					if slices.Contains(constants.PayCapableScopes, perm.Scope) {
						maxAmount = uint64(perm.MaxAmountLoki) //nolint:gosec // app-internal budget value, always non-negative
						budgetRenewal = perm.BudgetRenewal
						expiresAt = perm.ExpiresAt
						break
					}
				}
			}

			// Override with provided values
			if updateAppRequest.MaxAmountLoki != nil {
				maxAmount = *updateAppRequest.MaxAmountLoki
			}
			if updateAppRequest.BudgetRenewal != nil {
				budgetRenewal = *updateAppRequest.BudgetRenewal
			}
			if updateAppRequest.ExpiresAt != nil {
				parsedExpiresAt, err := api.parseExpiresAt(*updateAppRequest.ExpiresAt)
				if err != nil {
					return fmt.Errorf("invalid expiresAt: %v", err)
				}
				expiresAt = parsedExpiresAt
			}
			if updateAppRequest.ExpiresAt == nil && updateAppRequest.UpdateExpiresAt {
				expiresAt = nil
			}

			// Update existing permissions with new budget and expiry
			err := tx.Model(&db.AppPermission{}).Where("app_id", userApp.ID).Updates(map[string]interface{}{
				"ExpiresAt":     expiresAt,
				"MaxAmountLoki": maxAmount,
				"BudgetRenewal": budgetRenewal,
			}).Error
			if err != nil {
				return err
			}

			// Sync the denormalized App.ExpiresAt used by the cleanup cron index.
			if updateAppRequest.ExpiresAt != nil || updateAppRequest.UpdateExpiresAt {
				if err := tx.Model(&db.App{}).Where("id = ?", userApp.ID).
					Update("expires_at", expiresAt).Error; err != nil {
					return err
				}
			}

			// Handle scope changes only if scopes were provided
			if updateAppRequest.Scopes != nil {

				if len(updateAppRequest.Scopes) == 0 {
					return fmt.Errorf("won't update an app to have no request methods")
				}

				existingScopeMap := make(map[string]bool)
				for _, perm := range existingPermissions {
					existingScopeMap[perm.Scope] = true
				}

				if slices.Contains(updateAppRequest.Scopes, constants.SUPERUSER_SCOPE) && !existingScopeMap[constants.SUPERUSER_SCOPE] {
					return fmt.Errorf("cannot update app to add superuser permission")
				}

				// Add new permissions
				for _, scope := range updateAppRequest.Scopes {
					if !existingScopeMap[scope] {
						perm := db.AppPermission{
							App:           *userApp,
							Scope:         scope,
							ExpiresAt:     expiresAt,
							MaxAmountLoki: int(maxAmount),
							BudgetRenewal: budgetRenewal,
						}
						if err := tx.Create(&perm).Error; err != nil {
							return err
						}
					}
					delete(existingScopeMap, scope)
				}

				// Remove old permissions
				for scope := range existingScopeMap {
					if err := tx.Where("app_id = ? AND scope = ?", userApp.ID, scope).Delete(&db.AppPermission{}).Error; err != nil {
						return err
					}
				}
			}
		}

		// Publish update event
		api.svc.GetEventPublisher().Publish(&events.Event{
			Event: "nwc_app_updated",
			Properties: map[string]interface{}{
				"name": name,
				"id":   userApp.ID,
			},
		})

		// commit transaction
		return nil
	})
	if err != nil {
		return err
	}

	if userApp.Kind == db.AppKindJITHub &&
		(updateAppRequest.JITPerWalletMaxMloki != nil || updateAppRequest.JITMaxExpSecs != nil) {
		if err := api.appsSvc.UpdateJITHubConfig(userApp.ID,
			updateAppRequest.JITPerWalletMaxMloki, updateAppRequest.JITMaxExpSecs); err != nil {
			return err
		}
	}

	if userApp.Kind == db.AppKindCircleHub &&
		(updateAppRequest.CircleMaxExpSecs != nil || updateAppRequest.CircleFeesPpm != nil ||
			updateAppRequest.CirclePerWalletMaxMloki != nil || updateAppRequest.CircleMinBudgetRenewal != nil) {
		if err := api.appsSvc.UpdateCircleHubConfig(userApp.ID,
			updateAppRequest.CircleMaxExpSecs, updateAppRequest.CircleFeesPpm,
			updateAppRequest.CirclePerWalletMaxMloki, updateAppRequest.CircleMinBudgetRenewal); err != nil {
			return err
		}
	}

	return nil
}

func (api *api) DeleteApp(userApp *db.App) error {
	// jit_wallet/circle_wallet hold a shared balance that must be reclaimed
	// back to their hub before the row disappears — apps.DeleteApp has no
	// such logic (it only guards the hub kinds against orphaning children),
	// so a plain delete here would silently destroy any remaining balance.
	// Route through the same reclaim-then-delete path the dedicated
	// DeleteJITWallet/DeleteCircleWalletChild endpoints use, so this generic
	// path (e.g. the app detail page's Disconnect action) is safe too,
	// regardless of claim state.
	if userApp.Kind == db.AppKindJITWallet || userApp.Kind == db.AppKindCircleWallet {
		return service.ReclaimAndDeleteSubWallet(context.Background(), api.db,
			api.svc.GetTransactionsService(), api.svc.GetLNClient(), *userApp)
	}

	return api.appsSvc.DeleteApp(userApp)
}

func (api *api) GetApp(ctx context.Context, dbApp *db.App) *App {

	paySpecificPermission := db.AppPermission{}
	appPermissions := []db.AppPermission{}
	var expiresAt *time.Time
	api.db.Where("app_id = ?", dbApp.ID).Find(&appPermissions)

	requestMethods := []string{}
	for _, appPerm := range appPermissions {
		expiresAt = appPerm.ExpiresAt
		if slices.Contains(constants.PayCapableScopes, appPerm.Scope) {
			// find the pay-capable permission (pay_invoice, or jit_claim_funds
			// for a jit_wallet)
			paySpecificPermission = appPerm
		}
		requestMethods = append(requestMethods, appPerm.Scope)
	}

	// A pay_invoice-scoped app tracks its budget on that row. Some kinds are
	// user-configurable-budget but never granted pay_invoice (e.g. circle_hub,
	// which only issues children — it must never gain pay_invoice itself), so
	// fall back to any permission row: CreateApp/UpdateApp always write the
	// same MaxAmountLoki/BudgetRenewal to every scope row of an app, so any
	// row carries the same value a pay_invoice row would have.
	budgetPermission := paySpecificPermission
	if budgetPermission.Scope == "" && len(appPermissions) > 0 {
		budgetPermission = appPermissions[0]
	}

	// renewsIn := ""
	budgetUsage := uint64(0)
	maxAmount := uint64(budgetPermission.MaxAmountLoki) //nolint:gosec // app-internal budget value, always non-negative
	if dbApp.Kind == db.AppKindCircleHub {
		// A circle_hub never spends directly (only its children do), so the
		// generic outgoing-transaction usage sum below is always ~0 and
		// meaningless here. "Used" for a hub's own budget means "currently
		// committed to live children" — the same metric enforced in
		// nip47/controllers/create_circle_wallet_controller.go.
		commitmentMloki, err := queries.GetCircleCommitmentMloki(api.db, dbApp.ID)
		if err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", dbApp.ID).Msg("Failed to compute circle hub commitment")
		} else {
			budgetUsage = uint64(commitmentMloki) / 1000 //nolint:gosec // sum of non-negative budget commitments
		}
	} else {
		budgetUsage = queries.GetBudgetUsageSat(api.db, &budgetPermission)
	}

	var metadata Metadata
	if dbApp.Metadata != nil {
		jsonErr := json.Unmarshal(dbApp.Metadata, &metadata)
		if jsonErr != nil {
			logger.Logger.Error().Err(jsonErr).
				Uint("app_id", dbApp.ID).
				Msg("Failed to deserialize app metadata")
		}
	}

	walletPubkey := api.keys.GetNostrPublicKey()
	uniqueWalletPubkey := false
	if dbApp.WalletPubkey != nil {
		walletPubkey = *dbApp.WalletPubkey
		uniqueWalletPubkey = true
	}

	response := App{
		ID:                 dbApp.ID,
		Name:               dbApp.Name,
		Description:        dbApp.Description,
		CreatedAt:          dbApp.CreatedAt,
		UpdatedAt:          dbApp.UpdatedAt,
		AppPubkey:          dbApp.AppPubkey,
		ExpiresAt:          expiresAt,
		MaxAmountLoki:      maxAmount,
		Scopes:             requestMethods,
		BudgetUsage:        budgetUsage,
		BudgetRenewal:      budgetPermission.BudgetRenewal,
		Kind:               dbApp.Kind,
		Isolated:           dbApp.IsIsolated(),
		Metadata:           metadata,
		WalletPubkey:       walletPubkey,
		UniqueWalletPubkey: uniqueWalletPubkey,
		LastUsedAt:         dbApp.LastUsedAt,
	}

	if dbApp.IsIsolated() {
		response.Balance = queries.GetIsolatedBalance(api.db, dbApp.ID)
	}

	if dbApp.Kind == db.AppKindJITHub {
		if cfg, cfgErr := api.appsSvc.GetJITHubConfig(dbApp.ID); cfgErr == nil {
			response.JITPerWalletMaxMloki = &cfg.PerWalletMaxMloki
			response.JITMaxExpSecs = &cfg.MaxExpSecs
		}
	}

	if dbApp.Kind == db.AppKindCircleHub {
		response.CircleIdentity = api.circleIdentitySummaryForApp(ctx, dbApp.ID, true)
		if cfg, cfgErr := api.appsSvc.GetCircleHubConfig(dbApp.ID); cfgErr == nil {
			response.CircleMaxExpSecs = &cfg.MaxExpSecs
			response.CircleFeesPpm = &cfg.FeesPpm
			response.CirclePerWalletMaxMloki = &cfg.PerWalletMaxMloki
			response.CircleMinBudgetRenewal = &cfg.MinBudgetRenewal
		}
	}

	return &response
}

// circleIdentitySummaryForApp resolves a circle_hub app's attached identity
// and its policy-specific counts for inline enrichment. allowBlockingFetch
// mirrors buildCircleIdentityCounts: ListApps passes false (PeekContactCount,
// never fetches — a following-policy identity with no cached count yet
// simply omits FollowingCount, which the frontend renders as still-loading),
// while GetApp passes true since it's a single deliberate target.
func (api *api) circleIdentitySummaryForApp(ctx context.Context, appID uint, allowBlockingFetch bool) *CircleIdentitySummaryWithCounts {
	cfg, err := api.appsSvc.GetCircleHubConfig(appID)
	if err != nil {
		return nil
	}
	summary, err := api.buildCircleIdentityCounts(ctx, &cfg.CircleIdentity, allowBlockingFetch)
	if err != nil {
		return nil
	}
	return summary
}

// buildCircleIdentityCounts computes policy-specific counts for identity.
// allowBlockingFetch controls whether a following-policy count may cold-fetch
// via a live relay query (GetCircleIdentity, a single deliberate target) or
// must stay non-blocking (circleIdentitySummaryForApp, iterating many rows).
func (api *api) buildCircleIdentityCounts(ctx context.Context, identity *db.CircleIdentity, allowBlockingFetch bool) (*CircleIdentitySummaryWithCounts, error) {
	summary := &CircleIdentitySummaryWithCounts{
		CircleIdentitySummary: CircleIdentitySummary{
			ID:             identity.ID,
			Name:           identity.Name,
			Policy:         identity.Policy,
			ProviderPubkey: identity.ProviderPubkey,
		},
	}
	switch identity.Policy {
	case db.CirclePolicyFollowing:
		if allowBlockingFetch {
			count, err := api.svc.ContactCount(ctx, identity.ProviderPubkey)
			if err != nil {
				return nil, err
			}
			summary.FollowingCount = &count
		} else if count, ok := api.svc.PeekContactCount(identity.ProviderPubkey); ok {
			summary.FollowingCount = &count
		}
		if syncedAt, ok := api.svc.PeekContactSyncedAt(identity.ProviderPubkey); ok {
			summary.PolicySyncedAt = &syncedAt
		}
	case db.CirclePolicyAllowlist:
		var count int64
		if err := api.db.Model(&db.CircleIdentityAllowedPubkey{}).
			Where("circle_identity_id = ?", identity.ID).Count(&count).Error; err != nil {
			return nil, err
		}
		summary.AllowlistCount = int(count)
		// Model-level Find (not a raw MAX(created_at) scan) so gorm applies its
		// usual string->time.Time conversion for the sqlite driver.
		var latest db.CircleIdentityAllowedPubkey
		err := api.db.Where("circle_identity_id = ?", identity.ID).
			Order("created_at DESC").First(&latest).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if err == nil {
			summary.PolicySyncedAt = &latest.CreatedAt
		}
	}
	return summary, nil
}

// ListCircleIdentities returns every CircleIdentity, for the circle-creation-time
// picker and the manage-identities list — both need UsedByCount to show which
// identities are safe to delete, so it's computed here via one grouped query
// rather than a per-identity round trip.
func (api *api) ListCircleIdentities() ([]CircleIdentitySummary, error) {
	identities, err := api.appsSvc.ListCircleIdentities()
	if err != nil {
		return nil, err
	}

	var counts []struct {
		CircleIdentityID uint
		Count            int
	}
	if err := api.db.Model(&db.CircleHubConfig{}).
		Select("circle_identity_id, count(*) as count").
		Group("circle_identity_id").
		Find(&counts).Error; err != nil {
		return nil, err
	}
	usedByCount := make(map[uint]int, len(counts))
	for _, c := range counts {
		usedByCount[c.CircleIdentityID] = c.Count
	}

	summaries := make([]CircleIdentitySummary, 0, len(identities))
	for _, identity := range identities {
		summaries = append(summaries, CircleIdentitySummary{
			ID:             identity.ID,
			Name:           identity.Name,
			Policy:         identity.Policy,
			ProviderPubkey: identity.ProviderPubkey,
			UsedByCount:    usedByCount[identity.ID],
		})
	}
	return summaries, nil
}

// DeleteCircleIdentity removes a standalone CircleIdentity — refuses
// (ErrInvalidParams) if any circle_hub app still references it.
func (api *api) DeleteCircleIdentity(id uint) error {
	return api.appsSvc.DeleteCircleIdentity(id)
}

// GetCircleIdentity returns full identity detail: policy-specific counts (a
// following-policy count may cold-fetch once, since this is a single
// deliberate target, not a list), the full allowlist pubkeys for allowlist
// policy, and how many circle_hub apps currently reference this identity.
func (api *api) GetCircleIdentity(ctx context.Context, id uint) (*CircleIdentityResponse, error) {
	identity, err := api.appsSvc.GetCircleIdentity(id)
	if err != nil {
		return nil, err
	}
	summary, err := api.buildCircleIdentityCounts(ctx, identity, true)
	if err != nil {
		return nil, err
	}
	resp := &CircleIdentityResponse{CircleIdentitySummaryWithCounts: *summary}

	if identity.Policy == db.CirclePolicyAllowlist {
		var entries []db.CircleIdentityAllowedPubkey
		if err := api.db.Where("circle_identity_id = ?", identity.ID).Order("pubkey asc").Find(&entries).Error; err != nil {
			return nil, err
		}
		for _, e := range entries {
			resp.AllowlistPubkeys = append(resp.AllowlistPubkeys, e.Pubkey)
		}
	}

	var usedByCount int64
	if err := api.db.Model(&db.CircleHubConfig{}).
		Where("circle_identity_id = ?", identity.ID).Count(&usedByCount).Error; err != nil {
		return nil, err
	}
	resp.UsedByCount = int(usedByCount)

	return resp, nil
}

func (api *api) ListApps(limit uint64, offset uint64, filters ListAppsFilters, orderBy string) (*ListAppsResponse, error) {
	// TODO: join dbApps and permissions
	dbApps := []db.App{}
	query := api.db

	if filters.Name != "" {
		// searching for "Damus" will return "Damus" and "Damus (1)"
		// Use case-insensitive search for both SQLite and PostgreSQL
		if api.db.Name() == "postgres" {
			query = query.Where("name ILIKE ?", filters.Name+"%")
		} else {
			query = query.Where("name LIKE ?", filters.Name+"%")
		}
	}

	if filters.AppStoreAppId != "" {
		query = query.Where(datatypes.JSONQuery("metadata").Equals(filters.AppStoreAppId, "app_store_app_id"))
	}

	if filters.Unused {
		// find unused non-subwallet apps not used in the past 60 days
		query = query.Where("last_used_at IS NULL OR last_used_at < ?", time.Now().Add(-60*24*time.Hour))
	}

	if filters.SubWallets != nil && !*filters.SubWallets {
		// exclude subwallets :scream:
		if api.db.Name() == "sqlite" {
			query = query.Where("metadata is NULL OR JSON_EXTRACT(metadata, '$.app_store_app_id') IS NULL OR JSON_EXTRACT(metadata, '$.app_store_app_id') != ?", constants.SUBWALLET_APPSTORE_APP_ID)
		} else {
			query = query.Where("metadata IS NULL OR metadata->>'app_store_app_id' IS NULL OR metadata->>'app_store_app_id' != ?", constants.SUBWALLET_APPSTORE_APP_ID)
		}
	}

	// jit_wallet children are ephemeral, spend-only wallets issued on demand by
	// a JIT Hub — they're reachable via the hub's allocations list/reveal flow
	// and their own AppDetails page, but must never surface in general app
	// listings (Connections page, command palette, unused-apps nudges, etc.).
	query = query.Where("kind != ?", db.AppKindJITWallet)

	if orderBy == "" {
		orderBy = "last_used_at"
	}
	if orderBy == "last_used_at" {
		// when ordering by last used at, apps with last_used_at is NULL should be ordered last
		orderBy = "last_used_at IS NULL, " + orderBy
	}

	query = query.Order(orderBy + " DESC")

	if limit == 0 {
		limit = 100
	}
	var totalCount int64
	result := query.Model(&db.App{}).Count(&totalCount)
	if result.Error != nil {
		logger.Logger.Error().Err(result.Error).Msg("Failed to count DB apps")
		return nil, result.Error
	}
	query = query.Offset(utils.ClampUint64ToInt(offset)).Limit(utils.ClampUint64ToInt(limit))

	err := query.Find(&dbApps).Error

	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list apps")
		return nil, err
	}

	appIds := []uint64{}
	for _, app := range dbApps {
		appIds = append(appIds, uint64(app.ID))
	}

	appPermissions := []db.AppPermission{}
	err = api.db.Where("app_id IN ?", appIds).Find(&appPermissions).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list app permissions")
		return nil, err
	}

	permissionsMap := make(map[uint][]db.AppPermission)
	for _, perm := range appPermissions {
		permissionsMap[perm.AppId] = append(permissionsMap[perm.AppId], perm)
	}

	apiApps := []App{}
	for _, dbApp := range dbApps {
		walletPubkey := api.keys.GetNostrPublicKey()
		uniqueWalletPubkey := false
		if dbApp.WalletPubkey != nil {
			walletPubkey = *dbApp.WalletPubkey
			uniqueWalletPubkey = true
		}
		apiApp := App{
			ID:                 dbApp.ID,
			Name:               dbApp.Name,
			Description:        dbApp.Description,
			CreatedAt:          dbApp.CreatedAt,
			UpdatedAt:          dbApp.UpdatedAt,
			AppPubkey:          dbApp.AppPubkey,
			Kind:               dbApp.Kind,
			Isolated:           dbApp.IsIsolated(),
			WalletPubkey:       walletPubkey,
			UniqueWalletPubkey: uniqueWalletPubkey,
			LastUsedAt:         dbApp.LastUsedAt,
		}

		if dbApp.IsIsolated() {
			apiApp.Balance = queries.GetIsolatedBalance(api.db, dbApp.ID)
		}

		if dbApp.Kind == db.AppKindCircleHub {
			apiApp.CircleIdentity = api.circleIdentitySummaryForApp(context.Background(), dbApp.ID, false)
		}

		var budgetPermission *db.AppPermission
		for i := range permissionsMap[dbApp.ID] {
			appPermission := permissionsMap[dbApp.ID][i]
			apiApp.Scopes = append(apiApp.Scopes, appPermission.Scope)
			apiApp.ExpiresAt = appPermission.ExpiresAt
			if slices.Contains(constants.PayCapableScopes, appPermission.Scope) {
				budgetPermission = &appPermission
			}
		}
		// See GetApp for why non-pay_invoice kinds (e.g. circle_hub) fall back
		// to any permission row for their budget fields.
		if budgetPermission == nil && len(permissionsMap[dbApp.ID]) > 0 {
			budgetPermission = &permissionsMap[dbApp.ID][0]
		}
		if budgetPermission != nil {
			apiApp.BudgetRenewal = budgetPermission.BudgetRenewal
			apiApp.MaxAmountLoki = uint64(budgetPermission.MaxAmountLoki) //nolint:gosec // app-internal budget value, always non-negative
			if dbApp.Kind == db.AppKindCircleHub {
				// See GetApp: "used" for a circle_hub means live commitment
				// to children, not its own (always ~0) outgoing spend.
				if commitmentMloki, err := queries.GetCircleCommitmentMloki(api.db, dbApp.ID); err != nil {
					logger.Logger.Error().Err(err).Uint("app_id", dbApp.ID).Msg("Failed to compute circle hub commitment")
				} else {
					apiApp.BudgetUsage = uint64(commitmentMloki) / 1000 //nolint:gosec // sum of non-negative budget commitments
				}
			} else {
				apiApp.BudgetUsage = queries.GetBudgetUsageSat(api.db, budgetPermission)
			}
		}

		var metadata Metadata
		if dbApp.Metadata != nil {
			jsonErr := json.Unmarshal(dbApp.Metadata, &metadata)
			if jsonErr != nil {
				logger.Logger.Error().Err(jsonErr).
					Uint("app_id", dbApp.ID).
					Msg("Failed to deserialize app metadata")
			}
			apiApp.Metadata = metadata
		}

		apiApps = append(apiApps, apiApp)
	}
	return &ListAppsResponse{
		Apps:       apiApps,
		TotalCount: uint64(totalCount), //nolint:gosec // DB row count, always non-negative
	}, nil
}

func (api *api) ListChannels(ctx context.Context) ([]Channel, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	channels, err := api.svc.GetLNClient().ListChannels(ctx)
	if err != nil {
		return nil, err
	}

	apiChannels := []Channel{}
	for _, channel := range channels {
		status := "offline"
		if channel.Active {
			status = "online"
		} else if channel.Confirmations != nil && channel.ConfirmationsRequired != nil && *channel.ConfirmationsRequired > *channel.Confirmations {
			status = "opening"
		}

		apiChannels = append(apiChannels, Channel{
			LocalBalance:                             channel.LocalBalance,
			LocalSpendableBalance:                    channel.LocalSpendableBalance,
			RemoteBalance:                            channel.RemoteBalance,
			Id:                                       channel.Id,
			RemotePubkey:                             channel.RemotePubkey,
			FundingTxId:                              channel.FundingTxId,
			FundingTxVout:                            channel.FundingTxVout,
			Active:                                   channel.Active,
			Public:                                   channel.Public,
			InternalChannel:                          channel.InternalChannel,
			Confirmations:                            channel.Confirmations,
			ConfirmationsRequired:                    channel.ConfirmationsRequired,
			ForwardingFeeBaseMloki:                   channel.ForwardingFeeBaseMloki,
			ForwardingFeeProportionalMillionths:      channel.ForwardingFeeProportionalMillionths,
			UnspendablePunishmentReserve:             channel.UnspendablePunishmentReserve,
			CounterpartyUnspendablePunishmentReserve: channel.CounterpartyUnspendablePunishmentReserve,
			Error:                                    channel.Error,
			IsOutbound:                               channel.IsOutbound,
			Status:                                   status,
		})
	}

	slices.SortFunc(apiChannels, func(a, b Channel) int {
		// sort by channel size first
		aSize := a.LocalBalance + a.RemoteBalance
		bSize := b.LocalBalance + b.RemoteBalance
		if aSize != bSize {
			return int(bSize - aSize)
		}

		// then by local balance in the channel
		if a.LocalBalance != b.LocalBalance {
			return int(b.LocalBalance - a.LocalBalance)
		}

		// finally sort by channel ID to prevent sort randomly changing
		return strings.Compare(b.Id, a.Id)
	})

	return apiChannels, nil
}

func (api *api) ResetRouter(key string) error {
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}
	err := api.svc.GetLNClient().ResetRouter(key)
	if err != nil {
		return err
	}

	// Because the above method has to stop the node to reset the router,
	// We also need to stop the lnclient and ask the user to start it again
	return api.Stop()
}

func (api *api) ChangeUnlockPassword(changeUnlockPasswordRequest *ChangeUnlockPasswordRequest) error {
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}

	autoUnlockPassword, err := api.cfg.Get("AutoUnlockPassword", "")
	if err != nil {
		return err
	}
	if autoUnlockPassword != "" {
		return errors.New("please disable auto-unlock before using this feature")
	}

	err = api.cfg.ChangeUnlockPassword(changeUnlockPasswordRequest.CurrentUnlockPassword, changeUnlockPasswordRequest.NewUnlockPassword)

	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to change unlock password")
		return err
	}

	// Because all the encrypted fields have changed
	// we also need to stop the lnclient and ask the user to start it again
	return api.Stop()
}

func (api *api) SetAutoUnlockPassword(unlockPassword string) error {
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}

	err := api.cfg.SetAutoUnlockPassword(unlockPassword)

	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to set auto unlock password")
		return err
	}

	return nil
}

func (api *api) Stop() error {
	if !startMutex.TryLock() {
		// do not allow to stop twice in case this is somehow called twice
		return errors.New("app is busy")
	}
	defer startMutex.Unlock()

	logger.Logger.Info().Msg("Running Stop command")
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}

	// stop the lnclient, nostr relay etc.
	// The user will be forced to re-enter their unlock password to restart the node
	ctx, cancel := context.WithTimeout(context.Background(), constants.APP_SHUTDOWN_TIMEOUT)
	defer cancel()
	api.svc.StopApp(ctx)

	return nil
}

func (api *api) GetNodeConnectionInfo(ctx context.Context) (*lnclient.NodeConnectionInfo, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	return api.svc.GetLNClient().GetNodeConnectionInfo(ctx)
}

func (api *api) RefundSwap(refundSwapRequest *RefundSwapRequest) error {
	if api.svc.GetSwapsService() == nil {
		return errors.New("SwapsService not started")
	}
	return api.svc.GetSwapsService().RefundSwap(refundSwapRequest.SwapId, refundSwapRequest.Address, false)
}

func (api *api) GetAutoSwapConfig() (*GetAutoSwapConfigResponse, error) {
	swapOutBalanceThresholdStr, _ := api.cfg.Get(config.AutoSwapBalanceThresholdKey, "")
	swapOutAmountStr, _ := api.cfg.Get(config.AutoSwapAmountKey, "")
	swapOutDestination, _ := api.cfg.Get(config.AutoSwapDestinationKey, "")

	swapOutEnabled := swapOutBalanceThresholdStr != "" && swapOutAmountStr != ""
	var swapOutBalanceThreshold, swapOutAmount uint64
	if swapOutEnabled {
		var err error
		if swapOutBalanceThreshold, err = strconv.ParseUint(swapOutBalanceThresholdStr, 10, 64); err != nil {
			return nil, fmt.Errorf("invalid autoswap out balance threshold: %w", err)
		}
		if swapOutAmount, err = strconv.ParseUint(swapOutAmountStr, 10, 64); err != nil {
			return nil, fmt.Errorf("invalid autoswap out amount: %w", err)
		}
	}

	return &GetAutoSwapConfigResponse{
		Type:             constants.SWAP_TYPE_OUT,
		Enabled:          swapOutEnabled,
		BalanceThreshold: swapOutBalanceThreshold,
		SwapAmount:       swapOutAmount,
		Destination:      swapOutDestination,
	}, nil
}

func (api *api) LookupSwap(swapId string) (*LookupSwapResponse, error) {
	if api.svc.GetSwapsService() == nil {
		return nil, errors.New("SwapsService not started")
	}
	dbSwap, err := api.svc.GetSwapsService().GetSwap(swapId)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to fetch swap info")
		return nil, err
	}

	return toApiSwap(dbSwap), nil
}

func (api *api) ListSwaps() (*ListSwapsResponse, error) {
	if api.svc.GetSwapsService() == nil {
		return nil, errors.New("SwapsService not started")
	}
	swaps, err := api.svc.GetSwapsService().ListSwaps()
	if err != nil {
		return nil, err
	}

	apiSwaps := []Swap{}
	for _, swap := range swaps {
		apiSwaps = append(apiSwaps, *toApiSwap(&swap))
	}

	return &ListSwapsResponse{
		Swaps: apiSwaps,
	}, nil
}

func toApiSwap(swap *swaps.Swap) *Swap {
	return &Swap{
		Id:                 swap.SwapId,
		Type:               swap.Type,
		State:              swap.State,
		Invoice:            swap.Invoice,
		SendAmount:         swap.SendAmount,
		ReceiveAmount:      swap.ReceiveAmount,
		PaymentHash:        swap.PaymentHash,
		DestinationAddress: swap.DestinationAddress,
		RefundAddress:      swap.RefundAddress,
		LockupAddress:      swap.LockupAddress,
		LockupTxId:         swap.LockupTxId,
		ClaimTxId:          swap.ClaimTxId,
		AutoSwap:           swap.AutoSwap,
		BoltzPubkey:        swap.BoltzPubkey,
		CreatedAt:          swap.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          swap.UpdatedAt.Format(time.RFC3339),
		UsedXpub:           swap.UsedXpub,
	}
}

func (api *api) GetSwapInInfo() (*SwapInfoResponse, error) {
	if api.svc.GetSwapsService() == nil {
		return nil, errors.New("SwapsService not started")
	}
	swapInInfo, err := api.svc.GetSwapsService().GetSwapInInfo()
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to calculate fee info")
		return nil, err
	}

	return &SwapInfoResponse{
		LokiServiceFee:  swapInInfo.LokiServiceFee,
		BoltzServiceFee: swapInInfo.BoltzServiceFee,
		BoltzNetworkFee: swapInInfo.BoltzNetworkFee,
		MinAmount:       swapInInfo.MinAmount,
		MaxAmount:       swapInInfo.MaxAmount,
	}, nil
}

func (api *api) GetSwapOutInfo() (*SwapInfoResponse, error) {
	if api.svc.GetSwapsService() == nil {
		return nil, errors.New("SwapsService not started")
	}
	swapOutInfo, err := api.svc.GetSwapsService().GetSwapOutInfo()
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to calculate fee info")
		return nil, err
	}

	return &SwapInfoResponse{
		LokiServiceFee:  swapOutInfo.LokiServiceFee,
		BoltzServiceFee: swapOutInfo.BoltzServiceFee,
		BoltzNetworkFee: swapOutInfo.BoltzNetworkFee,
		MinAmount:       swapOutInfo.MinAmount,
		MaxAmount:       swapOutInfo.MaxAmount,
	}, nil
}

func (api *api) InitiateSwapOut(ctx context.Context, initiateSwapOutRequest *InitiateSwapRequest) (*swaps.SwapResponse, error) {
	lnClient := api.svc.GetLNClient()
	if lnClient == nil {
		return nil, errors.New("LNClient not started")
	}

	if api.svc.GetSwapsService() == nil {
		return nil, errors.New("SwapsService not started")
	}

	amount := initiateSwapOutRequest.SwapAmount
	destination := initiateSwapOutRequest.Destination

	if amount == 0 {
		return nil, errors.New("invalid swap amount")
	}

	swapOutResponse, err := api.svc.GetSwapsService().SwapOut(amount, destination, false, false)
	if err != nil {
		logger.Logger.Error().Err(err).
			Uint64("amount", amount).
			Str("destination", destination).
			Msg("Failed to initiate swap out")
		return nil, err
	}

	return swapOutResponse, nil
}

func (api *api) InitiateSwapIn(ctx context.Context, initiateSwapInRequest *InitiateSwapRequest) (*swaps.SwapResponse, error) {
	lnClient := api.svc.GetLNClient()
	if lnClient == nil {
		return nil, errors.New("LNClient not started")
	}

	if api.svc.GetSwapsService() == nil {
		return nil, errors.New("SwapsService not started")
	}

	amount := initiateSwapInRequest.SwapAmount

	if amount == 0 {
		return nil, errors.New("invalid swap amount")
	}

	swapInResponse, err := api.svc.GetSwapsService().SwapIn(amount, false)
	if err != nil {
		logger.Logger.Error().Err(err).
			Uint64("amount", amount).
			Msg("Failed to initiate swap in")
		return nil, err
	}

	return swapInResponse, nil
}

func (api *api) EnableAutoSwapOut(ctx context.Context, enableAutoSwapsRequest *EnableAutoSwapRequest) error {
	err := api.cfg.SetUpdate(config.AutoSwapBalanceThresholdKey, strconv.FormatUint(enableAutoSwapsRequest.BalanceThreshold, 10), "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save autoswap balance threshold to config")
		return err
	}

	err = api.cfg.SetUpdate(config.AutoSwapAmountKey, strconv.FormatUint(enableAutoSwapsRequest.SwapAmount, 10), "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save autoswap amount to config")
		return err
	}

	err = api.cfg.SetUpdate(config.AutoSwapDestinationKey, enableAutoSwapsRequest.Destination, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save autoswap destination to config")
		return err
	}

	if api.svc.GetSwapsService() == nil {
		return errors.New("SwapsService not started")
	}
	return api.svc.GetSwapsService().EnableAutoSwapOut()
}

func (api *api) DisableAutoSwap() error {
	keys := []string{config.AutoSwapBalanceThresholdKey, config.AutoSwapAmountKey, config.AutoSwapDestinationKey}

	for _, key := range keys {
		if err := api.cfg.SetUpdate(key, "", ""); err != nil {
			logger.Logger.Error().Err(err).Str("key", key).Msg("Failed to remove autoswap config")
			return err
		}
	}

	if api.svc.GetSwapsService() != nil {
		api.svc.GetSwapsService().StopAutoSwapOut()
	}
	return nil
}

func (api *api) GetSwapMnemonic() string {
	return api.keys.GetSwapMnemonic()
}

func (api *api) GetNodeStatus(ctx context.Context) (*lnclient.NodeStatus, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	return api.svc.GetLNClient().GetNodeStatus(ctx)
}

func (api *api) ListPeers(ctx context.Context) ([]lnclient.PeerDetails, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	return api.svc.GetLNClient().ListPeers(ctx)
}

func (api *api) ConnectPeer(ctx context.Context, connectPeerRequest *ConnectPeerRequest) error {
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}
	return api.svc.GetLNClient().ConnectPeer(ctx, connectPeerRequest)
}

func (api *api) OpenChannel(ctx context.Context, openChannelRequest *OpenChannelRequest) (*OpenChannelResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	return api.svc.GetLNClient().OpenChannel(ctx, openChannelRequest)
}

func (api *api) DisconnectPeer(ctx context.Context, peerId string) error {
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}
	logger.Logger.Info().Str("peer_id", peerId).Msg("Disconnecting peer")
	return api.svc.GetLNClient().DisconnectPeer(ctx, peerId)
}

func (api *api) CloseChannel(ctx context.Context, peerId, channelId string, force bool) (*CloseChannelResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	logger.Logger.Info().
		Str("peer_id", peerId).
		Str("channel_id", channelId).
		Bool("force", force).
		Msg("Closing channel")
	return api.svc.GetLNClient().CloseChannel(ctx, &lnclient.CloseChannelRequest{
		NodeId:    peerId,
		ChannelId: channelId,
		Force:     force,
	})
}

func (api *api) UpdateChannel(ctx context.Context, updateChannelRequest *UpdateChannelRequest) error {
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}
	logger.Logger.Info().Interface("request", updateChannelRequest).Msg("updating channel")
	return api.svc.GetLNClient().UpdateChannel(ctx, updateChannelRequest)
}

func (api *api) GetNewOnchainAddress(ctx context.Context) (string, error) {
	if api.svc.GetLNClient() == nil {
		return "", errors.New("LNClient not started")
	}
	address, err := api.svc.GetLNClient().GetNewOnchainAddress(ctx)
	if err != nil {
		return "", err
	}

	err = api.cfg.SetUpdate(config.OnchainAddressKey, address, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save new onchain address to config")
	}

	return address, nil
}

func (api *api) GetUnusedOnchainAddress(ctx context.Context) (string, error) {
	if api.svc.GetLNClient() == nil {
		return "", errors.New("LNClient not started")
	}

	currentAddress, err := api.cfg.Get(config.OnchainAddressKey, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get current address from config")
		return "", err
	}

	if currentAddress != "" {
		// check if address has any transactions
		response, err := api.RequestMempoolApi(ctx, "/address/"+currentAddress+"/txs")
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to get current address transactions")
			return currentAddress, nil
		}

		transactions, ok := response.([]interface{})
		if !ok {
			logger.Logger.Error().Interface("response", response).Msg("Failed to cast mempool address txs response")
			return currentAddress, nil
		}

		if len(transactions) == 0 {
			// address has not been used yet
			return currentAddress, nil
		}
	}

	newAddress, err := api.GetNewOnchainAddress(ctx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to retrieve new onchain address")
		return "", err
	}
	return newAddress, nil
}

func (api *api) SignMessage(ctx context.Context, message string) (*SignMessageResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	signature, err := api.svc.GetLNClient().SignMessage(ctx, message)
	if err != nil {
		return nil, err
	}
	return &SignMessageResponse{
		Message:   message,
		Signature: signature,
	}, nil
}

func (api *api) RedeemOnchainFunds(ctx context.Context, toAddress string, amount uint64, feeRate *uint64, sendAll bool) (*RedeemOnchainFundsResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	txId, err := api.svc.GetLNClient().RedeemOnchainFunds(ctx, toAddress, amount, feeRate, sendAll)
	if err != nil {
		return nil, err
	}
	return &RedeemOnchainFundsResponse{
		TxId: txId,
	}, nil
}

func (api *api) GetBalances(ctx context.Context) (*BalancesResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	balances, err := api.svc.GetLNClient().GetBalances(ctx, false)
	if err != nil {
		return nil, err
	}
	return balances, nil
}

// TODO: remove dependency on this endpoint
func (api *api) RequestMempoolApi(ctx context.Context, endpoint string) (interface{}, error) {
	url := api.cfg.GetMempoolApi() + "/api" + endpoint

	client := http.Client{
		Timeout: time.Second * 10,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.Logger.Error().Err(err).Str("url", url).Msg("Failed to create http request")
		return nil, err
	}

	res, err := client.Do(req)
	if err != nil {
		logger.Logger.Error().Err(err).Str("url", url).Msg("Failed to send request")
		return nil, err
	}

	defer res.Body.Close()

	body, readErr := io.ReadAll(res.Body)
	if readErr != nil {
		logger.Logger.Error().Err(readErr).Str("url", url).Msg("Failed to read response body")
		return nil, errors.New("failed to read response body")
	}

	var jsonContent interface{}
	jsonErr := json.Unmarshal(body, &jsonContent)
	if jsonErr != nil {
		logger.Logger.Error().Err(jsonErr).Str("url", url).Msg("Failed to deserialize json")
		return nil, fmt.Errorf("failed to deserialize json %s", url)
	}
	return jsonContent, nil
}

func (api *api) GetInfo(ctx context.Context) (*InfoResponse, error) {
	info := InfoResponse{}
	backendType, _ := api.cfg.Get("LNBackendType", "")
	autoUnlockPassword, _ := api.cfg.Get("AutoUnlockPassword", "")
	info.SetupCompleted = api.cfg.SetupCompleted()
	info.Currency = api.cfg.GetCurrency()
	info.FlokicoinDisplayFormat = api.cfg.GetFlokicoinDisplayFormat()
	info.StartupState = api.svc.GetStartupState()
	if api.startupError != nil {
		info.StartupError = api.startupError.Error()
		info.StartupErrorTime = api.startupErrorTime
	}
	info.BackendType = backendType
	info.Version = version.Tag
	info.AutoUnlockPasswordEnabled = autoUnlockPassword != ""
	info.AutoUnlockPasswordSupported = true
	info.Relays = []InfoResponseRelay{}
	for _, relayStatus := range api.svc.GetRelayStatuses() {
		info.Relays = append(info.Relays, InfoResponseRelay{
			Url:    relayStatus.Url,
			Online: relayStatus.Online,
		})
	}
	info.MessageboardNwcUrl = api.cfg.GetMessageboardNwcUrl()

	lnClient := api.svc.GetLNClient()
	var nodeInfo *lnclient.NodeInfo
	if lnClient != nil {
		var err error
		nodeInfo, err = lnClient.GetInfo(ctx)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to get nodeInfo")
			return nil, err
		}
		info.Network = nodeInfo.Network
		info.Running = true
	}

	info.NodeAlias, _ = api.cfg.Get("NodeAlias", "")
	if info.NodeAlias == "" && nodeInfo != nil {
		info.NodeAlias = nodeInfo.Alias
	}
	info.LokihubServicesURL = api.cfg.GetLokihubServicesURL()
	info.SwapServiceUrl = api.cfg.GetSwapServiceURL()
	info.Relay = api.cfg.GetRelay()
	info.GeneralRelay = api.cfg.GetGeneralRelay()
	info.SearchRelay = api.cfg.GetSearchRelay()

	// Populate selected LSPs
	if lm := api.svc.GetLiquidityManager(); lm != nil {
		selected, err := lm.GetSelectedLSPs()
		if err == nil {
			info.LSPs = make([]LSPInfo, len(selected))
			for i, lsp := range selected {
				info.LSPs[i] = LSPInfo{
					Name:    lsp.Name,
					Pubkey:  lsp.Pubkey,
					Host:    lsp.Host,
					Website: lsp.Website,
					Active:  lsp.Active,
				}
			}
		}
	}

	info.MempoolUrl = api.cfg.GetMempoolApi()
	info.EnableSwap = api.cfg.EnableSwap()
	info.EnableMessageboardNwc = api.cfg.EnableMessageboardNwc()
	info.WorkDir = api.cfg.GetDefaultWorkDir()
	info.EnablePolling = constants.DEFAULT_ENABLE_POLLING

	return &info, nil
}

func (api *api) SetCurrency(currency string) error {
	if currency == "" {
		return fmt.Errorf("currency value cannot be empty")
	}

	err := api.cfg.SetCurrency(currency)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update currency")
		return err
	}

	return nil
}

func (api *api) SetFlokicoinDisplayFormat(format string) error {
	if format != constants.FLOKICOIN_DISPLAY_FORMAT_LOKI && format != constants.FLOKICOIN_DISPLAY_FORMAT_FLC && format != constants.FLOKICOIN_DISPLAY_FORMAT_AUTO {
		return fmt.Errorf("flokicoin display format must be '%s', '%s' or '%s'", constants.FLOKICOIN_DISPLAY_FORMAT_LOKI, constants.FLOKICOIN_DISPLAY_FORMAT_FLC, constants.FLOKICOIN_DISPLAY_FORMAT_AUTO)
	}

	err := api.cfg.SetFlokicoinDisplayFormat(format)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update flokicoin display format")
		return err
	}

	return nil
}

func (api *api) UpdateSettings(updateSettingsRequest *UpdateSettingsRequest) error {
	if updateSettingsRequest.Currency != "" {
		err := api.SetCurrency(updateSettingsRequest.Currency)
		if err != nil {
			return fmt.Errorf("failed to set currency: %w", err)
		}
	}

	if updateSettingsRequest.FlokicoinDisplayFormat != "" {
		err := api.SetFlokicoinDisplayFormat(updateSettingsRequest.FlokicoinDisplayFormat)
		if err != nil {
			return fmt.Errorf("failed to set flokicoin display format: %w", err)
		}
	}

	if updateSettingsRequest.EnableSwap != nil {
		err := api.cfg.SetEnableSwap(*updateSettingsRequest.EnableSwap)
		if err != nil {
			return fmt.Errorf("failed to set EnableSwap: %w", err)
		}
		// Reload swap service if it was enabled or disabled (to stop/start connection)
		// Currently Reload() just reconnects. If disabled, we might want to close connection?
		// For now, Reload() is fine as it will re-read config (although Connect logic in NewSwapsService doesn't check EnableSwap,
		// maybe it should? But user only asked for "disable as feature").
		// If "disable as feature" means backend endpoints reject requests (which we implemented),
		// then Reload() might not be strictly necessary for *disabling*, but good for *enabling* if we want to ensure connection is fresh.
		// However, if we change URL, we definitely need Reload().
		// Let's call Reload() if URL changed OR if EnableSwap changed (just to be safe/consistent).
		if *updateSettingsRequest.EnableSwap {
			api.svc.InitSwapsService()
		}
		if api.svc.GetSwapsService() != nil {
			api.svc.GetSwapsService().Reload()
		}
	}

	if updateSettingsRequest.EnableMessageboardNwc != nil {
		err := api.cfg.SetEnableMessageboardNwc(*updateSettingsRequest.EnableMessageboardNwc)
		if err != nil {
			return fmt.Errorf("failed to set EnableMessageboardNwc: %w", err)
		}
	}

	if updateSettingsRequest.SwapServiceUrl != "" {
		if err := utils.ValidateHTTPURL(updateSettingsRequest.SwapServiceUrl); err != nil {
			return fmt.Errorf("invalid Swap Service URL: %w", err)
		}
		err := api.cfg.SetSwapServiceURL(updateSettingsRequest.SwapServiceUrl)
		if err != nil {
			return fmt.Errorf("failed to set SwapServiceUrl: %w", err)
		}
		if api.svc.GetSwapsService() != nil {
			api.svc.GetSwapsService().Reload()
		}
	}

	if updateSettingsRequest.Relay != "" {
		if err := utils.ValidateWebSocketURL(updateSettingsRequest.Relay); err != nil {
			return fmt.Errorf("invalid Relay URL: %w", err)
		}
		err := api.cfg.SetRelay(updateSettingsRequest.Relay)
		if err != nil {
			return fmt.Errorf("failed to set Relay: %w", err)
		}
		if err := api.svc.ReloadNostr(); err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to reload Nostr service")
		}
	}

	if updateSettingsRequest.GeneralRelay != nil {
		for _, u := range strings.Split(*updateSettingsRequest.GeneralRelay, ",") {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			if err := utils.ValidateWebSocketURL(u); err != nil {
				return fmt.Errorf("invalid General Relay URL: %w", err)
			}
		}
		if err := api.cfg.SetGeneralRelay(*updateSettingsRequest.GeneralRelay); err != nil {
			return fmt.Errorf("failed to set GeneralRelay: %w", err)
		}
		// Re-warm the pool against the new relay list now, backgrounded since a
		// slow/unreachable relay shouldn't block this settings save.
		go api.svc.WarmGeneralRelays()
	}

	if updateSettingsRequest.SearchRelay != nil {
		for _, u := range strings.Split(*updateSettingsRequest.SearchRelay, ",") {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			if err := utils.ValidateWebSocketURL(u); err != nil {
				return fmt.Errorf("invalid Search Relay URL: %w", err)
			}
		}
		if err := api.cfg.SetSearchRelay(*updateSettingsRequest.SearchRelay); err != nil {
			return fmt.Errorf("failed to set SearchRelay: %w", err)
		}
	}

	if updateSettingsRequest.MempoolApi != "" {
		if err := utils.ValidateHTTPURL(updateSettingsRequest.MempoolApi); err != nil {
			return fmt.Errorf("invalid Flokicoin Explorer URL: %w", err)
		}
		err := api.cfg.SetMempoolApi(updateSettingsRequest.MempoolApi)
		if err != nil {
			return fmt.Errorf("failed to set MempoolApi: %w", err)
		}
	}

	if updateSettingsRequest.MessageboardNwcUrl != "" {
		if err := utils.ValidateMessageBoardURL(updateSettingsRequest.MessageboardNwcUrl); err != nil {
			return fmt.Errorf("invalid Messageboard NWC URL: %w", err)
		}
		err := api.cfg.SetMessageboardNwcUrl(updateSettingsRequest.MessageboardNwcUrl)
		if err != nil {
			return fmt.Errorf("failed to set MessageboardNwcUrl: %w", err)
		}
	}

	// Process LSP updates using new helper (which uses LSPManager and atomic updates)
	if updateSettingsRequest.LSPs != nil {
		if err := api.saveLSPsToDatabase(updateSettingsRequest.LSPs); err != nil {
			return fmt.Errorf("failed to update LSPs: %w", err)
		}
	}

	return nil
}

func (api *api) SetNodeAlias(ctx context.Context, nodeAlias string) error {
	err := api.cfg.SetUpdate("NodeAlias", nodeAlias, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save node alias to config")
		return err
	}

	// Also implement this on the FLND node
	if api.svc.GetLNClient() != nil {
		err = api.svc.GetLNClient().SetNodeAlias(ctx, nodeAlias)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to set node alias on FLND node")
			return err
		}
	}

	return nil
}

func (api *api) GetMnemonic(unlockPassword string) (*MnemonicResponse, error) {
	if !api.cfg.CheckUnlockPassword(unlockPassword) {
		return nil, fmt.Errorf("wrong password")
	}

	mnemonic, err := api.cfg.Get("Mnemonic", unlockPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch encryption key: %w", err)
	}

	resp := MnemonicResponse{
		Mnemonic: mnemonic,
	}

	return &resp, nil
}

func (api *api) SetNextBackupReminder(backupReminderRequest *BackupReminderRequest) error {
	err := api.cfg.SetUpdate("NextBackupReminder", backupReminderRequest.NextBackupReminder, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save next backup reminder to config")
	}
	return nil
}

var startMutex sync.Mutex

func (api *api) Start(startRequest *StartRequest) error {
	api.startupError = nil
	err := api.startInternal(startRequest)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to start node")
		api.startupError = err
		api.startupErrorTime = time.Now()
		return err
	}
	return nil
}

func (api *api) startInternal(startRequest *StartRequest) (err error) {
	if !startMutex.TryLock() {
		// do not allow to start twice in case this is somehow called twice
		return errors.New("app is busy")
	}
	defer startMutex.Unlock()
	return api.svc.StartApp(startRequest.UnlockPassword)
}

func (api *api) discoverFlndConfig(ctx context.Context) (string, string, string, error) {
	// 1. First priority: Raw environment variables from AppConfig
	env := api.cfg.GetEnv()
	if env.FLNDAddress != "" && env.FLNDMacaroonFile != "" {
		// Read files from env paths
		macBytes, err := os.ReadFile(env.FLNDMacaroonFile)
		if err == nil {
			certHex := ""
			if env.FLNDCertFile != "" {
				certBytes, err := os.ReadFile(env.FLNDCertFile)
				if err == nil {
					certHex = hex.EncodeToString(certBytes)
				}
			}
			macaroonHex := hex.EncodeToString(macBytes)

			// Verify if the node is actually up (important for Docker startup order)
			err = api.verifyFLNDConnection(ctx, env.FLNDAddress, certHex, macaroonHex)
			if err == nil {
				logger.Logger.Info().Str("address", env.FLNDAddress).Msg("Using FLND credentials directly from environment variables")
				return env.FLNDAddress, macaroonHex, certHex, nil
			}
			logger.Logger.Warn().Err(err).Msg("FLND environment variables failed verification, checking database next")
		}
	}

	// 2. Second priority: Database values (already saved from previous runs or env processing)
	address, _ := api.cfg.Get("LNDAddress", "")
	certHex, _ := api.cfg.Get("LNDCertHex", "")
	macaroonHex, _ := api.cfg.Get("LNDMacaroonHex", "")

	if address != "" && macaroonHex != "" {
		err := api.verifyFLNDConnection(ctx, address, certHex, macaroonHex)
		if err == nil {
			logger.Logger.Info().Str("address", address).Msg("Using FLND credentials from database")
			return address, macaroonHex, certHex, nil
		}
		logger.Logger.Warn().Err(err).Msg("FLND credentials from database failed verification, falling back to discovery")
	}

	// 3. Fallback: Standard Local Discovery (only if no explicit address was provided in Env)
	if env.FLNDAddress != "" {
		// If the user provided an address but it failed verification, we return what they provided
		// instead of falling back to localhost (which would be wrong in Docker).
		// This ensures the UI shows "Offline" but with the CORRECT address/port.
		logger.Logger.Info().Msg("Returning unverified environment credentials to UI")

		// Try to at least get hex values for the UI/Setup
		macaroonHex := ""
		macBytes, err := os.ReadFile(env.FLNDMacaroonFile)
		if err == nil {
			macaroonHex = hex.EncodeToString(macBytes)
		}
		certHex := ""
		if env.FLNDCertFile != "" {
			certBytes, err := os.ReadFile(env.FLNDCertFile)
			if err == nil {
				certHex = hex.EncodeToString(certBytes)
			}
		}
		return env.FLNDAddress, macaroonHex, certHex, nil
	}

	// Traditional discovery (localhost)
	address = FlndEndpoint

	flndDir := chainutil.AppDataDir("flnd", false)
	// flnd data dir structure: [DataDir]/data/chain/flokicoin/main/admin.macaroon
	macaroonPath := filepath.Join(flndDir, "data", "chain", "flokicoin", "main", "admin.macaroon")
	certPath := filepath.Join(flndDir, "tls.cert")

	macaroonBytes, err := os.ReadFile(macaroonPath) //nolint:gosec // path is built from a fixed OS app-data dir + hardcoded subpath, not external input
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read admin.macaroon: %w", err)
	}

	certBytes, err := os.ReadFile(certPath) //nolint:gosec // path is built from a fixed OS app-data dir + hardcoded subpath, not external input
	if err != nil && !os.IsNotExist(err) {
		return "", "", "", fmt.Errorf("failed to read tls.cert: %w", err)
	}

	certHex = ""
	if len(certBytes) > 0 {
		certHex = hex.EncodeToString(certBytes)
	}

	return address, hex.EncodeToString(macaroonBytes), certHex, nil
}

func (api *api) Setup(ctx context.Context, setupRequest *SetupRequest) error {
	if !startMutex.TryLock() {
		// do not allow to start twice in case this is somehow called twice
		return errors.New("app is busy")
	}
	defer startMutex.Unlock()
	info, err := api.GetInfo(ctx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get info")
		return err
	}
	if info.SetupCompleted {
		logger.Logger.Error().Msg("Cannot re-setup node")
		return errors.New("setup already completed")
	}

	if setupRequest.UnlockPassword == "" {
		return errors.New("no unlock password provided")
	}

	err = api.cfg.SaveUnlockPasswordCheck(setupRequest.UnlockPassword)
	if err != nil {
		return err
	}

	// update next backup reminder
	err = api.cfg.SetUpdate("NextBackupReminder", setupRequest.NextBackupReminder, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save next backup reminder")
	}

	// only update non-empty values
	if setupRequest.LNBackendType != "" {
		err = api.cfg.SetUpdate("LNBackendType", setupRequest.LNBackendType, "")
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save backend type")
			return err
		}
	}
	if setupRequest.Mnemonic != "" {
		err = api.cfg.SetUpdate("Mnemonic", setupRequest.Mnemonic, setupRequest.UnlockPassword)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save encrypted mnemonic")
			return err
		}
	}
	// Handle AutoConnect or CustomConfig to pre-fill FLND details
	if setupRequest.AutoConnect || setupRequest.CustomConfig != nil {
		var customDataDir string
		// Default address, but if custom config has rpcListen (port), we might need to adjust.
		// However, user requirement says 'Uses the parsed datadir to find Macaroon/Cert, uses rpc.flnd for port'
		// Note from plan: 'Backend Logic: Uses datadir to find Macaroon/Cert, uses rpc.flnd for port (combined with localhost)'

		if setupRequest.CustomConfig != nil {
			customDataDir = setupRequest.CustomConfig.DataDir
		}
		if customDataDir == "" {
			customDataDir = api.cfg.GetDefaultWorkDir()
		}

		address, macaroonHex, certHex, err := api.discoverFlndConfig(ctx)
		if err != nil {
			return err
		}

		if setupRequest.CustomConfig != nil && setupRequest.CustomConfig.RpcListen != "" {
			port := setupRequest.CustomConfig.RpcListen
			if strings.HasPrefix(port, ":") {
				address = "127.0.0.1" + port
			} else if !strings.Contains(port, ":") {
				// Assume it is just a port number
				address = "127.0.0.1:" + port
			} else {
				// Full address?
				address = port
			}
		}

		setupRequest.FLNDAddress = address
		setupRequest.FLNDMacaroonHex = macaroonHex
		setupRequest.FLNDCertHex = certHex
		setupRequest.LNBackendType = config.FLNDBackendType

		if customDataDir != "" {
			err = api.cfg.SetUpdate("LNDDataDir", customDataDir, setupRequest.UnlockPassword)
			if err != nil {
				logger.Logger.Error().Err(err).Msg("Failed to save FLND data dir")
				return err
			}
		}
	}

	if setupRequest.FLNDAddress != "" {
		err = api.cfg.SetUpdate("LNDAddress", setupRequest.FLNDAddress, setupRequest.UnlockPassword)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save FLND address")
			return err
		}
	}
	if setupRequest.FLNDCertHex != "" {
		err = api.cfg.SetUpdate("LNDCertHex", setupRequest.FLNDCertHex, setupRequest.UnlockPassword)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save FLND cert hex")
			return err
		}
	}
	if setupRequest.FLNDMacaroonHex != "" {
		err = api.cfg.SetUpdate("LNDMacaroonHex", setupRequest.FLNDMacaroonHex, setupRequest.UnlockPassword)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save FLND macaroon hex")
			return err
		}
	}

	if setupRequest.LSP != "" {
		err = api.cfg.SetLSP(setupRequest.LSP)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save LSP")
			return err
		}
	}

	if setupRequest.LokihubServicesURL != "" {
		err = api.cfg.SetLokihubServicesURL(setupRequest.LokihubServicesURL)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save LokihubServicesURL")
			return err
		}
	}

	if setupRequest.SwapServiceUrl != "" {
		err = api.cfg.SetSwapServiceURL(setupRequest.SwapServiceUrl)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save SwapServiceUrl")
			return err
		}
	}

	if setupRequest.Relay != "" {
		err = api.cfg.SetRelay(setupRequest.Relay)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save Relay")
			return err
		}
	}

	if setupRequest.MempoolApi != "" {
		err = api.cfg.SetMempoolApi(setupRequest.MempoolApi)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save MempoolApi")
			return err
		}
	}

	if setupRequest.EnableSwap != nil {
		if err := api.cfg.SetEnableSwap(*setupRequest.EnableSwap); err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save EnableSwap")
		}
	}

	if setupRequest.MessageboardNwcUrl != "" {
		err = api.cfg.SetMessageboardNwcUrl(setupRequest.MessageboardNwcUrl)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save MessageboardNwcUrl")
			return err
		}
	}

	return nil
}

func (api *api) GetWalletCapabilities(ctx context.Context) (*WalletCapabilitiesResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}

	methods := api.svc.GetLNClient().GetSupportedNIP47Methods()
	notificationTypes := api.svc.GetLNClient().GetSupportedNIP47NotificationTypes()

	scopes, err := permissions.RequestMethodsToScopes(methods)
	if err != nil {
		return nil, err
	}
	if len(notificationTypes) > 0 {
		scopes = append(scopes, constants.NOTIFICATIONS_SCOPE)
	}

	return &WalletCapabilitiesResponse{
		Methods:           methods,
		NotificationTypes: notificationTypes,
		Scopes:            scopes,
	}, nil
}

func (api *api) SendPaymentProbes(ctx context.Context, sendPaymentProbesRequest *SendPaymentProbesRequest) (*SendPaymentProbesResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}

	var errMessage string
	err := api.svc.GetLNClient().SendPaymentProbes(ctx, sendPaymentProbesRequest.Invoice)
	if err != nil {
		errMessage = err.Error()
	}

	return &SendPaymentProbesResponse{Error: errMessage}, nil
}

func (api *api) MigrateNodeStorage(ctx context.Context, to string) error {
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}
	if to != "VSS" {
		return fmt.Errorf("migration type not supported: %s", to)
	}

	// LDK VSS Logic Removed
	return api.Stop()
}

func (api *api) SendSpontaneousPaymentProbes(ctx context.Context, sendSpontaneousPaymentProbesRequest *SendSpontaneousPaymentProbesRequest) (*SendSpontaneousPaymentProbesResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}

	var errMessage string
	err := api.svc.GetLNClient().SendSpontaneousPaymentProbes(ctx, sendSpontaneousPaymentProbesRequest.Amount, sendSpontaneousPaymentProbesRequest.NodeId)
	if err != nil {
		errMessage = err.Error()
	}

	return &SendSpontaneousPaymentProbesResponse{Error: errMessage}, nil
}

func (api *api) GetNetworkGraph(ctx context.Context, nodeIds []string) (NetworkGraphResponse, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	return api.svc.GetLNClient().GetNetworkGraph(ctx, nodeIds)
}

func (api *api) SyncWallet() error {
	if api.svc.GetLNClient() == nil {
		return errors.New("LNClient not started")
	}
	api.svc.GetLNClient().UpdateLastWalletSyncRequest()
	return nil
}
func (api *api) ListOnchainTransactions(ctx context.Context, limit, offset uint64) ([]lnclient.OnchainTransaction, error) {
	if api.svc.GetLNClient() == nil {
		return nil, errors.New("LNClient not started")
	}
	return api.svc.GetLNClient().ListOnchainTransactions(ctx, 0, 0, limit, offset)
}

func (api *api) GetLogOutput(ctx context.Context, logType string, getLogRequest *GetLogOutputRequest) (*GetLogOutputResponse, error) {
	var err error
	var logData []byte

	switch logType {
	case LogTypeNode:
		if api.svc.GetLNClient() == nil {
			return nil, errors.New("LNClient not started")
		}

		logData, err = api.svc.GetLNClient().GetLogOutput(ctx, getLogRequest.MaxLen)
		if err != nil {
			return nil, err
		}
	case LogTypeApp:
		logFileName := logger.GetLogFilePath()
		if logFileName == "" {
			logData = []byte("file log is disabled")
		} else {
			logData, err = utils.ReadFileTail(logFileName, getLogRequest.MaxLen)
			if err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("invalid log type: '%s'", logType)
	}

	return &GetLogOutputResponse{Log: string(logData)}, nil
}

func (api *api) Health(ctx context.Context) (*HealthResponse, error) {
	var alarms []HealthAlarm

	relayStatuses := api.svc.GetRelayStatuses()
	if len(relayStatuses) > 0 {
		isAnyNostrRelayOffline := false
		offlineRelayUrls := []string{}
		for _, relayStatus := range relayStatuses {
			if !relayStatus.Online {
				isAnyNostrRelayOffline = true
				offlineRelayUrls = append(offlineRelayUrls, relayStatus.Url)
			}
		}
		if isAnyNostrRelayOffline {
			alarms = append(alarms, NewHealthAlarm(HealthAlarmKindNostrRelayOffline, offlineRelayUrls))
		}
	}

	lnClient := api.svc.GetLNClient()

	if lnClient != nil {
		nodeStatus, _ := lnClient.GetNodeStatus(ctx)
		if nodeStatus == nil || !nodeStatus.IsReady {
			alarms = append(alarms, NewHealthAlarm(HealthAlarmKindNodeNotReady, nodeStatus))
		}

		channels, err := lnClient.ListChannels(ctx)
		if err != nil {
			return nil, err
		}

		offlineChannels := slices.DeleteFunc(channels, func(channel lnclient.Channel) bool {
			if channel.Active {
				return true
			}
			if channel.Confirmations == nil || channel.ConfirmationsRequired == nil {
				return false
			}
			return *channel.Confirmations < *channel.ConfirmationsRequired
		})

		if len(offlineChannels) > 0 {
			alarms = append(alarms, NewHealthAlarm(HealthAlarmKindChannelsOffline, nil))
		}
	}

	return &HealthResponse{Alarms: alarms}, nil
}

func (api *api) GetCustomNodeCommands() (*CustomNodeCommandsResponse, error) {
	lnClient := api.svc.GetLNClient()
	if lnClient == nil {
		return nil, errors.New("LNClient not started")
	}

	allCommandDefs := lnClient.GetCustomNodeCommandDefinitions()
	commandDefs := make([]CustomNodeCommandDef, 0, len(allCommandDefs))
	for _, commandDef := range allCommandDefs {
		argDefs := make([]CustomNodeCommandArgDef, 0, len(commandDef.Args))
		for _, argDef := range commandDef.Args {
			argDefs = append(argDefs, CustomNodeCommandArgDef{
				Name:        argDef.Name,
				Description: argDef.Description,
			})
		}
		commandDefs = append(commandDefs, CustomNodeCommandDef{
			Name:        commandDef.Name,
			Description: commandDef.Description,
			Args:        argDefs,
		})
	}

	return &CustomNodeCommandsResponse{Commands: commandDefs}, nil
}

func (api *api) ExecuteCustomNodeCommand(ctx context.Context, command string) (interface{}, error) {
	lnClient := api.svc.GetLNClient()
	if lnClient == nil {
		return nil, errors.New("LNClient not started")
	}

	// Split command line into arguments. Command name must be the first argument.
	parsedArgs, err := utils.ParseCommandLine(command)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node command: %w", err)
	} else if len(parsedArgs) == 0 {
		return nil, errors.New("no command provided")
	}

	// Look up the requested command definition.
	allCommandDefs := lnClient.GetCustomNodeCommandDefinitions()
	commandDefIdx := slices.IndexFunc(allCommandDefs, func(def lnclient.CustomNodeCommandDef) bool {
		return def.Name == parsedArgs[0]
	})
	if commandDefIdx < 0 {
		return nil, fmt.Errorf("unknown command: %q", parsedArgs[0])
	}

	// Build flag set.
	commandDef := allCommandDefs[commandDefIdx]
	flagSet := flag.NewFlagSet(commandDef.Name, flag.ContinueOnError)
	for _, argDef := range commandDef.Args {
		flagSet.String(argDef.Name, "", argDef.Description)
	}

	if err = flagSet.Parse(parsedArgs[1:]); err != nil {
		return nil, fmt.Errorf("failed to parse command arguments: %w", err)
	}

	// Collect flags that have been set.
	argValues := make(map[string]string)
	flagSet.Visit(func(f *flag.Flag) {
		argValues[f.Name] = f.Value.String()
	})

	reqArgs := make([]lnclient.CustomNodeCommandArg, 0, len(argValues))
	for _, argDef := range commandDef.Args {
		if argValue, ok := argValues[argDef.Name]; ok {
			reqArgs = append(reqArgs, lnclient.CustomNodeCommandArg{
				Name:  argDef.Name,
				Value: argValue,
			})
		}
	}

	nodeResp, err := lnClient.ExecuteCustomNodeCommand(ctx, &lnclient.CustomNodeCommandRequest{
		Name: commandDef.Name,
		Args: reqArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("node failed to execute custom command: %w", err)
	}

	return nodeResp.Response, nil
}

func (api *api) SendEvent(event string, properties interface{}) {
	api.svc.GetEventPublisher().Publish(&events.Event{
		Event:      event,
		Properties: properties,
	})
}

func (api *api) parseExpiresAt(expiresAtString string) (*time.Time, error) {
	var expiresAt *time.Time
	if expiresAtString != "" {
		var err error
		expiresAtValue, err := time.Parse(time.RFC3339, expiresAtString)
		if err != nil {
			logger.Logger.Error().Interface("expiresAt", expiresAtString).Msg("Invalid expiresAt")
			return nil, fmt.Errorf("invalid expiresAt: %v", err)
		}
		expiresAt = &expiresAtValue
	}
	return expiresAt, nil
}

func (api *api) GetForwards() (*GetForwardsResponse, error) {
	var forwards []db.Forward
	err := api.db.Find(&forwards).Error
	if err != nil {
		return nil, err
	}

	var totalOutboundAmount uint64
	var totalFeeEarned uint64

	for _, forward := range forwards {
		totalOutboundAmount += forward.OutboundAmountForwardedMloki
		totalFeeEarned += forward.TotalFeeEarnedMloki
	}

	numForwards := len(forwards)

	return &GetForwardsResponse{
		OutboundAmountForwardedMloki: totalOutboundAmount,
		TotalFeeEarnedMloki:          totalFeeEarned,
		NumForwards:                  uint64(numForwards),
	}, nil
}

func (api *api) SetupLocal(ctx context.Context, req *SetupLocalRequest) error {
	if !startMutex.TryLock() {
		return errors.New("app is busy")
	}
	defer startMutex.Unlock()

	// Preliminary checks
	info, err := api.GetInfo(ctx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get info")
		return err
	}
	if info.SetupCompleted {
		logger.Logger.Error().Msg("Cannot re-setup node")
		return errors.New("setup already completed")
	}
	if req.UnlockPassword == "" {
		return errors.New("no unlock password provided")
	}

	// Check password early
	if err := api.cfg.SaveUnlockPasswordCheck(req.UnlockPassword); err != nil {
		return err
	}

	// 1. Discover Config from Default DataDir
	address, macaroonHex, certHex, err := api.discoverFlndConfig(ctx)
	if err != nil {
		return err
	}

	// 2. Override Address logic removed (always use discovered default)

	// 3. Verify Connection
	if err := api.verifyFLNDConnection(ctx, address, certHex, macaroonHex); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to verify FLND connection")
		return fmt.Errorf("failed to verify connection: %w", err)
	}

	// 4. Save Config
	if err := api.cfg.SetUpdate("LNDAddress", address, req.UnlockPassword); err != nil {
		return err
	}
	if err := api.cfg.SetUpdate("LNDCertHex", certHex, req.UnlockPassword); err != nil {
		return err
	}
	if err := api.cfg.SetUpdate("LNDMacaroonHex", macaroonHex, req.UnlockPassword); err != nil {
		return err
	}
	// Set Backend Type last
	if err := api.cfg.SetUpdate("LNBackendType", config.FLNDBackendType, ""); err != nil {
		return err
	}

	if req.LSP != "" {
		if err := api.cfg.SetLSP(req.LSP); err != nil {
			return err
		}
	}

	if req.LokihubServicesURL != "" {
		if err := utils.ValidateHTTPURL(req.LokihubServicesURL); err != nil {
			return fmt.Errorf("invalid Hub Services URL: %w", err)
		}
		if err := api.cfg.SetLokihubServicesURL(req.LokihubServicesURL); err != nil {
			return err
		}
	}
	if req.SwapServiceUrl != "" {
		if err := utils.ValidateHTTPURL(req.SwapServiceUrl); err != nil {
			return fmt.Errorf("invalid Swap Service URL: %w", err)
		}
		if err := api.cfg.SetSwapServiceURL(req.SwapServiceUrl); err != nil {
			return err
		}
	}

	if req.Relay != "" {
		if err := utils.ValidateWebSocketURL(req.Relay); err != nil {
			return fmt.Errorf("invalid Relay URL: %w", err)
		}
		if err := api.cfg.SetRelay(req.Relay); err != nil {
			return err
		}
	}

	if req.MessageboardNwcUrl != "" {
		if err := utils.ValidateMessageBoardURL(req.MessageboardNwcUrl); err != nil {
			return fmt.Errorf("invalid Messageboard NWC URL: %w", err)
		}
		if err := api.cfg.SetMessageboardNwcUrl(req.MessageboardNwcUrl); err != nil {
			return err
		}
	}

	if req.MempoolApi != "" {
		if err := utils.ValidateHTTPURL(req.MempoolApi); err != nil {
			return fmt.Errorf("invalid Flokicoin Explorer URL: %w", err)
		}
		if err := api.cfg.SetMempoolApi(req.MempoolApi); err != nil {
			return err
		}
	}

	// Process LSPs array (new approach)
	if len(req.LSPs) > 0 {
		if err := api.saveLSPsToDatabase(req.LSPs); err != nil {
			return fmt.Errorf("failed to save LSPs: %w", err)
		}
	} else if req.LSP != "" {
		// Backward compatibility: single LSP string (deprecated)
		if err := api.cfg.SetLSP(req.LSP); err != nil {
			return err
		}
	}

	return nil
}

func (api *api) SetupManual(ctx context.Context, req *SetupManualRequest) error {
	if !startMutex.TryLock() {
		return errors.New("app is busy")
	}
	defer startMutex.Unlock()

	// Preliminary checks
	info, err := api.GetInfo(ctx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get info")
		return err
	}
	if info.SetupCompleted {
		logger.Logger.Error().Msg("Cannot re-setup node")
		return errors.New("setup already completed")
	}
	if req.UnlockPassword == "" {
		return errors.New("no unlock password provided")
	}

	// Check password early
	if err := api.cfg.SaveUnlockPasswordCheck(req.UnlockPassword); err != nil {
		return err
	}

	// 1. Verify Connection
	if err := api.verifyFLNDConnection(ctx, req.FLNDAddress, req.FLNDCertHex, req.FLNDMacaroonHex); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to verify FLND connection")
		return fmt.Errorf("failed to verify connection: %w", err)
	}

	// 2. Save Config
	if err := api.cfg.SetUpdate("LNDAddress", req.FLNDAddress, req.UnlockPassword); err != nil {
		return err
	}
	if err := api.cfg.SetUpdate("LNDCertHex", req.FLNDCertHex, req.UnlockPassword); err != nil {
		return err
	}
	if err := api.cfg.SetUpdate("LNDMacaroonHex", req.FLNDMacaroonHex, req.UnlockPassword); err != nil {
		return err
	}
	// Set Backend Type last
	if err := api.cfg.SetUpdate("LNBackendType", config.FLNDBackendType, ""); err != nil {
		return err
	}

	if req.LSP != "" {
		if err := api.cfg.SetLSP(req.LSP); err != nil {
			return err
		}
	}

	if req.LokihubServicesURL != "" {
		if err := utils.ValidateHTTPURL(req.LokihubServicesURL); err != nil {
			return fmt.Errorf("invalid Hub Services URL: %w", err)
		}
		if err := api.cfg.SetLokihubServicesURL(req.LokihubServicesURL); err != nil {
			return err
		}
	}
	if req.SwapServiceUrl != "" {
		if err := utils.ValidateHTTPURL(req.SwapServiceUrl); err != nil {
			return fmt.Errorf("invalid Swap Service URL: %w", err)
		}
		if err := api.cfg.SetSwapServiceURL(req.SwapServiceUrl); err != nil {
			return err
		}
	}

	if req.Relay != "" {
		if err := utils.ValidateWebSocketURL(req.Relay); err != nil {
			return fmt.Errorf("invalid Relay URL: %w", err)
		}
		if err := api.cfg.SetRelay(req.Relay); err != nil {
			return err
		}
	}

	if req.MessageboardNwcUrl != "" {
		if err := utils.ValidateMessageBoardURL(req.MessageboardNwcUrl); err != nil {
			return fmt.Errorf("invalid Messageboard NWC URL: %w", err)
		}
		if err := api.cfg.SetMessageboardNwcUrl(req.MessageboardNwcUrl); err != nil {
			return err
		}
	}

	if req.MempoolApi != "" {
		if err := utils.ValidateHTTPURL(req.MempoolApi); err != nil {
			return fmt.Errorf("invalid Flokicoin Explorer URL: %w", err)
		}
		if err := api.cfg.SetMempoolApi(req.MempoolApi); err != nil {
			return err
		}
	}

	// Process LSPs array (new approach)
	if len(req.LSPs) > 0 {
		if err := api.saveLSPsToDatabase(req.LSPs); err != nil {
			return fmt.Errorf("failed to save LSPs: %w", err)
		}
	} else if req.LSP != "" {
		// Backward compatibility: single LSP string (deprecated)
		if err := api.cfg.SetLSP(req.LSP); err != nil {
			return err
		}
	}

	return nil
}

// verifyFLNDConnection attempts to connect to FLND with retries to ensure credentials are valid
func (api *api) verifyFLNDConnection(ctx context.Context, address, certHex, macaroonHex string) error {
	logger.Logger.Info().Msg("Verifying FLND connection...")

	// Using wrapper directly to avoid main app wrapper logic
	flndClient, err := wrapper.NewFLNDclient(wrapper.FLNDoptions{
		Address:     address,
		CertHex:     certHex,
		MacaroonHex: macaroonHex,
	})
	if err != nil {
		return err
	}
	defer flndClient.Close()

	// Retry 3 times
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(1 * time.Second)
		}

		// Try GetInfo
		_, err := flndClient.GetInfo(ctx, &lnrpc.GetInfoRequest{})
		if err == nil {
			return nil
		}
		lastErr = err
		logger.Logger.Warn().Err(err).Int("attempt", i+1).Msg("Verification attempt failed")
	}

	return lastErr
}

func (api *api) GetServices(ctx context.Context) (interface{}, error) {
	// 1. Try Cache
	cachedJSON := api.cfg.GetCachedServicesJSON()
	if cachedJSON != "" {
		var result interface{}
		if err := json.Unmarshal([]byte(cachedJSON), &result); err == nil {
			return result, nil
		}
		logger.Logger.Error().Msg("Failed to unmarshal cached services JSON, falling back to network")
	}

	// 2. Fallback to Network (only if cache empty or invalid)
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	lokihubServicesURL := api.cfg.GetLokihubServicesURL()

	// Ensure no trailing slash
	lokihubServicesURL = strings.TrimSuffix(lokihubServicesURL, "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lokihubServicesURL+"/services.json", nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch services: %s", resp.Status)
	}

	var result interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result, nil
}

// ReplaceCircleAllowlist replaces the full allowlist for the CircleIdentity
// attached to a circle_hub app. Since an identity may be shared by
// multiple providers, this affects every provider referencing it.
func (api *api) ReplaceCircleAllowlist(app *db.App, pubkeys []string) error {
	if app.Kind != db.AppKindCircleHub {
		return fmt.Errorf("app is not a circle_hub")
	}
	cfg, err := api.appsSvc.GetCircleHubConfig(app.ID)
	if err != nil {
		return fmt.Errorf("circle_hub has no config: %w", err)
	}
	identityID := cfg.CircleIdentityID
	return api.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("circle_identity_id = ?", identityID).Delete(&db.CircleIdentityAllowedPubkey{}).Error; err != nil {
			return err
		}
		for _, pk := range pubkeys {
			pk = strings.TrimSpace(pk)
			if pk == "" {
				continue
			}
			// Validate: must be a 64-char lowercase-hex 32-byte Nostr pubkey.
			if len(pk) != 64 || pk != strings.ToLower(pk) {
				return fmt.Errorf("%w: %q is not a valid 64-char lowercase-hex pubkey", constants.ErrInvalidParams, pk)
			}
			if _, decErr := hex.DecodeString(pk); decErr != nil {
				return fmt.Errorf("%w: %q is not a valid 64-char lowercase-hex pubkey", constants.ErrInvalidParams, pk)
			}
			if err := tx.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: identityID, Pubkey: pk}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveCircleAllowedPubkey removes a single pubkey from the CircleIdentity
// attached to a circle_hub app's allowlist.
func (api *api) RemoveCircleAllowedPubkey(app *db.App, pubkey string) error {
	if app.Kind != db.AppKindCircleHub {
		return fmt.Errorf("app is not a circle_hub")
	}
	// Same format check as ReplaceCircleAllowlist: a malformed pubkey can never
	// match a stored row (a no-op delete either way), but validating up front
	// gives the caller a clear error instead of a silent no-op.
	if len(pubkey) != 64 || pubkey != strings.ToLower(pubkey) {
		return fmt.Errorf("%w: %q is not a valid 64-char lowercase-hex pubkey", constants.ErrInvalidParams, pubkey)
	}
	if _, err := hex.DecodeString(pubkey); err != nil {
		return fmt.Errorf("%w: %q is not a valid 64-char lowercase-hex pubkey", constants.ErrInvalidParams, pubkey)
	}
	cfg, err := api.appsSvc.GetCircleHubConfig(app.ID)
	if err != nil {
		return fmt.Errorf("circle_hub has no config: %w", err)
	}
	return api.db.Where("circle_identity_id = ? AND pubkey = ?", cfg.CircleIdentityID, pubkey).
		Delete(&db.CircleIdentityAllowedPubkey{}).Error
}

// ListCircleAllowlist returns the current allowlisted pubkeys for the
// CircleIdentity attached to a circle_hub app.
func (api *api) ListCircleAllowlist(app *db.App) ([]string, error) {
	if app.Kind != db.AppKindCircleHub {
		return nil, fmt.Errorf("app is not a circle_hub")
	}
	cfg, err := api.appsSvc.GetCircleHubConfig(app.ID)
	if err != nil {
		return nil, fmt.Errorf("circle_hub has no config: %w", err)
	}
	var entries []db.CircleIdentityAllowedPubkey
	if err := api.db.Where("circle_identity_id = ?", cfg.CircleIdentityID).Order("pubkey asc").Find(&entries).Error; err != nil {
		return nil, err
	}
	pubkeys := make([]string, 0, len(entries))
	for _, e := range entries {
		pubkeys = append(pubkeys, e.Pubkey)
	}
	return pubkeys, nil
}

// fetchCircleFollowingPubkeys re-fetches providerPubkey's kind:3 contact list
// via the shared nostrSocialCache (WarmCircleFollowingCache), so the manual
// "Sync"/refresh flow stays in agreement with the automatic following-policy
// auth path instead of running its own separate relay query. Shared by
// RefreshCircleAllowlist (which applies the result) and PreviewCircleRefresh
// (which only diffs it).
func (api *api) fetchCircleFollowingPubkeys(ctx context.Context, providerPubkey string) ([]string, error) {
	contacts, err := api.svc.WarmCircleFollowingCache(ctx, providerPubkey)
	if err != nil {
		return nil, err
	}
	pubkeys := make([]string, 0, len(contacts))
	for pubkey := range contacts {
		pubkeys = append(pubkeys, pubkey)
	}
	return pubkeys, nil
}

// circleHubFollowingConfig loads app's CircleHubConfig and validates
// it's eligible for a following-policy relay refresh — shared by
// RefreshCircleAllowlist and PreviewCircleRefresh so both reject the same way
// for an allowlist-policy circle (refreshing from relays would silently
// clobber a manually-curated member list with the owner's raw following list,
// which only makes sense for following-policy circles in the first place).
func (api *api) circleHubFollowingConfig(app *db.App) (*db.CircleHubConfig, error) {
	if app.Kind != db.AppKindCircleHub {
		return nil, fmt.Errorf("app is not a circle_hub")
	}
	providerConfig, err := api.appsSvc.GetCircleHubConfig(app.ID)
	if err != nil {
		return nil, fmt.Errorf("circle_hub has no config: %w", err)
	}
	if providerConfig.CircleIdentity.Policy != db.CirclePolicyFollowing {
		return nil, fmt.Errorf("%w: refresh from relays only applies to following-policy circles", constants.ErrInvalidParams)
	}
	if providerConfig.CircleIdentity.ProviderPubkey == "" {
		return nil, fmt.Errorf("circle_hub has no provider_pubkey set")
	}
	return providerConfig, nil
}

// RefreshCircleAllowlist re-fetches the provider's kind:3 contacts from nostr relays
// and rebuilds the allowlist for the CircleIdentity attached to this circle_hub app.
// Only valid for following-policy circles — see circleHubFollowingConfig.
func (api *api) RefreshCircleAllowlist(ctx context.Context, app *db.App) error {
	providerConfig, err := api.circleHubFollowingConfig(app)
	if err != nil {
		return err
	}
	providerPubkey := providerConfig.CircleIdentity.ProviderPubkey
	pubkeys, err := api.fetchCircleFollowingPubkeys(ctx, providerPubkey)
	if err != nil {
		return err
	}
	// fetchCircleFollowingPubkeys already warms the nostrSocialCache entry as
	// part of this same fetch (see WarmCircleFollowingCache), so the
	// following-count/synced-at shown on the app's detail page reflects this
	// refresh immediately without a second, separate relay round-trip.
	return api.ReplaceCircleAllowlist(app, pubkeys)
}

// PreviewCircleRefresh re-fetches the provider's kind:3 contacts and reports
// how they'd differ from the currently-stored allowlist, without applying
// anything. Only valid for following-policy circles — see
// circleHubFollowingConfig.
func (api *api) PreviewCircleRefresh(ctx context.Context, app *db.App) (*CircleRefreshPreview, error) {
	providerConfig, err := api.circleHubFollowingConfig(app)
	if err != nil {
		return nil, err
	}
	fresh, err := api.fetchCircleFollowingPubkeys(ctx, providerConfig.CircleIdentity.ProviderPubkey)
	if err != nil {
		return nil, err
	}
	current, err := api.ListCircleAllowlist(app)
	if err != nil {
		return nil, err
	}

	currentSet := make(map[string]bool, len(current))
	for _, pk := range current {
		currentSet[pk] = true
	}
	freshSet := make(map[string]bool, len(fresh))
	var added []string
	for _, pk := range fresh {
		freshSet[pk] = true
		if !currentSet[pk] {
			added = append(added, pk)
		}
	}
	var removed []string
	for _, pk := range current {
		if !freshSet[pk] {
			removed = append(removed, pk)
		}
	}

	return &CircleRefreshPreview{Pubkeys: fresh, Added: added, Removed: removed}, nil
}

// getCircleChildren returns app's circle_wallet children, newest first, using
// tx (so callers can run this consistently with a transaction, e.g. inside
// DeleteCircleHub).
func getCircleChildren(tx *gorm.DB, parentAppID uint) ([]db.App, error) {
	var children []db.App
	err := tx.Where("parent_app_id = ? AND parent_kind = ?", parentAppID, db.ParentKindCircle).
		Order("created_at DESC").
		Find(&children).Error
	return children, err
}

// ListCircleChildrenBalances returns a page of a circle_hub's circle_wallet
// children together with each one's current isolated balance, for both the
// wallets list and the pre-delete confirmation UI. limit == 0 returns every
// child unpaginated (used by the pre-delete confirmation UI, which needs the
// full set). totalCount is the child count before paging, for the caller to
// compute page count from.
func (api *api) ListCircleChildrenBalances(app *db.App, limit uint64, offset uint64) ([]CircleChildBalance, uint64, error) {
	if app.Kind != db.AppKindCircleHub {
		return nil, 0, fmt.Errorf("app is not a circle_hub")
	}

	children, err := getCircleChildren(api.db, app.ID)
	if err != nil {
		return nil, 0, err
	}
	totalCount := uint64(len(children))

	if limit > 0 {
		children = paginateSlice(children, limit, offset)
	}

	balancesByID, err := queries.GetIsolatedBalancesByAppIDs(api.db, childAppIDs(children))
	if err != nil {
		return nil, 0, err
	}

	requesterPubkeysByID, err := queries.GetCircleWalletMembershipPubkeysByWalletAppIDs(api.db, childAppIDs(children))
	if err != nil {
		return nil, 0, err
	}

	balances := make([]CircleChildBalance, 0, len(children))
	for _, child := range children {
		balances = append(balances, CircleChildBalance{
			AppID:           child.ID,
			Name:            child.Name,
			RequesterPubkey: requesterPubkeysByID[child.ID],
			AppPubkey:       child.AppPubkey,
			BalanceMloki:    balancesByID[child.ID],
		})
	}
	return balances, totalCount, nil
}

// paginateSlice returns the [offset, offset+limit) window of s, clamped to
// its bounds. Used by list endpoints that build their full result in memory
// (e.g. by merging multiple sources) before paging it, rather than paginating
// at the SQL level.
func paginateSlice[T any](s []T, limit uint64, offset uint64) []T {
	if offset >= uint64(len(s)) {
		return []T{}
	}
	end := offset + limit
	if end > uint64(len(s)) {
		end = uint64(len(s))
	}
	return s[offset:end]
}

// childAppIDs extracts the IDs from a slice of apps, for batched balance lookups.
func childAppIDs(children []db.App) []uint {
	ids := make([]uint, len(children))
	for i, child := range children {
		ids[i] = child.ID
	}
	return ids
}

// DeleteCircleWalletChild removes a single circle_wallet child from a
// circle_hub, in any state (empty or with a remaining balance) — unlike
// DeleteCircleHub, which only ever operates on the whole hub at once. Any
// remaining balance is first reclaimed back to the hub and the child app
// deleted (service.ReclaimAndDeleteSubWallet), the same way
// DeleteJITHubAllocation handles a claimed JIT wallet.
func (api *api) DeleteCircleWalletChild(hubAppID uint, childAppID uint) error {
	var hub db.App
	if err := api.db.First(&hub, hubAppID).Error; err != nil {
		return fmt.Errorf("app not found: %w", err)
	}
	if hub.Kind != db.AppKindCircleHub {
		return fmt.Errorf("app is not a circle_hub")
	}

	var child db.App
	if err := api.db.Where("id = ? AND parent_app_id = ? AND parent_kind = ?",
		childAppID, hubAppID, db.ParentKindCircle).First(&child).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: circle wallet not found for this hub", constants.ErrInvalidParams)
		}
		return err
	}

	return service.ReclaimAndDeleteSubWallet(context.Background(), api.db,
		api.svc.GetTransactionsService(), api.svc.GetLNClient(), child)
}

// DeleteCircleHub deletes a circle_hub app and its circle_wallet children.
// mode must be db.CircleDeleteModeAll (delete every child regardless of balance) or
// db.CircleDeleteModeEmptyOnly (delete only zero-balance children — if any child
// still has balance, the provider itself is left intact so the admin can retry once
// the rest have drained). Guarded by the same per-provider advisory lock used by
// create_circle_wallet, so this can't race a concurrent wallet creation on the app.
func (api *api) DeleteCircleHub(app *db.App, mode string) (*DeleteCircleHubResult, error) {
	if app.Kind != db.AppKindCircleHub {
		return nil, fmt.Errorf("%w: app is not a circle_hub", constants.ErrInvalidParams)
	}
	if mode != db.CircleDeleteModeAll && mode != db.CircleDeleteModeEmptyOnly {
		return nil, fmt.Errorf("%w: mode must be %q or %q", constants.ErrInvalidParams,
			db.CircleDeleteModeAll, db.CircleDeleteModeEmptyOnly)
	}

	result := &DeleteCircleHubResult{DeletedChildIDs: []uint{}, SkippedChildIDs: []uint{}}
	err := api.db.Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(app.ID)).Error; err != nil { //nolint:gosec // app IDs are small auto-increment DB primary keys
				return fmt.Errorf("acquire circle delete lock: %w", err)
			}
		}

		children, err := getCircleChildren(tx, app.ID)
		if err != nil {
			return err
		}

		// Only mode=empty_only needs balances at all — mode=all deletes every
		// child regardless, so skip the extra round-trip in the common case.
		var balancesByID map[uint]int64
		if mode == db.CircleDeleteModeEmptyOnly {
			balancesByID, err = queries.GetIsolatedBalancesByAppIDs(tx, childAppIDs(children))
			if err != nil {
				return err
			}
		}

		var idsToDelete []uint
		for _, child := range children {
			if mode == db.CircleDeleteModeEmptyOnly && balancesByID[child.ID] != 0 {
				result.SkippedChildIDs = append(result.SkippedChildIDs, child.ID)
				continue
			}
			idsToDelete = append(idsToDelete, child.ID)
		}

		if len(idsToDelete) > 0 {
			if err := tx.Where("id IN ?", idsToDelete).Delete(&db.App{}).Error; err != nil {
				return err
			}
			result.DeletedChildIDs = idsToDelete
		}

		if len(result.SkippedChildIDs) > 0 {
			// At least one child still has balance and was left intact — abort the
			// provider deletion so an admin can retry once it drains.
			return nil
		}

		if err := tx.Delete(app).Error; err != nil {
			return err
		}
		result.HubDeleted = true
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, childID := range result.DeletedChildIDs {
		api.eventPublisher.Publish(&events.Event{
			Event:      "nwc_app_deleted",
			Properties: map[string]interface{}{"id": childID},
		})
	}
	if result.HubDeleted {
		api.eventPublisher.Publish(&events.Event{
			Event:      "nwc_app_deleted",
			Properties: map[string]interface{}{"name": app.Name, "id": app.ID},
		})
	}

	return result, nil
}

// jitClaimStatus buckets a claim row into one of the JITAllocationStatus*
// values. Unlike the old spend-fraction-based grouping (unavoidable when one
// wallet == one recipient sharing a balance that could be partially drained),
// a claim's status is now a plain binary — ClaimedAt set or not — since
// claim_funds either pays a slice out completely or rolls back entirely.
// "expired" only applies to a still-unclaimed row whose wallet's deadline has
// passed.
func jitClaimStatus(claimed bool, expiresAt *int64, now time.Time) string {
	if claimed {
		return JITAllocationStatusClaimed
	}
	if expiresAt != nil && *expiresAt < now.Unix() {
		return JITAllocationStatusExpired
	}
	return JITAllocationStatusUnclaimed
}

// ListJITWalletClaims returns a page of a jit_hub's recipient slices (one row
// per JITWalletClaim, across every jit_wallet child), newest first. limit ==
// 0 returns every row unpaginated. status filters by JITAllocationStatus*
// ("" means unfiltered); the returned counts always reflect the full,
// unfiltered set regardless of status or paging.
func (api *api) ListJITWalletClaims(appID uint, limit uint64, offset uint64, status string) ([]JITWalletClaimResponse, uint64, JITWalletClaimCounts, error) {
	var app db.App
	if err := api.db.First(&app, appID).Error; err != nil {
		return nil, 0, JITWalletClaimCounts{}, fmt.Errorf("app not found: %w", err)
	}
	if app.Kind != db.AppKindJITHub {
		return nil, 0, JITWalletClaimCounts{}, fmt.Errorf("app is not a jit_hub")
	}

	rows, err := api.appsSvc.ListJITWalletClaims(appID)
	if err != nil {
		return nil, 0, JITWalletClaimCounts{}, err
	}

	result := make([]JITWalletClaimResponse, 0, len(rows))
	for _, row := range rows {
		r := JITWalletClaimResponse{
			ID:            row.ID,
			WalletAppID:   row.WalletAppID,
			IdentityType:  row.IdentityType,
			IdentityValue: row.IdentityValue,
			AmountMloki:   row.AmountMloki,
			Claimed:       row.ClaimedAt != nil,
			CreatedAt:     row.CreatedAt.Unix(),
		}
		if row.ClaimedAt != nil {
			claimedAt := row.ClaimedAt.Unix()
			r.ClaimedAt = &claimedAt
		}
		if row.WalletExpiresAt != nil {
			ts := row.WalletExpiresAt.Unix()
			r.ExpiresAt = &ts
		}
		result = append(result, r)
	}

	// Counts are taken over the full, unfiltered set - computed before the
	// status filter below so a UI's tab counts stay accurate regardless of
	// which tab (if any) is currently selected.
	now := time.Now()
	counts := JITWalletClaimCounts{All: uint64(len(result))}
	for _, r := range result {
		switch jitClaimStatus(r.Claimed, r.ExpiresAt, now) {
		case JITAllocationStatusUnclaimed:
			counts.Unclaimed++
		case JITAllocationStatusClaimed:
			counts.Claimed++
		case JITAllocationStatusExpired:
			counts.Expired++
		}
	}

	if status != "" {
		filtered := make([]JITWalletClaimResponse, 0, len(result))
		for _, r := range result {
			if jitClaimStatus(r.Claimed, r.ExpiresAt, now) == status {
				filtered = append(filtered, r)
			}
		}
		result = filtered
	}

	totalCount := uint64(len(result))
	if limit > 0 {
		result = paginateSlice(result, limit, offset)
	}
	return result, totalCount, counts, nil
}

// DeleteJITWalletClaim removes an unclaimed slice, sweeping its AmountMloki
// back to the hub via an internal transfer before deleting the row — so
// removing one bad recipient from a shared wallet doesn't strand unclaimable
// balance in it. If that was the wallet's last remaining recipient, the now
// claims-less wallet is reclaimed and deleted too: ListJITWalletClaims is an
// inner join starting from the claims table (apps/jit_hub_service.go), so a
// wallet with zero claims can never appear there under any filter - left
// behind, it would still count against its hub's apps.DeleteApp child-count
// guard forever, with no way for an operator to ever find and remove it
// through the normal listing/delete flow again.
func (api *api) DeleteJITWalletClaim(hubAppID uint, walletAppID uint, claimID uint) error {
	var hub db.App
	if err := api.db.First(&hub, hubAppID).Error; err != nil {
		return fmt.Errorf("app not found: %w", err)
	}
	if hub.Kind != db.AppKindJITHub {
		return fmt.Errorf("app is not a jit_hub")
	}

	var wallet db.App
	if err := api.db.Where("id = ? AND parent_app_id = ? AND parent_kind = ? AND kind = ?",
		walletAppID, hubAppID, db.ParentKindJIT, db.AppKindJITWallet).First(&wallet).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: JIT wallet not found for this hub", constants.ErrInvalidParams)
		}
		return err
	}

	claim, err := api.appsSvc.DeleteJITWalletClaim(walletAppID, claimID)
	if err != nil {
		return err
	}

	if claim.AmountMloki > 0 && wallet.ParentAppID != nil {
		invoice, err := api.svc.GetTransactionsService().MakeInvoice(
			context.Background(), uint64(claim.AmountMloki), "jit claim removed: sweep back to hub", "", 0,
			nil, api.svc.GetLNClient(), wallet.ParentAppID, nil, nil, nil, nil, nil, nil,
			&transactions.InternalMakeInvoiceMeta{InternalTransfer: true},
		)
		if err != nil {
			return fmt.Errorf("failed to create sweep-back invoice: %w", err)
		}
		if _, err := api.svc.GetTransactionsService().SendPaymentSync(
			invoice.PaymentRequest, nil, map[string]interface{}{"internal_transfer": true},
			api.svc.GetLNClient(), &walletAppID, nil,
		); err != nil {
			return fmt.Errorf("failed to sweep removed claim's balance back to hub: %w", err)
		}
	}

	remainingClaims, err := api.appsSvc.ListClaimsForWallet(walletAppID)
	if err != nil {
		return fmt.Errorf("failed to check remaining claims: %w", err)
	}
	if len(remainingClaims) > 0 {
		return nil
	}
	return service.ReclaimAndDeleteSubWallet(context.Background(), api.db,
		api.svc.GetTransactionsService(), api.svc.GetLNClient(), wallet)
}

// DeleteJITWallet reclaims any remaining balance of a jit_wallet child back
// to its hub and deletes it (service.ReclaimAndDeleteSubWallet), regardless
// of how much of it has already been spent — the same pattern
// DeleteCircleWalletChild uses for the sibling Circles feature.
func (api *api) DeleteJITWallet(hubAppID uint, walletAppID uint) error {
	var hub db.App
	if err := api.db.First(&hub, hubAppID).Error; err != nil {
		return fmt.Errorf("app not found: %w", err)
	}
	if hub.Kind != db.AppKindJITHub {
		return fmt.Errorf("app is not a jit_hub")
	}

	var wallet db.App
	if err := api.db.Where("id = ? AND parent_app_id = ? AND parent_kind = ? AND kind = ?",
		walletAppID, hubAppID, db.ParentKindJIT, db.AppKindJITWallet).First(&wallet).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: JIT wallet not found for this hub", constants.ErrInvalidParams)
		}
		return err
	}

	return service.ReclaimAndDeleteSubWallet(context.Background(), api.db,
		api.svc.GetTransactionsService(), api.svc.GetLNClient(), wallet)
}

// GetJITWalletConnection returns the NWC pairing URI for an already-created JIT
// wallet. Unlike a normal app's pairing secret (a random key generated once at
// creation and never persisted), a JIT wallet's pairing key is deterministic —
// derived on demand from the wallet's own app ID via keys.GetJITPairingKey,
// specifically so the connection_key claim flow never has to store it. That
// same determinism means it can be safely re-derived here any number of
// times: nothing is rotated or invalidated for a client already using it.
func (api *api) GetJITWalletConnection(appID uint) (*JITWalletConnectionResponse, error) {
	var app db.App
	if err := api.db.First(&app, appID).Error; err != nil {
		return nil, fmt.Errorf("app not found: %w", err)
	}
	if app.Kind != db.AppKindJITWallet {
		return nil, fmt.Errorf("app is not a jit_wallet")
	}
	if app.WalletPubkey == nil {
		return nil, fmt.Errorf("jit wallet has no wallet pubkey")
	}

	pairingSecretKey, err := api.keys.GetJITPairingKey(app.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to derive JIT pairing key: %w", err)
	}

	var b strings.Builder
	b.WriteString("nostr+walletconnect://")
	b.WriteString(*app.WalletPubkey)
	b.WriteString("?relay=")
	b.WriteString(strings.Join(api.cfg.GetRelayUrls(), "&relay="))
	b.WriteString("&secret=")
	b.WriteString(pairingSecretKey)

	return &JITWalletConnectionResponse{PairingURI: b.String()}, nil
}

// GetJITWalletRecipients returns every recipient slice of a single
// jit_wallet, claimed or not — the admin-API counterpart of list_recipients
// (which is scoped to whoever holds the wallet's NWC connection), used by a
// jit_wallet's own AppDetails page. A wallet can serve more than one
// beneficiary now, so this can return more than one row.
func (api *api) GetJITWalletRecipients(appID uint) ([]JITWalletClaimResponse, error) {
	var app db.App
	if err := api.db.First(&app, appID).Error; err != nil {
		return nil, fmt.Errorf("app not found: %w", err)
	}
	if app.Kind != db.AppKindJITWallet {
		return nil, fmt.Errorf("app is not a jit_wallet")
	}

	claims, err := api.appsSvc.ListClaimsForWallet(app.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to list JIT wallet recipients: %w", err)
	}

	result := make([]JITWalletClaimResponse, 0, len(claims))
	for _, c := range claims {
		r := JITWalletClaimResponse{
			ID:            c.ID,
			WalletAppID:   c.WalletAppID,
			IdentityType:  c.IdentityType,
			IdentityValue: c.IdentityValue,
			AmountMloki:   c.AmountMloki,
			Claimed:       c.ClaimedAt != nil,
			CreatedAt:     c.CreatedAt.Unix(),
		}
		if c.ClaimedAt != nil {
			claimedAt := c.ClaimedAt.Unix()
			r.ClaimedAt = &claimedAt
		}
		if app.ExpiresAt != nil {
			ts := app.ExpiresAt.Unix()
			r.ExpiresAt = &ts
		}
		result = append(result, r)
	}
	return result, nil
}

// CreateJITWallet is the admin equivalent of a hub calling create_jit_wallet
// over NWC: it immediately creates, funds, and returns the plaintext pairing
// URI for a shared JIT wallet serving every recipient in the request in one
// shot — the connection is meant to be distributed to the whole recipient
// group by the hub owner afterward.
func (api *api) CreateJITWallet(hubID uint, req *CreateJITWalletRequest) (*CreateJITWalletResponse, error) {
	var hub db.App
	if err := api.db.First(&hub, hubID).Error; err != nil {
		return nil, fmt.Errorf("app not found: %w", err)
	}
	if hub.Kind != db.AppKindJITHub {
		return nil, fmt.Errorf("app is not a jit_hub")
	}

	// Serialize concurrent create_jit_wallet attempts against this hub (across
	// this admin HTTP path and the NWC path,
	// nip47/controllers/create_jit_wallet_controller.go) so two racing
	// requests can't both pass jitwallet.Resolve's balance pre-check against
	// the same stale balance before either one's Commit actually transfers
	// funds out.
	release, ok := jitwallet.LockHub(hub.ID)
	if !ok {
		return nil, fmt.Errorf("%w: wallet creation already in progress for this hub", constants.ErrInvalidParams)
	}
	defer release()

	recipients := make([]jitwallet.RecipientInput, len(req.Recipients))
	for i, r := range req.Recipients {
		if r.AmountMloki < 0 {
			return nil, fmt.Errorf("%w: recipient amount_mloki must not be negative", constants.ErrInvalidParams)
		}
		recipients[i] = jitwallet.RecipientInput{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			IAPubkey:      r.IAPubkey,
			AmountMloki:   uint64(r.AmountMloki),
		}
	}

	result, err := jitwallet.Create(context.Background(), jitwallet.Deps{
		AppsService:         api.appsSvc,
		TransactionsService: api.svc.GetTransactionsService(),
		LNClient:            api.svc.GetLNClient(),
		Keys:                api.keys,
		DB:                  api.db,
		RelayURLs:           api.cfg.GetRelayUrls(),
		IAChecker:           api.iaManager,
	}, jitwallet.Params{
		HubApp:     &hub,
		Recipients: recipients,
		ExpirySecs: req.ExpirySecs,
	})
	if err != nil {
		return nil, err
	}

	recipientResults := make([]JITWalletRecipient, len(result.Recipients))
	for i, r := range result.Recipients {
		recipientResults[i] = JITWalletRecipient{
			IdentityType:  r.IdentityType,
			IdentityValue: r.IdentityValue,
			AmountMloki:   int64(r.AmountMloki), //nolint:gosec // msat amounts are always far below int64 range
		}
	}

	return &CreateJITWalletResponse{
		AppID:      result.WalletApp.ID,
		PairingURI: result.PairingURI,
		ExpiresAt:  result.ExpiresAt.Unix(),
		Recipients: recipientResults,
	}, nil
}

// ListIdentityAuthorities returns every registered Identity Authority.
func (api *api) ListIdentityAuthorities() ([]IdentityAuthorityResponse, error) {
	authorities, err := api.iaManager.List()
	if err != nil {
		return nil, err
	}
	result := make([]IdentityAuthorityResponse, 0, len(authorities))
	for _, a := range authorities {
		result = append(result, identityAuthorityToResponse(a))
	}
	return result, nil
}

// AddIdentityAuthority registers a new trusted Identity Authority.
func (api *api) AddIdentityAuthority(req *AddIdentityAuthorityRequest) (*IdentityAuthorityResponse, error) {
	authority, err := api.iaManager.Add(req.Pubkey, req.Name, req.RelayURLs)
	if err != nil {
		return nil, err
	}
	response := identityAuthorityToResponse(*authority)
	return &response, nil
}

// DeleteIdentityAuthority removes an Identity Authority from the trusted registry.
func (api *api) DeleteIdentityAuthority(pubkey string) error {
	return api.iaManager.Delete(pubkey)
}

func identityAuthorityToResponse(a apps.IdentityAuthority) IdentityAuthorityResponse {
	var relayURLs []string
	if a.RelayURLs != "" {
		relayURLs = strings.Split(a.RelayURLs, ",")
	}
	return IdentityAuthorityResponse{
		Pubkey:    a.Pubkey,
		Name:      a.Name,
		RelayURLs: relayURLs,
		CreatedAt: a.CreatedAt.Unix(),
	}
}
