package controllers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nodesign"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
)

type circleWalletInfo struct {
	AvailableMillis int64      `json:"available_millis"`
	MaxExpSecs      int        `json:"max_exp_secs"`
	FeesPpm         int        `json:"fees_ppm"`
	FeeReserve      feeReserve `json:"fee_reserve"`
	// DustThresholdMillis: a balance at or below it is dust; what a new wallet
	// gets (NIP-CW §Lifecycle and Deletion).
	DustThresholdMillis int64  `json:"dust_threshold_millis"`
	CirclePolicy        string `json:"circle_policy"`
	// Identity is the node key that signs the kind-11190 announcement, x-only,
	// equal to the circlehub1 string's TLV 4 (NIP-CW §The Circle Wallet Hub
	// Connection). Always present.
	Identity string `json:"identity"`
	// PerWalletMaxMillis, PerWalletMaxReceiveMillis and MinBudgetRenewal are the other limits a join is
	// checked against (NIP-CW get_info), so a client can validate before asking.
	PerWalletMaxMillis        int64  `json:"per_wallet_max_millis,omitempty"`
	PerWalletMaxReceiveMillis int64  `json:"per_wallet_max_receive_millis,omitempty"`
	MinBudgetRenewal          string `json:"min_budget_renewal,omitempty"`
	// Accepting is false once the host closes the circle to new members.
	Accepting bool `json:"accepting"`
	// RotationDelaySecs is present iff the Hub serves NIP-CW §Recovering a
	// Lost Connection.
	RotationDelaySecs *int64 `json:"rotation_delay_secs,omitempty"`
}

// feeReserve is the most routing fee a payment holds back: ppm of the amount,
// rounded down, at least min_millis (circlemoney.FeeReserve).
type feeReserve struct {
	Ppm       int64 `json:"ppm"`
	MinMillis int64 `json:"min_millis"`
}

type getInfoResponse struct {
	Alias   *string `json:"alias"`
	Color   *string `json:"color"`
	Pubkey  *string `json:"pubkey"`
	Network *string `json:"network"`
	// Chain is the chain's name, on every connection a Cash Hub serves (NIP-CASH
	// §Network and Chain).
	Chain       *string  `json:"chain,omitempty"`
	BlockHeight *uint32  `json:"block_height"`
	BlockHash   *string  `json:"block_hash"`
	Methods     []string `json:"methods"`
	// PrivateMethods names granted methods this connection does NOT serve, because they
	// travel over the NIP-CASH private transport (§Scope Surface). Omitted when empty, so
	// only a bill carries it.
	//
	// Without it there is no wire-level signal that the bill methods exist at all: they
	// were removed from Methods because 23194 refuses them, and the kind-11190
	// announcement carries only inbox, limits and relays.
	PrivateMethods   []string          `json:"private_methods,omitempty"`
	Notifications    []string          `json:"notifications"`
	Metadata         interface{}       `json:"metadata,omitempty"`
	LightningAddress *string           `json:"lud16"`
	CircleWallet     *circleWalletInfo `json:"circle_wallet,omitempty"`
	// CashOrder is an Order connection's selling terms (NIP-CO §Terms): {} when
	// it is retired or its Hub sells nothing.
	CashOrder json.RawMessage `json:"cash_order,omitempty"`
}

