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
	"fmt"
	"net"
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

// lastSource is the Source on the newest activity record.
func lastSource(t *testing.T) string {
	t.Helper()
	recent := activity.GetLog().Recent(1)
	if len(recent) == 0 {
		t.Fatal("no activity recorded")
	}
	return recent[0].Source
}

// MIM sends GSSM's body shape plus "source": "mim"; only that field tells
// them apart. Unmatched events are used so the handler records the source
// synchronously.
func TestHook_SourceDetection(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"gssm (no source field)",
			`{"event_type":"pool_offline","entity":"Pool A","message":"offline","severity":"critical","category":"pool","details":{},"timestamp":"2026-11-06T15:04:05Z"}`,
			"gssm"},
		{"mim",
			`{"source":"mim","event_type":"host_down","entity":"ubs1","message":"Host ubs1 is unreachable","severity":"critical","category":"hosts","details":{},"timestamp":"2026-11-06T15:04:05Z"}`,
			"mim"},
		{"mim test button",
			`{"source":"mim","event_type":"test","entity":"MIM","message":"This is a test notification from MIM.","severity":"info","category":"system","details":{"note":"test event; no host is affected"},"timestamp":"2026-11-06T15:04:05Z"}`,
			"mim"},
		{"gss",
			`{"subject":"Startup","message":"GSS started","type":"shutdown","timestamp":"2026-11-06T15:04:05Z"}`,
			"gss"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(t)
			rec := doRequest(t, srv, "POST", "/hook", "secret-1", tc.body)
			if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
				t.Fatalf("status = %d (body: %s)", rec.Code, rec.Body.String())
			}
			if got := lastSource(t); got != tc.want {
				t.Errorf("source = %q, want %q", got, tc.want)
			}
		})
	}
}

// A MIM event routes on event_type and its host name feeds the entity filter.
func TestHook_MIMPayloadRoutesOnEventType(t *testing.T) {
	srv, mgr := newTestServer(t)
	cfg := mgr.GetConfig()
	cfg.Mappings = append(cfg.Mappings, config.Mapping{
		ID: "map3", Name: "ubs1 down", Token: "*",
		Events:       []string{"host_down"},
		EntityFilter: "ubs1",
		Devices:      []config.DeviceRef{{SKU: "H1", Device: "EE:FF"}},
		Effect:       "off", Params: config.EffectParams{},
		CooldownSeconds: 0, Enabled: true,
	})
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}

	rec := doRequest(t, srv, "POST", "/hook", "secret-1",
		`{"source":"mim","event_type":"host_down","entity":"ubs1","message":"down","severity":"critical","category":"hosts","details":{},"timestamp":"2026-11-06T15:04:05Z"}`)
	if !strings.Contains(rec.Body.String(), "queued 1") {
		t.Errorf("ubs1: body = %q, want queued 1", rec.Body.String())
	}
	rec = doRequest(t, srv, "POST", "/hook", "secret-1",
		`{"source":"mim","event_type":"host_down","entity":"ubs2","message":"down"}`)
	if !strings.Contains(rec.Body.String(), "no mapping") {
		t.Errorf("ubs2: body = %q, want no mapping", rec.Body.String())
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

// A webhook port that cannot be bound leaves the app running (no restart
// loop) but must REPORT not running, so the Health page and the Docker
// healthcheck can say so.
func TestStart_BindFailureReportsNotRunning(t *testing.T) {
	// Hold the WILDCARD address, as a real conflicting process on :8787
	// would. (macOS lets a wildcard bind coexist with a 127.0.0.1 one.)
	held, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	srv, _ := newTestServer(t)
	srv.Start(port)
	defer srv.Stop(context.Background())
	if srv.Running() {
		t.Fatalf("port %d is held, yet Running() = true", port)
	}
	if srv.Port() != port {
		t.Errorf("Port() = %d, want %d", srv.Port(), port)
	}
}

func TestStart_BindsAndServesHealth(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	srv, _ := newTestServer(t)
	srv.Start(port)
	defer srv.Stop(context.Background())
	if !srv.Running() {
		t.Fatal("free port, yet Running() = false")
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health = %d", resp.StatusCode)
	}
}
