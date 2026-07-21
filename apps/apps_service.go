package apps

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/logger"
	"github.com/nbd-wtf/go-nostr"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type AppsService interface {
	// CreateApp creates a new NWC app connection.
	// kind must be one of the db.AppKind* constants.
	// parentAppID / parentKind are set for sub-wallets (jit_wallet, circle_child).
	CreateApp(name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
		expiresAt *time.Time, scopes []string, kind string,
		parentAppID *uint, parentKind string,
		metadata map[string]interface{}) (*db.App, string, error)
	// CreateAppTx is CreateApp run inside a caller-provided transaction instead of
	// opening its own. Use this when app creation must be atomic with another
	// check made in the same transaction (e.g. a Postgres advisory lock guarding
	// a budget-commitment check) — see create_circle_wallet_controller.go and
	// create_jit_wallet_controller.go's allocation-claim path.
	//
	// Unlike CreateApp, this does NOT publish "nwc_app_created" itself — the
	// caller's transaction is not guaranteed committed yet when CreateAppTx
	// returns (there may be more work left in the same transaction, e.g. a
	// membership-uniqueness insert), and createAppConsumer's event handler
	// looks the new app up via a separate, non-transactional connection. If
	// the event fired before commit, that lookup would race the commit and
	// could miss it, permanently skipping the new app's relay subscription
	// setup with no retry. The caller MUST call NotifyAppCreated(app) itself,
	// once its own transaction has successfully committed.
	CreateAppTx(tx *gorm.DB, name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
		expiresAt *time.Time, scopes []string, kind string,
		parentAppID *uint, parentKind string,
		metadata map[string]interface{}) (*db.App, string, error)
	// NotifyAppCreated publishes the "nwc_app_created" event for app, which
	// (among other things) triggers the new app's relay subscription setup.
	// CreateApp calls this itself, after its own transaction commits. Callers
	// of CreateAppTx must call this explicitly once their own outer
	// transaction has committed — see CreateAppTx's doc comment.
	NotifyAppCreated(app *db.App)
	// CreateJITHub creates a jit_hub app and persists its JITHubConfig.
	CreateJITHub(name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
		expiresAt *time.Time, scopes []string, metadata map[string]interface{},
		config db.JITHubConfig) (*db.App, string, error)
	// GetJITHubConfig returns the JITHubConfig for a jit_hub app.
	GetJITHubConfig(appID uint) (*db.JITHubConfig, error)
	// UpdateJITHubConfig updates a jit_hub's PerWalletMaxMloki and/or MaxExpSecs.
	// A nil pointer leaves that field unchanged; a non-nil pointer must be positive.
	UpdateJITHubConfig(appID uint, perWalletMaxMloki *int, maxExpSecs *int) error
	// CreateCircleHub creates a circle_hub app and persists its CircleHubConfig,
	// attaching it to either an existing CircleIdentity (identityRef.ExistingID) or a
	// brand-new one created from identityRef's remaining fields.
	CreateCircleHub(name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
		expiresAt *time.Time, scopes []string, metadata map[string]interface{},
		identityRef CircleIdentityRef, config db.CircleHubConfig) (*db.App, string, error)
	// GetCircleHubConfig returns the CircleHubConfig (with CircleIdentity preloaded)
	// for a circle_hub app.
	GetCircleHubConfig(appID uint) (*db.CircleHubConfig, error)
	// UpdateCircleHubConfig updates a circle_hub's MaxExpSecs, FeesPpm,
	// PerWalletMaxMloki, and/or MinBudgetRenewal. A nil pointer leaves that
	// field unchanged.
	UpdateCircleHubConfig(appID uint, maxExpSecs *int, feesPpm *int, perWalletMaxMloki *int, minBudgetRenewal *string) error
	// CreateCircleIdentity creates a standalone, reusable CircleIdentity.
	CreateCircleIdentity(name, policy, providerPubkey string) (*db.CircleIdentity, error)
	// GetCircleIdentity returns a CircleIdentity by ID.
	GetCircleIdentity(id uint) (*db.CircleIdentity, error)
	// ListCircleIdentities returns every CircleIdentity, ordered by ID.
	ListCircleIdentities() ([]db.CircleIdentity, error)
	// DeleteCircleIdentity refuses (ErrInvalidParams) if any circle_hub still
	// references the identity; otherwise deletes it (cascading its allowlist).
	DeleteCircleIdentity(id uint) error
	DeleteApp(app *db.App) error
	GetAppByPubkey(pubkey string) *db.App
	GetAppById(id uint) *db.App
	SetAppMetadata(appId uint, metadata map[string]interface{}) error
	HasLightningAddress(app *db.App) bool

	// CreateJITWalletClaims batch-inserts one JITWalletClaim row per recipient
	// of a freshly-created, shared jit_wallet app. Called once by
	// jitwallet.Commit right after the wallet app itself is created.
	CreateJITWalletClaims(walletAppID uint, entries []db.JITWalletClaim) error
	// ListClaimsForWallet returns every recipient slice (claimed or not) of a
	// single jit_wallet app — the roster the list_recipients NIP-47 method
	// exposes. Ordered by created_at asc.
	ListClaimsForWallet(walletAppID uint) ([]db.JITWalletClaim, error)
	// ListJITHubWalletChildren returns every jit_wallet app that is a child of
	// hubID, queried directly from apps. Ordered by created_at asc.
	ListJITHubWalletChildren(hubID uint) ([]db.App, error)
	// ListJITWalletClaims returns every JITWalletClaim belonging to any
	// jit_wallet child of hubID, joined with its wallet's ExpiresAt, newest
	// first, unfiltered/unpaginated — the caller applies status filtering,
	// counts, and pagination in memory.
	ListJITWalletClaims(hubID uint) ([]JITWalletClaimRow, error)
	// GetJITWalletClaim is a read-only lookup of one recipient's still-unclaimed
	// slice, used by claim_funds to verify identity/attestation proof (which
	// needs the row's IAPubkey for connection_key mode) BEFORE attempting the
	// atomic claim — so an invalid/unverifiable proof never touches the atomic
	// slot at all, which would otherwise let anyone who merely knows a
	// recipient's identity_value (exposed via list_recipients) briefly grief a
	// legitimate concurrent claimer without ever proving ownership. Returns
	// nil, nil if no unclaimed row matches.
	GetJITWalletClaim(walletAppID uint, identityType, identityValue string) (*db.JITWalletClaim, error)
	// ClaimJITWalletSlice atomically marks one recipient's slice claimed, guarded
	// by "WHERE claimed_at IS NULL" so concurrent/replayed claims can't double-pay.
	// Returns the claimed row's AmountMloki, or a constants.ErrInvalidParams-wrapped
	// error if no matching unclaimed row exists (never existed, wrong identity, or
	// already claimed — all three look identical to the caller by design).
	ClaimJITWalletSlice(walletAppID uint, identityType, identityValue string) (amountMloki int64, err error)
	// UnclaimJITWalletSlice reverts ClaimJITWalletSlice, guarded so it can only
	// undo the exact claim it's asked to. Used to roll back a slice claim when
	// the invoice-amount check or the payment itself subsequently fails.
	UnclaimJITWalletSlice(walletAppID uint, identityType, identityValue string) error
	// DeleteJITWalletClaim removes an unclaimed slice from a jit_wallet. Returns
	// an error if the claim is not found, belongs to a different wallet, or has
	// already been claimed. The caller is responsible for sweeping the slice's
	// AmountMloki back to the hub before calling this.
	DeleteJITWalletClaim(walletAppID uint, claimID uint) (*db.JITWalletClaim, error)
}

