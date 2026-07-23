package swaps

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chainutil"
	"github.com/flokiorg/go-flokicoin/chainutil/hdkeychain"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/lightzapp/lightz-client/pkg/lightz"
	"github.com/rs/zerolog"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Swap = db.Swap

// lightzWebsocket abstracts *lightz.Websocket so the dispatch goroutine and
// per-swap listeners can be tested with a mock without a live WebSocket server.
// UpdatesChan returns the channel that receives swap status updates; it wraps
// the concrete Updates field because interface methods cannot be struct fields.
type lightzWebsocket interface {
	Connect() error
	Close() error
	Subscribe(swapIds []string) error
	Unsubscribe(swapId string)
	Connected() bool
	Reconnect() error
	UpdatesChan() <-chan lightz.SwapUpdate
}

// wsWrapper adapts *lightz.Websocket to satisfy lightzWebsocket.
type wsWrapper struct{ *lightz.Websocket }

func (w *wsWrapper) UpdatesChan() <-chan lightz.SwapUpdate { return w.Updates }

type swapsService struct {
	autoSwapOutCancelFn context.CancelFunc
	db                  *gorm.DB
	ctx                 context.Context
	lnClient            lnclient.LNClient
	cfg                 config.Config
	keys                keys.Keys
	eventPublisher      events.EventPublisher
	transactionsService transactions.TransactionsService
	lightzApi           *lightz.Api
	lightzWs            lightzWebsocket
	swapListeners       map[string]chan lightz.SwapUpdate
	swapListenersLock   sync.Mutex
	logger              zerolog.Logger
}

type SwapsService interface {
	StopAutoSwapOut()
	EnableAutoSwapOut() error
	SwapOut(amount uint64, destination string, autoSwap, usedXpubDerivation bool) (*SwapResponse, error)
	SwapIn(amount uint64, autoSwap bool) (*SwapResponse, error)
	GetSwapOutInfo() (*SwapInfo, error)
	GetSwapInInfo() (*SwapInfo, error)
	RefundSwap(swapId, address string, enableRetries bool) error
	GetSwap(swapId string) (*Swap, error)
	ListSwaps() ([]Swap, error)
	Reload()
}

const (
	LokiSwapServiceFee = 1.0
)

type FeeRates struct {
	FastestFee  uint64 `json:"fastestFee"`
	HalfHourFee uint64 `json:"halfHourFee"`
	HourFee     uint64 `json:"hourFee"`
	EconomyFee  uint64 `json:"economyFee"`
	MinimumFee  uint64 `json:"minimumFee"`
}

type TxStatusInfo struct {
	Confirmed   bool   `json:"confirmed"`
	BlockHeight uint32 `json:"block_height"`
	BlockHash   string `json:"block_hash"`
	BlockTime   uint64 `json:"block_time"`
}

type MempoolTx struct {
	TxId   string       `json:"txid"`
	Status TxStatusInfo `json:"status"`
}

type SwapInfo struct {
	LokiServiceFee  float64 `json:"lokiServiceFee"`
	BoltzServiceFee float64 `json:"lightzServiceFee"`
	BoltzNetworkFee uint64  `json:"lightzNetworkFee"`
	MinAmount       uint64  `json:"minAmount"`
	MaxAmount       uint64  `json:"maxAmount"`
}

type SwapResponse struct {
	SwapId      string `json:"swapId"`
	PaymentHash string `json:"paymentHash"`
}

// ... (struct definitions)

func (svc *swapsService) Reload() {
	if svc.cfg.EnableSwap() {
		if svc.lightzWs == nil {
			svc.logger.Info().Msg("Starting swap service...")
			svc.Start()
		} else if svc.lightzApi.URL != svc.cfg.GetSwapServiceURL() {
			svc.logger.Info().Msg("Swap Service URL changed, restarting...")
			svc.Stop()
			svc.Start()
		} else {
			svc.logger.Info().Msg("Swap Service already running")
		}
	} else {
		if svc.lightzWs != nil {
			svc.logger.Info().Msg("Stopping swap service...")
			svc.Stop()
		}
	}
}

func (svc *swapsService) Start() {
	svc.lightzApi = &lightz.Api{URL: svc.cfg.GetSwapServiceURL()}
	svc.lightzWs = &wsWrapper{svc.lightzApi.NewWebsocket()}
	go svc.runConnectAndDispatch()
}

// runConnectAndDispatch connects to the websocket, re-subscribes any in-flight
// swaps, then dispatches updates to per-swap listener channels until the
// websocket is closed. Extracted so tests can inject a mock lightzWebsocket.
func (svc *swapsService) runConnectAndDispatch() {
	ws := svc.lightzWs // snapshot — owned for this goroutine's lifetime; prevents TOCTOU race with Stop()
	if ws == nil {
		return
	}
	for {
		err := ws.Connect()
		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to connect to swap service websocket, retrying in 2s...")
			time.Sleep(2 * time.Second)
			continue
		}
		break
	}
	svc.logger.Info().Msg("Connected to swap service websocket")

	// Re-subscribe any in-progress swaps so that goroutines waiting on
	// their per-swap channels are not orphaned after a Reload().
	svc.swapListenersLock.Lock()
	activeIds := make([]string, 0, len(svc.swapListeners))
	for id := range svc.swapListeners {
		activeIds = append(activeIds, id)
	}
	svc.swapListenersLock.Unlock()
	if len(activeIds) > 0 {
		if err := ws.Subscribe(activeIds); err != nil {
			svc.logger.Error().Err(err).Msg("Failed to re-subscribe active swaps after reconnect")
		}
	}

	for {
		update, ok := <-ws.UpdatesChan()
		if !ok {
			svc.logger.Error().Msg("Received error from swap service websocket")
			return // Exit goroutine if channel is closed, likely due to reloading again
		}

		svc.swapListenersLock.Lock()
		ch, ok := svc.swapListeners[update.Id]
		svc.swapListenersLock.Unlock()
		if ok {
			ch <- update
		} else {
			svc.logger.Error().Str("swap_id", update.Id).Msg("Failed to receive update from swap service")
		}
	}
}

func (svc *swapsService) Stop() {
	if svc.lightzWs != nil {
		if err := svc.lightzWs.Close(); err != nil {
			svc.logger.Warn().Err(err).Msg("Failed to close lightz websocket")
		}
		svc.lightzWs = nil
	}
}