func (controller *nip47Controller) HandleGetInfoEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc) {
	supportedNotifications := []string{}
	if controller.permissionsService.PermitsNotifications(app) {
		supportedNotifications = controller.lnClient.GetSupportedNIP47NotificationTypes()
	}

	responsePayload := &getInfoResponse{
		Methods: controller.permissionsService.GetPermittedMethods(app, controller.lnClient),
		// The complement of Methods: granted, but served on the private transport. Derived
		// from this app's own grants, so it never claims a method this bill lacks.
		PrivateMethods: controller.permissionsService.GetPrivateMethods(app),
		Notifications:  supportedNotifications,
	}

	// basic permissions check
	// this is inconsistent with other methods. Ideally we move fetching node info to a separate method,
	// so that get_info does not require its own scope. This would require a change in the NIP-47 spec.
	hasPermission, _, _ := controller.permissionsService.HasPermission(app, constants.GET_INFO_SCOPE)
	if hasPermission {
		logger.Logger.Debug().
			Interface("request_event_id", requestEventId).
			Msg("Getting info")

		info, err := controller.lnClient.GetInfo(ctx)
		if err != nil {
			logger.Logger.Info().Err(err).
				Interface("request_event_id", requestEventId).
				Msg("Failed to fetch node info")

			publishResponse(&models.Response{
				ResultType: nip47Request.Method,
				Error:      mapNip47Error(err),
			}, nostr.Tags{})
			return
		}

		network := info.Network
		// Some implementations return "flokicoin" while NIP47 expects "mainnet"
		if network == "flokicoin" {
			network = "mainnet"
		}

		responsePayload.Alias = &info.Alias
		responsePayload.Color = &info.Color
		responsePayload.Pubkey = &info.Pubkey
		responsePayload.Network = &network
		responsePayload.BlockHeight = &info.BlockHeight
		responsePayload.BlockHash = &info.BlockHash

		if app != nil {
			metadata := map[string]interface{}{}
			if app.Metadata != nil {
				jsonErr := json.Unmarshal(app.Metadata, &metadata)
				if jsonErr != nil {
					logger.Logger.Error().Err(jsonErr).
						Uint("id", app.ID).
						Interface("metadata", app.Metadata).
						Msg("Failed to deserialize app metadata")
				}
			}
			if metadata["id"] == nil {
				metadata["id"] = app.ID
			}
			if metadata["name"] == nil {
				metadata["name"] = app.Name
			}
			if !app.IsIsolated() {
				lightningAddress, _ := controller.cfg.Get("LightningAddress", "")
				responsePayload.LightningAddress = &lightningAddress
			} else if metadata["app_store_app_id"] == constants.SUBWALLET_APPSTORE_APP_ID && metadata["lud16"] != nil {
				lightningAddress := metadata["lud16"].(string)
				responsePayload.LightningAddress = &lightningAddress
			}

			responsePayload.Metadata = metadata
		}
	}

	// Every connection a Cash Hub serves answers network and chain, whatever its
	// scopes (NIP-CASH §Network and Chain), and a Cash Hub its node identity as
	// pubkey, x-only, equal to its cashhub1 string's TLV 4.
	if app != nil && isCashConnection(app.Kind) {
		info, err := controller.lnClient.GetInfo(ctx)
		if err != nil {
			publishResponse(&models.Response{ResultType: nip47Request.Method, Error: mapNip47Error(err)}, nostr.Tags{})
			return
		}
		network := cashNetwork(info.Network)
		chain := nodesign.Chain
		responsePayload.Network, responsePayload.Chain = &network, &chain
		if app.Kind == db.AppKindCashOrder {
			if err := controller.orderConnectionInfo(app, responsePayload); err != nil {
				logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to read an order connection's terms")
				publishResponse(&models.Response{ResultType: nip47Request.Method,
					Error: &models.Error{Code: constants.ERROR_INTERNAL, Message: "could not read this connection's terms"}}, nostr.Tags{})
				return
			}
		}
		if app.Kind == db.AppKindCashHub || app.Kind == db.AppKindCircleHub {
			// The node identity that signs the announcement and every string.
			identity, err := nipcash.MinterXOnly(controller.lnClient.GetPubkey())
			if err != nil {
				publishResponse(&models.Response{ResultType: nip47Request.Method,
					Error: &models.Error{Code: constants.ERROR_INTERNAL, Message: "could not read the node identity"}}, nostr.Tags{})
				return
			}
			if app.Kind == db.AppKindCashHub {
				responsePayload.Pubkey = &identity
			} else {
				circle, err := controller.circleWalletTerms(app, identity)
				if err != nil {
					publishResponse(&models.Response{ResultType: nip47Request.Method,
						Error: &models.Error{Code: constants.ERROR_INTERNAL, Message: "could not read this circle's terms"}}, nostr.Tags{})
					return
				}
				responsePayload.CircleWallet = circle
			}
		}
	}

	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     responsePayload,
	}, nostr.Tags{})
}

