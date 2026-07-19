/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package effects

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
)

// Controller is the Govee surface the engine drives — an interface so engine
// tests run against a fake instead of the network.
type Controller interface {
	Power(ctx context.Context, sku, device string, on bool) error
	Color(ctx context.Context, sku, device string, rgb int) error
	Brightness(ctx context.Context, sku, device string, percent int) error
	State(ctx context.Context, sku, device string) (*govee.DeviceState, error)
	// Control is the generic capability write — the device-controller UI
	// drives arbitrary capabilities (toggles, modes, music settings) with it.
	Control(ctx context.Context, sku, device, capType, instance string, value interface{}) error
}

// SceneApplier is the optional Controller extension for applying a
// dynamic-scene value (govee.Client implements it). Used both to replay a
// captured scene and to apply a device's configured after-effect scene.
type SceneApplier interface {
	ApplyScene(ctx context.Context, sku, device, instance string, value json.RawMessage) error
}

// TransportReporter is the optional Controller extension that names which
// path serves a device (govee.Client implements it). This is OBSERVABILITY
// ONLY — the engine labels Activity rows with it and never branches on it, so
// the transport-blind design holds: effects behave identically either way.
type TransportReporter interface {
	TransportFor(device string) string
}

// Verifier is the optional Controller extension for post-effect verification
// (govee.Client implements it). LAN writes are fire-and-forget UDP with no
// ack, so "sent" proves nothing: after an effect we ask the transport to
// confirm the device actually answered. On the cloud path this is a no-op
// (every write already returned a status code), so it costs nothing there.
type Verifier interface {
	VerifyReachable(ctx context.Context, sku, device string) error
}

// The interface assertions happen at runtime (Controller is the declared
// dependency); these guards make any govee.Client drift a compile error.
var (
	_ SceneApplier      = (*govee.Client)(nil)
	_ Verifier          = (*govee.Client)(nil)
	_ TransportReporter = (*govee.Client)(nil)
)

// SceneRef names a scene to apply (from the device's scene catalog).
type SceneRef struct {
	Instance string          // "lightScene" | "diyScene" | "snapshot"
	Name     string          // display only
	Value    json.RawMessage // controllable value, per-device
}

// Device identifies one target device. AfterScene, when set, is applied after
// the effect completes — the deliberate final state for scene-driven lamps
// (the Govee API cannot report the active scene, so restore can't guess it).
type Device struct {
	SKU        string
	Device     string
	AfterScene *SceneRef
}

// Job is one queued effect run against one or more devices.
type Job struct {
	MappingID   string
	MappingName string
	Effect      string
	Params      Params
	Devices     []Device
	// Activity context (who triggered it)
	TokenName string
	Source    string
	Event     string
	Entity    string
}

// QuietHours mirrors config.QuietHoursConfig without importing config.
// Timezone is an optional IANA name; empty means host local time.
type QuietHours struct {
	Enabled  bool
	Start    string // "HH:MM"
	End      string // "HH:MM"
	Timezone string
}

// Location resolves the timezone the window is evaluated in. An empty or
// unparseable name falls back to host local time (and the caller logs it).
func (q QuietHours) Location() (*time.Location, error) {
	if q.Timezone == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil {
		return time.Local, err
	}
	return loc, nil
}

// Describe renders the effective window for logs and the Settings page —
// quiet hours failing silently is what made a timezone mismatch invisible.
func (q QuietHours) Describe(now time.Time) string {
	if !q.Enabled {
		return "disabled"
	}
	loc, err := q.Location()
	tz := loc.String()
	if err != nil {
		tz = fmt.Sprintf("%s (INVALID — falling back to %s)", q.Timezone, loc)
	}
	return fmt.Sprintf("%s-%s %s (now %s)", q.Start, q.End, tz, now.In(loc).Format("15:04"))
}

// Engine runs effects asynchronously with per-device serialization,
// per-mapping cooldowns, and quiet-hours suppression.
type Engine struct {
	controller Controller
	log        *logger.Logger
	act        *activity.Log
	now        func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu          sync.Mutex
	deviceLocks map[string]*sync.Mutex
	lastRun     map[string]time.Time // mapping ID → last accepted run

	quietMu sync.RWMutex
	quiet   QuietHours
}

// perDeviceTimeout bounds one device's effect execution (the longest legal
// flash is 20 cycles × 5s × 2 colors = 200s of sleeps plus API calls).
const perDeviceTimeout = 5 * time.Minute

// NewEngine creates the effects engine.
func NewEngine(controller Controller, act *activity.Log) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		controller:  controller,
		log:         logger.New(logger.ModuleEffects),
		act:         act,
		now:         time.Now,
		ctx:         ctx,
		cancel:      cancel,
		deviceLocks: make(map[string]*sync.Mutex),
		lastRun:     make(map[string]time.Time),
	}
}

