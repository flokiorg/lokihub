package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/adrg/xdg"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/ohstr/nmilat/nipcash/transport"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type config struct {
	Env        *AppConfig
	db         *gorm.DB
	cache      map[string]map[string]string // key -> encryptionKeyHash -> value
	cacheMutex sync.Mutex
	jwtSecret  string
	// jwtSecretMutex guards jwtSecret. Two requests can reach the unlock path
	// at once — /api/start and /api/unlock both take it — and without this
	// each could observe no stored secret, generate its own and write it,
	// leaving every token already signed with the loser's secret silently
	// invalid.
	jwtSecretMutex sync.Mutex
}

const (
	unlockPasswordCheck = "THIS STRING SHOULD MATCH IF PASSWORD IS CORRECT"
)

func NewConfig(env *AppConfig, db *gorm.DB) (*config, error) {
	cfg := &config{
		db:    db,
		cache: map[string]map[string]string{},
	}
	err := cfg.init(env)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

func (cfg *config) init(env *AppConfig) error {
	cfg.Env = env

	if cfg.Env.Relay != "" {
		err := cfg.SetUpdate("Relay", cfg.Env.Relay, "")
		if err != nil {
			return err
		}
	}
	if cfg.Env.LNBackendType != "" {
		err := cfg.SetIgnore("LNBackendType", cfg.Env.LNBackendType, "")
		if err != nil {
			return err
		}
	}

	// FLND specific to support env variables
	if cfg.Env.FLNDAddress != "" {
		err := cfg.SetUpdate("LNDAddress", cfg.Env.FLNDAddress, "")
		if err != nil {
			return err
		}
	}

	if cfg.Env.FLNDCertFile != "" {
		certBytes, err := os.ReadFile(cfg.Env.FLNDCertFile)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to read FLND cert file")
			return err
		}
		certHex := hex.EncodeToString(certBytes)
		err = cfg.SetUpdate("LNDCertHex", certHex, "")
		if err != nil {
			return err
		}
	} else {
		// If no FLNDCertFile is provided, clear any stored certificate
		// hex value so that no certificate is used for TLS verification.
		err := cfg.SetUpdate("LNDCertHex", "", "")
		if err != nil {
			return err
		}
	}

	if cfg.Env.FLNDMacaroonFile != "" {
		macBytes, err := os.ReadFile(cfg.Env.FLNDMacaroonFile)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to read FLND macaroon file")
			return err
		}
		macHex := hex.EncodeToString(macBytes)
		err = cfg.SetUpdate("LNDMacaroonHex", macHex, "")
		if err != nil {
			return err
		}
	}

	if cfg.Env.LSP != "" {
		err := cfg.SetUpdate("LSP", cfg.Env.LSP, "")
		if err != nil {
			return err
		}
	}

	return nil
}

func (cfg *config) SetupCompleted() bool {
	nodeLastStartTime, _ := cfg.Get("NodeLastStartTime", "")

	logger.Logger.Debug().
		Bool("has_node_last_start_time", nodeLastStartTime != "").
		Msg("Checking if setup is completed")
	return nodeLastStartTime != ""
}

func (cfg *config) GetJWTSecret() (string, error) {
	cfg.jwtSecretMutex.Lock()
	defer cfg.jwtSecretMutex.Unlock()

	if cfg.jwtSecret == "" {
		return "", errors.New("config not unlocked")
	}

	return cfg.jwtSecret, nil
}

