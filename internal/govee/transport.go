/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package govee

// Transport resolution: LAN fast path, cloud fallback.
//
// This is the ONLY place the hybrid exists. Everything above the client —
// effects.Engine, dispatch, mappings, handlers — is transport-blind by design
// and does not change. Cloud remains the source of truth for the device
// catalog, capabilities, and scenes; LAN only carries the hot ops it actually
// speaks (see lan.go).
//
// Fallback is per-device, never per-install: a device with no LAN route (or a
// stale one) transparently uses cloud, so a Mac dev box with no multicast
// behaves exactly as it did before this feature existed.

import (
	"context"
	"time"
)

// EnableLAN starts LAN discovery. A bind failure is non-fatal: it is logged
// and the client stays cloud-only. Safe to call once at startup.
func (c *Client) EnableLAN() {
	svc, err := newLANService(c.log)
	if err != nil {
		c.log.Warn("LAN control unavailable (%v) — continuing with the cloud API only", err)
		return
	}
	c.lan = svc
	c.lan.Start()
	c.log.Info("LAN control enabled — scanning for devices")
}

// StopLAN shuts the LAN service down (no-op when disabled).
func (c *Client) StopLAN() {
	if c.lan != nil {
		c.lan.Stop()
	}
}

// LANEnabled reports whether the LAN service is running.
func (c *Client) LANEnabled() bool { return c.lan != nil }

// LANRoutes returns the discovered routes (device ID → route). Empty when LAN
// is off or nothing answered — the UI's LAN Control column reads this.
func (c *Client) LANRoutes() map[string]LANRoute {
	if c.lan == nil {
		return map[string]LANRoute{}
	}
	return c.lan.Routes()
}

// LANLastScan reports when the last scan was sent (zero when LAN is off).
func (c *Client) LANLastScan() time.Time {
	if c.lan == nil {
		return time.Time{}
	}
	return c.lan.LastScan()
}

// LANRescan sends a discovery scan and waits for the reply window — the
// manual Refresh / Re-scan path.
func (c *Client) LANRescan(ctx context.Context) {
	if c.lan == nil {
		return
	}
	c.lan.ScanAndWait(ctx)
}

// lanRouteFor returns a device's LAN route when the fast path is available.
func (c *Client) lanRouteFor(device string) (LANRoute, bool) {
	if c.lan == nil {
		return LANRoute{}, false
	}
	return c.lan.Route(device)
}

// lanServe routes a generic capability write onto the LAN fast path when the
// device has a route AND LAN speaks that capability. Returns handled=false
// for anything LAN cannot do (scenes, segments, music, toggles) so the caller
// falls through to cloud. This is what lets the device-controller UI benefit
// without knowing transports exist.
func (c *Client) lanServe(device, capType, instance string, value interface{}) (handled bool, err error) {
	route, ok := c.lanRouteFor(device)
	if !ok {
		return false, nil
	}
	n, numeric := toInt(value)
	if !numeric {
		return false, nil // struct/array values are cloud-only anyway
	}
	switch {
	case capType == CapOnOff && instance == InstPower:
		return true, c.lan.Turn(route.IP, n == 1)
	case capType == CapRange && instance == InstBrightness:
		return true, c.lan.Brightness(route.IP, n)
	case capType == CapColor && instance == InstColorRgb:
		return true, c.lan.ColorRGB(route.IP, n)
	case capType == CapColor && instance == InstColorTemp:
		return true, c.lan.ColorTemp(route.IP, n)
	}
	return false, nil
}

// toInt coerces a JSON-decoded number (float64) or a plain int.
func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case int64:
		return int(n), true
	}
	return 0, false
}

// VerifyReachable confirms a device answered on the transport that just
// served it. The engine calls this after an effect (optional Verifier
// interface) so Activity can report the truth instead of merely "sent".
//
//   - LAN-served: LAN writes are fire-and-forget, so a silent device means the
//     effect went nowhere. A failed read-back drops the route, triggers a
//     re-scan, and returns the error — deliberately NO cloud fallback here,
//     because falling back would let us report "effect ok" for writes that
//     vanished.
//   - Cloud-served: every cloud write already returned a status code, so there
//     is nothing left to verify. Returns nil without spending a call.
func (c *Client) VerifyReachable(ctx context.Context, sku, device string) error {
	route, ok := c.lanRouteFor(device)
	if !ok {
		return nil // cloud path — already acknowledged per write
	}
	if _, err := c.lan.Status(ctx, route.IP); err != nil {
		c.lan.dropRoute(device)
		return err
	}
	return nil
}
