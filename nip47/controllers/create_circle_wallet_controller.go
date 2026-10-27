package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"gorm.io/gorm"
)

// circleRateLimitPerHour is the fallback used by tests, which build a
// config.AppConfig literal directly rather than through envconfig.Process
// (so its struct-tag default never applies). Runtime callers always go
// through controller.cfg.GetEnv().CircleWalletRateLimitPerHour instead.
const circleRateLimitPerHour = 3

// errCircleQuotaExceeded rolls a join back whose circle's holdings have reached
// the holdings ceiling.
var errCircleQuotaExceeded = errors.New("the circle's holdings have reached its ceiling")

// errJoinPastDeadline rolls a join back that reached its commit after its
// proof's freshness window (NIP-CW §Processing Algorithm, step 9).
var errJoinPastDeadline = errors.New("join reached commit after its deadline")

// joinErrorCode and joinErrorReason read a nipcw join refusal.
func joinErrorCode(err error) string {
	var joinErr *nipcw.JoinProofError
	if errors.As(err, &joinErr) {
		return joinErr.Code
	}
	return constants.ERROR_BAD_REQUEST
}

func joinErrorReason(err error) string {
	var joinErr *nipcw.JoinProofError
	if errors.As(err, &joinErr) {
		return joinErr.Reason
	}
	return err.Error()
}

// errCircleMemberAlreadyHasWallet is a sentinel for the one-active-wallet-
// per-(hub,identity) guard — see the CircleWalletMembership insert below.
var errCircleMemberAlreadyHasWallet = errors.New("identity already has an active circle wallet under this hub")

// create_circle_wallet's request is github.com/ohstr/nmilat/nipcw's own
// exported CreateCircleWalletRequest (Pubkey/MaxAmount/Expiry/BudgetRenewal/
// IdentityEvent) — same wire shape, field-for-field identical JSON tags to
// this controller's former local createCircleWalletParams, adopted directly
// instead of maintaining a parallel copy (nmilat migration). The
// response stays this controller's own createCircleWalletResponse: nipcw's
// own wire response type is unexported (it carries a still-NIP-44-encrypted
// encrypted_details blob nipcw only exposes post-decryption to its own client
// callers), so there's no exported type to adopt for it.

// createCircleWalletResponse carries exactly one field on purpose — see
// circleWalletDetails. Anything added here is public to the whole circle.
type createCircleWalletResponse struct {
	EncryptedDetails string `json:"encrypted_details"`
}