func NewSwapsService(ctx context.Context, db *gorm.DB, cfg config.Config, keys keys.Keys, eventPublisher events.EventPublisher,
	lnClient lnclient.LNClient, transactionsService transactions.TransactionsService) SwapsService {
	svc := &swapsService{
		ctx:                 ctx,
		cfg:                 cfg,
		db:                  db,
		keys:                keys,
		eventPublisher:      eventPublisher,
		transactionsService: transactionsService,
		lnClient:            lnClient,
		swapListeners:       make(map[string]chan lightz.SwapUpdate),
		logger:              logger.Logger.With().Str("component", "swaps").Logger(),
	}

	svc.Reload()

	err := svc.EnableAutoSwapOut()
	if err != nil {
		svc.logger.Error().Err(err).Msg("Couldn't enable auto swaps")
	}

	go svc.subscribePendingSwaps()

	return svc
}

func (svc *swapsService) StopAutoSwapOut() {
	if svc.autoSwapOutCancelFn != nil {
		svc.logger.Info().Msg("Stopping auto swap out service...")
		svc.autoSwapOutCancelFn()
		svc.logger.Info().Msg("Auto swap out service stopped")
	}
}

func (svc *swapsService) EnableAutoSwapOut() error {
	svc.StopAutoSwapOut()

	ctx, cancelFn := context.WithCancel(svc.ctx)
	swapDestination, _ := svc.cfg.Get(config.AutoSwapDestinationKey, "")
	balanceThresholdStr, _ := svc.cfg.Get(config.AutoSwapBalanceThresholdKey, "")
	amountStr, _ := svc.cfg.Get(config.AutoSwapAmountKey, "")

	if balanceThresholdStr == "" || amountStr == "" {
		cancelFn()
		svc.logger.Info().Msg("Auto swap not configured")
		return nil
	}

	balanceThreshold, err := strconv.ParseUint(balanceThresholdStr, 10, 64)
	if err != nil {
		cancelFn()
		return errors.New("invalid auto swap configuration")
	}

	amount, err := strconv.ParseUint(amountStr, 10, 64)
	if err != nil {
		cancelFn()
		return errors.New("invalid auto swap configuration")
	}

	svc.logger.Info().Msg("Starting auto swap workflow")

	go func() {
		for {
			select {
			case <-time.After(1 * time.Hour):
				svc.logger.Debug().Msg("Checking to see if we can swap")
				balance, err := svc.lnClient.GetBalances(ctx, false)
				if err != nil {
					svc.logger.Error().Err(err).Msg("Failed to get balance")
					continue
				}
				lightningBalance := uint64(balance.Lightning.TotalSpendable)
				balanceThresholdMilliSats := balanceThreshold * 1000
				if lightningBalance < balanceThresholdMilliSats {
					svc.logger.Info().Msg("Threshold requirements not met for swap, ignoring")
					continue
				}

				actualDestination := swapDestination
				var usedXpubDerivation bool
				if swapDestination != "" {
					if err := svc.validateXpub(swapDestination); err == nil {
						actualDestination, err = svc.getNextUnusedAddressFromXpub()
						if err != nil {
							svc.logger.Error().Err(err).Msg("Failed to get next address from xpub")
							continue
						}
						usedXpubDerivation = true
					}
				}

				svc.logger.Info().
					Uint64("amount", amount).
					Str("destination", actualDestination).
					Msg("Initiating swap")
				_, err = svc.SwapOut(amount, actualDestination, true, usedXpubDerivation)
				if err != nil {
					svc.logger.Error().Err(err).Msg("Failed to initiate swap")
					continue
				}
			case <-ctx.Done():
				svc.logger.Info().Msg("Stopping auto swap workflow")
				return
			}
		}
	}()

	svc.autoSwapOutCancelFn = cancelFn

	return nil
}

func (svc *swapsService) SwapOut(amount uint64, destination string, autoSwap, usedXpubDerivation bool) (*SwapResponse, error) {
	if !svc.cfg.EnableSwap() {
		return nil, errors.New("swap feature is disabled")
	}
	if destination == "" {
		var err error
		destination, err = svc.lnClient.GetNewOnchainAddress(svc.ctx)
		if err != nil {
			return nil, fmt.Errorf("could not get onchain address from config: %s", err)
		}
	}

	preimage := make([]byte, 32)
	_, err := rand.Read(preimage)
	if err != nil {
		return nil, err
	}
	preimageHash := sha256.Sum256(preimage)
	paymentHash := hex.EncodeToString(preimageHash[:])

	reversePairs, err := svc.lightzApi.GetReversePairs()
	if err != nil {
		return nil, fmt.Errorf("could not get reverse pairs: %s", err)
	}

	pair := lightz.Pair{From: lightz.CurrencyBtc, To: lightz.CurrencyBtc}
	pairInfo, err := lightz.FindPair(pair, reversePairs)
	if err != nil {
		return nil, fmt.Errorf("could not find reverse pair: %s", err)
	}

	fees := pairInfo.Fees
	serviceFeePercentage := lightz.Percentage(fees.Percentage)

	serviceFee := lightz.CalculatePercentage(serviceFeePercentage, amount)
	networkFee := fees.MinerFees.Lockup + fees.MinerFees.Claim

	svc.logger.Info().
		Uint64("serviceFee", serviceFee).
		Uint64("networkFee", networkFee).
		Msg("Calculated fees for swap out")

	lokiFee := &lightz.ExtraFees{
		Percentage: LokiSwapServiceFee,
		Id:         "lokiServiceFee",
	}

	dbSwap := db.Swap{
		Type:               constants.SWAP_TYPE_OUT,
		State:              constants.SWAP_STATE_PENDING,
		DestinationAddress: destination,
		PaymentHash:        paymentHash,
		Preimage:           hex.EncodeToString(preimage),
		AutoSwap:           autoSwap,
		UsedXpub:           usedXpubDerivation,
		ReceiveAmount:      amount,
	}

	var ourKeys *btcec.PrivateKey
	var swap *lightz.CreateReverseSwapResponse

	defer func() {
		if err != nil && dbSwap.ID != 0 {
			svc.logger.Error().Err(err).Msg("Marking swap state as failed")
			svc.markSwapState(&dbSwap, constants.SWAP_STATE_FAILED)
		}
	}()

	err = svc.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Save(&dbSwap).Error
		if err != nil {
			return err
		}

		ourKeys, err = svc.keys.GetSwapKey(dbSwap.ID)
		if err != nil {
			return fmt.Errorf("error generating swap child private key: %w", err)
		}

		swapRequest := lightz.CreateReverseSwapRequest{
			From:           lightz.CurrencyBtc,
			To:             lightz.CurrencyBtc,
			ClaimPublicKey: ourKeys.PubKey().SerializeCompressed(),
			PreimageHash:   preimageHash[:],
			Description:    "Lightning to on-chain swap",
			PairHash:       pairInfo.Hash,
			ReferralId:     "loki",
			ExtraFees:      lokiFee,
			OnchainAmount:  amount + fees.MinerFees.Claim,
		}

		swap, err = svc.lightzApi.CreateReverseSwap(swapRequest)

		if err != nil {
			return fmt.Errorf("could not create swap: %s", err)
		}

		swapTreeJson, err := json.Marshal(swap.SwapTree)
		if err != nil {
			return err
		}

		paymentRequest, err := decodepay.Decode(swap.Invoice)
		if err != nil {
			return fmt.Errorf("failed to decode bolt11 invoice")
		}

		err = tx.Model(&dbSwap).Updates(&db.Swap{
			SwapId:             swap.Id,
			SendAmount:         uint64(paymentRequest.MSat / 1000),
			Invoice:            swap.Invoice,
			LockupAddress:      swap.LockupAddress,
			TimeoutBlockHeight: swap.TimeoutBlockHeight,
			BoltzPubkey:        hex.EncodeToString(swap.RefundPublicKey),
			SwapTree:           datatypes.JSON(swapTreeJson),
		}).Error
		if err != nil {
			return err
		}

		// commit transaction
		return nil
	})

	if err != nil {
		svc.logger.Error().Err(err).
			Str("payment_hash", paymentHash).
			Msg("Failed to save swap")
		return nil, err
	}

	svc.logger.Info().Str("swap_id", swap.Id).Msg("Swap created")

	if autoSwap {
		// block until the swap finishes to ensure we can't do multiple concurrent auto swaps
		svc.startSwapOutListener(&dbSwap)
	} else {
		// run in parallel as we need to return the swap ID in the HTTP response
		go svc.startSwapOutListener(&dbSwap)
	}

	return &SwapResponse{
		SwapId:      swap.Id,
		PaymentHash: paymentHash,
	}, nil
}