// loadJWTSecret reads the stored JWT secret, generating one on first use, and
// is safe to call from several requests at once.
//
// Idempotent on purpose: if a secret is already loaded it returns immediately.
// Unlock is reachable concurrently (/api/start and /api/unlock both take it,
// and a reconnecting browser will fire both), and without the early return two
// callers could each find nothing stored, each generate a secret, and each
// write it — whichever lost would have already signed tokens that no longer
// validate, logging that session out for no visible reason.
func (cfg *config) loadJWTSecret(encryptionKey string) error {
	cfg.jwtSecretMutex.Lock()
	defer cfg.jwtSecretMutex.Unlock()

	if cfg.jwtSecret != "" {
		return nil
	}

	// TODO: remove encryptedJwtSecret check after 2027-01-01
	// - all hubs should have updated to use an encrypted JWT secret by then
	encryptedJwtSecret, err := cfg.Get("JWTSecret", "")
	if err != nil {
		return err
	}
	jwtSecret, err := cfg.Get("JWTSecret", encryptionKey)
	if err != nil {
		return err
	}
	// generate a new one if none exists yet OR if the user has an unencrypted secret
	if jwtSecret == "" || jwtSecret == encryptedJwtSecret {
		hexSecret, err := randomHex(32)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("failed to generate JWT secret")
			return err
		}
		jwtSecret = hexSecret
		logger.Logger.Info().Msg("Generated new JWT secret")

		if err := cfg.SetUpdate("JWTSecret", jwtSecret, encryptionKey); err != nil {
			logger.Logger.Error().Err(err).Msg("failed to save JWT secret")
			return err
		}
	}
	cfg.jwtSecret = jwtSecret
	return nil
}

func (cfg *config) Unlock(encryptionKey string) error {
	if !cfg.CheckUnlockPassword(encryptionKey) {
		return errors.New("incorrect password")
	}

	if err := cfg.loadJWTSecret(encryptionKey); err != nil {
		return err
	}

	// Seed the default General relay list on first run only. SetIgnore is a
	// no-op if "GeneralRelay" already exists, even if the user has since
	// cleared it to empty, so this never overwrites an explicit user choice.
	if err := cfg.SetIgnore("GeneralRelay", strings.Join(constants.DefaultGeneralRelays, ","), ""); err != nil {
		logger.Logger.Error().Err(err).Msg("failed to seed default GeneralRelay")
		return err
	}

	// Seed the default Search relay list on first run only, same as GeneralRelay.
	if err := cfg.SetIgnore("SearchRelay", strings.Join(constants.DefaultSearchRelays, ","), ""); err != nil {
		logger.Logger.Error().Err(err).Msg("failed to seed default SearchRelay")
		return err
	}

	return nil
}

// GetRelayUrls returns the hub's configured relay URLs, empty entries removed.
//
// The filtering is load-bearing, not tidiness. strings.Split("", ",") returns
// []string{""} — a one-element slice holding the empty string, NOT an empty
// slice — so an unset or empty Relay config used to yield one relay whose URL
// was "". That value then reached ~30 call sites, including every pairing URI
// and cash token this hub mints.
//
// A minted bill carrying a single empty relay hint is the worst shape available:
// it is structurally valid, its bech32 checksum verifies, nipcash.Decode accepts
// it, and even the client's own guard misses it — NewNWCClient rejects a token
// with ZERO relays by name, but an empty one passes its length check and then
// passes url.Parse, which returns no error for "". The holder gets an obscure
// dial failure against a bill that looks perfect.
//
// A correctly configured hub is unaffected: splitting a real comma-separated
// list produces the same entries as before. Only the misconfigured case changes,
// from "one unusable relay" to "no relays", which callers can actually detect.
// Entries are trimmed for the same reason — " wss://a " is a config typo, not a
// distinct relay.
func (cfg *config) GetRelayUrls() []string {
	relayUrls, _ := cfg.Get("Relay", "")
	var urls []string
	for _, relayUrl := range strings.Split(relayUrls, ",") {
		if trimmed := strings.TrimSpace(relayUrl); trimmed != "" {
			urls = append(urls, trimmed)
		}
	}
	return urls
}

// GetGeneralRelayUrls returns the relays used to fetch general Nostr social
// data — profiles, notes, and events, including Circle contact lists
// (kind:0/kind:1/kind:3) — independent of GetRelayUrls, which is used for
// NWC. An explicitly empty list is a valid state (the user deleted every
// relay), unlike strings.Split("", ",") which would otherwise yield a single
// blank-URL element.
func (cfg *config) GetGeneralRelayUrls() []string {
	value, _ := cfg.Get("GeneralRelay", "")
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ",")
}

