/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package config

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"

	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	"github.com/mmfpsolutions/mmfp-govee/internal/security"
)

// LoggingConfig contains logging settings
type LoggingConfig struct {
	Level       string `json:"level"`
	LogToFile   bool   `json:"logToFile"`
	LogFilePath string `json:"logFilePath,omitempty"`
	MaxSizeMb   int    `json:"maxSizeMb,omitempty"`
	MaxAgeDays  int    `json:"maxAgeDays,omitempty"`
	MaxBackups  int    `json:"maxBackups,omitempty"`
	Compress    bool   `json:"compress,omitempty"`
}

// QuietHoursConfig suppresses effect execution inside a local-time window
// (e.g. 22:00 → 07:00). Webhooks are still received and logged to Activity;
// only the light effects are skipped.
type QuietHoursConfig struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"` // "HH:MM" local time
	End     string `json:"end"`   // "HH:MM" local time
}

// Token is a per-caller webhook secret. The token value is encrypted at rest
// (ENC: prefix) and decrypted at point of use.
type Token struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// SceneRef names a scene from the device's scene catalog. Values are
// PER-DEVICE (paramId differs across devices) — never copy one device's
// value to another.
type SceneRef struct {
	Instance string          `json:"instance"` // "lightScene" | "diyScene" | "snapshot"
	Name     string          `json:"name"`     // display only
	Value    json.RawMessage `json:"value"`    // controllable value from /device/scenes
}

// DeviceRef identifies one Govee device (from /user/devices). AfterScene,
// when set, is applied after each effect on this device — the deliberate
// final state for lamps that normally run a scene (the Govee API cannot
// report the active scene, so restore can't put one back).
type DeviceRef struct {
	SKU        string    `json:"sku"`
	Device     string    `json:"device"`
	AfterScene *SceneRef `json:"afterScene,omitempty"`
}

// EffectParams parameterizes a canned effect. Colors are 24-bit RGB ints
// (0xRRGGBB), matching the Govee colorRgb capability.
type EffectParams struct {
	ColorA     int    `json:"colorA,omitempty"`
	ColorB     int    `json:"colorB,omitempty"`
	Cycles     int    `json:"cycles,omitempty"`
	DelayMs    int    `json:"delayMs,omitempty"`
	Brightness int    `json:"brightness,omitempty"`
	EndState   string `json:"endState,omitempty"` // "holdA" | "holdB" | "off"
	Restore    bool   `json:"restore,omitempty"`  // capture device state before, restore after
}

// Mapping routes (token, event) → devices + effect.
type Mapping struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Token           string       `json:"token"` // token NAME, or "*" for any valid token
	Events          []string     `json:"events"`
	EntityFilter    string       `json:"entityFilter,omitempty"` // substring match on payload entity ("" = all)
	Devices         []DeviceRef  `json:"devices"`
	Effect          string       `json:"effect"`
	Params          EffectParams `json:"params"`
	CooldownSeconds int          `json:"cooldownSeconds,omitempty"`
	Enabled         bool         `json:"enabled"`
}

// Config represents the main application configuration
type Config struct {
	WebServerPort         int               `json:"webServerPort"`
	WebhookPort           int               `json:"webhookPort"`
	Title                 string            `json:"title,omitempty"`
	GoveeAPIKey           string            `json:"goveeApiKey"`
	DisableAuthentication *bool             `json:"disableAuthentication,omitempty"` // default TRUE (auth is opt-in)
	CookieMaxAge          int               `json:"cookieMaxAge,omitempty"`
	Logging               *LoggingConfig    `json:"logging,omitempty"`
	QuietHours            *QuietHoursConfig `json:"quietHours,omitempty"`
	Tokens                []Token           `json:"tokens"`
	Mappings              []Mapping         `json:"mappings"`
	// DeviceScenes assigns each device its normal scene (keyed by device ID).
	// Every effect on that device — webhook mappings AND the Devices-page
	// test blink — ends by re-applying this scene, unless the mapping's
	// device ref carries its own afterScene override.
	DeviceScenes map[string]*SceneRef `json:"deviceScenes,omitempty"`
}

// AfterSceneFor resolves the scene to apply after an effect on a device:
// the mapping-level override wins, then the device's assigned scene, then
// none.
func (c *Config) AfterSceneFor(d DeviceRef) *SceneRef {
	if d.AfterScene != nil {
		return d.AfterScene
	}
	if c.DeviceScenes != nil {
		return c.DeviceScenes[d.Device]
	}
	return nil
}

// AuthDisabled reports whether the web UI auth gate is off. Auth is opt-in:
// a missing disableAuthentication field means disabled (true).
func (c *Config) AuthDisabled() bool {
	if c.DisableAuthentication == nil {
		return true
	}
	return *c.DisableAuthentication
}

const (
	DefaultWebServerPort = 3008
	DefaultWebhookPort   = 8787
	DefaultCooldownSecs  = 10
)

// Manager handles configuration loading and hot-reloading
type Manager struct {
	config     *Config
	configPath string
	mu         sync.RWMutex
	log        *logger.Logger
}

var (
	instance *Manager
	once     sync.Once
)

// GetManager returns the singleton configuration manager instance
func GetManager(configDir string) *Manager {
	once.Do(func() {
		instance = &Manager{
			configPath: filepath.Join(configDir, "config.json"),
			log:        logger.New(logger.ModuleConfig),
		}
	})
	return instance
}