// HandleCreateCircleWalletEvent serves a join (NIP-CW §Processing Algorithm), in
// order: the params (BAD_REQUEST, nothing recorded), whether the Hub accepts
// joins (RESTRICTED), the identity proof (UNAUTHORIZED) and its single-use
// record (REQUEST_REUSED), membership (NOT_READY, RESTRICTED), one wallet per
// member (WALLET_EXISTS), the Hub's limits (LIMIT_EXCEEDED, naming the field),
// the rate limit (RATE_LIMITED), then one transaction: the holdings check
// (INSUFFICIENT_CAPACITY), the connection key (RESTRICTED when in use), the
// membership row (WALLET_EXISTS) and the proof's freshness at commit
// (BAD_REQUEST). Joins are served on the private transport only.
func (controller *nip47Controller) HandleCreateCircleWalletEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	method := nip47Request.Method
	refuse := func(code, message string) { respondError(publishResponse, method, code, message) }

	if app.Kind != db.AppKindCircleHub {
		refuse(constants.ERROR_NOT_IMPLEMENTED, "create_circle_wallet is served by a circle_hub only")
		return
	}

	// Step 1: the params' presence and types; the rest of step 1 is checked by
	// VerifyJoinProof before any proof check.
	params, err := nipcw.ParseCreateCircleWalletRequest(nip47Request.Params)
	if err != nil {
		refuse(joinErrorCode(err), joinErrorReason(err))
		return
	}

	logger.Logger.Info().Uint("app_id", app.ID).Str("requester", params.Pubkey).Msg("Handling create_circle_wallet request")

	providerConfig, err := controller.appsService.GetCircleHubConfig(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to load Circle Hub config")
		refuse(constants.ERROR_INTERNAL, "failed to load Circle Hub config")
		return
	}
	if app.WalletPubkey == nil {
		logger.Logger.Error().Uint("app_id", app.ID).Msg("Circle Hub has no wallet pubkey set")
		refuse(constants.ERROR_INTERNAL, "circle hub is not fully initialized")
		return
	}
	// The member reaches the wallet only through the relays sent back, so a Hub
	// with none must not issue one (NIP-CW §Response).
	relays := controller.cfg.GetRelayUrls()
	if len(relays) == 0 {
		logger.Logger.Error().Uint("app_id", app.ID).Msg("No relay configured; refusing to issue an unreachable circle wallet")
		refuse(constants.ERROR_INTERNAL, "this hub has no relay to offer")
		return
	}

	// A Hub that is not accepting joins refuses every one, consuming nothing.
	if providerConfig.AcceptingJoins != nil && !*providerConfig.AcceptingJoins {
		refuse(constants.ERROR_RESTRICTED, "this circle is not accepting new members")
		return
	}

	// Steps 1–3: the rest of the params, the identity proof bound to this Hub's
	// pubkey (the circlehub1 string's TLV 0), this connection_pubkey and these
	// terms, then its single-use record, recorded here before any later step.
	var recordErr error
	consumed := func(id string) bool {
		err := controller.db.Create(&db.CircleWalletIdentityProof{AppID: app.ID, EventID: id, Kind: db.CircleProofJoin,
			RetainUntil: nipcw.JoinProofRetainUntil(eventCreatedAt(params.IdentityEvent))}).Error
		if err == nil {
			return false
		}
		if !db.IsDuplicateKeyError(err) {
			recordErr = err
		}
		return true
	}
	_, err = nipcw.VerifyJoinProof(params, *app.WalletPubkey, time.Now(), consumed)
	if recordErr != nil {
		logger.Logger.Error().Err(recordErr).Uint("app_id", app.ID).Msg("Failed to record a circle join's identity proof")
		refuse(constants.ERROR_INTERNAL, "failed to record identity_event, please retry")
		return
	}
	if err != nil {
		refuse(joinErrorCode(err), joinErrorReason(err))
		return
	}
	proofCreatedAt := eventCreatedAt(params.IdentityEvent)

	// Step 4: membership.
	authorized, err := controller.socialCache.IsAuthorized(ctx, params.Pubkey, &providerConfig.CircleIdentity, controller.db)
	if err != nil {
		if !errors.Is(err, constants.ErrSocialCacheWarmingUp) && !errors.Is(err, constants.ErrCircleMembershipUnresolved) {
			logger.Logger.Error().Err(err).Str("requester", params.Pubkey).Msg("Social cache authorization check failed")
		}
		// Membership cannot be decided now: NOT_READY, never a refusal.
		refuse(nipcw.ErrNotReady, "the circle's membership could not be checked yet, please retry shortly")
		return
	}
	if !authorized {
		refuse(nipcw.ErrRestricted, "requester is not a member of this circle")
		return
	}

	// Step 5: one wallet per member; the membership row's unique index inside the
	// transaction is the authoritative guard. A wallet past its expiry is
	// Expired whether or not the lifecycle job has recorded it, and no longer
	// counts: record that first (NIP-CW §Lifecycle and Deletion).
	if err := controller.expireDueMembership(app.ID, params.Pubkey, providerConfig.GraceSecs); err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to record an expired circle wallet")
		refuse(constants.ERROR_INTERNAL, "membership check failed")
		return
	}
	var existingMembershipCount int64
	if err := controller.db.Model(&db.CircleWalletMembership{}).
		Where("circle_hub_app_id = ? AND requester_pubkey = ?", app.ID, params.Pubkey).
		Count(&existingMembershipCount).Error; err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to check existing circle wallet membership")
		refuse(constants.ERROR_INTERNAL, "membership check failed")
		return
	}
	if existingMembershipCount > 0 {
		refuse(nipcw.ErrWalletExists, errCircleMemberAlreadyHasWallet.Error())
		return
	}

	// Step 6: the Hub's limits, each refusal naming its field.
	limit := func(field string) { refuse(nipcw.ErrLimitExceeded, field+" exceeds the Hub's limit") }
	if providerConfig.PerWalletMaxMloki > 0 && params.MaxAmount > uint64(providerConfig.PerWalletMaxMloki) {
		limit("max_amount")
		return
	}
	if params.MaxReceive > uint64(providerConfig.PerWalletMaxReceiveMloki) { //nolint:gosec // validated positive
		limit("max_receive")
		return
	}
	// Paying out over Lightning holds back at least the fee reserve on top of the
	// amount, so a lower cap makes a wallet that can never pay outside this node.
	if params.MaxAmount < constants.MIN_CIRCLE_WALLET_MAX_MLOKI {
		refuse(nipcw.ErrLimitExceeded, fmt.Sprintf("max_amount is below the Hub's minimum of %d", constants.MIN_CIRCLE_WALLET_MAX_MLOKI))
		return
	}
	resolvedBudgetRenewal := params.BudgetRenewal
	if resolvedBudgetRenewal == "" {
		resolvedBudgetRenewal = constants.BUDGET_RENEWAL_NEVER
	}
	if constants.BudgetRenewalRank(resolvedBudgetRenewal) < constants.BudgetRenewalRank(providerConfig.MinBudgetRenewal) {
		limit("budget_renewal")
		return
	}
	if params.Expiry > uint64(constants.MAX_EXPIRY_SECS) || (providerConfig.MaxExpSecs > 0 && params.Expiry > uint64(providerConfig.MaxExpSecs)) { //nolint:gosec // non-negative
		limit("expiry")
		return
	}
	// An omitted or zero expiry takes the Hub's default lifetime, never its
	// ceiling (NIP-CW §Request); a default of 0 never expires.
	expirySecs := int(params.Expiry) //nolint:gosec // bounded above
	if expirySecs == 0 {
		expirySecs = providerConfig.DefaultExpSecs
	}
	var expiresAt *time.Time
	if expirySecs > 0 {
		at := time.Now().Add(time.Duration(expirySecs) * time.Second)
		expiresAt = &at
	}

	// Step 7: the rate limit per requester pubkey.
	if !controller.circleRateLimiter.Allow(params.Pubkey, controller.cfg.GetEnv().CircleWalletRateLimitPerHour) {
		refuse(nipcw.ErrRateLimited, "rate limit exceeded for create_circle_wallet")
		return
	}

	// 6. Concurrent requests for the same Hub are serialised by the database,
	// not in process: production builds a new controller per request, so a
	// controller-local guard would never see a second request. The transaction
	// below takes a Hub-scoped lock (a Postgres advisory lock; SQLite opens
	// every write transaction with BEGIN IMMEDIATE), and the membership row's
	// unique index is the authoritative one-wallet-per-identity guard.

	circleScopes := []string{
		constants.MAKE_INVOICE_SCOPE,
		constants.PAY_INVOICE_SCOPE,
		constants.GET_BALANCE_SCOPE,
		constants.LOOKUP_INVOICE_SCOPE,
		constants.LIST_TRANSACTIONS_SCOPE,
		constants.GET_INFO_SCOPE,
		// The notifications of NIP-CW §Recovering a Lost Connection.
		constants.NOTIFICATIONS_SCOPE,
	}

	// 9. The holdings check and the wallet's creation as one atomic unit. On
	// Postgres, an advisory lock on the Hub scoped to this transaction
	// serialises concurrent joins across processes, and every holdings check
	// of the circle's payments takes the same lock.
	var newApp *db.App
	var encryptedDetails string
	err = controller.db.Transaction(func(tx *gorm.DB) error {
		if err := db.LockApp(tx, app.ID); err != nil {
			return fmt.Errorf("acquire circle lock: %w", err)
		}
		// The connection key is checked and claimed as one atomic step with
		// every rotation's acceptance (NIP-CW §Recovering a Lost Connection).
		if err := db.LockNamespace(tx, db.LockNsConnKey, 0); err != nil {
			return fmt.Errorf("acquire connection key lock: %w", err)
		}
		if taken, err := circlelife.ConnectionKeyTaken(tx, params.ConnectionPubkey); err != nil {
			return err
		} else if taken {
			return apps.ErrDuplicateAppPubkey
		}

		// The circle's holdings must be below its holdings ceiling (NIP-CW
		// §Processing Algorithm step 9): a new wallet starts empty, so the
		// check is the circle's room, not the requested caps.
		holdings, err := circlemoney.Holdings(tx, app.ID, time.Now())
		if err != nil {
			return fmt.Errorf("holdings check failed: %w", err)
		}
		if holdings >= providerConfig.HoldingsCeilingMloki {
			return errCircleQuotaExceeded
		}

		// The member's own connection_pubkey is the wallet's client key, so no
		// pairing secret is generated here, or anywhere (NIP-CW §Pairing
		// Connection). A key already authorized for another connection on this
		// node is refused, and rolls back everything this transaction did.
		createdApp, _, err := controller.appsService.CreateAppTx(tx,
			apps.GenerateChildName(app.Name, params.Pubkey),
			params.ConnectionPubkey,
			params.MaxAmount/1000,
			resolvedBudgetRenewal,
			expiresAt,
			circleScopes,
			db.AppKindCircleWallet,
			&app.ID,
			db.ParentKindCircle,
			map[string]interface{}{"requester_pubkey": params.Pubkey},
		)
		if err != nil {
			return err
		}

		// NIP-CW §Fees: the member's rate is the one in force now, and stays;
		// its receive cap is the one it asked for.
		// Its budget periods count from its creation (NIP-CW §Budget Renewal Floor).
		// It starts Active, with the Hub's dust threshold fixed for its life.
		maxReceive := int64(params.MaxReceive) //nolint:gosec // bounded by the Hub's ceiling above
		if err := tx.Model(&db.App{}).Where("id = ?", createdApp.ID).Updates(map[string]interface{}{
			"circle_fees_ppm":             providerConfig.FeesPpm,
			"circle_max_receive_mloki":    maxReceive,
			"circle_budget_anchor_at":     createdApp.CreatedAt.Unix(),
			"circle_state":                circlelife.StateActive,
			"circle_state_at":             createdApp.CreatedAt.Unix(),
			"circle_dust_threshold_mloki": providerConfig.DustThresholdMloki,
			"circle_inbox_key_index":      constants.PRIVATE_TRANSPORT_KEY_INDEX,
			"circle_rotation_delay_secs":  rotationDelay(providerConfig),
		}).Error; err != nil {
			return fmt.Errorf("failed to record the member's terms: %w", err)
		}
		// The wallet's expiry is its own (apps.expires_at): an Expired wallet
		// still pays out, so no permission of it expires.
		if err := tx.Model(&db.AppPermission{}).Where("app_id = ?", createdApp.ID).Update("expires_at", nil).Error; err != nil {
			return fmt.Errorf("failed to record the wallet's expiry: %w", err)
		}

		// One active wallet per identity per hub, atomic guard: a unique-
		// constraint violation here means a concurrent request for the same
		// identity won the race since the pre-check above — roll back the
		// whole transaction, including the App/permission rows just created.
		if err := tx.Create(&db.CircleWalletMembership{
			CircleHubAppID:  app.ID,
			RequesterPubkey: params.Pubkey,
			WalletAppID:     createdApp.ID,
		}).Error; err != nil {
			return errCircleMemberAlreadyHasWallet
		}

		// The details, encrypted once, are the join's record: a lost reply is
		// answered with exactly these bytes (NIP-CW §Lost Join Reply).
		encrypted, err := controller.joinDetails(app, createdApp, params, providerConfig, relays, expiresAt, resolvedBudgetRenewal)
		if err != nil {
			return err
		}
		// A record of an earlier join with the same key whose wallet is gone gives
		// way to this one: one record answers each (signer, connection_pubkey).
		if err := tx.Where("authorizer_pubkey = ? AND connection_pubkey = ? AND NOT EXISTS (SELECT 1 FROM apps WHERE apps.id = circle_join_records.wallet_app_id)",
			params.Pubkey, params.ConnectionPubkey).Delete(&db.CircleJoinRecord{}).Error; err != nil {
			return fmt.Errorf("failed to replace an earlier join record: %w", err)
		}
		if err := tx.Create(&db.CircleJoinRecord{
			CircleHubAppID:   app.ID,
			HubWalletPubkey:  *app.WalletPubkey,
			HubConnPubkey:    app.AppPubkey,
			AuthorizerPubkey: params.Pubkey,
			ConnectionPubkey: params.ConnectionPubkey,
			ProofCreatedAt:   proofCreatedAt,
			Deadline:         proofCreatedAt + nipcw.JoinFreshness,
			EncryptedDetails: encrypted,
			WalletAppID:      createdApp.ID,
		}).Error; err != nil {
			return fmt.Errorf("failed to record the join: %w", err)
		}
		encryptedDetails = encrypted

		// The join may not commit once its proof has left its freshness window.
		if time.Now().Unix() > proofCreatedAt+nipcw.JoinFreshness {
			return errJoinPastDeadline
		}

		newApp = createdApp
		return nil
	})

	switch {
	case errors.Is(err, errCircleQuotaExceeded):
		refuse(nipcw.ErrInsufficientCapacity, "the circle's holdings have reached its ceiling")
		return
	case errors.Is(err, errCircleMemberAlreadyHasWallet):
		refuse(nipcw.ErrWalletExists, errCircleMemberAlreadyHasWallet.Error())
		return
	case errors.Is(err, apps.ErrDuplicateAppPubkey):
		refuse(nipcw.ErrRestricted, "connection_pubkey is in use on this node: generate a new connection key")
		return
	case errors.Is(err, errJoinPastDeadline):
		refuse(constants.ERROR_BAD_REQUEST, "join reached commit after its deadline")
		return
	case err != nil:
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to create a circle wallet")
		refuse(constants.ERROR_INTERNAL, "failed to create the circle wallet")
		return
	}

	// The outer transaction above has now committed, so newApp is durably
	// visible to other connections — only now is it safe to publish
	// nwc_app_created (see CreateAppTx's doc comment). Publishing any earlier
	// would race createAppConsumer's event-driven lookup against this
	// transaction's own commit, silently skipping the new wallet's relay
	// subscription setup with no retry — a wallet that would then never
	// receive any NIP-47 request for its entire lifetime.
	controller.appsService.NotifyAppCreated(newApp)

	logger.Logger.Info().
		Uint("circle_wallet_id", newApp.ID).
		Uint("parent_app_id", app.ID).
		Msg("Circle wallet created")

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result: createCircleWalletResponse{
			EncryptedDetails: encryptedDetails,
		},
	}, nostr.Tags{})
}