func (svc *swapsService) SwapIn(amount uint64, autoSwap bool) (*SwapResponse, error) {
	if !svc.cfg.EnableSwap() {
		return nil, errors.New("swap feature is disabled")
	}
	amountMSat := amount * 1000
	invoice, err := svc.transactionsService.MakeInvoice(svc.ctx, amountMSat, "On-chain to lightning swap", "", 0, nil, svc.lnClient, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	submarinePairs, err := svc.lightzApi.GetSubmarinePairs()
	if err != nil {
		return nil, fmt.Errorf("could not get submarine pairs: %s", err)
	}

	pair := lightz.Pair{From: lightz.CurrencyBtc, To: lightz.CurrencyBtc}
	pairInfo, err := lightz.FindPair(pair, submarinePairs)
	if err != nil {
		return nil, fmt.Errorf("could not find submarine pair: %s", err)
	}

	fees := pairInfo.Fees
	serviceFeePercentage := lightz.Percentage(fees.Percentage)

	serviceFee := lightz.CalculatePercentage(serviceFeePercentage, amount)
	networkFee := fees.MinerFees

	svc.logger.Info().
		Uint64("serviceFee", serviceFee).
		Interface("networkFee", networkFee).
		Msg("Calculated fees for swap in")

	lokiFee := &lightz.ExtraFees{
		Percentage: LokiSwapServiceFee,
		Id:         "lokiServiceFee",
	}

	dbSwap := db.Swap{
		Type:        constants.SWAP_TYPE_IN,
		State:       constants.SWAP_STATE_PENDING,
		Invoice:     invoice.PaymentRequest,
		PaymentHash: invoice.PaymentHash,
		AutoSwap:    autoSwap,
	}

	var ourKeys *btcec.PrivateKey
	var swap *lightz.CreateSwapResponse

	defer func() {
		if err != nil && dbSwap.ID != 0 {
			svc.logger.Error().Err(err).Msg("Marking swap state as failed")
			svc.markSwapState(&dbSwap, constants.SWAP_STATE_FAILED)
		}
	}()

	err = svc.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Save(&dbSwap).Error
		if err != nil {
			return err
		}

		ourKeys, err = svc.keys.GetSwapKey(dbSwap.ID)
		if err != nil {
			return fmt.Errorf("error generating swap child private key: %w", err)
		}

		swap, err = svc.lightzApi.CreateSwap(lightz.CreateSwapRequest{
			From:            lightz.CurrencyBtc,
			To:              lightz.CurrencyBtc,
			RefundPublicKey: ourKeys.PubKey().SerializeCompressed(),
			Invoice:         invoice.PaymentRequest,
			PairHash:        pairInfo.Hash,
			ReferralId:      "loki",
			ExtraFees:       lokiFee,
		})
		if err != nil {
			return fmt.Errorf("could not create swap: %s", err)
		}

		swapTreeJson, err := json.Marshal(swap.SwapTree)
		if err != nil {
			return err
		}

		err = tx.Model(&dbSwap).Updates(&db.Swap{
			SwapId:             swap.Id,
			SendAmount:         swap.ExpectedAmount,
			LockupAddress:      swap.Address,
			TimeoutBlockHeight: swap.TimeoutBlockHeight,
			BoltzPubkey:        hex.EncodeToString(swap.ClaimPublicKey),
			SwapTree:           datatypes.JSON(swapTreeJson),
		}).Error
		if err != nil {
			return err
		}

		// commit transaction
		return nil
	})

	if err != nil {
		svc.logger.Error().Err(err).
			Str("payment_hash", invoice.PaymentHash).
			Msg("Failed to save swap")
		return nil, err
	}

	metadata := map[string]interface{}{
		"swap_id": swap.Id,
	}
	err = svc.transactionsService.SetTransactionMetadata(svc.ctx, invoice.ID, metadata)
	if err != nil {
		svc.logger.Error().Err(err).Fields(map[string]interface{}{
			"swap_id":      swap.Id,
			"payment_hash": invoice.PaymentHash,
			"metadata":     metadata,
		}).Msg("Failed to add swap metadata to lightning payment")
		return nil, err
	}

	svc.logger.Info().Str("swap_id", swap.Id).Msg("Swap created")

	go svc.startSwapInListener(&dbSwap)

	return &SwapResponse{
		SwapId:      swap.Id,
		PaymentHash: invoice.PaymentHash,
	}, nil
}

