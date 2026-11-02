// Package seed carries the service catalogue, currencies, FAQ, info and
// fiat-rate oracle pointer that lokihub formerly fetched from the remote
// lokihub-services repository. They are now embedded in the binary at build
// time, so a fresh hub works offline and the catalogue cannot drift away from
// the release it shipped with.
//
// seed is a leaf package on purpose: loki and config both read it, and loki
// already imports config, so the seed types live here rather than in config to
// avoid an import cycle. Nothing in this package imports lokihub code.
package seed

import (
	_ "embed"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed seed.yaml
var embedded []byte

// ServiceEntry is one suggested explorer or relay. The JSON tags reproduce the
// shape the frontend's /api/setup/config consumer expects (name/url/description).
type ServiceEntry struct {
	Name        string `yaml:"name" json:"name"`
	URL         string `yaml:"url" json:"url"`
	Description string `yaml:"description" json:"description"`
}

// Currency mirrors loki.LokiCurrency. loki converts into its own type to keep
// this package a leaf.
type Currency struct {
	Name   string `yaml:"name" json:"name"`
	Symbol string `yaml:"symbol" json:"symbol"`
}

// FAQ mirrors loki.FAQ.
type FAQ struct {
	Question string `yaml:"question" json:"question"`
	Answer   string `yaml:"answer" json:"answer"`
}

// Info mirrors loki.LokiInfo. Version is a fallback only — loki prefers the
// appversion build tag.
type Info struct {
	Version      string `yaml:"version" json:"version"`
	ReleaseNotes string `yaml:"release_notes" json:"releaseNotes"`
}

// FiatRateOracle points at the NIP-78 (kind 30078) event that publishes the
// Flokicoin fiat rate. RefreshInterval and StalenessTTL are Go duration
// strings ("300s", "1h"); ratesoracle parses them with sensible fallbacks.
type FiatRateOracle struct {
	Npub            string   `yaml:"npub"`
	Kind            int      `yaml:"kind"`
	DTag            string   `yaml:"d_tag"`
	Relays          []string `yaml:"relays"`
	RefreshInterval string   `yaml:"refresh_interval"`
	StalenessTTL    string   `yaml:"staleness_ttl"`
}

// Seed is the whole embedded catalogue. Services is keyed by category
// (flokicoin_explorer, nostr_relay); LSPs are intentionally absent.
type Seed struct {
	Services       map[string][]ServiceEntry `yaml:"services" json:"services"`
	Currencies     map[string]Currency       `yaml:"currencies" json:"currencies"`
	FAQ            []FAQ                     `yaml:"faq" json:"faq"`
	Info           Info                      `yaml:"info" json:"info"`
	FiatRateOracle FiatRateOracle            `yaml:"fiat_rate_oracle" json:"-"`
}

var (
	once   sync.Once
	loaded *Seed
)

// Get returns the parsed seed, parsed once and cached. It always returns a
// non-nil value: a malformed embedded seed is a build-time invariant and
// panics immediately. If LOKIHUB_SEED_FILE points at a readable, valid YAML
// file it overrides the embedded seed; an unreadable or invalid override is
// ignored in favour of the embedded one.
func Get() *Seed {
	once.Do(func() {
		loaded = mustParse(embedded)
		if path := os.Getenv("LOKIHUB_SEED_FILE"); path != "" {
			if data, err := os.ReadFile(path); err == nil {
				if s, err := parse(data); err == nil {
					loaded = s
				}
			}
		}
	})
	return loaded
}

func parse(data []byte) (*Seed, error) {
	var s Seed
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func mustParse(data []byte) *Seed {
	s, err := parse(data)
	if err != nil {
		panic("seed: embedded seed.yaml is invalid: " + err.Error())
	}
	return s
}
