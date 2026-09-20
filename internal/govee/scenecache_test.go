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
	"sync/atomic"
	"testing"
)

// Two scenes on /device/scenes, none on /device/diy-scenes.
func sceneHandler(hits *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if r.URL.Path == "/device/diy-scenes" {
			w.Write([]byte(`{"code":200,"msg":"success","payload":{"capabilities":[]}}`))
			return
		}
		w.Write([]byte(`{"code":200,"msg":"success","payload":{"capabilities":[
		  {"type":"devices.capabilities.dynamic_scene","instance":"lightScene","parameters":{"options":[
		    {"name":"Sunrise","value":{"paramId":16433,"id":9558}},
		    {"name":"Sunset","value":{"paramId":16434,"id":9559}}
		  ]}}
		]}}`))
	}
}

// The gap this closes: a restart used to empty every scene dropdown, and
// during an outage it could not refill.
func TestSceneCache_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	first, srv := newTestClient(sceneHandler(nil))
	first.EnableSceneCache(dir)
	scenes, err := first.ListScenes(context.Background(), "H607C", "34:FD:CC", false)
	srv.Close()
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(scenes) != 2 {
		t.Fatalf("got %d scenes, want 2", len(scenes))
	}

	// A fresh process, with the cloud now down.
	restarted, outage := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(outageBody))
	})
	defer outage.Close()
	restarted.EnableSceneCache(dir)

	got, err := restarted.ListScenes(context.Background(), "H607C", "34:FD:CC", false)
	if err != nil {
		t.Fatalf("cached scenes should be served during an outage, got: %v", err)
	}
	if len(got) != 2 || got[0].Name != "Sunrise" {
		t.Fatalf("scene catalog not served from cache: %+v", got)
	}
	// Values are per-device and must round-trip through JSON intact.
	if string(got[0].Value) != `{"paramId":16433,"id":9558}` {
		t.Errorf("scene value corrupted by the cache: %s", got[0].Value)
	}
}

// refresh=1 is the operator explicitly asking the cloud, so it must surface
// an outage rather than quietly returning the cached list — same rule as the
// device catalog's Refresh button.
func TestSceneCache_ExplicitRefreshSurfacesOutage(t *testing.T) {
	dir := t.TempDir()

	seed, srv := newTestClient(sceneHandler(nil))
	seed.EnableSceneCache(dir)
	if _, err := seed.ListScenes(context.Background(), "H607C", "34:FD:CC", false); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	client, outage := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(outageBody))
	})
	defer outage.Close()
	client.EnableSceneCache(dir)

	if _, err := client.ListScenes(context.Background(), "H607C", "34:FD:CC", true); err == nil {
		t.Error("explicit refresh masked the outage")
	}
}

func sceneCapDevice(id string) Device {
	return Device{
		SKU: "H607C", Device: id, DeviceName: "Lamp " + id,
		Capabilities: []Capability{{Type: CapDynamicScene, Instance: "lightScene"}},
	}
}

// Warming fills gaps only: devices already cached cost nothing, so a restart
// does not re-spend the whole catalog's worth of API calls.
func TestSceneCache_WarmFillsGapsOnly(t *testing.T) {
	var hits int32
	client, srv := newTestClient(sceneHandler(&hits))
	defer srv.Close()
	client.EnableSceneCache(t.TempDir())

	devices := []Device{
		sceneCapDevice("dev-1"),
		sceneCapDevice("dev-2"),
		// No scene capability — must not be fetched at all.
		{SKU: "H5059", Device: "sensor-1", DeviceName: "Leak Detector"},
	}

	client.WarmSceneCache(context.Background(), devices)
	afterFirst := atomic.LoadInt32(&hits)
	// 2 scene-capable devices × 2 endpoints (scenes + diy-scenes).
	if afterFirst != 4 {
		t.Fatalf("warm made %d calls, want 4 (2 devices × 2 endpoints; the sensor must be skipped)", afterFirst)
	}

	// A second warm — the next restart — must be free.
	client.WarmSceneCache(context.Background(), devices)
	if got := atomic.LoadInt32(&hits); got != afterFirst {
		t.Errorf("second warm re-fetched %d time(s); it should fill gaps only", got-afterFirst)
	}
}