// SetQuietHours updates the suppression window (hot-reload on config save).
func (e *Engine) SetQuietHours(q QuietHours) {
	e.quietMu.Lock()
	e.quiet = q
	e.quietMu.Unlock()
}

// Stop cancels all in-flight effects and waits (bounded) for workers to exit.
func (e *Engine) Stop() {
	e.cancel()
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		e.log.Warn("Effects engine stop timed out; abandoning workers")
	}
}

// Enqueue accepts a job (or rejects it via cooldown / quiet hours) and
// returns the Activity result string recorded for the accepted/skipped run.
// cooldown <= 0 means no cooldown (used by UI test runs).
func (e *Engine) Enqueue(job Job, cooldown time.Duration) string {
	if e.inQuietHours() {
		e.record(job, "", "quiet hours")
		return "quiet hours"
	}

	if cooldown > 0 {
		e.mu.Lock()
		if last, ok := e.lastRun[job.MappingID]; ok && e.now().Sub(last) < cooldown {
			e.mu.Unlock()
			e.record(job, "", "cooldown")
			return "cooldown"
		}
		e.lastRun[job.MappingID] = e.now()
		e.mu.Unlock()
	}

	e.record(job, "", "queued")
	for _, dev := range job.Devices {
		dev := dev
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.runOnDevice(job, dev)
		}()
	}
	return "queued"
}

// EnqueueControl queues one manual capability write. It takes the same
// per-device serialization lock as effects (a manual toggle can't interleave
// with a running flash) but deliberately skips cooldowns AND quiet hours —
// the operator is explicitly acting. label is the Activity display name
// (e.g. "Manual control (brightness)").
func (e *Engine) EnqueueControl(dev Device, capType, instance string, value interface{}, label string) {
	job := Job{
		MappingID:   "control-" + dev.Device,
		MappingName: label,
		Devices:     []Device{dev},
		TokenName:   "ui",
		Source:      "ui",
		Event:       "manual_control",
	}
	e.record(job, "", "queued")

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		lock := e.deviceLock(dev.Device)
		lock.Lock()
		defer lock.Unlock()

		ctx, cancel := context.WithTimeout(e.ctx, perDeviceTimeout)
		defer cancel()

		transport := e.transportFor(dev.Device)
		if err := e.controller.Control(ctx, dev.SKU, dev.Device, capType, instance, value); err != nil {
			e.log.Error("Manual control %s/%s on %s failed: %v", capType, instance, dev.Device, err)
			e.recordVia(job, dev.Device, "effect failed: "+err.Error(), transport)
			return
		}
		e.recordVia(job, dev.Device, "effect ok", transport)
	}()
}

// record writes an Activity record for this job. device scopes the record to
// one device (per-device outcomes); "" means job-level, which carries the
// job's full device list.
func (e *Engine) record(job Job, device, result string) {
	e.recordVia(job, device, result, "")
}

// recordVia is record() plus the transport that served the device. transport
// must be sampled BEFORE the work runs (see transportFor).
func (e *Engine) recordVia(job Job, device, result, transport string) {
	if e.act == nil {
		return
	}
	var devices []string
	if device != "" {
		devices = []string{device}
	} else {
		for _, d := range job.Devices {
			devices = append(devices, d.Device)
		}
	}
	e.act.Add(activity.Record{
		TokenName:   job.TokenName,
		Source:      job.Source,
		Event:       job.Event,
		Entity:      job.Entity,
		MappingID:   job.MappingID,
		MappingName: job.MappingName,
		Devices:     devices,
		Transport:   transport,
		Result:      result,
	})
}

// transportFor names the path serving a device, for Activity labelling. Must
// be called BEFORE the work: a failed LAN read-back drops the route, so a
// later call would report "cloud" for an effect attempted over LAN — exactly
// backwards on the failures worth investigating.
func (e *Engine) transportFor(device string) string {
	if r, ok := e.controller.(TransportReporter); ok {
		return r.TransportFor(device)
	}
	return ""
}

// InQuietHours reports whether effects are being suppressed right now — the
// Settings page shows this so a timezone mismatch is visible immediately.
func (e *Engine) InQuietHours() bool { return e.inQuietHours() }