// GetSearchRelayUrls returns the relays used only for NIP-50 search queries —
// independent of GetRelayUrls (NWC) and GetGeneralRelayUrls (general social
// data). See GetGeneralRelayUrls for why the empty case is guarded explicitly.
func (cfg *config) GetSearchRelayUrls() []string {
	value, _ := cfg.Get("SearchRelay", "")
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ",")
}

func (cfg *config) GetNetwork() string {
	env := cfg.GetEnv()

	if env.Network != "" {
		return env.Network
	}

	return "flokicoin"
}

func (cfg *config) GetMempoolApi() string {
	url, err := cfg.Get("MempoolApi", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch MempoolApi")
	}
	if url != "" {
		return url
	}
	return cfg.Env.MempoolApi
}

func (cfg *config) SetMempoolApi(value string) error {
	// MempoolApi can be empty to use default
	err := cfg.SetUpdate("MempoolApi", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update MempoolApi")
		return err
	}
	return nil
}

func (cfg *config) getEncryptionKeyHash(encryptionKey string) string {
	if encryptionKey == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(encryptionKey))
	// For cache key purposes, 8 bytes (16 hex chars) provides:
	//   2^64 possible values = ~18 quintillion combinations
	//   More than sufficient to avoid collisions for cache keys
	return hex.EncodeToString(hash[:8])
}

func (cfg *config) Get(key string, encryptionKey string) (string, error) {
	cfg.cacheMutex.Lock()
	defer cfg.cacheMutex.Unlock()

	encKeyHash := cfg.getEncryptionKeyHash(encryptionKey)

	if keyCache, ok := cfg.cache[key]; ok {
		if cachedValue, ok := keyCache[encKeyHash]; ok {
			return cachedValue, nil
		}
	}
	logger.Logger.Debug().Str("key", key).Msg("missed config cache")

	value, err := cfg.get(key, encryptionKey, cfg.db)
	if err != nil {
		return "", err
	}

	if cfg.cache[key] == nil {
		cfg.cache[key] = make(map[string]string)
	}
	cfg.cache[key][encKeyHash] = value
	logger.Logger.Debug().Str("key", key).Msg("set config cache")
	return value, nil
}

func (cfg *config) get(key string, encryptionKey string, gormDB *gorm.DB) (string, error) {
	var userConfig db.UserConfig
	err := gormDB.Where(&db.UserConfig{Key: key}).Limit(1).Find(&userConfig).Error
	if err != nil {
		return "", fmt.Errorf("failed to get configuration value: %w", gormDB.Error)
	}

	value := userConfig.Value
	if userConfig.Value != "" && encryptionKey != "" && userConfig.Encrypted {
		decrypted, err := AesGcmDecryptWithPassword(value, encryptionKey)
		if err != nil {
			return "", err
		}
		value = decrypted
	}
	return value, nil
}

func (cfg *config) set(key string, value string, clauses clause.OnConflict, encryptionKey string, gormDB *gorm.DB) error {
	if encryptionKey != "" {
		encrypted, err := AesGcmEncryptWithPassword(value, encryptionKey)
		if err != nil {
			return fmt.Errorf("failed to encrypt: %v", err)
		}
		value = encrypted
	}
	userConfig := db.UserConfig{Key: key, Value: value, Encrypted: encryptionKey != ""}
	result := gormDB.Clauses(clauses).Create(&userConfig)

	if result.Error != nil {
		return fmt.Errorf("failed to save key to config: %v", result.Error)
	}

	logger.Logger.Debug().Str("key", key).Msg("clearing config cache")
	cfg.cacheMutex.Lock()
	defer cfg.cacheMutex.Unlock()
	delete(cfg.cache, key)

	return nil
}

func (cfg *config) SetIgnore(key string, value string, encryptionKey string) error {
	clauses := clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoNothing: true,
	}
	err := cfg.set(key, value, clauses, encryptionKey, cfg.db)
	if err != nil {
		logger.Logger.Error().Err(err).Str("key", key).Msg("Failed to set config key with ignore")
		return err
	}
	return nil
}

