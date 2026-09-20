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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// sceneCacheFile is the on-disk copy of the per-device scene catalogs.
//
// Kept SEPARATE from devices-cache.json because the two have different
// lifecycles: the device catalog arrives whole in one call, while scene
// catalogs are fetched per device and accumulate over time. Mixing them would
// mean rewriting the whole device list every time one lamp's scenes load.
const sceneCacheFile = "scenes-cache.json"

// sceneEntry is one device's scene catalog plus when it was fetched.
//
// Scene VALUES are per-device (Govee's paramId differs between two lamps of
// the same model), so this is keyed by device ID and a SKU match is never
// enough to reuse another device's entry.
type sceneEntry struct {
	SKU      string    `json:"sku"`
	CachedAt time.Time `json:"cachedAt"`
	Scenes   []Scene   `json:"scenes"`
}

type sceneCacheFileFormat struct {
	Devices map[string]sceneEntry `json:"devices"`
}

// EnableSceneCache points the client at a directory in which to keep scene
// catalogs across restarts, and primes memory from whatever is already there.
func (c *Client) EnableSceneCache(configDir string) {
	c.cacheMu.Lock()
	c.sceneCachePath = filepath.Join(configDir, sceneCacheFile)
	c.cacheMu.Unlock()

	loaded, err := c.loadSceneCache()
	if err != nil {
		c.log.Debug("No scene cache to prime: %v", err)
		return
	}

	c.cacheMu.Lock()
	if c.scenes == nil {
		c.scenes = make(map[string]sceneEntry, len(loaded))
	}
	for device, entry := range loaded {
		c.scenes[device] = entry
	}
	n := len(c.scenes)
	c.cacheMu.Unlock()

	c.log.Info("Scene catalogs primed from cache: %d device(s)", n)
}

func (c *Client) loadSceneCache() (map[string]sceneEntry, error) {
	c.cacheMu.RLock()
	path := c.sceneCachePath
	c.cacheMu.RUnlock()
	if path == "" {
		return nil, fmt.Errorf("scene cache not enabled")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f sceneCacheFileFormat
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("parse scene cache: %w", err)
	}
	if len(f.Devices) == 0 {
		return nil, fmt.Errorf("scene cache is empty")
	}
	return f.Devices, nil
}

// saveSceneCache writes every known scene catalog. Best-effort: a failure
// here must never fail the fetch the caller asked for.
func (c *Client) saveSceneCache() {
	c.cacheMu.RLock()
	path := c.sceneCachePath
	snapshot := make(map[string]sceneEntry, len(c.scenes))
	for device, entry := range c.scenes {
		snapshot[device] = entry
	}
	c.cacheMu.RUnlock()

	if path == "" || len(snapshot) == 0 {
		return
	}

	// Compact, NOT MarshalIndent: a Scene.Value is a json.RawMessage holding
	// the exact bytes Govee gave us, and indenting rewrites it. The value is
	// what gets replayed to /device/control, so the cache must store it
	// verbatim rather than a re-formatted equivalent. Use `jq` to read this
	// file.
	body, err := json.Marshal(sceneCacheFileFormat{Devices: snapshot})
	if err != nil {
		c.log.Warn("Could not encode scene cache: %v", err)
		return
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		c.log.Warn("Could not write scene cache: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		c.log.Warn("Could not replace scene cache: %v", err)
		os.Remove(tmp)
		return
	}
	c.log.Debug("Scene cache written: %d device(s) → %s", len(snapshot), path)
}

// WarmSceneCache fetches scene catalogs for scene-capable devices that aren't
// cached yet, so the dropdown is populated for EVERY device rather than only
// the ones whose detail page happened to be opened before an outage.
//
// It fills gaps only — a device already in the cache is skipped, so this costs
// its ~2 calls per device exactly once in the install's life, not on every
// restart. Runs in the background; the client's own call spacing paces it.
func (c *Client) WarmSceneCache(ctx context.Context, devices []Device) {
	var missing []Device
	c.cacheMu.RLock()
	for _, d := range devices {
		if !hasSceneCapability(d) {
			continue
		}
		if _, ok := c.scenes[d.Device]; !ok {
			missing = append(missing, d)
		}
	}
	c.cacheMu.RUnlock()

	if len(missing) == 0 {
		return
	}
	c.log.Info("Warming scene catalogs for %d device(s) with no cached scenes", len(missing))

	var warmed int
	for _, d := range missing {
		if ctx.Err() != nil {
			return
		}
		if _, err := c.ListScenes(ctx, d.SKU, d.Device, false); err != nil {
			// One unreachable device must not abandon the rest, but a cloud
			// outage will fail every one of them — the breaker turns those
			// into instant no-ops rather than a long stall.
			c.log.Debug("Scene warm for %s failed: %v", d.DeviceName, err)
			continue
		}
		warmed++
	}
	c.log.Info("Scene catalog warm complete: %d of %d device(s)", warmed, len(missing))
}

func hasSceneCapability(d Device) bool {
	for _, cap := range d.Capabilities {
		if cap.Type == CapDynamicScene {
			return true
		}
	}
	return false
}