func (e *Engine) inQuietHours() bool {
	e.quietMu.RLock()
	q := e.quiet
	e.quietMu.RUnlock()
	if !q.Enabled {
		return false
	}
	start, err1 := parseHHMM(q.Start)
	end, err2 := parseHHMM(q.End)
	if err1 != nil || err2 != nil {
		// Fail OPEN: a malformed window must not silently kill every alert on
		// a mining monitor. Log it — silence is what hid the timezone bug.
		e.log.Warn("Quiet hours window %q-%q is malformed; ignoring it (effects will fire)", q.Start, q.End)
		return false
	}
	// Evaluate in the configured timezone, NOT blindly in container-local
	// time: a container defaulting to UTC shifted the window hours off and
	// fired alerts at 5am.
	loc, err := q.Location()
	if err != nil {
		e.log.Warn("Quiet hours timezone %q is invalid (%v); using %s", q.Timezone, err, loc)
	}
	now := e.now().In(loc)
	nowMin := now.Hour()*60 + now.Minute()
	if start == end {
		return false
	}
	if start < end {
		return nowMin >= start && nowMin < end
	}
	// window wraps midnight (e.g. 22:00 → 07:00)
	return nowMin >= start || nowMin < end
}

func parseHHMM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, err
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("out of range")
	}
	return h*60 + m, nil
}

// deviceLock returns the serialization mutex for one device ID.
func (e *Engine) deviceLock(device string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.deviceLocks[device]
	if !ok {
		l = &sync.Mutex{}
		e.deviceLocks[device] = l
	}
	return l
}

// runOnDevice executes the job's effect on one device under its lock.
func (e *Engine) runOnDevice(job Job, dev Device) {
	lock := e.deviceLock(dev.Device)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(e.ctx, perDeviceTimeout)
	defer cancel()

	// Sample the transport up front: verification can drop a stale LAN route,
	// and a row saying "cloud" for an effect that went out over LAN would be
	// a lie precisely where the truth matters.
	transport := e.transportFor(dev.Device)

	err := e.execute(ctx, job, dev)
	if err != nil {
		e.log.Error("Effect %s on %s/%s failed: %v", job.Effect, dev.SKU, dev.Device, err)
		e.recordVia(job, dev.Device, "effect failed: "+err.Error(), transport)
		return
	}
	if err := e.verify(ctx, dev); err != nil {
		// The device never answered, so the effect did not land — say so
		// rather than reporting a hopeful "ok". The transport has already
		// dropped the stale route and re-scanned; the next event uses cloud.
		e.log.Error("Effect %s on %s/%s sent but unverified: %v", job.Effect, dev.SKU, dev.Device, err)
		e.recordVia(job, dev.Device, "effect failed: device did not respond", transport)
		return
	}
	e.recordVia(job, dev.Device, "effect ok", transport)
}

func (e *Engine) execute(ctx context.Context, job Job, dev Device) error {
	p := job.Params

	// Phase 3: capture the current state up front so it can be restored after
	// the effect. Best-effort — a capture failure runs the effect anyway.
	// A configured after-effect scene supersedes capture/restore: the final
	// state is deliberate, so there is nothing to guess (and no state read).
	var captured *govee.DeviceState
	if p.Restore && dev.AfterScene == nil {
		state, err := e.controller.State(ctx, dev.SKU, dev.Device)
		if err != nil {
			e.log.Warn("State capture for %s failed (effect continues, no restore): %v", dev.Device, err)
		} else {
			captured = state
		}
	}

	var err error
	switch job.Effect {
	case EffectFlash:
		err = e.runFlash(ctx, dev, p)
	case EffectSolid:
		err = e.runSolid(ctx, dev, p)
	case EffectOn:
		err = e.runOn(ctx, dev, p)
	case EffectOff:
		err = e.controller.Power(ctx, dev.SKU, dev.Device, false)
	default:
		return fmt.Errorf("unknown effect %q", job.Effect)
	}
	if err != nil {
		return err
	}

	if dev.AfterScene != nil {
		return e.applyAfterScene(ctx, dev)
	}
	if captured != nil {
		e.restore(ctx, dev, captured)
	}
	return nil
}

// sceneSettleDelay is the pause between waking a device and sending the
// scene command. A scene sent while the device is still powering up is
// silently swallowed (observed on H607C after an endState-off flash) —
// Govee acks it but the lamp stays on the last solid color.
const sceneSettleDelay = 750 * time.Millisecond