func (cfg *config) SetUpdate(key string, value string, encryptionKey string) error {
	clauses := clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "encrypted"}),
	}
	err := cfg.set(key, value, clauses, encryptionKey, cfg.db)
	if err != nil {
		logger.Logger.Error().Err(err).Str("key", key).Msg("Failed to set config key with update")
		return err
	}
	return nil
}

func (cfg *config) ChangeUnlockPassword(currentUnlockPassword string, newUnlockPassword string) error {
	if newUnlockPassword == "" {
		return errors.New("new unlock password must not be empty")
	}
	if !cfg.CheckUnlockPassword(currentUnlockPassword) {
		return errors.New("incorrect password")
	}
	err := cfg.db.Transaction(func(tx *gorm.DB) error {

		var encryptedUserConfigs []db.UserConfig
		err := tx.Where(&db.UserConfig{Encrypted: true}).Find(&encryptedUserConfigs).Error
		if err != nil {
			return err
		}

		logger.Logger.Info().Int("count", len(encryptedUserConfigs)).Msg("Updating encrypted entries")

		for _, userConfig := range encryptedUserConfigs {
			decryptedValue, err := cfg.get(userConfig.Key, currentUnlockPassword, tx)
			if err != nil {
				logger.Logger.Error().Err(err).Str("key", userConfig.Key).Msg("Failed to decrypt key")
				return err
			}
			clauses := clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value"}),
			}
			err = cfg.set(userConfig.Key, decryptedValue, clauses, newUnlockPassword, tx)
			if err != nil {
				logger.Logger.Error().Err(err).Str("key", userConfig.Key).Msg("Failed to encrypt key")
				return err
			}
			logger.Logger.Info().Str("key", userConfig.Key).Msg("re-encrypted key")
		}

		// delete the JWT secret so it will be re-generated on next unlock (to log all sessions out on password change)
		err = tx.Where(&db.UserConfig{Key: "JWTSecret"}).Delete(&db.UserConfig{}).Error
		if err != nil {
			logger.Logger.Error().Err(err).Msg("failed to remove JWT secret during password change transaction")
			return fmt.Errorf("failed to delete new JWT secret: %w", err)
		}

		logger.Logger.Info().Msg("Successfully removed JWT secret as part of password change transaction")
		return nil
	})

	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to execute password change transaction")
		return err
	}

	// JWT secret will be set on config unlock (required after password change)
	cfg.jwtSecretMutex.Lock()
	cfg.jwtSecret = ""
	cfg.jwtSecretMutex.Unlock()
	return nil
}

func (cfg *config) SetAutoUnlockPassword(unlockPassword string) error {
	if unlockPassword != "" && !cfg.CheckUnlockPassword(unlockPassword) {
		return errors.New("incorrect password")
	}

	err := cfg.SetUpdate("AutoUnlockPassword", unlockPassword, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to update auto unlock password")
		return err
	}

	return nil
}

func (cfg *config) CheckUnlockPassword(encryptionKey string) bool {
	decryptedValue, err := cfg.Get("UnlockPasswordCheck", encryptionKey)
	if err != nil {
		return false
	}
	if decryptedValue == unlockPasswordCheck {
		return true
	}

	// An empty stored check means no password has ever been set. That is
	// permissive on purpose, and load-bearing: ChangeUnlockPassword calls
	// through here with an empty current password to set the first one, when
	// there is by definition nothing to check against.
	//
	// It must stop being permissive the moment the hub has run. Otherwise a
	// missing UnlockPasswordCheck row on a configured hub — a partial restore,
	// an interrupted migration, a hand-edited database — unlocks for any input
	// at all, and /api/start hands a full-access token to whoever asked. No
	// path produces that state today; this is here so none can, because the
	// failure is total rather than partial.
	return decryptedValue == "" && !cfg.SetupCompleted()
}

func (cfg *config) SaveUnlockPasswordCheck(encryptionKey string) error {
	err := cfg.SetUpdate("UnlockPasswordCheck", unlockPasswordCheck, encryptionKey)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save unlock password check to config")
		return err
	}
	return nil
}