// isCashConnection reports the app kinds a Cash Hub serves: its own connection,
// its bills, Circle Wallet Hubs and their wallets.
func isCashConnection(kind string) bool {
	switch kind {
	case db.AppKindCashHub, db.AppKindCashWallet, db.AppKindCircleHub, db.AppKindCircleWallet, db.AppKindCashOrder:
		return true
	}
	return false
}

// cashNetwork is NIP-47's network for the node's: flnd reports mainnet as
// "bitcoin" or "mainnet" (and an older build "flokicoin"); the test networks
// keep their names.
func cashNetwork(nodeNetwork string) string {
	return nodesign.Network(nodeNetwork)
}

// circleWalletTerms is a Circle Wallet Hub's circle_wallet block (NIP-CW §The
// Circle Wallet Hub Connection): every term always present. available_millis is
// what the circle can still take in, at least 0, capped at the per-wallet
// ceiling (the MAY cap).
func (controller *nip47Controller) circleWalletTerms(app *db.App, identity string) (*circleWalletInfo, error) {
	providerConfig, err := controller.appsService.GetCircleHubConfig(app.ID)
	if err != nil {
		return nil, err
	}
	available, err := circlemoney.Available(controller.db, app.ID, providerConfig, time.Now())
	if err != nil {
		return nil, err
	}
	if perWallet := int64(providerConfig.PerWalletMaxMloki); perWallet > 0 {
		available = min(available, perWallet)
	}
	return &circleWalletInfo{
		AvailableMillis:           available,
		MaxExpSecs:                providerConfig.MaxExpSecs,
		FeesPpm:                   providerConfig.FeesPpm,
		FeeReserve:                feeReserve{Ppm: int64(providerConfig.FeeReservePpm), MinMillis: providerConfig.FeeReserveMinMloki},
		DustThresholdMillis:       providerConfig.DustThresholdMloki,
		RotationDelaySecs:         rotationDelay(providerConfig),
		CirclePolicy:              providerConfig.CircleIdentity.Policy,
		Identity:                  identity,
		PerWalletMaxMillis:        int64(providerConfig.PerWalletMaxMloki),
		PerWalletMaxReceiveMillis: providerConfig.PerWalletMaxReceiveMloki,
		MinBudgetRenewal:          providerConfig.MinBudgetRenewal,
		Accepting:                 providerConfig.AcceptingJoins == nil || *providerConfig.AcceptingJoins,
	}, nil
}

// orderConnectionInfo is an Order connection's get_info (NIP-CO §Terms): get_info
// on this transport, cash_lookup and cash_order on the private one, the Hub
// identity as pubkey, and the Hub's terms, or {} once the connection is retired
// (from the moment the retirement commits) or while its Hub sells nothing.
func (controller *nip47Controller) orderConnectionInfo(app *db.App, out *getInfoResponse) error {
	identity, err := nipcash.MinterXOnly(controller.lnClient.GetPubkey())
	if err != nil {
		return err
	}
	out.Pubkey = &identity
	out.Methods = []string{models.GET_INFO_METHOD}
	out.PrivateMethods = []string{constants.NIP47MethodCashLookup, constants.NIP47MethodCashOrder}
	out.Alias, out.Color, out.BlockHeight, out.BlockHash, out.Metadata, out.LightningAddress = nil, nil, nil, nil, nil, nil
	out.CashOrder = json.RawMessage("{}")
	conn, err := cashorder.ConnectionOf(controller.db, app.ID)
	if err != nil || conn == nil || conn.RetiredAt != nil {
		return err
	}
	settings, ok, err := cashorder.GetSettings(controller.db, conn.HubAppID)
	if err != nil || !ok || settings.Terms.SellsNothing() {
		return err
	}
	terms, err := json.Marshal(settings.Terms)
	if err != nil {
		return err
	}
	out.CashOrder = terms
	return nil
}