// applyAfterScene powers the device on and applies its configured
// after-effect scene — the deliberate final state for scene-driven lamps.
func (e *Engine) applyAfterScene(ctx context.Context, dev Device) error {
	sa, ok := e.controller.(SceneApplier)
	if !ok {
		return fmt.Errorf("controller cannot apply scenes")
	}
	// Power on first: the effect may have ended in the off state, and a scene
	// command alone doesn't reliably wake every device. Then let the device
	// settle before the scene command, or it gets swallowed mid-wake.
	if err := e.controller.Power(ctx, dev.SKU, dev.Device, true); err != nil {
		return err
	}
	if err := sleepCtx(ctx, sceneSettleDelay); err != nil {
		return err
	}
	e.log.Debug("Applying after-effect scene %q (%s) on %s", dev.AfterScene.Name, dev.AfterScene.Instance, dev.Device)
	if err := sa.ApplyScene(ctx, dev.SKU, dev.Device, dev.AfterScene.Instance, dev.AfterScene.Value); err != nil {
		return fmt.Errorf("after-effect scene %q: %w", dev.AfterScene.Name, err)
	}
	return nil
}

func (e *Engine) runFlash(ctx context.Context, dev Device, p Params) error {
	if err := e.controller.Power(ctx, dev.SKU, dev.Device, true); err != nil {
		return err
	}
	if err := e.controller.Brightness(ctx, dev.SKU, dev.Device, p.Brightness); err != nil {
		return err
	}

	delay := time.Duration(p.DelayMs) * time.Millisecond
	for i := 0; i < p.Cycles; i++ {
		if err := e.controller.Color(ctx, dev.SKU, dev.Device, p.ColorA); err != nil {
			return err
		}
		if err := sleepCtx(ctx, delay); err != nil {
			return err
		}
		if err := e.controller.Color(ctx, dev.SKU, dev.Device, p.ColorB); err != nil {
			return err
		}
		if err := sleepCtx(ctx, delay); err != nil {
			return err
		}
	}

	switch p.EndState {
	case EndHoldA:
		return e.controller.Color(ctx, dev.SKU, dev.Device, p.ColorA)
	case EndHoldB:
		return e.controller.Color(ctx, dev.SKU, dev.Device, p.ColorB)
	case EndOff:
		return e.controller.Power(ctx, dev.SKU, dev.Device, false)
	}
	return nil
}

func (e *Engine) runSolid(ctx context.Context, dev Device, p Params) error {
	if err := e.controller.Power(ctx, dev.SKU, dev.Device, true); err != nil {
		return err
	}
	if err := e.controller.Brightness(ctx, dev.SKU, dev.Device, p.Brightness); err != nil {
		return err
	}
	return e.controller.Color(ctx, dev.SKU, dev.Device, p.ColorA)
}

func (e *Engine) runOn(ctx context.Context, dev Device, p Params) error {
	if err := e.controller.Power(ctx, dev.SKU, dev.Device, true); err != nil {
		return err
	}
	return e.controller.Brightness(ctx, dev.SKU, dev.Device, p.Brightness)
}

// restore best-effort re-applies a captured device state after an effect.
func (e *Engine) restore(ctx context.Context, dev Device, s *govee.DeviceState) {
	if s.PowerOn != nil && *s.PowerOn == 0 {
		// Device was off — turning it off again is the whole restore.
		if err := e.controller.Power(ctx, dev.SKU, dev.Device, false); err != nil {
			e.log.Warn("Restore power for %s failed: %v", dev.Device, err)
		}
		return
	}
	// Device was on: power it back on first — the effect may have ended in
	// the off state (endState "off"), and color/brightness alone won't wake it.
	if s.PowerOn != nil {
		if err := e.controller.Power(ctx, dev.SKU, dev.Device, true); err != nil {
			e.log.Warn("Restore power for %s failed: %v", dev.Device, err)
		}
	}
	if s.Brightness != nil {
		if err := e.controller.Brightness(ctx, dev.SKU, dev.Device, *s.Brightness); err != nil {
			e.log.Warn("Restore brightness for %s failed: %v", dev.Device, err)
		}
	}
	// A reported scene wins over solid color — re-applying colorRgb would
	// kill the scene the device was playing.
	if s.SceneValue != nil {
		if sa, ok := e.controller.(SceneApplier); ok {
			if err := sa.ApplyScene(ctx, dev.SKU, dev.Device, s.SceneInstance, s.SceneValue); err != nil {
				e.log.Warn("Restore scene for %s failed: %v", dev.Device, err)
			}
			return
		}
	}
	if s.ColorRgb != nil {
		if err := e.controller.Color(ctx, dev.SKU, dev.Device, *s.ColorRgb); err != nil {
			e.log.Warn("Restore color for %s failed: %v", dev.Device, err)
		}
	}
}

// verify asks the transport to confirm the device answered after an effect.
// No-op when the controller does not implement Verifier (test fakes).
func (e *Engine) verify(ctx context.Context, dev Device) error {
	v, ok := e.controller.(Verifier)
	if !ok {
		return nil
	}
	return v.VerifyReachable(ctx, dev.SKU, dev.Device)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