func (svc *swapsService) GetSwapOutInfo() (*SwapInfo, error) {
	if !svc.cfg.EnableSwap() || svc.lightzApi == nil {
		return nil, errors.New("swap feature is disabled")
	}
	reversePairs, err := svc.lightzApi.GetReversePairs()
	if err != nil {
		return nil, fmt.Errorf("could not get reverse pairs: %s", err)
	}

	pair := lightz.Pair{From: lightz.CurrencyBtc, To: lightz.CurrencyBtc}
	pairInfo, err := lightz.FindPair(pair, reversePairs)
	if err != nil {
		return nil, fmt.Errorf("could not find reverse pair: %s", err)
	}

	fees := pairInfo.Fees
	networkFee := fees.MinerFees.Lockup + fees.MinerFees.Claim
	limits := pairInfo.Limits

	return &SwapInfo{
		LokiServiceFee:  LokiSwapServiceFee,
		BoltzServiceFee: fees.Percentage,
		BoltzNetworkFee: networkFee,
		MinAmount:       limits.Minimal,
		MaxAmount:       limits.Maximal,
	}, nil
}

func (svc *swapsService) GetSwapInInfo() (*SwapInfo, error) {
	if !svc.cfg.EnableSwap() || svc.lightzApi == nil {
		return nil, errors.New("swap feature is disabled")
	}
	submarinePairs, err := svc.lightzApi.GetSubmarinePairs()
	if err != nil {
		return nil, fmt.Errorf("could not get reverse pairs: %s", err)
	}

	pair := lightz.Pair{From: lightz.CurrencyBtc, To: lightz.CurrencyBtc}
	pairInfo, err := lightz.FindPair(pair, submarinePairs)
	if err != nil {
		return nil, fmt.Errorf("could not find reverse pair: %s", err)
	}

	fees := pairInfo.Fees
	limits := pairInfo.Limits

	return &SwapInfo{
		LokiServiceFee:  LokiSwapServiceFee,
		BoltzServiceFee: fees.Percentage,
		BoltzNetworkFee: fees.MinerFees,
		MinAmount:       limits.Minimal,
		MaxAmount:       limits.Maximal,
	}, nil
}

func (svc *swapsService) markSwapState(dbSwap *db.Swap, state string) {
	if svc.db.Limit(1).Find(dbSwap, &db.Swap{
		SwapId: dbSwap.SwapId,
		State:  state,
	}).RowsAffected > 0 {
		svc.logger.Debug().Str("swap_id", dbSwap.SwapId).Str("state", state).Msg("swap already marked")
		return
	}

	dbErr := svc.db.Model(dbSwap).Updates(&db.Swap{
		State: state,
	}).Error
	if dbErr != nil {
		svc.logger.Error().Err(dbErr).Str("swap_id", dbSwap.SwapId).Msg("Failed to update swap state")
	}
}

func (svc *swapsService) RefundSwap(swapId, address string, enableRetries bool) error {
	var swap db.Swap
	query := svc.db.Limit(1).Find(&swap, &db.Swap{
		SwapId: swapId,
	})
	err := query.Error
	if err != nil {
		svc.logger.Error().Err(err).Str("swap_id", swapId).Msg("Failed to lookup swap")
		return err
	}
	if query.RowsAffected == 0 {
		svc.logger.Error().Str("swap_id", swapId).Msg("Could not find swap to process refund")
		return errors.New("Could not find swap")
	}

	if swap.Type != constants.SWAP_TYPE_IN {
		return errors.New("only On-chain -> Lightning swaps can be refunded")
	}

	if swap.ClaimTxId != "" {
		return fmt.Errorf("refund already processed with claim txid: %s", swap.ClaimTxId)
	}

	network, err := lightz.ParseChain(svc.cfg.GetNetwork())
	if err != nil {
		return err
	}

	// Fetch raw hex to construct the lockup transaction
	swapTransactionResp, err := svc.lightzApi.GetSwapTransaction(swapId)
	if err != nil {
		svc.logger.Error().Err(err).Str("swap_id", swapId).Msg("Failed to get lockup tx from swap id")
		return err
	}

	if swap.LockupTxId == "" {
		err = svc.db.Model(&swap).Updates(&db.Swap{
			LockupTxId: swapTransactionResp.Id,
		}).Error
		if err != nil {
			svc.logger.Error().Err(err).
				Str("swap_id", swapId).
				Str("lockupTxId", swapTransactionResp.Id).
				Msg("Failed to save lockup txid to swap")
			return err
		}
	}

	ourKeys, err := svc.keys.GetSwapKey(swap.ID)
	if err != nil {
		return fmt.Errorf("error generating swap child private key: %w", err)
	}

	var serializedTree lightz.SerializedTree
	if err := json.Unmarshal(swap.SwapTree, &serializedTree); err != nil {
		return err
	}

	lightzPubkeyBytes, err := hex.DecodeString(swap.BoltzPubkey)
	if err != nil {
		return fmt.Errorf("invalid lightz pubkey: %v", err)
	}

	lightzPubKey, err := btcec.ParsePubKey(lightzPubkeyBytes)
	if err != nil {
		return err
	}

	decodedPreimageHash, err := hex.DecodeString(swap.PaymentHash)
	if err != nil {
		return fmt.Errorf("invalid preimage hash: %v", err)
	}

	tree := serializedTree.Deserialize()
	if err := tree.Init(lightz.CurrencyBtc, false, ourKeys, lightzPubKey); err != nil {
		return err
	}

	if err := tree.Check(lightz.NormalSwap, swap.TimeoutBlockHeight, decodedPreimageHash); err != nil {
		return err
	}

	if err := tree.CheckAddress(swap.LockupAddress, network, nil); err != nil {
		return err
	}

	lockupTransaction, err := lightz.NewTxFromHex(lightz.CurrencyBtc, swapTransactionResp.Hex, nil)
	if err != nil {
		svc.logger.Error().Err(err).Str("swap_id", swapId).Msg("Failed to build lockup tx from hex")
		return err
	}
	vout, _, err := lockupTransaction.FindVout(network, swap.LockupAddress)
	if err != nil {
		svc.logger.Error().Err(err).Str("swap_id", swapId).Msg("Failed to find lockup address output")
		return err
	}

	if address == "" {
		address, err = svc.lnClient.GetNewOnchainAddress(svc.ctx)
		if err != nil {
			svc.logger.Error().Err(err).Str("swap_id", swapId).Msg("Failed to get new on-chain address from config")
			return err
		}
	}

	err = svc.db.Model(&swap).Updates(&db.Swap{
		RefundAddress: address,
	}).Error
	if err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swapId).
			Str("refundAddress", address).
			Msg("Failed to save refund address to swap")
		return err
	}

	var refundTransaction lightz.Transaction

	for i := 0; ; i++ {
		select {
		case <-svc.ctx.Done():
			svc.logger.Info().Str("swap_id", swapId).Msg("Swap refund context cancelled")
			return nil
		case <-time.After(time.Duration(min(i*5, 30)) * time.Second): // timeout
		}

		nodeInfo, err := svc.lnClient.GetInfo(svc.ctx)
		if err != nil {
			svc.logger.Error().Err(err).
				Str("swap_id", swapId).
				Int("iteration", i).
				Msg("Failed to request node info")
			continue
		}

		feeRates, err := svc.getFeeRates()
		if err != nil {
			svc.logger.Error().Err(err).
				Str("swap_id", swapId).
				Int("iteration", i).
				Msg("Failed to fetch fee rate to create claim transaction")
			continue
		}

		cooperative := swapTransactionResp.TimeoutBlockHeight > nodeInfo.BlockHeight

		fastestFee := float64(feeRates.FastestFee)
		refundTransaction, _, err = lightz.ConstructTransaction(
			network,
			lightz.CurrencyBtc,
			[]lightz.OutputDetails{
				{
					SwapId:             swapId,
					SwapType:           lightz.NormalSwap,
					Address:            address,
					LockupTransaction:  lockupTransaction,
					TimeoutBlockHeight: swapTransactionResp.TimeoutBlockHeight,
					Vout:               vout,
					PrivateKey:         ourKeys,
					SwapTree:           tree,
					Cooperative:        cooperative,
				},
			},
			lightz.Fee{
				SatsPerVbyte: &fastestFee,
			},
			svc.lightzApi,
		)
		if err != nil {
			svc.logger.Error().Err(err).
				Str("swap_id", swapId).
				Int("iteration", i).
				Bool("cooperative", cooperative).
				Msg("Could not create claim transaction refund")
			if enableRetries && cooperative {
				continue
			}
			return err
		}
		break
	}

	vout, _, _ = refundTransaction.FindVout(network, address)
	refundAmount, _ := refundTransaction.VoutValue(vout)

	txHex, err := refundTransaction.Serialize()
	if err != nil {
		svc.logger.Error().Err(err).Str("swap_id", swapId).Msg("Could not serialize refund transaction")
		return err
	}

	// TODO: Replace with LNClient broadcast method to avoid trusting lightz
	claimTxId, err := svc.lightzApi.BroadcastTransaction(lightz.CurrencyBtc, txHex)
	if err != nil {
		svc.logger.Error().Err(err).Str("swap_id", swapId).Msg("Could not broadcast transaction")
		return err
	}

	svc.logger.Info().
		Str("swap_id", swapId).
		Str("claimTxId", claimTxId).
		Msg("Claim transaction broadcasted for refund")

	err = svc.db.Model(&swap).Updates(&db.Swap{
		ClaimTxId:     claimTxId,
		ReceiveAmount: refundAmount,
		State:         constants.SWAP_STATE_REFUNDED,
	}).Error
	if err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swapId).
			Str("claimTxId", claimTxId).
			Msg("Failed to save claim txid to swap")
		return err
	}

	return nil
}

