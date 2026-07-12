/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package hooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/security"
)

// nullController satisfies effects.Controller without doing anything.
type nullController struct {
	mu    sync.Mutex
	calls int
}

func (n *nullController) bump() {
	n.mu.Lock()
	n.calls++
	n.mu.Unlock()
}

func (n *nullController) Power(context.Context, string, string, bool) error { n.bump(); return nil }
func (n *nullController) Color(context.Context, string, string, int) error  { n.bump(); return nil }
func (n *nullController) Brightness(context.Context, string, string, int) error {
	n.bump()
	return nil
}
func (n *nullController) Control(context.Context, string, string, string, string, interface{}) error {
	n.bump()
	return nil
}
func (n *nullController) State(context.Context, string, string) (*govee.DeviceState, error) {
	n.bump()
	return &govee.DeviceState{}, nil
}

// sharedConfigDir backs the config.Manager singleton for the whole test
// binary — GetManager latches its directory on first call, so a per-test
// t.TempDir would be torn down while later tests still write to it.
var sharedConfigDir string

// newTestServer stands up a hooks server against the real config manager
// (package singleton — each test swaps in its own config via Save).
func newTestServer(t *testing.T) (*Server, *config.Manager) {
	t.Helper()

	mgr := config.GetManager(sharedConfigDir)

	encToken, err := security.Encrypt("secret-1")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Tokens: []config.Token{{Name: "gss-main", Token: encToken}},
		Mappings: []config.Mapping{
			{
				ID: "map1", Name: "Block celebrate", Token: "gss-main",
				Events:  []string{"block_found", "miner_offline"},
				Devices: []config.DeviceRef{{SKU: "H1", Device: "AA:BB"}},
				Effect:  "solid", Params: config.EffectParams{ColorA: 0x00FF00},
				CooldownSeconds: 0, Enabled: true,
			},
			{
				ID: "map2", Name: "Garage only", Token: "*",
				Events:       []string{"miner_offline"},
				EntityFilter: "garage",
				Devices:      []config.DeviceRef{{SKU: "H1", Device: "CC:DD"}},
				Effect:       "off", Params: config.EffectParams{},
				CooldownSeconds: 0, Enabled: true,
			},
		},
	}
	// The singleton may hold state from a prior test; Save both persists and
	// swaps in this test's config.
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}

	engine := effects.NewEngine(&nullController{}, activity.GetLog())
	t.Cleanup(engine.Stop)
	return NewServer(mgr, engine, activity.GetLog()), mgr
}

func doRequest(t *testing.T, srv *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set(TokenHeader, token)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func TestHook_RejectsBadToken(t *testing.T) {
	srv, _ := newTestServer(t)

	if rec := doRequest(t, srv, "POST", "/hook", "wrong", `{"type":"block_found"}`); rec.Code != http.StatusForbidden {
		t.Errorf("bad token: status = %d, want 403", rec.Code)
	}
	if rec := doRequest(t, srv, "POST", "/hook", "", `{"type":"block_found"}`); rec.Code != http.StatusForbidden {
		t.Errorf("missing token: status = %d, want 403", rec.Code)
	}
}

func TestHook_QueryParamToken(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("POST", "/hook?token=secret-1", strings.NewReader(`{"type":"block_found"}`))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("query token: status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestHook_GSSPayloadRoutesOnType(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doRequest(t, srv, "POST", "/hook", "secret-1",
		`{"subject":"Block Found!","message":"DGB block 123","type":"block_found","timestamp":"2026-07-11T15:04:05Z"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "queued 1") {
		t.Errorf("body = %q, want queued 1", rec.Body.String())
	}
}

func TestHook_GSSMPayloadRoutesOnEventType(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doRequest(t, srv, "POST", "/hook", "secret-1",
		`{"event_type":"miner_offline","entity":"Bitaxe Garage","message":"offline","severity":"critical","category":"miner","details":{},"timestamp":"2026-07-11T15:04:05Z"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	// map1 matches on event; map2 matches on event + entity "garage"
	if !strings.Contains(rec.Body.String(), "queued 2") {
		t.Errorf("body = %q, want queued 2", rec.Body.String())
	}
}

func TestHook_EntityFilterExcludes(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doRequest(t, srv, "POST", "/hook", "secret-1",
		`{"event_type":"miner_offline","entity":"Attic Miner","message":"offline"}`)
	// Only map1 (no filter) matches; map2's "garage" filter excludes "Attic Miner"
	if !strings.Contains(rec.Body.String(), "queued 1") {
		t.Errorf("body = %q, want queued 1", rec.Body.String())
	}
}

func TestHook_ForcedEventPath(t *testing.T) {
	srv, _ := newTestServer(t)
	// GET with no body — the prototype/curl mode
	rec := doRequest(t, srv, "GET", "/hook/block_found", "secret-1", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "queued 1") {
		t.Errorf("body = %q, want queued 1", rec.Body.String())
	}
}

func TestHook_TestEventsReturn200(t *testing.T) {
	srv, _ := newTestServer(t)

	// GSSM Test button
	rec := doRequest(t, srv, "POST", "/hook", "secret-1",
		`{"event_type":"test","message":"This is a test notification from GSS Miners.","timestamp":"2026-07-11T15:04:05Z"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("GSSM test: status = %d, want 200", rec.Code)
	}

	// GSS Test button (subject key startup)
	rec = doRequest(t, srv, "POST", "/hook", "secret-1",
		`{"subject":"Test Notification","message":"...","type":"startup","timestamp":"2026-07-11T15:04:05Z"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("GSS test: status = %d, want 200", rec.Code)
	}
}

func TestHook_UnmatchedEventIs202(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doRequest(t, srv, "POST", "/hook", "secret-1", `{"type":"payment_complete"}`)
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no mapping") {
		t.Errorf("body = %q, want no mapping", rec.Body.String())
	}
}

func TestHook_BodyWithoutEventIs400(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doRequest(t, srv, "POST", "/hook", "secret-1", `{"hello":"world"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHook_DisabledMappingSkipped(t *testing.T) {
	srv, mgr := newTestServer(t)

	cfg := mgr.GetConfig()
	clone := *cfg
	clone.Mappings = append([]config.Mapping(nil), cfg.Mappings...)
	for i := range clone.Mappings {
		clone.Mappings[i].Enabled = false
	}
	if err := mgr.Save(&clone); err != nil {
		t.Fatal(err)
	}

	rec := doRequest(t, srv, "POST", "/hook", "secret-1", `{"type":"block_found"}`)
	if !strings.Contains(rec.Body.String(), "no mapping") {
		t.Errorf("body = %q, want no mapping (all disabled)", rec.Body.String())
	}
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mmfp-govee-hooks-test")
	if err != nil {
		panic(err)
	}
	sharedConfigDir = dir
	code := m.Run()
	os.RemoveAll(filepath.Join(dir))
	os.Exit(code)
}