// joinDetails is a join's encrypted_details (NIP-CW §Response): every term of
// the issued wallet and no secret, encrypted from the Hub connection key to the
// requester. The member builds its own connection from wallet_pubkey, relays
// and the secret it kept.
func (controller *nip47Controller) joinDetails(hub, wallet *db.App, params nipcw.CreateCircleWalletRequest, config *db.CircleHubConfig,
	relays []string, expiresAt *time.Time, budgetRenewal string) (string, error) {
	// Encrypted from the Hub's own key: the requester's only prior trust anchor is
	// the Hub connection they dialed, so the new wallet's key, merely asserted
	// inside this reply, must not vouch for it.
	hubWalletPrivKey, err := controller.keys.GetAppWalletKey(hub.ID)
	if err != nil {
		return "", fmt.Errorf("derive the circle hub key: %w", err)
	}
	var expiresAtUnix int64
	if expiresAt != nil {
		expiresAtUnix = expiresAt.Unix()
	}
	detailsJSON, err := json.Marshal(nipcw.CircleWalletDetails{
		WalletPubkey:     *wallet.WalletPubkey,
		ConnectionPubkey: params.ConnectionPubkey,
		Relays:           relays,
		MaxAmount:        params.MaxAmount / 1000 * 1000,
		// max_receive is applied as requested, within the Hub's ceiling.
		MaxReceive:          params.MaxReceive,
		ExpiresAt:           expiresAtUnix,
		FeesPpm:             uint64(config.FeesPpm),            //nolint:gosec // fees_ppm is validated non-negative
		DustThresholdMillis: uint64(config.DustThresholdMloki), //nolint:gosec // validated non-negative
		RotationDelaySecs:   rotationDelayDetail(config),
		BudgetRenewal:       budgetRenewal,
	})
	if err != nil {
		return "", err
	}
	return encryptForRecipient(params.Pubkey, hubWalletPrivKey, string(detailsJSON))
}

