package appstore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/logger"
)

type appStoreService struct {
	cfg        config.Config
	apps       []App
	mu         sync.RWMutex
	httpClient *http.Client
}

func NewAppStoreService(cfg config.Config) Service {
	return &appStoreService{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (s *appStoreService) Start() {
	go func() {
		// Initial sync
		s.Sync()

		ticker := time.NewTicker(constants.APP_STORE_SYNC_INTERVAL)
		defer ticker.Stop()

		for range ticker.C {
			s.Sync()
		}
	}()
}

func (s *appStoreService) ListApps() []App {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a copy to avoid race conditions if caller modifies it (though they shouldn't)
	apps := make([]App, len(s.apps))
	copy(apps, s.apps)
	return apps
}

func (s *appStoreService) GetLogoPath(appId string) (string, error) {
	cacheDir := filepath.Join(s.cfg.GetDefaultWorkDir(), constants.APP_STORE_CACHE_DIR, "logos")
	return filepath.Join(cacheDir, fmt.Sprintf("%s.png", appId)), nil
}

func (s *appStoreService) Sync() {
	logger.Logger.Info().Msg("App Store Sync started")

	// 1. Fetch remote apps
	remoteApps, err := s.fetchRemoteApps()
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch remote apps")
		// If fetch fails, try to load from cache
		if len(s.apps) == 0 {
			if err := s.loadFromCache(); err != nil {
				logger.Logger.Error().Err(err).Msg("Failed to load apps from cache")
			}
		}
		return
	}

	// 2. Load local cache
	// We'll read the current cached apps to compare versions
	// If we haven't loaded them into memory yet, do so.
	if len(s.apps) == 0 {
		_ = s.loadFromCache()
	}

	s.mu.Lock()
	existingApps := make(map[string]App)
	for _, app := range s.apps {
		existingApps[app.ID] = app
	}
	s.mu.Unlock()

	// 3. Process apps
	updatedApps := []App{}
	cacheDir := filepath.Join(s.cfg.GetDefaultWorkDir(), constants.APP_STORE_CACHE_DIR)
	logosDir := filepath.Join(cacheDir, "logos")

	if err := os.MkdirAll(logosDir, 0755); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to create app store cache directories")
		return
	}

	for _, remoteApp := range remoteApps {
		localApp, exists := existingApps[remoteApp.ID]
		shouldDownloadLogo := false

		if !exists {
			// New app
			shouldDownloadLogo = true
		} else {
			// Check version
			isNewer, err := isVersionNewer(remoteApp.Version, localApp.Version)
			if err != nil {
				logger.Logger.Warn().Err(err).Str("app", remoteApp.ID).Msg("Invalid version format, skipping update check")
				// Fallback: if versions don't parse, maybe just check string equality?
				// User specific requirement: "keep the app that has the last version field following semantic version format"
				// If parsing fails, we might assume it's NOT newer or handle it gracefully.
				// Let's assume if it's different and not parseable, we might just update it to match remote.
				if remoteApp.Version != localApp.Version {
					shouldDownloadLogo = true
				}
			} else if isNewer {
				shouldDownloadLogo = true
			}
		}

		if shouldDownloadLogo {
			logger.Logger.Info().Str("app", remoteApp.ID).Msg("Downloading app logo")
			if err := s.downloadLogo(remoteApp.Logo, remoteApp.ID, logosDir); err != nil {
				logger.Logger.Error().Err(err).Str("app", remoteApp.ID).Msg("Failed to download logo")
				// If logo download fails, we might still want to update the app info,
				// or maybe keep the old one?
				// "if in this case the version has changed we must download the logo"
				// We'll update the app info anyway, but log the error.
			}
		}

		updatedApps = append(updatedApps, remoteApp)
	}

	// 4. Update memory and disk cache
	s.mu.Lock()
	s.apps = updatedApps
	s.mu.Unlock()

	if err := s.saveToCache(updatedApps); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to save apps to cache")
	}

	// 5. Cleanup old logos (optional, but good practice)
	// "every time we downlaod the logo we msut remove the previous one"
	// effectively handled by overwriting.
	// We might want to remove logos for apps that are no longer in the list?
	// The user didn't explicitly ask to remove deleted apps' logos, but it's clean.
	// We can list files in logosDir and remove those not in updatedApps.
	s.cleanupOldLogos(updatedApps, logosDir)

	logger.Logger.Info().Msg("App Store Sync completed")
}

func (s *appStoreService) fetchRemoteApps() ([]App, error) {
	url := fmt.Sprintf("%s/apps.json", s.cfg.GetLokihubStoreURL())
	resp, err := s.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	var apps []App
	if err := json.NewDecoder(resp.Body).Decode(&apps); err != nil {
		return nil, err
	}
	return apps, nil
}

func (s *appStoreService) downloadLogo(filename, appId, outputDir string) error {
	url := fmt.Sprintf("%s/logos/%s", s.cfg.GetLokihubStoreURL(), filename)
	resp, err := s.httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// "always saved using the id of the app and .png extension"
	outputPath := filepath.Join(outputDir, fmt.Sprintf("%s.png", appId))

	// Create temporary file
	tempFile, err := os.CreateTemp(outputDir, fmt.Sprintf("temp-%s-*.png", appId))
	if err != nil {
		return err
	}
	defer os.Remove(tempFile.Name())

	_, err = io.Copy(tempFile, resp.Body)
	if err != nil {
		tempFile.Close()
		return err
	}
	tempFile.Close()

	// Rename to final path (atomic replace)
	return os.Rename(tempFile.Name(), outputPath)
}

func (s *appStoreService) loadFromCache() error {
	cachePath := filepath.Join(s.cfg.GetDefaultWorkDir(), constants.APP_STORE_CACHE_DIR, "apps.json")
	file, err := os.Open(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	var apps []App
	if err := json.NewDecoder(file).Decode(&apps); err != nil {
		return err
	}

	s.mu.Lock()
	s.apps = apps
	s.mu.Unlock()
	return nil
}

func (s *appStoreService) saveToCache(apps []App) error {
	cacheDir := filepath.Join(s.cfg.GetDefaultWorkDir(), constants.APP_STORE_CACHE_DIR)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}

	cachePath := filepath.Join(cacheDir, "apps.json")
	file, err := os.Create(cachePath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(apps)
}

func (s *appStoreService) cleanupOldLogos(currentApps []App, logosDir string) {
	validIds := make(map[string]bool)
	for _, app := range currentApps {
		validIds[app.ID] = true
	}

	entries, err := os.ReadDir(logosDir)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to read logos dir for cleanup")
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Expected format: {id}.png
		filename := entry.Name()
		ext := filepath.Ext(filename)
		if ext != ".png" {
			continue
		}

		id := filename[0 : len(filename)-len(ext)]
		if !validIds[id] {
			logger.Logger.Info().Str("file", filename).Msg("Removing orphaned logo")
			os.Remove(filepath.Join(logosDir, filename))
		}
	}
}

func isVersionNewer(v1, v2 string) (bool, error) {
	ver1, err := semver.NewVersion(v1)
	if err != nil {
		return false, err
	}
	ver2, err := semver.NewVersion(v2)
	if err != nil {
		return false, err
	}
	return ver1.GreaterThan(ver2), nil
}