func (svc *swapsService) GetSwap(swapId string) (*Swap, error) {
	var swap db.Swap
	err := svc.db.Limit(1).Find(&swap, &db.Swap{
		SwapId: swapId,
	}).Error

	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to get swap")
		return nil, err
	}

	return &swap, nil
}

func (svc *swapsService) ListSwaps() ([]Swap, error) {
	var swaps []db.Swap
	err := svc.db.Find(&swaps).Error

	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to list swaps")
		return nil, err
	}

	return swaps, nil
}

func (svc *swapsService) subscribePendingSwaps() {
	var swaps []db.Swap
	if err := svc.db.Where("state = ?", constants.SWAP_STATE_PENDING).Find(&swaps).Error; err != nil {
		svc.logger.Error().Err(err).Msg("failed to load pending swaps")
		return
	}
	if len(swaps) == 0 {
		return
	}

	svc.logger.Info().Int("count", len(swaps)).Msg("Resuming pending swaps...")

	ids := make([]string, len(swaps))
	for i, s := range swaps {
		ids[i] = s.SwapId
	}

	for _, swap := range swaps {
		switch swap.Type {
		case constants.SWAP_TYPE_IN:
			go svc.startSwapInListener(&swap)
		case constants.SWAP_TYPE_OUT:
			go svc.startSwapOutListener(&swap)
		}
	}
}

