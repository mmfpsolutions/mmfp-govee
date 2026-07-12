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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmfpsolutions/mmfp-govee/internal/security"
)

// The Manager is a package singleton (GetManager latches its directory on
// first call), so every test shares one manager rooted here.
var sharedConfigDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mmfp-govee-config-test")
	if err != nil {
		panic(err)
	}
	sharedConfigDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func writeConfigFile(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(sharedConfigDir, "config.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_DefaultsAndEncryptionMigration(t *testing.T) {
	writeConfigFile(t, `{
		"goveeApiKey": "plaintext-key",
		"tokens": [{"name": "gss-main", "token": "plaintext-secret"}],
		"mappings": [{"id": "abc", "name": "m", "token": "*", "events": ["block_found"],
			"devices": [{"sku": "H1", "device": "AA"}], "effect": "solid",
			"params": {"colorA": 65280}, "enabled": true}]
	}`)

	mgr := GetManager(sharedConfigDir)
	cfg, err := mgr.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Defaults
	if cfg.WebServerPort != DefaultWebServerPort {
		t.Errorf("WebServerPort = %d, want %d", cfg.WebServerPort, DefaultWebServerPort)
	}
	if cfg.WebhookPort != DefaultWebhookPort {
		t.Errorf("WebhookPort = %d, want %d", cfg.WebhookPort, DefaultWebhookPort)
	}
	if !cfg.AuthDisabled() {
		t.Error("AuthDisabled() = false, want true (auth is opt-in)")
	}
	if cfg.Mappings[0].CooldownSeconds != DefaultCooldownSecs {
		t.Errorf("CooldownSeconds default = %d, want %d", cfg.Mappings[0].CooldownSeconds, DefaultCooldownSecs)
	}

	// Encryption migration: in-memory values are encrypted...
	if !security.IsEncrypted(cfg.GoveeAPIKey) {
		t.Error("goveeApiKey not encrypted after load")
	}
	if !security.IsEncrypted(cfg.Tokens[0].Token) {
		t.Error("token secret not encrypted after load")
	}
	// ...and round-trip back to the original plaintext.
	key, err := security.DecryptIfEncrypted(cfg.GoveeAPIKey)
	if err != nil || key != "plaintext-key" {
		t.Errorf("decrypted key = %q, %v; want plaintext-key", key, err)
	}

	// The file on disk was rewritten with no plaintext secrets left.
	raw, err := os.ReadFile(filepath.Join(sharedConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "plaintext-key") || strings.Contains(string(raw), "plaintext-secret") {
		t.Error("plaintext secrets still present in rewritten config.json")
	}
	var onDisk map[string]interface{}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("rewritten config.json is not valid JSON: %v", err)
	}
}

func TestLoadConfig_AlreadyEncryptedIsStable(t *testing.T) {
	enc, err := security.Encrypt("the-key")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]interface{}{"goveeApiKey": enc})
	writeConfigFile(t, string(data))

	mgr := GetManager(sharedConfigDir)
	cfg, err := mgr.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.GoveeAPIKey != enc {
		t.Error("already-encrypted key was re-encrypted (should be stable)")
	}
}

func TestSetupRequired(t *testing.T) {
	writeConfigFile(t, `{"goveeApiKey": ""}`)
	mgr := GetManager(sharedConfigDir)
	if _, err := mgr.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if !mgr.SetupRequired() {
		t.Error("SetupRequired() = false with empty key, want true")
	}

	writeConfigFile(t, `{"goveeApiKey": "some-key"}`)
	if _, err := mgr.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if mgr.SetupRequired() {
		t.Error("SetupRequired() = true with key set, want false")
	}
}

func TestAfterSceneFor_Precedence(t *testing.T) {
	deviceScene := &SceneRef{Instance: "lightScene", Name: "Sunrise", Value: json.RawMessage(`{"id":1}`)}
	mappingScene := &SceneRef{Instance: "lightScene", Name: "Sunset", Value: json.RawMessage(`{"id":2}`)}

	cfg := &Config{DeviceScenes: map[string]*SceneRef{"AA:BB": deviceScene}}

	// Mapping-level override wins.
	got := cfg.AfterSceneFor(DeviceRef{Device: "AA:BB", AfterScene: mappingScene})
	if got == nil || got.Name != "Sunset" {
		t.Errorf("with override: got %+v, want Sunset", got)
	}

	// No override → device's assigned scene.
	got = cfg.AfterSceneFor(DeviceRef{Device: "AA:BB"})
	if got == nil || got.Name != "Sunrise" {
		t.Errorf("device default: got %+v, want Sunrise", got)
	}

	// Unassigned device → nil.
	if got = cfg.AfterSceneFor(DeviceRef{Device: "CC:DD"}); got != nil {
		t.Errorf("unassigned device: got %+v, want nil", got)
	}

	// Nil map → nil.
	empty := &Config{}
	if got = empty.AfterSceneFor(DeviceRef{Device: "AA:BB"}); got != nil {
		t.Errorf("nil map: got %+v, want nil", got)
	}
}

func TestGenerateID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := GenerateID()
		if len(id) != 8 {
			t.Fatalf("GenerateID() length = %d, want 8", len(id))
		}
		if seen[id] {
			t.Fatalf("GenerateID() produced a duplicate in 100 draws: %s", id)
		}
		seen[id] = true
	}
}

func TestGenerateSecret(t *testing.T) {
	s := GenerateSecret()
	if len(s) != 32 {
		t.Fatalf("GenerateSecret() length = %d, want 32", len(s))
	}
	if s == GenerateSecret() {
		t.Fatal("GenerateSecret() produced identical consecutive secrets")
	}
}
