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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// deviceCacheFile is the on-disk copy of the device catalog, written to the
// config directory after every successful refresh.
//
// The catalog used to live only in memory, which made a Govee outage far worse
// than it needed to be: the cloud API returning errors is survivable (the
// webhook → effect path drives devices straight from config, and LAN devices
// never touch the cloud at all), but a RESTART during an outage left the UI
// with nothing to draw — no device list, no controls, no way in. A catalog
// that changes maybe twice a year had no business being that fragile.
const deviceCacheFile = "devices-cache.json"

// deviceCache is the file format. CachedAt is the time of the successful
// FETCH, not of the file write, so a cache that is reloaded and re-saved does
// not look fresher than it is.
type deviceCache struct {
	CachedAt time.Time `json:"cachedAt"`
	Devices  []Device  `json:"devices"`
}

// EnableDeviceCache points the client at a directory in which to keep the
// device catalog across restarts, and primes memory from whatever is already
// there. Disabled (in-memory only) when never called, which is how the tests
// run.
//
// Priming matters for SPEED, not just for outages: without it the first page
// load after a restart pays a full cloud round-trip — and during an outage,
// a full 10s timeout — before the fallback can serve anything. The caller is
// expected to kick a background refresh afterwards so a primed catalog still
// gets brought up to date on a healthy start.
func (c *Client) EnableDeviceCache(configDir string) {
	c.cacheMu.Lock()
	c.cachePath = filepath.Join(configDir, deviceCacheFile)
	c.cacheMu.Unlock()

	cached, err := c.loadDeviceCache()
	if err != nil {
		c.log.Debug("No device cache to prime: %v", err)
		return
	}

	c.cacheMu.Lock()
	c.devices = cached.Devices
	c.devicesFrom = cached.CachedAt
	c.cacheMu.Unlock()

	c.log.Info("Device catalog primed from cache: %d devices (fetched %s)",
		len(cached.Devices), cached.CachedAt.Format("2006-01-02 15:04"))
}

// saveDeviceCache writes the catalog to disk. Best-effort: a failure here must
// never fail the refresh the caller actually asked for.
func (c *Client) saveDeviceCache(devices []Device, at time.Time) {
	c.cacheMu.RLock()
	path := c.cachePath
	c.cacheMu.RUnlock()
	if path == "" || len(devices) == 0 {
		return
	}

	body, err := json.MarshalIndent(deviceCache{CachedAt: at, Devices: devices}, "", "  ")
	if err != nil {
		c.log.Warn("Could not encode device cache: %v", err)
		return
	}

	// Write-then-rename: a crash mid-write leaves the previous good cache in
	// place rather than a truncated file that fails to parse on next boot.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		c.log.Warn("Could not write device cache: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		c.log.Warn("Could not replace device cache: %v", err)
		os.Remove(tmp)
		return
	}
	c.log.Debug("Device cache written: %d devices → %s", len(devices), path)
}

// loadDeviceCache reads the catalog written by a previous run.
func (c *Client) loadDeviceCache() (*deviceCache, error) {
	c.cacheMu.RLock()
	path := c.cachePath
	c.cacheMu.RUnlock()
	if path == "" {
		return nil, fmt.Errorf("device cache not enabled")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cached deviceCache
	if err := json.Unmarshal(body, &cached); err != nil {
		return nil, fmt.Errorf("parse device cache: %w", err)
	}
	if len(cached.Devices) == 0 {
		return nil, fmt.Errorf("device cache is empty")
	}
	return &cached, nil
}
