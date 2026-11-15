package manager

import (
	"encoding/json"
	"errors"

	"github.com/flokiorg/lokihub/lsps/persist"
)

const dbKeyLSPs = "lsps_settings_list"

// LoadLSPs retrieves the list of LSPs from the KV store
func LoadLSPs(kv persist.KVStore) ([]SettingsLSP, error) {
	if kv == nil {
		return nil, nil
	}
	data, err := kv.Read(dbKeyLSPs)
	if err != nil {
		return nil, nil // Assume empty/not found on error for resilience
	}
	if len(data) == 0 {
		return []SettingsLSP{}, nil
	}

	var list []SettingsLSP
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SaveLSPs writes the list of LSPs to the KV store
func SaveLSPs(kv persist.KVStore, list []SettingsLSP) error {
	if kv == nil {
		return errors.New("no persistence available")
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return kv.Write(dbKeyLSPs, data)
}