func (svc *swapsService) startSwapInListener(swap *db.Swap) {
	for {
		err := svc.lightzWs.Subscribe([]string{swap.SwapId})
		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to subscribe to lightz websocket, retrying in 2s...")
			time.Sleep(2 * time.Second)
			continue
		}
		break
	}

	svc.logger.Info().Str("swap_id", swap.SwapId).Msg("Subscribed to lightz websocket")

	updateCh := make(chan lightz.SwapUpdate)
	svc.swapListenersLock.Lock()
	svc.swapListeners[swap.SwapId] = updateCh
	svc.swapListenersLock.Unlock()

	var err error
	defer func() {
		svc.swapListenersLock.Lock()
		delete(svc.swapListeners, swap.SwapId)
		svc.swapListenersLock.Unlock()
		svc.lightzWs.Unsubscribe(swap.SwapId)
		if err != nil {
			svc.logger.Error().Err(err).Msg("Marking swap state as failed")
			svc.markSwapState(swap, constants.SWAP_STATE_FAILED)
		}
	}()

	var network *lightz.Network
	network, err = lightz.ParseChain(svc.cfg.GetNetwork())
	if err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to parse network")
		return
	}

	var ourKeys *btcec.PrivateKey
	ourKeys, err = svc.keys.GetSwapKey(swap.ID)
	if err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to generate swap child private key")
		return
	}

	var serializedTree lightz.SerializedTree
	if err = json.Unmarshal(swap.SwapTree, &serializedTree); err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to unmarshal swap tree")
		return
	}

	lightzPubkeyBytes, _ := hex.DecodeString(swap.BoltzPubkey)

	var lightzPubKey *btcec.PublicKey
	lightzPubKey, err = btcec.ParsePubKey(lightzPubkeyBytes)
	if err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to parse lightz pubkey")
		return
	}

	decodedPreimageHash, _ := hex.DecodeString(swap.PaymentHash)

	tree := serializedTree.Deserialize()
	if err = tree.Init(lightz.CurrencyBtc, false, ourKeys, lightzPubKey); err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to initialize swap tree")
		return
	}

	if err = tree.Check(lightz.NormalSwap, swap.TimeoutBlockHeight, decodedPreimageHash); err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to check swap tree")
		return
	}

	if err = tree.CheckAddress(swap.LockupAddress, network, nil); err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to check address")
		return
	}

	paymentRequest, _ := decodepay.Decode(swap.Invoice)
	amount := uint64(paymentRequest.MSat / 1000)

	for {
		select {
		case <-svc.ctx.Done():
			svc.logger.Error().Err(svc.ctx.Err()).
				Str("swap_id", swap.SwapId).
				Msg("Swap in context cancelled")
			return
		case update, ok := <-updateCh:
			if !ok {
				svc.logger.Error().Str("swap_id", update.Id).Msg("Failed to receive update from lightz")
				continue
			}
			if update.Id != swap.SwapId {
				continue
			}
			switch lightz.ParseEvent(update.Status) {
			case lightz.TransactionMempool:
				svc.logger.Info().
					Str("swap_id", swap.SwapId).
					Str("lockupTxId", update.Transaction.Id).
					Msg("Lockup transaction found in mempool")
				err = svc.db.Model(swap).Updates(&db.Swap{
					LockupTxId: update.Transaction.Id,
				}).Error
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Str("lockupTxId", update.Transaction.Id).
						Msg("Failed to save lockup txid to swap")
					return
				}
			case lightz.TransactionConfirmed:
				svc.logger.Info().
					Str("swap_id", swap.SwapId).
					Str("lockupTxId", swap.LockupTxId).
					Msg("Lockup transaction confirmed in mempool")
			case lightz.InvoicePaid:
				svc.markSwapState(swap, constants.SWAP_STATE_SUCCESS)
				err = svc.db.Model(swap).Updates(&db.Swap{
					ReceiveAmount: amount,
				}).Error
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Uint64("receiveAmount", amount).
						Msg("Failed to save received amount to swap")
					return
				}
				svc.logger.Info().Str("swap_id", swap.SwapId).Msg("Swap succeeded")
				svc.eventPublisher.Publish(&events.Event{
					Event: "nwc_swap_succeeded",
					Properties: map[string]interface{}{
						"swapType": constants.SWAP_TYPE_IN,
					},
				})
				return
			case lightz.TransactionLockupFailed, lightz.InvoiceFailedToPay, lightz.SwapExpired:
				svc.logger.Error().
					Str("swap_id", swap.SwapId).
					Str("reason", update.Status).
					Msg("Swap in failed, initiating refund")

				err = svc.RefundSwap(swap.SwapId, "", true)
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Msg("Could not process refund")
				}
				return
			default:
				// other event types are not relevant to this swap-in flow; keep waiting
			}
		}
	}
}