// JITWalletClaimRow is one row of ListJITWalletClaims' result — a
// JITWalletClaim joined with its wallet's ExpiresAt.
type JITWalletClaimRow struct {
	db.JITWalletClaim
	WalletExpiresAt *time.Time
}

type appsService struct {
	db             *gorm.DB
	eventPublisher events.EventPublisher
	keys           keys.Keys
	cfg            config.Config
}

func NewAppsService(db *gorm.DB, eventPublisher events.EventPublisher, keys keys.Keys, cfg config.Config) *appsService {
	return &appsService{
		db:             db,
		eventPublisher: eventPublisher,
		keys:           keys,
		cfg:            cfg,
	}
}

func (svc *appsService) CreateApp(name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
	expiresAt *time.Time, scopes []string, kind string,
	parentAppID *uint, parentKind string,
	metadata map[string]interface{}) (*db.App, string, error) {

	app, pairingSecretKey, err := svc.prepareApp(svc.db, name, pubkey, budgetRenewal, scopes, kind, parentAppID, parentKind, expiresAt, metadata)
	if err != nil {
		return nil, "", err
	}

	err = svc.db.Transaction(func(tx *gorm.DB) error {
		return svc.saveAppTx(tx, app, scopes, maxAmountLoki, budgetRenewal, expiresAt)
	})

	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save app")
		return nil, "", err
	}

	svc.NotifyAppCreated(app)

	return app, pairingSecretKey, nil
}