func (cfg *config) GetEnv() *AppConfig {
	return cfg.Env
}

func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

const defaultCurrency = "USD"
const defaultFlokicoinDisplayFormat = constants.FLOKICOIN_DISPLAY_FORMAT_AUTO

func (cfg *config) GetCurrency() string {
	currency, err := cfg.Get("Currency", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch currency")
		return defaultCurrency
	}
	if currency == "" {
		return defaultCurrency
	}
	return currency
}

func (cfg *config) SetCurrency(value string) error {
	if value == "" {
		return errors.New("currency value cannot be empty")
	}
	err := cfg.SetUpdate("Currency", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update currency")
		return err
	}
	return nil
}

func (cfg *config) GetFlokicoinDisplayFormat() string {
	format, err := cfg.Get("FlokicoinDisplayFormat", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch flokicoin display format")
		return defaultFlokicoinDisplayFormat
	}
	if format == "" {
		return defaultFlokicoinDisplayFormat
	}
	return format
}

func (cfg *config) SetFlokicoinDisplayFormat(value string) error {
	err := cfg.SetUpdate("FlokicoinDisplayFormat", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update flokicoin display format")
		return err
	}
	return nil
}

func (cfg *config) GetLokihubServicesURL() string {
	url, err := cfg.Get("LokihubServicesURL", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch LokihubServicesURL")
	}
	if url != "" {
		return url
	}
	return cfg.Env.LokihubServicesURL
}

func (cfg *config) SetLokihubServicesURL(value string) error {
	if value == "" {
		return errors.New("LokihubServicesURL cannot be empty")
	}
	err := cfg.SetUpdate("LokihubServicesURL", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update LokihubServicesURL")
		return err
	}
	return nil
}

func (cfg *config) GetLokihubStoreURL() string {
	url, err := cfg.Get("LokihubStoreURL", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch LokihubStoreURL")
	}
	if url != "" {
		return url
	}
	return cfg.Env.LokihubStoreURL
}

func (cfg *config) SetLokihubStoreURL(value string) error {
	if value == "" {
		return errors.New("LokihubStoreURL cannot be empty")
	}
	err := cfg.SetUpdate("LokihubStoreURL", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update LokihubStoreURL")
		return err
	}
	return nil
}

func (cfg *config) GetSwapServiceURL() string {
	url, err := cfg.Get("SwapServiceUrl", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch SwapServiceUrl")
	}
	if url != "" {
		return url
	}
	return cfg.Env.SwapServiceUrl
}

func (cfg *config) SetSwapServiceURL(value string) error {
	if value == "" {
		return errors.New("SwapServiceUrl cannot be empty")
	}
	err := cfg.SetUpdate("SwapServiceUrl", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update SwapServiceUrl")
		return err
	}
	return nil
}

func (cfg *config) GetMessageboardNwcUrl() string {
	url, err := cfg.Get("MessageboardNwcUrl", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch MessageboardNwcUrl")
	}
	if url != "" {
		return url
	}
	return cfg.Env.MessageboardNwcUrl
}

func (cfg *config) SetMessageboardNwcUrl(value string) error {
	// MessageboardNwcUrl can be empty
	err := cfg.SetUpdate("MessageboardNwcUrl", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update MessageboardNwcUrl")
		return err
	}
	return nil
}

func (cfg *config) GetRelay() string {
	url, err := cfg.Get("Relay", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch Relay")
	}
	if url != "" {
		return url
	}
	return cfg.Env.Relay
}

func (cfg *config) SetRelay(value string) error {
	if value == "" {
		return errors.New("relay cannot be empty")
	}
	err := cfg.SetUpdate("Relay", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update Relay")
		return err
	}
	return nil
}

func (cfg *config) GetGeneralRelay() string {
	value, err := cfg.Get("GeneralRelay", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch GeneralRelay")
	}
	return value
}

// SetGeneralRelay deliberately allows an empty value, unlike SetRelay — an
// empty General relay list is a valid, user-chosen state.
func (cfg *config) SetGeneralRelay(value string) error {
	err := cfg.SetUpdate("GeneralRelay", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update GeneralRelay")
		return err
	}
	return nil
}