func (svc *swapsService) startSwapOutListener(swap *db.Swap) {
	for {
		err := svc.lightzWs.Subscribe([]string{swap.SwapId})
		if err != nil {
			svc.logger.Error().Err(err).Msg("Failed to subscribe to lightz websocket, retrying in 2s...")
			time.Sleep(2 * time.Second)
			continue
		}
		break
	}

	svc.logger.Info().Str("swap_id", swap.SwapId).Msg("Subscribed to lightz websocket")

	updateCh := make(chan lightz.SwapUpdate)
	svc.swapListenersLock.Lock()
	svc.swapListeners[swap.SwapId] = updateCh
	svc.swapListenersLock.Unlock()

	var err error
	defer func() {
		svc.swapListenersLock.Lock()
		delete(svc.swapListeners, swap.SwapId)
		svc.swapListenersLock.Unlock()
		svc.lightzWs.Unsubscribe(swap.SwapId)
		if err != nil {
			svc.logger.Error().Err(err).Msg("Marking swap state as failed")
			svc.markSwapState(swap, constants.SWAP_STATE_FAILED)
		}
	}()

	var network *lightz.Network
	network, err = lightz.ParseChain(svc.cfg.GetNetwork())
	if err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to parse network")
		return
	}

	var ourKeys *btcec.PrivateKey
	ourKeys, err = svc.keys.GetSwapKey(swap.ID)
	if err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to generate swap child private key")
		return
	}

	var serializedTree lightz.SerializedTree
	if err = json.Unmarshal(swap.SwapTree, &serializedTree); err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to unmarshal swap tree")
		return
	}

	lightzPubkeyBytes, _ := hex.DecodeString(swap.BoltzPubkey)

	var lightzPubKey *btcec.PublicKey
	lightzPubKey, err = btcec.ParsePubKey(lightzPubkeyBytes)
	if err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to parse lightz pubkey")
		return
	}

	preimageBytes, _ := hex.DecodeString(swap.Preimage)
	preimageHash := sha256.Sum256(preimageBytes)

	tree := serializedTree.Deserialize()
	if err = tree.Init(lightz.CurrencyBtc, true, ourKeys, lightzPubKey); err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to initialize swap tree")
		return
	}

	if err = tree.Check(lightz.ReverseSwap, swap.TimeoutBlockHeight, preimageHash[:]); err != nil {
		svc.logger.Error().Err(err).
			Str("swap_id", swap.SwapId).
			Msg("Failed to check swap tree")
		return
	}

	claimTicker := time.NewTicker(10 * time.Second)
	defer claimTicker.Stop()

	paymentErrorCh := make(chan error, 1)

	for {
		select {
		case <-svc.ctx.Done():
			svc.logger.Error().Err(svc.ctx.Err()).
				Str("swap_id", swap.SwapId).
				Msg("Swap out context cancelled")
			return
		case err = <-paymentErrorCh:
			svc.logger.Error().Err(err).
				Str("swap_id", swap.SwapId).
				Msg("Failed to pay hold invoice, terminating swap out...")
			return
		case <-claimTicker.C:
			if swap.ClaimTxId != "" {
				tx, err := svc.getMempoolTx(swap.ClaimTxId)
				if err != nil {
					svc.logger.Debug().Err(err).
						Str("swap_id", swap.SwapId).
						Str("claimTxId", swap.ClaimTxId).
						Msg("Claim poll failed; will retry")
					break
				}
				if tx.Status.Confirmed {
					svc.markSwapState(swap, constants.SWAP_STATE_SUCCESS)
					svc.logger.Info().Str("swap_id", swap.SwapId).Msg("Swap succeeded")
					if swap.UsedXpub {
						svc.bumpAutoswapXpubIndex(swap.ID)
					}
					svc.eventPublisher.Publish(&events.Event{
						Event: "nwc_swap_succeeded",
						Properties: map[string]interface{}{
							"swapType": constants.SWAP_TYPE_OUT,
						},
					})
					return
				}
			}
		case update, ok := <-updateCh:
			if !ok {
				svc.logger.Error().Str("swap_id", update.Id).Msg("Failed to receive update from lightz")
				continue
			}
			if update.Id != swap.SwapId {
				continue
			}
			switch lightz.ParseEvent(update.Status) {
			case lightz.SwapCreated:
				svc.logger.Info().Str("swap_id", swap.SwapId).Msg("Paying the swap invoice")
				go func() {
					_, err := svc.transactionsService.LookupTransaction(svc.ctx, swap.PaymentHash, nil, svc.lnClient, nil)
					if err == transactions.NewNotFoundError() {
						svc.logger.Info().Str("swap_id", swap.SwapId).Msg("Already initiated swap invoice payment")
						return
					}
					metadata := map[string]interface{}{
						"swap_id": swap.SwapId,
					}
					svc.logger.Info().Str("swap_id", swap.SwapId).Msg("Initiating swap invoice payment")
					_, err = svc.transactionsService.SendPaymentSync(swap.Invoice, nil, metadata, svc.lnClient, nil, nil)
					if err != nil {
						svc.logger.Error().Err(err).
							Str("swap_id", swap.SwapId).
							Msg("Error paying the swap invoice")
						paymentErrorCh <- err
						return
					}
				}()
			case lightz.TransactionMempool:
				svc.logger.Info().
					Str("swap_id", swap.SwapId).
					Str("lockupTxId", update.Transaction.Id).
					Msg("Lockup transaction found in mempool")
				err = svc.db.Model(swap).Updates(&db.Swap{
					LockupTxId: update.Transaction.Id,
				}).Error
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Str("lockupTxId", update.Transaction.Id).
						Msg("Failed to save lockup txid to swap")
					return
				}
			case lightz.TransactionConfirmed:
				svc.logger.Info().
					Str("swap_id", swap.SwapId).
					Str("lockupTxId", swap.LockupTxId).
					Msg("Lockup transaction confirmed in mempool")

				var lockupTransaction lightz.Transaction
				lockupTransaction, err = lightz.NewTxFromHex(lightz.CurrencyBtc, update.Transaction.Hex, nil)
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Msg("Failed to build lockup tx from hex")
					return
				}

				var vout uint32
				vout, _, err = lockupTransaction.FindVout(network, swap.LockupAddress)
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Msg("Failed to find lockup address output")
					return
				}

				outputs := []lightz.OutputDetails{
					{
						SwapId:            swap.SwapId,
						SwapType:          lightz.ReverseSwap,
						Address:           swap.DestinationAddress,
						LockupTransaction: lockupTransaction,
						Vout:              vout,
						Preimage:          preimageBytes,
						PrivateKey:        ourKeys,
						SwapTree:          tree,
						Cooperative:       true,
					},
				}

				var lightzFee lightz.Fee
				if swap.ReceiveAmount != 0 {
					lockupAmount, err := lockupTransaction.VoutValue(vout)
					if err != nil {
						svc.logger.Error().Err(err).
							Str("swap_id", swap.SwapId).
							Msg("Failed to find lockup output value")
						return
					}
					fee := lockupAmount - swap.ReceiveAmount
					lightzFee.Sats = &fee
				} else {
					var feeRates *FeeRates
					feeRates, err = svc.getFeeRates()
					if err != nil {
						svc.logger.Error().Err(err).
							Str("swap_id", swap.SwapId).
							Msg("Failed to fetch fee rate to create claim transaction")
						return
					}
					fastestFee := float64(feeRates.FastestFee)
					lightzFee.SatsPerVbyte = &fastestFee
				}

				var claimTransaction lightz.Transaction
				claimTransaction, _, err = lightz.ConstructTransaction(network, lightz.CurrencyBtc, outputs, lightzFee, svc.lightzApi)
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Msg("Could not create claim transaction")
					return
				}

				vout, _, _ = claimTransaction.FindVout(network, swap.DestinationAddress)
				claimAmount, _ := claimTransaction.VoutValue(vout)

				var txHex string
				txHex, err = claimTransaction.Serialize()
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Msg("Could not serialize claim transaction")
					return
				}

				var claimTxId string
				for attempt := 1; attempt <= 5; attempt++ {
					// TODO: Replace with LNClient broadcast method to avoid trusting lightz
					claimTxId, err = svc.lightzApi.BroadcastTransaction(lightz.CurrencyBtc, txHex)
					if err != nil {
						svc.logger.Warn().Err(err).
							Str("swap_id", swap.SwapId).
							Int("attempt", attempt).
							Msg("Failed to broadcast transaction, retrying")
						time.Sleep(1 * time.Second)
						continue
					}
					break
				}

				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Msg("Could not broadcast transaction")
					return
				}

				svc.logger.Info().
					Str("swap_id", swap.SwapId).
					Str("claimTxId", claimTxId).
					Msg("Claim transaction broadcasted")

				err = svc.db.Model(swap).Updates(&db.Swap{
					ClaimTxId:     claimTxId,
					ReceiveAmount: claimAmount,
				}).Error
				if err != nil {
					svc.logger.Error().Err(err).
						Str("swap_id", swap.SwapId).
						Str("claimTxId", claimTxId).
						Uint64("claimAmount", claimAmount).
						Msg("Failed to save claim info to swap")
					return
				}
			case lightz.TransactionFailed, lightz.SwapExpired:
				svc.logger.Error().
					Str("swap_id", swap.SwapId).
					Str("reason", update.Status).
					Msg("Swap out failed, HTLC is cancelled")
				err = errors.New(update.Status)
				return
			default:
				// other event types are not relevant to this swap-out flow; keep waiting
			}
		}
	}
}

func (svc *swapsService) getMempoolTx(txId string) (*MempoolTx, error) {
	var transaction MempoolTx
	endpoint := fmt.Sprintf("/tx/%s", txId)
	if err := svc.requestMempoolApi(endpoint, &transaction); err != nil {
		return nil, err
	}
	return &transaction, nil
}

func (svc *swapsService) getFeeRates() (*FeeRates, error) {
	var rates FeeRates
	if err := svc.requestMempoolApi("/v1/fees/recommended", &rates); err != nil {
		return nil, err
	}
	return &rates, nil
}

