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
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Circuit breaker for the Govee cloud.
//
// During the 2026-09-19 outage every cloud call sat on the full 10s client
// timeout before failing. That is survivable for a single request, but the
// Devices page status sweep fans out one state read PER DEVICE and waits for
// all of them — so a page that normally paints in ~2s took 12s, on EVERY
// visit, for as long as Govee was down.
//
// Once several consecutive calls have failed in a way that means "the cloud
// is not answering," there is no information left in making the next caller
// wait 10s to learn the same thing. The breaker fails those calls instantly
// and probes for recovery in the BACKGROUND.
const (
	breakerThreshold = 3
	breakerCooldown  = 60 * time.Second
)

// outageError marks a failure meaning "the Govee cloud is not answering," as
// distinct from "Govee answered and rejected this particular request."
//
// The distinction is the whole safety property: a rejected parameter or a bad
// API key proves the cloud is UP, and must never open the breaker — otherwise
// one malformed control call would stop all traffic for a minute.
type outageError struct{ err error }

func (e outageError) Error() string { return e.err.Error() }
func (e outageError) Unwrap() error { return e.err }

func isOutage(err error) bool {
	var oe outageError
	return errors.As(err, &oe)
}

// breakerCheck reports whether the call should be abandoned before it is made,
// and starts a background recovery probe when the cooldown has lapsed.
func (c *Client) breakerCheck() error {
	c.mu.Lock()
	if c.breakerUntil.IsZero() {
		c.mu.Unlock()
		return nil
	}

	// The probe runs in the BACKGROUND and the caller is still rejected. A
	// synchronous half-open probe would hand one unlucky page load the full
	// 10s stall once per cooldown — the exact symptom the breaker exists to
	// remove. Recovery is still noticed within one cooldown; nobody waits for
	// it.
	startProbe := !c.breakerProbing && time.Now().After(c.breakerUntil)
	if startProbe {
		c.breakerProbing = true
		// Keep rejecting while the probe is in flight.
		c.breakerUntil = time.Now().Add(breakerCooldown)
	}
	until, lastErr := c.breakerUntil, c.breakerErr
	c.mu.Unlock()

	if startProbe {
		go c.probeRecovery()
	}
	return fmt.Errorf("govee cloud unreachable, not retrying for %s (last error: %v)",
		time.Until(until).Round(time.Second), lastErr)
}

// probeRecovery makes one cloud call to find out whether Govee is back. It
// deliberately calls doRequest rather than RefreshDevices: RefreshDevices
// clears the breaker up front, which would drop the failure streak to zero and
// let the next fan-out pay full timeouts all over again.
func (c *Client) probeRecovery() {
	ctx, cancel := context.WithTimeout(context.Background(), clientTimeout)
	defer cancel()

	var resp devicesResponse
	err := c.doRequest(ctx, http.MethodGet, "/user/devices", nil, &resp)
	c.recordOutcome(err)
	if err != nil {
		c.log.Debug("Govee cloud still unreachable: %v", err)
		return
	}

	c.log.Info("Govee cloud is reachable again — resuming cloud calls")

	// The probe already paid for the catalog, so keep it. This is what makes
	// a stale cached catalog heal itself the moment Govee recovers, with no
	// Refresh click and no restart.
	if len(resp.Data) > 0 {
		now := time.Now()
		c.cacheMu.Lock()
		c.devices = resp.Data
		c.devicesFrom = now
		c.cacheMu.Unlock()
		c.saveDeviceCache(resp.Data, now)
		c.log.Info("Device catalog refreshed after recovery: %d devices", len(resp.Data))
	}
}

// recordOutcome feeds every completed request back into the breaker.
func (c *Client) recordOutcome(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.breakerProbing = false

	// A success — or any answer Govee actually formed an opinion about —
	// proves the cloud is reachable.
	if !isOutage(err) {
		c.outageStreak = 0
		c.breakerUntil = time.Time{}
		c.breakerErr = nil
		return
	}

	c.outageStreak++
	c.breakerErr = err
	if c.outageStreak >= breakerThreshold {
		c.breakerUntil = time.Now().Add(breakerCooldown)
	}
}

// clearBreaker forces the next call to reach the network. RefreshDevices uses
// it so the UI's Refresh button is always a real question to the cloud, never
// an instant breaker rejection.
func (c *Client) clearBreaker() {
	c.mu.Lock()
	c.outageStreak = 0
	c.breakerUntil = time.Time{}
	c.breakerProbing = false
	c.breakerErr = nil
	c.mu.Unlock()
}