func (cfg *config) GetSearchRelay() string {
	value, err := cfg.Get("SearchRelay", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch SearchRelay")
	}
	return value
}

// SetSearchRelay deliberately allows an empty value, same rationale as SetGeneralRelay.
func (cfg *config) SetSearchRelay(value string) error {
	err := cfg.SetUpdate("SearchRelay", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update SearchRelay")
		return err
	}
	return nil
}

// TrustedNwcRelay reports whether the relays in GetRelayUrls are this hub's
// own. It changes two things, both of which assume the relay is ours:
//
//  1. The hub subscribes once for all NIP-47 requests (kind 23194) and
//     matches the "p" tag locally, instead of opening one subscription per
//     app wallet. Subscription count then no longer grows with the number of
//     cash bills in circulation, and a newly created wallet is covered the
//     instant it exists rather than once its own subscription attaches.
//  2. Events from those relays skip signature verification (AssumeValid):
//     the relay already verified them on ingest, so re-checking is duplicate
//     secp256k1 work that an attacker can trigger cheaply.
//
// Neither is safe on a relay the operator does not control: (1) would make
// the hub a firehose subscriber to strangers' NWC traffic, and (2) would let
// a hostile relay inject forged events. Defaults to true because a Cash Hub
// runs its own relay; startNostr logs a warning naming the relays it applies
// to so an operator who points this elsewhere can see it.
//
// It never applies to GetGeneralRelayUrls, which are public by design.
func (cfg *config) TrustedNwcRelay() bool {
	value, err := cfg.Get("TrustedNwcRelay", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch TrustedNwcRelay")
		return true
	}
	if value == "" {
		return true
	}
	return value == "true"
}

// SetTrustedNwcRelay records whether the NWC relays are this hub's own. The
// caller is expected to ReloadNostr afterwards: the subscription shape is
// chosen in startNostr, so a change only takes effect when nostr restarts.
func (cfg *config) SetTrustedNwcRelay(trusted bool) error {
	value := "false"
	if trusted {
		value = "true"
	}
	if err := cfg.SetUpdate("TrustedNwcRelay", value, ""); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update TrustedNwcRelay")
		return err
	}
	return nil
}

// privateTransportEnabledKey backs PrivateTransportEnabled.
const privateTransportEnabledKey = "PrivateTransportEnabled"

// PrivateTransportEnabled reports whether this hub serves the wrapped private
// transport alongside the standard kind-23194 one.
//
// Defaults to FALSE, unlike TrustedNwcRelay. The receive path is incomplete — the
// gate accepts envelopes but nothing unwraps them yet — so a hub with this on
// would publish an announcement inviting clients to an inbox it cannot answer, and
// those clients would experience silence. Announcing a capability before it works
// is the exact failure mode this work exists to remove, so the default stays off
// until the path is finished.
func (cfg *config) PrivateTransportEnabled() bool {
	value, err := cfg.Get(privateTransportEnabledKey, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch PrivateTransportEnabled")
		return false
	}
	if value == "" {
		return cfg.Env.PrivateTransportEnabled
	}
	return value == "true"
}

// SetPrivateTransportEnabled records whether the private transport is served. The
// caller is expected to ReloadNostr afterwards: the subscription and the
// announcement are both set up in startNostr.
func (cfg *config) SetPrivateTransportEnabled(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	if err := cfg.SetUpdate(privateTransportEnabledKey, value, ""); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update PrivateTransportEnabled")
		return err
	}
	return nil
}

// privateEnvelope* are the settings keys backing PrivateEnvelopeLimits.
const (
	privateEnvelopeMaxBytesKey        = "PrivateEnvelopeMaxBytes"
	privateEnvelopeMaxItemsKey        = "PrivateEnvelopeMaxItems"
	privateEnvelopePadBucketBytesKey  = "PrivateEnvelopePadBucketBytes"
	privateEnvelopeMaxVerifyBudgetKey = "PrivateEnvelopeMaxVerifyBudget"
	privateEnvelopeMaxConsolidateKey  = "PrivateEnvelopeMaxConsolidateSources"
)