func (svc *swapsService) requestMempoolApi(endpoint string, result interface{}) error {
	for attempt := 1; attempt <= 10; attempt++ {
		err := svc.doMempoolRequest(endpoint, result)
		if err != nil {
			svc.logger.Error().Err(err).
				Int("attempt", attempt).
				Str("endpoint", endpoint).
				Msg("Mempool API request failed, retrying")
			time.Sleep(1 * time.Second)
			continue
		}
		return nil
	}

	return fmt.Errorf("ran out of attempts to request %s", endpoint)
}

func (svc *swapsService) doMempoolRequest(endpoint string, result interface{}) error {
	url := svc.cfg.GetMempoolApi() + endpoint
	url = strings.ReplaceAll(url, "testnet/", "")

	client := http.Client{
		Timeout: time.Second * 10,
	}

	req, err := http.NewRequestWithContext(svc.ctx, http.MethodGet, url, nil)
	if err != nil {
		svc.logger.Error().Err(err).
			Str("url", url).
			Msg("Failed to create http request")
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		svc.logger.Error().Err(err).
			Str("url", url).
			Msg("Failed to send request")
		return err
	}

	defer func() { _ = res.Body.Close() }()

	body, readErr := io.ReadAll(res.Body)
	if readErr != nil {
		svc.logger.Error().Err(readErr).
			Str("url", url).
			Msg("Failed to read response body")
		return errors.New("failed to read response body")
	}

	jsonErr := json.Unmarshal(body, &result)
	if jsonErr != nil {
		svc.logger.Error().Err(jsonErr).
			Str("url", url).
			Msg("Failed to deserialize json")
		return fmt.Errorf("failed to deserialize json %s %s", url, string(body))
	}
	return nil
}

func (svc *swapsService) bumpAutoswapXpubIndex(swapId uint) {
	indexStr, err := svc.cfg.Get(config.AutoSwapXpubIndexStart, "")
	if err != nil {
		svc.logger.Error().Msg("failed to get auto swap xpub index")
		return
	}
	if indexStr == "" {
		indexStr = "0"
	}
	index, err := strconv.ParseUint(indexStr, 10, 32)
	if err != nil {
		svc.logger.Error().Msg("failed to parse auto swap xpub index")
		return
	}

	err = svc.cfg.SetUpdate(config.AutoSwapXpubIndexStart, strconv.FormatUint(uint64(index+1), 10), "")
	if err != nil {
		svc.logger.Error().Err(err).Msg("Failed to update auto swap xpub index")
	}
	svc.logger.Info().
		Uint("swap_id", swapId).
		Uint64("nextIndex", index+1).
		Msg("Updated xpub index start for swap address")
}

func (svc *swapsService) deriveAddressFromXpub(xpub string, index uint32) (string, error) {
	var netParams *chaincfg.Params
	switch svc.cfg.GetNetwork() {
	case "bitcoin", "mainnet":
		netParams = &chaincfg.MainNetParams
	case "testnet":
		netParams = &chaincfg.TestNet3Params
	case "regtest":
		netParams = &chaincfg.RegressionNetParams
	case "signet":
		netParams = &chaincfg.SigNetParams
	default:
		return "", fmt.Errorf("unsupported network: %s", svc.cfg.GetNetwork())
	}

	extPubKey, err := hdkeychain.NewKeyFromString(xpub)
	if err != nil {
		return "", fmt.Errorf("failed to parse xpub: %w", err)
	}

	externalChain, err := extPubKey.Derive(0)
	if err != nil {
		return "", fmt.Errorf("failed to derive external chain: %w", err)
	}

	addressKey, err := externalChain.Derive(index)
	if err != nil {
		return "", fmt.Errorf("failed to derive address key at index %d: %w", index, err)
	}

	pubKey, err := addressKey.ECPubKey()
	if err != nil {
		return "", fmt.Errorf("failed to get public key: %w", err)
	}

	pubKeyHash := chainutil.Hash160(pubKey.SerializeCompressed())
	address, err := chainutil.NewAddressWitnessPubKeyHash(pubKeyHash, netParams)
	if err != nil {
		return "", fmt.Errorf("failed to create address: %w", err)
	}

	return address.EncodeAddress(), nil
}

func (svc *swapsService) checkAddressHasTransactions(address string, esploraApiRequester func(endpoint string) (interface{}, error)) (bool, error) {
	response, err := esploraApiRequester("/address/" + address + "/txs")
	if err != nil {
		return false, fmt.Errorf("failed to get address transactions: %w", err)
	}

	transactions, ok := response.([]interface{})
	if !ok {
		return false, fmt.Errorf("unexpected response format from esplora API")
	}

	return len(transactions) > 0, nil
}

func (svc *swapsService) getNextUnusedAddressFromXpub() (string, error) {
	destination, _ := svc.cfg.Get(config.AutoSwapDestinationKey, "")
	if destination == "" {
		return "", errors.New("no destination configured")
	}

	if err := svc.validateXpub(destination); err != nil {
		return "", errors.New("destination is not a valid XPUB")
	}

	indexStr, err := svc.cfg.Get(config.AutoSwapXpubIndexStart, "")
	if err != nil {
		return "", err
	}
	if indexStr == "" {
		indexStr = "0"
	}
	index, err := strconv.ParseUint(indexStr, 10, 32)
	if err != nil {
		return "", err
	}

	esploraApiRequester := func(endpoint string) (interface{}, error) {
		url := svc.cfg.GetMempoolApi() + endpoint

		client := http.Client{
			Timeout: time.Second * 10,
		}

		req, err := http.NewRequestWithContext(svc.ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = res.Body.Close() }()

		body, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, err
		}

		var jsonContent interface{}
		err = json.Unmarshal(body, &jsonContent)
		if err != nil {
			return nil, err
		}
		return jsonContent, nil
	}

	const addressLookAheadLimit = 100

	for i := uint32(index); i < uint32(index)+addressLookAheadLimit; i++ {
		address, err := svc.deriveAddressFromXpub(destination, i)
		if err != nil {
			return "", fmt.Errorf("failed to derive address at index %d: %w", i, err)
		}

		hasTransactions, err := svc.checkAddressHasTransactions(address, esploraApiRequester)
		if err != nil {
			return "", fmt.Errorf("failed to check address for transactions at index %d: %w", i, err)
		}

		if !hasTransactions {
			return address, nil
		}
	}

	return "", fmt.Errorf("could not find unused address within %d addresses starting from index %d", addressLookAheadLimit, index)
}

func (svc *swapsService) validateXpub(xpub string) error {
	_, err := hdkeychain.NewKeyFromString(xpub)
	if err != nil {
		return fmt.Errorf("invalid xpub: %w", err)
	}
	return nil
}