// Warming after a restart must skip devices primed from disk.
func TestSceneCache_WarmSkipsDevicesPrimedFromDisk(t *testing.T) {
	dir := t.TempDir()

	seed, srv := newTestClient(sceneHandler(nil))
	seed.EnableSceneCache(dir)
	if _, err := seed.ListScenes(context.Background(), "H607C", "dev-1", false); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	var hits int32
	restarted, srv2 := newTestClient(sceneHandler(&hits))
	defer srv2.Close()
	restarted.EnableSceneCache(dir)

	restarted.WarmSceneCache(context.Background(), []Device{sceneCapDevice("dev-1"), sceneCapDevice("dev-2")})
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("warm made %d calls, want 2 (only dev-2; dev-1 came from disk)", got)
	}
}

// Govee returns scenes in its own order and exposes no categories, so the
// dropdown's only chance of being navigable is alphabetical.
func TestSceneCache_ScenesComeBackSorted(t *testing.T) {
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/device/diy-scenes" {
			// A DIY scene that sorts into the middle, and one that collides
			// with a built-in name.
			w.Write([]byte(`{"code":200,"msg":"success","payload":{"capabilities":[
			  {"type":"devices.capabilities.dynamic_scene","instance":"diyScene","parameters":{"options":[
			    {"name":"Nightlight","value":{"paramId":3,"id":3}},
			    {"name":"aurora","value":{"paramId":4,"id":4}}
			  ]}}
			]}}`))
			return
		}
		w.Write([]byte(`{"code":200,"msg":"success","payload":{"capabilities":[
		  {"type":"devices.capabilities.dynamic_scene","instance":"lightScene","parameters":{"options":[
		    {"name":"Sunset","value":{"paramId":1,"id":1}},
		    {"name":"Aurora","value":{"paramId":2,"id":2}},
		    {"name":"dracarys","value":{"paramId":5,"id":5}}
		  ]}}
		]}}`))
	})
	defer srv.Close()
	client.EnableSceneCache(t.TempDir())

	scenes, err := client.ListScenes(context.Background(), "H607C", "dev-1", false)
	if err != nil {
		t.Fatal(err)
	}

	// Case-insensitive: "aurora" and "Aurora" sort together, not in two
	// separate ASCII blocks with the lowercase names exiled to the end.
	want := []string{"aurora", "Aurora", "dracarys", "Nightlight", "Sunset"}
	if len(scenes) != len(want) {
		t.Fatalf("got %d scenes, want %d", len(scenes), len(want))
	}
	for i, w := range want {
		if scenes[i].Name != w {
			got := make([]string, len(scenes))
			for j, s := range scenes {
				got[j] = s.Name
			}
			t.Fatalf("position %d = %q, want %q (full order: %v)", i, scenes[i].Name, w, got)
		}
	}
	// The name collision must break deterministically by instance
	// ("diyScene" < "lightScene"); which one wins matters less than it being
	// stable across reloads.
	if scenes[0].Instance != "diyScene" || scenes[1].Instance != "lightScene" {
		t.Errorf("tie-break wrong: %q then %q", scenes[0].Instance, scenes[1].Instance)
	}
}

// A cache written before sorting existed must come back sorted, not in
// whatever order it happens to hold on disk.
func TestSceneCache_PrimingSortsLegacyCache(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"devices":{"dev-1":{"sku":"H607C","cachedAt":"2026-09-19T20:00:00Z","scenes":[
	  {"name":"Sunset","instance":"lightScene","value":{"id":1}},
	  {"name":"Aurora","instance":"lightScene","value":{"id":2}}
	]}}}`
	if err := os.WriteFile(filepath.Join(dir, sceneCacheFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	client, srv := newTestClient(sceneHandler(nil))
	defer srv.Close()
	client.EnableSceneCache(dir)

	scenes, err := client.ListScenes(context.Background(), "H607C", "dev-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenes) != 2 || scenes[0].Name != "Aurora" {
		t.Errorf("legacy cache served unsorted: %+v", scenes)
	}
}