// PrivateEnvelopeLimits reports the size policy applied to one private-transport
// batch envelope, resolved in three layers: a runtime value set through hub
// settings wins; otherwise the env var; otherwise the SDK default.
//
// The SDK owns the defaults and the ceiling (nipcash/transport) rather than this
// package, so the hub and every client agree on what a valid envelope is. A
// value that would exceed NIP-44's plaintext ceiling is refused at the setter,
// but this getter also falls back to the defaults if the stored policy is
// somehow invalid — a hub that cannot parse its own limits must still serve,
// and a loud log plus working defaults beats refusing every envelope.
//
// These are node-level, not per-Cash-Hub: the gate runs before any item has been
// attributed to a Hub, and one envelope can carry items for bills from several
// Hubs, so there is no per-Hub policy to consult where the limit is enforced.
func (cfg *config) PrivateEnvelopeLimits() transport.Limits {
	defaults := transport.DefaultLimits()
	env := cfg.Env

	maxBytes := cfg.privateEnvelopeInt(privateEnvelopeMaxBytesKey, env.PrivateEnvelopeMaxBytes, defaults.MaxEnvelopeBytes)

	// The consolidate cap is not independent of the envelope size — a source
	// costs ~952 bytes — so an unconfigured cap is derived from whatever envelope
	// size resolved above, not taken flat from the default. Otherwise a hub that
	// only lowers PRIVATE_ENVELOPE_MAX_BYTES ends up holding a policy Validate
	// rejects, through no fault of its own.
	//
	// An explicitly configured cap is honoured as-is and left to Validate, so an
	// operator who sets an impossible value is told rather than silently corrected.
	defaultSources := min(defaults.MaxConsolidateSources, transport.MaxSourcesForEnvelope(maxBytes))

	limits := transport.Limits{
		MaxEnvelopeBytes:      maxBytes,
		MaxItems:              cfg.privateEnvelopeInt(privateEnvelopeMaxItemsKey, env.PrivateEnvelopeMaxItems, defaults.MaxItems),
		PadBucketBytes:        cfg.privateEnvelopeInt(privateEnvelopePadBucketBytesKey, env.PrivateEnvelopePadBucketBytes, defaults.PadBucketBytes),
		MaxVerifyBudget:       cfg.privateEnvelopeInt(privateEnvelopeMaxVerifyBudgetKey, env.PrivateEnvelopeMaxVerifyBudget, defaults.MaxVerifyBudget),
		MaxConsolidateSources: cfg.privateEnvelopeInt(privateEnvelopeMaxConsolidateKey, env.PrivateEnvelopeMaxConsolidateSources, defaultSources),
	}

	if err := limits.Validate(); err != nil {
		logger.Logger.Error().Err(err).
			Interface("limits", limits).
			Msg("Stored private envelope limits are invalid; falling back to defaults")
		return defaults
	}
	return limits
}

// privateEnvelopeInt resolves one knob: stored setting, else env var, else
// default. A stored value that is not a positive integer is ignored with a
// warning rather than failing the read.
func (cfg *config) privateEnvelopeInt(key string, envValue, defaultValue int) int {
	stored, err := cfg.Get(key, "")
	if err != nil {
		logger.Logger.Error().Err(err).Str("key", key).Msg("Failed to fetch private envelope limit")
	} else if stored != "" {
		parsed, convErr := strconv.Atoi(stored)
		if convErr != nil || parsed <= 0 {
			logger.Logger.Warn().Str("key", key).Str("value", stored).
				Msg("Ignoring an unparseable private envelope limit")
		} else {
			return parsed
		}
	}
	if envValue > 0 {
		return envValue
	}
	return defaultValue
}

