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
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
)

// The config.Manager is a package singleton; all tests in this package share
// one config dir created in TestMain.
var testCfgManager *config.Manager

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mmfp-govee-handlers-test")
	if err != nil {
		panic(err)
	}
	testCfgManager = config.GetManager(dir)
	if _, err := testCfgManager.WriteDefaultConfig(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// captureController records the device IDs the engine actually drives.
type captureController struct {
	devices chan string
}

func (c *captureController) seen(device string) {
	select {
	case c.devices <- device:
	default:
	}
}

func (c *captureController) Power(_ context.Context, _, device string, _ bool) error {
	c.seen(device)
	return nil
}
func (c *captureController) Color(_ context.Context, _, device string, _ int) error {
	c.seen(device)
	return nil
}
func (c *captureController) Brightness(_ context.Context, _, device string, _ int) error {
	c.seen(device)
	return nil
}
func (c *captureController) Control(_ context.Context, _, device, _, _ string, _ interface{}) error {
	c.seen(device)
	return nil
}
func (c *captureController) State(context.Context, string, string) (*govee.DeviceState, error) {
	return &govee.DeviceState{}, nil
}

// Govee device IDs are MAC-style (colons) and arrive percent-encoded in the
// path — the handler must decode them before they reach the Govee API.
// Regression: an encoded ID was passed through verbatim and Govee 400'd.
func TestHandleDeviceTest_DecodesEscapedDeviceID(t *testing.T) {
	ctrl := &captureController{devices: make(chan string, 1)}
	engine := effects.NewEngine(ctrl, activity.GetLog())
	defer engine.Stop()

	r := chi.NewRouter()
	r.Post("/api/v1/devices/{device}/test", HandleDeviceTest(engine, testCfgManager))

	req := httptest.NewRequest("POST",
		"/api/v1/devices/34%3AFD%3ACC%3A44%3AA9%3A3A%3A60%3AAC/test",
		strings.NewReader(`{"sku":"H607C"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	select {
	case device := <-ctrl.devices:
		if device != "34:FD:CC:44:A9:3A:60:AC" {
			t.Errorf("engine drove device %q, want decoded 34:FD:CC:44:A9:3A:60:AC", device)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine never drove the device")
	}
}

func lightCaps() []govee.Capability {
	return []govee.Capability{
		{Type: govee.CapOnOff, Instance: govee.InstPower},
		{Type: govee.CapColor, Instance: govee.InstColorRgb},
	}
}

// Groups are omitted and the rest come back alphabetical by name.
func TestToDeviceViews_FiltersGroupsAndSorts(t *testing.T) {
	devices := []govee.Device{
		{Device: "d1", SKU: "H6022", DeviceName: "Table Lamp 2", Capabilities: lightCaps()},
		{Device: "g1", SKU: "BaseGroup", DeviceName: "Bedroom Group"}, // group SKU
		{Device: "d2", SKU: "H607C", DeviceName: "den floor lamp", Capabilities: lightCaps()},
		{Device: "g2", SKU: "SameModeGroup", DeviceName: "Pathway lights"}, // group SKU
		{Device: "d3", SKU: "H1370", DeviceName: "Bath Ceiling Fan", Capabilities: lightCaps()},
		{Device: "g3", SKU: "H9999", DeviceName: "Weird Empty", Capabilities: nil}, // no caps → group-like
	}

	views := toDeviceViews(devices, &config.Config{}, nil)

	if len(views) != 3 {
		t.Fatalf("got %d views, want 3 (groups + capabilityless devices filtered)", len(views))
	}
	got := []string{views[0].DeviceName, views[1].DeviceName, views[2].DeviceName}
	want := []string{"Bath Ceiling Fan", "den floor lamp", "Table Lamp 2"} // case-insensitive order
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %q, want %q (full order: %v)", i, got[i], want[i], got)
		}
	}
	for _, v := range views {
		if isGroupName(v.DeviceName) {
			t.Errorf("group %q leaked into the views", v.DeviceName)
		}
	}
}

func isGroupName(name string) bool {
	return name == "Bedroom Group" || name == "Pathway lights" || name == "Weird Empty"
}