// CreateAppTx is CreateApp run inside a caller-provided transaction. See the
// interface doc comment for why this exists (atomicity with an advisory lock
// held by the caller) and why, unlike CreateApp, it does not call
// NotifyAppCreated itself.
func (svc *appsService) CreateAppTx(tx *gorm.DB, name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
	expiresAt *time.Time, scopes []string, kind string,
	parentAppID *uint, parentKind string,
	metadata map[string]interface{}) (*db.App, string, error) {

	app, pairingSecretKey, err := svc.prepareApp(tx, name, pubkey, budgetRenewal, scopes, kind, parentAppID, parentKind, expiresAt, metadata)
	if err != nil {
		return nil, "", err
	}

	if err := svc.saveAppTx(tx, app, scopes, maxAmountLoki, budgetRenewal, expiresAt); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save app")
		return nil, "", err
	}

	return app, pairingSecretKey, nil
}

// NotifyAppCreated publishes "nwc_app_created" for app. See the AppsService
// interface doc comment on CreateAppTx for why this is a separate method
// rather than something CreateAppTx calls itself.
func (svc *appsService) NotifyAppCreated(app *db.App) {
	svc.eventPublisher.Publish(&events.Event{
		Event: "nwc_app_created",
		Properties: map[string]interface{}{
			"name": app.Name,
			"id":   app.ID,
		},
	})
}

// prepareApp validates inputs, resolves the pairing keypair and a free name,
// and builds the App row — everything that doesn't need to happen inside the
// eventual save transaction. Shared by CreateApp and CreateAppTx. queryDB is
// the handle its two uniqueness-check reads run against — CreateApp passes
// svc.db (no transaction open yet), CreateAppTx passes the caller's tx. This
// must be the caller's own transaction handle when called from CreateAppTx,
// not svc.db: an independent read via svc.db while the caller's tx is still
// open raced that open transaction (both contending on the same underlying
// SQLite file lock) and could stall indefinitely — found via
// create_circle_wallet_controller.go, the only CreateAppTx caller,
// deadlocking inside its own outer transaction.
func (svc *appsService) prepareApp(queryDB *gorm.DB, name string, pubkey string, budgetRenewal string, scopes []string, kind string,
	parentAppID *uint, parentKind string, expiresAt *time.Time, metadata map[string]interface{}) (*db.App, string, error) {

	if name == "" {
		return nil, "", errors.New("no app name provided")
	}

	if kind == "" {
		kind = db.AppKindStandard
	}

	if db.IsIsolatedKind(kind) {
		if slices.Contains(scopes, constants.SIGN_MESSAGE_SCOPE) {
			return nil, "", errors.New("sub-wallet app connection cannot have sign_message scope")
		}
	}

	if budgetRenewal == "" {
		budgetRenewal = constants.BUDGET_RENEWAL_NEVER
	}

	if !slices.Contains(constants.GetBudgetRenewals(), budgetRenewal) {
		return nil, "", fmt.Errorf("invalid budget renewal. Must be one of %s", strings.Join(constants.GetBudgetRenewals(), ","))
	}

	if len(scopes) == 0 {
		return nil, "", errors.New("no scopes provided")
	}

	var pairingPublicKey string
	var pairingSecretKey string
	var err error
	if pubkey == "" {
		pairingSecretKey = nostr.GeneratePrivateKey()
		pairingPublicKey, err = nostr.GetPublicKey(pairingSecretKey)
		if err != nil {
			return nil, "", err
		}
	} else {
		pairingPublicKey = pubkey
		decoded, err := hex.DecodeString(pairingPublicKey)
		if err != nil || len(decoded) != 32 {
			logger.Logger.Error().Interface("pairingPublicKey", pairingPublicKey).Msg("Invalid public key format")
			return nil, "", fmt.Errorf("invalid public key format: %s", pairingPublicKey)
		}
	}

	var metadataBytes []byte
	if metadata != nil {
		metadataBytes, err = json.Marshal(metadata)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to serialize metadata")
			return nil, "", err
		}
	}

	// use a suffix to avoid duplicate names
	nameIndex := 0
	var freeName string
	for ; ; nameIndex++ {
		freeName = name
		if nameIndex > 0 {
			freeName += fmt.Sprintf(" (%d)", nameIndex)
		}
		var existingApp db.App
		err := queryDB.Where("name = ?", freeName).First(&existingApp).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break
		}
		if err != nil {
			return nil, "", err
		}
	}

	var existingByPubkey db.App
	err = queryDB.Where("app_pubkey = ?", pairingPublicKey).First(&existingByPubkey).Error
	if err == nil {
		return nil, "", errors.New("duplicated key not allowed")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", err
	}

	app := &db.App{
		Name:        freeName,
		AppPubkey:   pairingPublicKey,
		Kind:        kind,
		ParentAppID: parentAppID,
		ParentKind:  parentKind,
		ExpiresAt:   expiresAt,
		Metadata:    datatypes.JSON(metadataBytes),
	}

	return app, pairingSecretKey, nil
}

