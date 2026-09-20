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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const outageBody = `{"code":400,"message":"request error: ChannelException","data":{}}`

// The point of the breaker: after the threshold, calls stop reaching the
// network at all, so a fan-out across devices no longer costs one timeout
// each.
func TestBreaker_OpensAfterThresholdAndStopsCallingOut(t *testing.T) {
	var hits int32
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(outageBody))
	})
	defer srv.Close()

	for i := 0; i < breakerThreshold; i++ {
		_ = client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)
	}
	if got := atomic.LoadInt32(&hits); got != breakerThreshold {
		t.Fatalf("network hits before the breaker opened = %d, want %d", got, breakerThreshold)
	}

	// Further calls must fail without touching the network.
	err := client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)
	if err == nil {
		t.Fatal("expected the breaker to reject the call")
	}
	if !strings.Contains(err.Error(), "not retrying") {
		t.Errorf("error does not read as a breaker rejection: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != breakerThreshold {
		t.Errorf("breaker let %d calls through; it should have stopped at %d", got, breakerThreshold)
	}
	if !strings.Contains(err.Error(), "ChannelException") {
		t.Errorf("breaker rejection drops the underlying cause: %v", err)
	}
}

// A rejected request proves the cloud is UP. One bad control call must never
// stop all traffic — this is the breaker's key safety property.
func TestBreaker_RequestRejectionDoesNotOpenIt(t *testing.T) {
	var hits int32
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"code":400,"message":"parameter is invalid","data":{}}`))
	})
	defer srv.Close()

	for i := 0; i < breakerThreshold+3; i++ {
		_ = client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)
	}
	if got := atomic.LoadInt32(&hits); got != breakerThreshold+3 {
		t.Errorf("a parameter rejection opened the breaker: %d of %d calls reached the network",
			got, breakerThreshold+3)
	}
}

// Likewise a 401: a bad API key is a config problem, and the cloud answering
// 401 proves it is reachable.
func TestBreaker_AuthRejectionDoesNotOpenIt(t *testing.T) {
	var hits int32
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Unauthorized"}`))
	})
	defer srv.Close()

	for i := 0; i < breakerThreshold+2; i++ {
		_ = client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)
	}
	if got := atomic.LoadInt32(&hits); got != breakerThreshold+2 {
		t.Errorf("a 401 opened the breaker: %d of %d calls reached the network",
			got, breakerThreshold+2)
	}
}

// A 5xx IS the cloud failing, so it must open the breaker.
func TestBreaker_ServerErrorOpensIt(t *testing.T) {
	var hits int32
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
	})
	defer srv.Close()

	for i := 0; i < breakerThreshold+3; i++ {
		_ = client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)
	}
	if got := atomic.LoadInt32(&hits); got != breakerThreshold {
		t.Errorf("5xx should have opened the breaker at %d calls, got %d through",
			breakerThreshold, got)
	}
}

// Refresh is the operator explicitly asking the cloud — it must always reach
// the network, and a success must close the breaker for everyone else.
func TestBreaker_RefreshProbesAndRecoveryClosesIt(t *testing.T) {
	var healthy atomic.Bool
	var hits int32
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if healthy.Load() {
			w.Write([]byte(oneDeviceBody))
			return
		}
		w.Write([]byte(outageBody))
	})
	defer srv.Close()

	for i := 0; i < breakerThreshold; i++ {
		_ = client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)
	}
	if err := client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil); err == nil {
		t.Fatal("breaker should be open")
	}

	// Govee recovers; the operator hits Refresh.
	healthy.Store(true)
	before := atomic.LoadInt32(&hits)
	if _, err := client.RefreshDevices(context.Background()); err != nil {
		t.Fatalf("Refresh must bypass the open breaker, got: %v", err)
	}
	if atomic.LoadInt32(&hits) != before+1 {
		t.Error("Refresh did not reach the network")
	}

	// And the breaker is now closed for ordinary callers.
	if err := client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil); err != nil {
		t.Errorf("breaker stayed open after a successful refresh: %v", err)
	}
}

// When the cooldown lapses, callers are STILL rejected instantly and exactly
// one background probe goes out. A synchronous probe would hand one unlucky
// page load a full 10s stall every cooldown.
func TestBreaker_CooldownProbesInBackgroundWithoutBlockingCallers(t *testing.T) {
	var hits int32
	release := make(chan struct{})
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		// Only the probe hangs (like the real outage did). The setup calls
		// below must fail fast, or the test pays a client timeout each.
		if atomic.AddInt32(&hits, 1) > breakerThreshold {
			<-release
		}
		w.Write([]byte(outageBody))
	})
	defer srv.Close()
	defer close(release)

	for i := 0; i < breakerThreshold; i++ {
		_ = client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)
	}

	// Expire the cooldown without waiting a real minute.
	client.mu.Lock()
	client.breakerUntil = time.Now().Add(-time.Second)
	client.mu.Unlock()

	before := atomic.LoadInt32(&hits)
	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil); err == nil {
			t.Fatal("caller should still be rejected while the probe runs")
		}
	}
	// The handler is blocked on `release`, so any caller that waited on it
	// would show up as elapsed time here.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("callers waited %v on the background probe; they should return instantly", elapsed)
	}

	waitFor(t, func() bool { return atomic.LoadInt32(&hits)-before == 1 },
		"exactly one background probe")
	if got := atomic.LoadInt32(&hits) - before; got != 1 {
		t.Errorf("cooldown sent %d probes, want exactly 1", got)
	}
}

// Recovery is noticed by the background probe alone — no Refresh click, no
// restart — and it refreshes the stale catalog on the way through.
func TestBreaker_BackgroundProbeRecoversAndRefreshesCatalog(t *testing.T) {
	var healthy atomic.Bool
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.Write([]byte(oneDeviceBody))
			return
		}
		w.Write([]byte(outageBody))
	})
	defer srv.Close()
	client.EnableDeviceCache(t.TempDir())

	for i := 0; i < breakerThreshold; i++ {
		_ = client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)
	}
	if err := client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil); err == nil {
		t.Fatal("breaker should be open")
	}

	healthy.Store(true)
	client.mu.Lock()
	client.breakerUntil = time.Now().Add(-time.Second)
	client.mu.Unlock()

	// This call triggers the probe and is itself rejected.
	_ = client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil)

	waitFor(t, func() bool {
		return client.do(context.Background(), http.MethodGet, "/user/devices", nil, nil) == nil
	}, "breaker to close after a successful background probe")

	devices, err := client.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices after recovery: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceName != "Den Floor Lamp" {
		t.Errorf("catalog was not refreshed by the recovery probe: %+v", devices)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
