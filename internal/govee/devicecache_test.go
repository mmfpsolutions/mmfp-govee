/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package govee

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const oneDeviceBody = `{"code":200,"message":"success","data":[
  {"sku":"H607C","device":"34:FD:CC:44:A9:3A:60:AC","deviceName":"Den Floor Lamp",
   "capabilities":[{"type":"devices.capabilities.on_off","instance":"powerSwitch"}]}
]}`

// A healthy refresh writes the catalog; a later run with the cloud down reads
// it back. This is the restart-during-an-outage path.
func TestDeviceCache_SurvivesOutageAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	healthy, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(oneDeviceBody))
	})
	healthy.EnableDeviceCache(dir)
	devices, err := healthy.RefreshDevices(context.Background())
	srv.Close()
	if err != nil {
		t.Fatalf("healthy refresh: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}

	// A fresh client is a fresh process: empty memory cache, and a cloud that
	// is now returning Govee's outage envelope.
	restarted, outage := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":400,"message":"request error: ChannelException","data":{}}`))
	})
	defer outage.Close()
	restarted.EnableDeviceCache(dir)

	got, err := restarted.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices should fall back to the cached catalog, got: %v", err)
	}
	if len(got) != 1 || got[0].DeviceName != "Den Floor Lamp" {
		t.Fatalf("cached catalog not served: %+v", got)
	}

	// The fetch time must be the ORIGINAL one — the Devices page shows it, and
	// a refreshed timestamp would present stale data as current.
	if restarted.CachedAt().IsZero() {
		t.Error("CachedAt is zero; the UI would show no age for stale data")
	}

	// Refresh is the operator explicitly asking the cloud, so it must still
	// report the outage rather than quietly handing back the cache.
	if _, err := restarted.RefreshDevices(context.Background()); err == nil {
		t.Error("RefreshDevices masked the outage")
	} else if !strings.Contains(err.Error(), "ChannelException") {
		t.Errorf("RefreshDevices lost Govee's message: %v", err)
	}
}

// With no cache on disk, an outage must surface as the Govee error — not as a
// confusing empty device list.
func TestDeviceCache_NoFileKeepsTheError(t *testing.T) {
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":400,"message":"request error: ChannelException","data":{}}`))
	})
	defer srv.Close()
	client.EnableDeviceCache(t.TempDir())

	if _, err := client.ListDevices(context.Background()); err == nil {
		t.Fatal("expected the outage error with no cache on disk")
	}
}

// A truncated or hand-mangled cache must not take the app down with it.
func TestDeviceCache_CorruptFileFallsBackToTheError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, deviceCacheFile), []byte(`{"devices":[`), 0o600); err != nil {
		t.Fatal(err)
	}

	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":400,"message":"request error: ChannelException","data":{}}`))
	})
	defer srv.Close()
	client.EnableDeviceCache(dir)

	if _, err := client.ListDevices(context.Background()); err == nil {
		t.Fatal("a corrupt cache must not be served as a device list")
	}
}