// saveAppTx persists app, its AppPermission rows, and its derived wallet
// pubkey, all within tx. Shared by CreateApp (which wraps it in its own
// transaction) and CreateAppTx (which runs it inside the caller's).
func (svc *appsService) saveAppTx(tx *gorm.DB, app *db.App, scopes []string, maxAmountLoki uint64,
	budgetRenewal string, expiresAt *time.Time) error {

	if app.ParentAppID != nil {
		if err := svc.verifyParentHubTx(tx, *app.ParentAppID, app.ParentKind); err != nil {
			return err
		}
	}

	if err := tx.Save(app).Error; err != nil {
		return err
	}

	for _, scope := range scopes {
		appPermission := db.AppPermission{
			App:           *app,
			Scope:         scope,
			ExpiresAt:     expiresAt,
			MaxAmountLoki: int(maxAmountLoki),
			BudgetRenewal: budgetRenewal,
		}
		if err := tx.Create(&appPermission).Error; err != nil {
			return err
		}
	}

	appWalletPrivKey, err := svc.keys.GetAppWalletKey(app.ID)
	if err != nil {
		return fmt.Errorf("error generating wallet child private key: %w", err)
	}

	appWalletPubkey, err := nostr.GetPublicKey(appWalletPrivKey)
	if err != nil {
		return fmt.Errorf("error generating wallet child public key: %w", err)
	}

	return tx.Model(app).Update("wallet_pubkey", appWalletPubkey).Error
}

// verifyParentHubTx checks, within tx, that parentAppID still exists and is
// the hub kind parentKind expects. Called before inserting a circle_wallet/
// jit_wallet child, inside the same transaction as the insert, so a
// concurrent DeleteApp on that hub can't race a child creation into
// orphaning it: App.ParentAppID has no DB-level cascade. On Postgres, an
// advisory lock keyed by the hub's own ID serializes this check+insert
// against deleteHubAppTx's own check+delete across processes — the same lock
// and keyspace create_circle_wallet_controller.go already uses for its own
// commitment check. Sqlite needs no explicit lock: its transactions
// serialize by default (only one writer active at a time), so wrapping the
// check and the insert in one transaction — which saveAppTx's callers
// already do — is sufficient there.
func (svc *appsService) verifyParentHubTx(tx *gorm.DB, parentAppID uint, parentKind string) error {
	expectedHubKind, ok := map[string]string{
		db.ParentKindCircle: db.AppKindCircleHub,
		db.ParentKindJIT:    db.AppKindJITHub,
	}[parentKind]
	if !ok {
		return fmt.Errorf("%w: unrecognized parent_kind %q", constants.ErrInvalidParams, parentKind)
	}

	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(parentAppID)).Error; err != nil {
			return fmt.Errorf("acquire parent hub lock: %w", err)
		}
	}

	var parent db.App
	if err := tx.First(&parent, parentAppID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: parent app %d not found", constants.ErrInvalidParams, parentAppID)
		}
		return err
	}
	if parent.Kind != expectedHubKind {
		return fmt.Errorf("%w: parent app %d is not a %s", constants.ErrInvalidParams, parentAppID, expectedHubKind)
	}
	return nil
}