// expireDueMembership records as Expired a member's wallet under hub whose
// expiry has passed, which frees the member to join again.
func (controller *nip47Controller) expireDueMembership(hubID uint, member string, graceSecs int64) error {
	return controller.db.Transaction(func(tx *gorm.DB) error {
		var membership db.CircleWalletMembership
		if err := tx.Where("circle_hub_app_id = ? AND requester_pubkey = ?", hubID, member).Limit(1).Find(&membership).Error; err != nil || membership.ID == 0 {
			return err
		}
		var wallet db.App
		if err := tx.Where("id = ?", membership.WalletAppID).Limit(1).Find(&wallet).Error; err != nil || wallet.ID == 0 {
			return err
		}
		now := time.Now()
		if circlelife.EffectiveState(&wallet, now) != circlelife.StateExpired || circlelife.State(&wallet) == circlelife.StateExpired {
			return nil
		}
		if err := db.LockApp(tx, hubID); err != nil {
			return err
		}
		return circlelife.Transition(tx, &wallet, circlelife.EventExpire, now, graceSecs)
	})
}

// rotationDelay is a new wallet's rotation delay: the Hub's, when it serves
// rotation, fixed for the wallet's life; nil otherwise.
func rotationDelay(cfg *db.CircleHubConfig) *int64 {
	if !cfg.RotationEnabled {
		return nil
	}
	d := cfg.RotationDelaySecs
	return &d
}

func rotationDelayDetail(cfg *db.CircleHubConfig) *uint64 {
	if d := rotationDelay(cfg); d != nil {
		v := uint64(*d) //nolint:gosec // validated positive
		return &v
	}
	return nil
}
