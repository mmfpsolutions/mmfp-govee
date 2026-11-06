/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package v1

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
)

type fakeListener struct {
	running bool
	port    int
}

func (f fakeListener) Running() bool { return f.running }
func (f fakeListener) Port() int     { return f.port }

type fakeGoveeStatus struct {
	lanRunning   bool
	routes       int
	lastScan     time.Time
	breakerOpen  bool
	breakerUntil time.Time
	breakerErr   error
	used, limit  int
	cachedAt     time.Time
}

func (f fakeGoveeStatus) LANEnabled() bool { return f.lanRunning }
func (f fakeGoveeStatus) LANRoutes() map[string]govee.LANRoute {
	m := map[string]govee.LANRoute{}
	for i := 0; i < f.routes; i++ {
		m[string(rune('a'+i))] = govee.LANRoute{}
	}
	return m
}
func (f fakeGoveeStatus) LANLastScan() time.Time { return f.lastScan }
func (f fakeGoveeStatus) BreakerState() (bool, time.Time, error) {
	return f.breakerOpen, f.breakerUntil, f.breakerErr
}
func (f fakeGoveeStatus) CallsToday() (int, int) { return f.used, f.limit }
func (f fakeGoveeStatus) CachedAt() time.Time    { return f.cachedAt }

func boolPtr(b bool) *bool { return &b }

func TestBuildServices_AllHealthy(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{LAN: &config.LANConfig{Enabled: boolPtr(true)}}
	v := buildServices(cfg, fakeListener{running: true, port: 8787}, fakeGoveeStatus{
		lanRunning: true, routes: 8, lastScan: now.Add(-4 * time.Minute),
		used: 412, limit: 10000, cachedAt: now.Add(-12 * time.Minute),
	}, now)

	if !v.Webhook.Running || v.Webhook.Port != 8787 {
		t.Errorf("webhook = %+v", v.Webhook)
	}
	if !v.LAN.Enabled || !v.LAN.Running || v.LAN.Devices != 8 || v.LAN.LastScanSecs == nil || *v.LAN.LastScanSecs != 240 {
		t.Errorf("lan = %+v", v.LAN)
	}
	if v.Cloud.Paused || v.Cloud.CallsToday != 412 || v.Cloud.CallsLimit != 10000 || v.Cloud.LastError != "" {
		t.Errorf("cloud = %+v", v.Cloud)
	}
	if v.Catalog.FetchedSecs == nil || *v.Catalog.FetchedSecs != 720 {
		t.Errorf("catalog = %+v", v.Catalog)
	}
}

func TestBuildServices_Faults(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	// LAN enabled in config but the transport isn't running (UDP 4002 held);
	// webhook bind failed; breaker open; catalog never fetched.
	cfg := &config.Config{LAN: &config.LANConfig{Enabled: boolPtr(true)}}
	v := buildServices(cfg, fakeListener{running: false, port: 8787}, fakeGoveeStatus{
		breakerOpen: true, breakerUntil: now.Add(42 * time.Second),
		breakerErr: errors.New("govee API error 400: request error: ChannelException"),
		used:       3, limit: 10000,
	}, now)

	if v.Webhook.Running {
		t.Error("webhook should report not running")
	}
	if !v.LAN.Enabled || v.LAN.Running || v.LAN.LastScanSecs != nil {
		t.Errorf("lan = %+v, want enabled but not running, never scanned", v.LAN)
	}
	if !v.Cloud.Paused || v.Cloud.RetryInSecs != 42 || !strings.Contains(v.Cloud.LastError, "ChannelException") {
		t.Errorf("cloud = %+v", v.Cloud)
	}
	if v.Catalog.FetchedSecs != nil {
		t.Errorf("catalog = %+v, want never fetched", v.Catalog)
	}

	// LAN disabled in config is "off", not a fault.
	cfg.LAN.Enabled = boolPtr(false)
	if lan := buildServices(cfg, nil, fakeGoveeStatus{}, now).LAN; lan.Enabled || lan.Running {
		t.Errorf("disabled lan = %+v", lan)
	}
}

// The payload carries every card and nothing secret from config.
func TestHandleMetrics_PayloadAndNoSecrets(t *testing.T) {
	original := testCfgManager.GetConfig()
	t.Cleanup(func() { testCfgManager.Save(original) })
	clone := cloneConfig(original)
	clone.GoveeAPIKey = "super-secret-govee-key"
	clone.Tokens = []config.Token{{Name: "gss-main", Token: "super-secret-token"}}
	if err := testCfgManager.Save(clone); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	HandleMetrics(testCfgManager, fakeGoveeStatus{limit: 10000}, fakeListener{running: true, port: 8787})(
		rec, httptest.NewRequest("GET", "/api/v1/metrics", nil))

	body := rec.Body.String()
	if strings.Contains(body, "super-secret") {
		t.Fatalf("metrics payload leaks a secret: %s", body)
	}
	var out struct {
		Data map[string]map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, card := range []string{"runtime", "memory", "gc", "services"} {
		if _, ok := out.Data[card]; !ok {
			t.Errorf("payload missing %q card", card)
		}
	}
	for _, svc := range []string{"webhook", "lan", "cloud", "catalog"} {
		if _, ok := out.Data["services"][svc]; !ok {
			t.Errorf("services missing %q", svc)
		}
	}
}