// LoadConfig loads the configuration from file
func (m *Manager) LoadConfig() (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Loading configuration from: %s", m.configPath)

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("error parsing config file: %w", err)
	}

	applyDefaults(&config)

	// Encrypt any plaintext sensitive fields in place and rewrite the file
	// if anything changed. Zero-touch migration — plaintext values continue
	// to work transparently via security.DecryptIfEncrypted at every read
	// site until the rewrite lands.
	if err := m.migrateEncryption(&config); err != nil {
		m.log.Warn("Encryption migration failed: %v", err)
	}

	m.config = &config
	m.log.Info("Configuration loaded successfully")

	return &config, nil
}

func applyDefaults(config *Config) {
	if config.WebServerPort == 0 {
		config.WebServerPort = DefaultWebServerPort
	}
	if config.WebhookPort == 0 {
		config.WebhookPort = DefaultWebhookPort
	}
	if config.Title == "" {
		config.Title = "MMFP Govee"
	}
	if config.CookieMaxAge == 0 {
		config.CookieMaxAge = 3600
	}
	if config.Logging == nil {
		config.Logging = &LoggingConfig{Level: "info"}
	}
	if config.Logging.MaxSizeMb == 0 {
		config.Logging.MaxSizeMb = 20
	}
	if config.Logging.MaxAgeDays == 0 {
		config.Logging.MaxAgeDays = 30
	}
	if config.Logging.MaxBackups == 0 {
		config.Logging.MaxBackups = 10
	}
	for i := range config.Mappings {
		if config.Mappings[i].CooldownSeconds == 0 {
			config.Mappings[i].CooldownSeconds = DefaultCooldownSecs
		}
	}
}

// migrateEncryption walks the known sensitive fields and encrypts any that
// are still in plaintext form (no ENC: prefix). Rewrites config.json with
// 0600 perms if anything was migrated. Errors are returned but the caller
// treats them as non-fatal — a config load shouldn't fail because of a
// migration hiccup; the affected fields just stay plaintext until next save.
func (m *Manager) migrateEncryption(cfg *Config) error {
	needsRewrite := false

	if cfg.GoveeAPIKey != "" && !security.IsEncrypted(cfg.GoveeAPIKey) {
		encrypted, err := security.Encrypt(cfg.GoveeAPIKey)
		if err != nil {
			return fmt.Errorf("encrypt goveeApiKey: %w", err)
		}
		cfg.GoveeAPIKey = encrypted
		needsRewrite = true
	}

	for i, t := range cfg.Tokens {
		if t.Token != "" && !security.IsEncrypted(t.Token) {
			encrypted, err := security.Encrypt(t.Token)
			if err != nil {
				return fmt.Errorf("encrypt tokens[%d]: %w", i, err)
			}
			cfg.Tokens[i].Token = encrypted
			needsRewrite = true
		}
	}

	if !needsRewrite {
		return nil
	}

	data, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal config for encryption migration: %w", err)
	}
	if err := os.WriteFile(m.configPath, data, 0600); err != nil {
		return fmt.Errorf("rewrite config with encrypted secrets: %w", err)
	}
	m.log.Info("Migrated plaintext secrets in config.json to encrypted form")
	return nil
}

// ReloadConfig reloads the configuration from file
func (m *Manager) ReloadConfig() (*Config, error) {
	m.log.Info("Reloading configuration...")
	return m.LoadConfig()
}

// GetConfig returns the current configuration
func (m *Manager) GetConfig() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

// GetConfigDir returns the configuration directory path
func (m *Manager) GetConfigDir() string {
	return filepath.Dir(m.configPath)
}

// ConfigFileExists returns true if the config.json file exists on disk
func (m *Manager) ConfigFileExists() bool {
	_, err := os.Stat(m.configPath)
	return !os.IsNotExist(err)
}

// SetupRequired returns true if the app is in first-time setup mode
// (no Govee API key configured yet)
func (m *Manager) SetupRequired() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config == nil {
		return true
	}
	return m.config.GoveeAPIKey == ""
}

// WriteDefaultConfig writes a baseline config.json to disk and loads it into
// memory. Called during first-time setup so that subsequent saves patch
// against a full config.
func (m *Manager) WriteDefaultConfig() (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.configPath), 0755); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}

	cfg := &Config{
		Tokens:   []Token{},
		Mappings: []Mapping{},
	}
	applyDefaults(cfg)

	data, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return nil, fmt.Errorf("marshal default config: %w", err)
	}
	if err := os.WriteFile(m.configPath, data, 0600); err != nil {
		return nil, fmt.Errorf("write default config: %w", err)
	}

	m.config = cfg
	m.log.Info("Default configuration written to %s", m.configPath)
	return cfg, nil
}

// Save persists the given config to disk and swaps it into memory. Callers
// mutate a copy from GetConfig (or build updates) and hand it here; secrets
// must already be in encrypted form (helpers below take care of that).
func (m *Manager) Save(cfg *Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	applyDefaults(cfg)

	data, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(m.configPath, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	m.config = cfg
	return nil
}

// GenerateID returns an 8-character base62 alphanumeric ID (crypto/rand),
// same convention as GSSM entity IDs.
func GenerateID() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	id := make([]byte, 8)
	for i := range id {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			// crypto/rand failure is unrecoverable for ID generation
			panic(fmt.Sprintf("crypto/rand failure: %v", err))
		}
		id[i] = charset[n.Int64()]
	}
	return string(id)
}

// GenerateSecret returns a 32-character base62 secret for webhook tokens.
func GenerateSecret() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	secret := make([]byte, 32)
	for i := range secret {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			panic(fmt.Sprintf("crypto/rand failure: %v", err))
		}
		secret[i] = charset[n.Int64()]
	}
	return string(secret)
}