// SetPrivateEnvelopeLimits records a new envelope policy. It validates first, so
// a hub cannot store a policy that would make every envelope fail — in
// particular one above NIP-44's plaintext ceiling, which no amount of retrying
// would fix.
//
// Takes effect on the next envelope; nothing needs restarting, unlike
// SetTrustedNwcRelay, because the limits are read per request rather than baked
// into the subscription shape.
func (cfg *config) SetPrivateEnvelopeLimits(limits transport.Limits) error {
	if err := limits.Validate(); err != nil {
		return fmt.Errorf("refusing to store an invalid private envelope policy: %w", err)
	}

	for key, value := range map[string]int{
		privateEnvelopeMaxBytesKey:        limits.MaxEnvelopeBytes,
		privateEnvelopeMaxItemsKey:        limits.MaxItems,
		privateEnvelopePadBucketBytesKey:  limits.PadBucketBytes,
		privateEnvelopeMaxVerifyBudgetKey: limits.MaxVerifyBudget,
		privateEnvelopeMaxConsolidateKey:  limits.MaxConsolidateSources,
	} {
		if err := cfg.SetUpdate(key, strconv.Itoa(value), ""); err != nil {
			logger.Logger.Error().Err(err).Str("key", key).Msg("Failed to update private envelope limit")
			return err
		}
	}
	return nil
}

func (cfg *config) EnableSwap() bool {
	value, err := cfg.Get("EnableSwap", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch EnableSwap")
		return cfg.Env.EnableSwap
	}
	if value == "" {
		return cfg.Env.EnableSwap
	}
	return value == "true"
}

func (cfg *config) SetEnableSwap(enable bool) error {
	var value string
	if enable {
		value = "true"
	} else {
		value = "false"
	}
	err := cfg.SetUpdate("EnableSwap", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update EnableSwap")
		return err
	}
	// Update the in-memory Env as well so subsequent calls to EnableSwap() return the new value
	// Note: This relies on EnableSwap() checking cfg.Env.EnableSwap which we might need to update or
	// we should change EnableSwap() to check the db/cache like other methods.
	// Looking at EnableSwap() implementation: return cfg.Env.EnableSwap.
	// This means we need to update cfg.Env.EnableSwap.
	// However, looking at other Set methods (e.g. SetMempoolApi), they don't seem to update Env.
	// Let's check how other config values are retrieved. properties like GetMempoolApi check db/cache first then Env.
	// EnableSwap currently ONLY checks Env. I should probably update EnableSwap to check DB too, or just update Env here.
	// Since we are moving to dynamic config, I should update EnableSwap to look up the value using Get() like others.
	// Rewriting EnableSwap to use Get() is better.
	return nil
}

func (cfg *config) EnableMessageboardNwc() bool {
	value, err := cfg.Get("EnableMessageboardNwc", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch EnableMessageboardNwc")
		return cfg.Env.EnableMessageboardNwc
	}
	if value == "" {
		return cfg.Env.EnableMessageboardNwc
	}
	return value == "true"
}

func (cfg *config) SetEnableMessageboardNwc(enable bool) error {
	var value string
	if enable {
		value = "true"
	} else {
		value = "false"
	}
	err := cfg.SetUpdate("EnableMessageboardNwc", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update EnableMessageboardNwc")
		return err
	}
	return nil
}

func (cfg *config) GetDefaultWorkDir() string {
	if cfg.Env.Workdir != "" {
		return cfg.Env.Workdir
	}
	return filepath.Join(xdg.DataHome, "lokihub")
}

func (cfg *config) GetLSP() string {
	url, err := cfg.Get("LSP", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch LSP")
	}
	if url != "" {
		return url
	}
	return cfg.Env.LSP
}

func (cfg *config) SetLSP(value string) error {
	// LSP can be empty
	err := cfg.SetUpdate("LSP", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update LSP")
		return err
	}
	return nil
}

func (cfg *config) GetCachedServicesJSON() string {
	json, err := cfg.Get("CachedServicesJSON", "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch CachedServicesJSON")
	}
	return json
}

func (cfg *config) SetCachedServicesJSON(value string) error {
	// We use SetUpdate to persist it unencrypted (it's public data)
	err := cfg.SetUpdate("CachedServicesJSON", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update CachedServicesJSON")
		return err
	}
	return nil
}
