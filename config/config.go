package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/adrg/xdg"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type config struct {
	Env        *AppConfig
	db         *gorm.DB
	cache      map[string]map[string]string // key -> encryptionKeyHash -> value
	cacheMutex sync.Mutex
	jwtSecret  string
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
	if cfg.Env.LNDAddress != "" {
		err := cfg.SetUpdate("LNDAddress", cfg.Env.LNDAddress, "")
		if err != nil {
			return err
		}
	}
	if cfg.Env.LNDCertFile != "" {
		certBytes, err := os.ReadFile(cfg.Env.LNDCertFile)
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
		// If no LNDCertFile is provided, clear any stored certificate
		// hex value so that no certificate is used for TLS verification.
		err := cfg.SetUpdate("LNDCertHex", "", "")
		if err != nil {
			return err
		}
	}
	if cfg.Env.LNDMacaroonFile != "" {
		macBytes, err := os.ReadFile(cfg.Env.LNDMacaroonFile)
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
	if cfg.jwtSecret == "" {
		return "", errors.New("config not unlocked")
	}

	return cfg.jwtSecret, nil
}

func (cfg *config) Unlock(encryptionKey string) error {
	if !cfg.CheckUnlockPassword(encryptionKey) {
		return errors.New("incorrect password")
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

		err = cfg.SetUpdate("JWTSecret", jwtSecret, encryptionKey)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("failed to save JWT secret")
			return err
		}
	}
	cfg.jwtSecret = jwtSecret
	return nil
}

func (cfg *config) GetRelayUrls() []string {
	relayUrls, _ := cfg.Get("Relay", "")
	return strings.Split(relayUrls, ",")
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
			logger.Logger.Debug().Str("key", key).Msg("hit config cache")
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
	cfg.jwtSecret = ""
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

	return err == nil && (decryptedValue == "" || decryptedValue == unlockPasswordCheck)
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
const defaultFlokicoinDisplayFormat = constants.FLOKICOIN_DISPLAY_FORMAT_LOKI

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
		return errors.New("Relay cannot be empty")
	}
	err := cfg.SetUpdate("Relay", value, "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update Relay")
		return err
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
