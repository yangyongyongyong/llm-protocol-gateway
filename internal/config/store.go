package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const configFileVersion = 1

type Store struct {
	path string
}

type persistedState struct {
	Version      int                          `json:"version"`
	Providers    []domain.Provider            `json:"providers"`
	Routes       []domain.Route               `json:"routes"`
	Models       []domain.Model               `json:"models"`
	PublicAccess *domain.PublicAccessSettings `json:"publicAccess,omitempty"`
	LogLevel     string                       `json:"logLevel,omitempty"`
	// NOTE: apiKeySource is persisted verbatim as the user entered it (for example
	// env:VAR or literal:token). Storing secrets in the OS Keychain (via a
	// keychain: source) is a planned future improvement so they never touch disk.
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func NewDefaultStore() (*Store, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	return NewStore(path), nil
}

func DefaultConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("GATEWAY_CONFIG")); path != "" {
		return filepath.Abs(path)
	}
	if configDir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(configDir) != "" {
		return filepath.Join(configDir, "llm-protocol-gateway", "config.json"), nil
	}
	// Fall back to a home-directory path when the user config dir is unavailable.
	// This must be cwd-independent so a restart from a different working directory
	// still resolves to the same config file.
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".llm-protocol-gateway", "config.json"), nil
	}
	// Last-resort cwd-relative fallback so this function can never hard-fail.
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, "data", "config.json"), nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load(defaultState domain.GatewayState) (domain.GatewayState, error) {
	state := defaultState
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return domain.GatewayState{}, err
	}

	var persisted persistedState
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return domain.GatewayState{}, fmt.Errorf("parse %s: %w", s.path, err)
	}

	state.Providers = nonNilProviders(persisted.Providers)
	state.Routes = nonNilRoutes(persisted.Routes)
	state.Models = nonNilModels(persisted.Models)
	state.Metrics = domain.MetricsSnapshot{}
	if persisted.PublicAccess != nil {
		state.PublicAccess = *persisted.PublicAccess
	}
	state.LogLevel = strings.TrimSpace(persisted.LogLevel)
	return state, nil
}

func (s *Store) Save(state domain.GatewayState) error {
	publicAccess := state.PublicAccess
	// Never persist the live tunnel runtime snapshot; it is process-scoped.
	publicAccess.Tunnel = nil
	persisted := persistedState{
		Version:      configFileVersion,
		Providers:    nonNilProviders(state.Providers),
		Routes:       nonNilRoutes(state.Routes),
		Models:       nonNilModels(state.Models),
		PublicAccess: &publicAccess,
		LogLevel:     strings.TrimSpace(state.LogLevel),
	}

	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return nil
}

func nonNilProviders(items []domain.Provider) []domain.Provider {
	if items == nil {
		return []domain.Provider{}
	}
	return items
}

func nonNilRoutes(items []domain.Route) []domain.Route {
	if items == nil {
		return []domain.Route{}
	}
	return items
}

func nonNilModels(items []domain.Model) []domain.Model {
	if items == nil {
		return []domain.Model{}
	}
	return items
}