func (svc *appsService) DeleteApp(app *db.App) error {
	var err error
	switch app.Kind {
	case db.AppKindCircleHub, db.AppKindJITHub:
		err = svc.deleteHubAppTx(app)
	default:
		err = svc.db.Delete(app).Error
	}
	if err != nil {
		return err
	}
	svc.eventPublisher.Publish(&events.Event{
		Event: "nwc_app_deleted",
		Properties: map[string]interface{}{
			"name": app.Name,
			"id":   app.ID,
		},
	})
	return nil
}

// deleteHubAppTx deletes a circle_hub/jit_hub app, refusing if it still has
// children (App.ParentAppID has no DB-level cascade, so a plain delete would
// orphan them). The child-count check and the delete run inside one
// transaction, with the same Postgres advisory lock (keyed by this hub's own
// ID) that verifyParentHubTx takes before inserting a new child — otherwise a
// concurrent circle_wallet/jit_wallet creation for this exact hub could still
// commit in the gap between a separate count-then-delete, orphaning it. On
// sqlite no explicit lock is needed: wrapping the count and the delete in one
// transaction is sufficient on its own, since sqlite transactions serialize
// by default (only one writer active at a time).
func (svc *appsService) deleteHubAppTx(app *db.App) error {
	parentKind, kindLabel, hint := db.ParentKindCircle, "circle_hub", "member wallet(s); use the circle delete endpoint instead"
	if app.Kind == db.AppKindJITHub {
		// Same orphan hazard as circle_hub, but for jit_hub: a jit_wallet
		// child's periodic reclaim (jit_cleanup_service.ReclaimAndDeleteSubWallet)
		// would try to credit a parent app row that no longer exists — a
		// FOREIGN KEY violation on every cleanup tick, forever.
		parentKind, kindLabel, hint = db.ParentKindJIT, "jit_hub", "issued wallet(s); wait for them to expire/reclaim or delete them first"
	}

	return svc.db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(app.ID)).Error; err != nil {
				return fmt.Errorf("acquire hub delete lock: %w", err)
			}
		}

		var childCount int64
		if err := tx.Model(&db.App{}).
			Where("parent_app_id = ? AND parent_kind = ?", app.ID, parentKind).
			Count(&childCount).Error; err != nil {
			return err
		}
		if childCount > 0 {
			// This generic delete has no cascade/balance-safety for the hub's
			// children — refuse rather than silently orphan them. Circle hubs
			// have a dedicated POST /api/apps/:id/circle/delete flow
			// (api.DeleteCircleHub) that checks each child's balance and lets
			// the caller choose how to proceed instead.
			return fmt.Errorf("%w: %s still has %d %s", constants.ErrInvalidParams, kindLabel, childCount, hint)
		}

		return tx.Delete(app).Error
	})
}

func (svc *appsService) GetAppByPubkey(pubkey string) *db.App {
	dbApp := db.App{}
	findResult := svc.db.Where("app_pubkey = ?", pubkey).First(&dbApp)
	if findResult.RowsAffected == 0 {
		return nil
	}
	return &dbApp
}

func (svc *appsService) GetAppById(id uint) *db.App {
	dbApp := db.App{}
	findResult := svc.db.Where("id = ?", id).First(&dbApp)
	if findResult.RowsAffected == 0 {
		return nil
	}
	return &dbApp
}

func (svc *appsService) GetAppByName(name string) *db.App {
	dbApp := db.App{}
	findResult := svc.db.Where("name = ?", name).First(&dbApp)
	if findResult.RowsAffected == 0 {
		return nil
	}
	return &dbApp
}

func (svc *appsService) SetAppMetadata(id uint, metadata map[string]interface{}) error {
	var metadataBytes []byte
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to serialize metadata")
		return err
	}

	err = svc.db.Model(&db.App{}).Where("id", id).Update("metadata", datatypes.JSON(metadataBytes)).Error
	if err != nil {
		logger.Logger.Error().Err(err).Interface("metadata", metadata).Msg("failed to update transaction metadata")
		return err
	}

	return nil
}

func (svc *appsService) HasLightningAddress(app *db.App) bool {
	if app.Metadata == nil {
		return false
	}

	var metadata map[string]interface{}
	err := json.Unmarshal(app.Metadata, &metadata)
	if err != nil {
		return false
	}

	lud16, exists := metadata["lud16"]
	return exists && lud16 != nil
}
