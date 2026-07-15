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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
)

// fakeController records calls and asserts per-device serialization.
type fakeController struct {
	mu       sync.Mutex
	calls    []string
	inFlight map[string]bool // device → currently executing (serialization check)
	overlap  bool
	state    *govee.DeviceState
}

func newFakeController() *fakeController {
	return &fakeController{inFlight: make(map[string]bool)}
}

func (f *fakeController) enter(device, call string) {
	f.mu.Lock()
	if f.inFlight[device] {
		f.overlap = true
	}
	f.inFlight[device] = true
	f.calls = append(f.calls, device+":"+call)
	f.mu.Unlock()
	time.Sleep(2 * time.Millisecond) // widen the overlap window
	f.mu.Lock()
	f.inFlight[device] = false
	f.mu.Unlock()
}

func (f *fakeController) Power(_ context.Context, _, device string, on bool) error {
	if on {
		f.enter(device, "power-on")
	} else {
		f.enter(device, "power-off")
	}
	return nil
}

func (f *fakeController) Color(_ context.Context, _, device string, rgb int) error {
	f.enter(device, "color")
	return nil
}

func (f *fakeController) Brightness(_ context.Context, _, device string, percent int) error {
	f.enter(device, "brightness")
	return nil
}

func (f *fakeController) Control(_ context.Context, _, device, _, instance string, _ interface{}) error {
	f.enter(device, "control:"+instance)
	return nil
}

func (f *fakeController) State(_ context.Context, _, device string) (*govee.DeviceState, error) {
	f.enter(device, "state")
	if f.state != nil {
		return f.state, nil
	}
	return &govee.DeviceState{}, nil
}

func (f *fakeController) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func testJob(id string, devices ...string) Job {
	job := Job{
		MappingID:   id,
		MappingName: "test " + id,
		Effect:      EffectSolid,
		Params:      Params{ColorA: 0x00FF00, Brightness: 100},
		TokenName:   "t",
		Source:      "test",
		Event:       "block_found",
	}
	for _, d := range devices {
		job.Devices = append(job.Devices, Device{SKU: "H1", Device: d})
	}
	return job
}

func TestEngine_RunsEffect(t *testing.T) {
	fake := newFakeController()
	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	if got := e.Enqueue(testJob("m1", "dev-1"), 0); got != "queued" {
		t.Fatalf("Enqueue = %q, want queued", got)
	}
	// solid = power + brightness + color = 3 calls
	waitFor(t, func() bool { return fake.callCount() >= 3 })
}

func TestEngine_Cooldown(t *testing.T) {
	fake := newFakeController()
	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	if got := e.Enqueue(testJob("m2", "dev-2"), time.Minute); got != "queued" {
		t.Fatalf("first Enqueue = %q, want queued", got)
	}
	if got := e.Enqueue(testJob("m2", "dev-2"), time.Minute); got != "cooldown" {
		t.Fatalf("second Enqueue = %q, want cooldown", got)
	}
}

func TestEngine_PerDeviceSerialization(t *testing.T) {
	fake := newFakeController()
	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	// Two jobs (distinct mappings, no cooldown clash) against the SAME device
	// must not interleave calls.
	e.Enqueue(testJob("m3", "dev-3"), 0)
	e.Enqueue(testJob("m4", "dev-3"), 0)

	waitFor(t, func() bool { return fake.callCount() >= 6 })
	if fake.overlap {
		t.Error("calls to the same device overlapped — serialization broken")
	}
}

func TestEngine_QuietHours(t *testing.T) {
	fake := newFakeController()
	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	// Window covering "now": freeze the clock at 23:00 with a 22:00→07:00
	// wrap-around window.
	e.now = func() time.Time {
		return time.Date(2026, 7, 11, 23, 0, 0, 0, time.Local)
	}
	e.SetQuietHours(QuietHours{Enabled: true, Start: "22:00", End: "07:00"})

	if got := e.Enqueue(testJob("m5", "dev-5"), 0); got != "quiet hours" {
		t.Fatalf("Enqueue = %q, want quiet hours", got)
	}
	if fake.callCount() != 0 {
		t.Error("effect ran during quiet hours")
	}

	// 12:00 is outside the window.
	e.now = func() time.Time {
		return time.Date(2026, 7, 11, 12, 0, 0, 0, time.Local)
	}
	if got := e.Enqueue(testJob("m5", "dev-5"), 0); got != "queued" {
		t.Fatalf("Enqueue outside window = %q, want queued", got)
	}
}

func TestEngine_QuietHours_NonWrappingWindow(t *testing.T) {
	e := NewEngine(newFakeController(), activity.GetLog())
	defer e.Stop()
	e.SetQuietHours(QuietHours{Enabled: true, Start: "09:00", End: "17:00"})

	e.now = func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.Local) }
	if !e.inQuietHours() {
		t.Error("12:00 should be inside 09:00-17:00")
	}
	e.now = func() time.Time { return time.Date(2026, 7, 11, 20, 0, 0, 0, time.Local) }
	if e.inQuietHours() {
		t.Error("20:00 should be outside 09:00-17:00")
	}
}

// Regression: a flash ending in "off" on an already-on lamp must power it
// back on during restore — color/brightness alone leave the device dark.
func TestEngine_RestorePowersBackOnAfterEndOff(t *testing.T) {
	fake := newFakeController()
	brightness, color, power := 40, 0x123456, 1
	fake.state = &govee.DeviceState{PowerOn: &power, Brightness: &brightness, ColorRgb: &color}

	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	job := testJob("m7", "dev-7")
	job.Effect = EffectFlash
	job.Params = Params{ColorA: 0xFFFFFF, ColorB: 0x3B82F6, Cycles: 1, DelayMs: 100,
		Brightness: 100, EndState: EndOff, Restore: true}
	e.Enqueue(job, 0)

	// state, power-on, brightness, color×2, power-off (endState),
	// then restore: power-on, color, brightness = 9 calls
	waitFor(t, func() bool { return fake.callCount() >= 9 })

	fake.mu.Lock()
	defer fake.mu.Unlock()
	last3 := fake.calls[len(fake.calls)-3:]
	if last3[0] != "dev-7:power-on" {
		t.Errorf("restore did not power the lamp back on; final calls: %v", fake.calls)
	}
	for _, c := range last3 {
		if c == "dev-7:power-off" {
			t.Errorf("lamp left off after restore; final calls: %v", fake.calls)
		}
	}
}

// Activity records carry the devices involved: the job-level "queued" row
// lists every target; per-device outcome rows list that one device.
func TestEngine_ActivityRecordsCarryDevices(t *testing.T) {
	fake := newFakeController()
	act := activity.GetLog()
	e := NewEngine(fake, act)
	defer e.Stop()

	e.Enqueue(testJob("m-devrec", "dev-a", "dev-b"), 0)

	collect := func() (queued, outcomes []activity.Record) {
		for _, r := range act.Recent(0) {
			if r.MappingID != "m-devrec" {
				continue
			}
			if r.Result == "queued" {
				queued = append(queued, r)
			} else {
				outcomes = append(outcomes, r)
			}
		}
		return
	}
	// Outcome records land after the last controller call — wait on them.
	waitFor(t, func() bool { _, o := collect(); return len(o) >= 2 })
	queued, outcomes := collect()
	if len(queued) != 1 || len(queued[0].Devices) != 2 {
		t.Errorf("queued record devices = %+v, want both dev-a and dev-b", queued)
	}
	if len(outcomes) < 2 {
		t.Fatalf("expected 2 per-device outcome records, got %d", len(outcomes))
	}
	for _, r := range outcomes {
		if len(r.Devices) != 1 {
			t.Errorf("outcome record devices = %v, want exactly one", r.Devices)
		}
	}
}

// verifyFake adds post-effect verification to the fake controller.
type verifyFake struct {
	fakeController
	verifyErr error
	verified  chan string
}

func (v *verifyFake) VerifyReachable(_ context.Context, _, device string) error {
	select {
	case v.verified <- device:
	default:
	}
	return v.verifyErr
}

// LAN writes are fire-and-forget, so a silent device means the effect never
// landed. The read-back must turn that into an honest failure, NOT "effect ok".
func TestEngine_UnverifiedEffectReportsFailure(t *testing.T) {
	fake := &verifyFake{
		fakeController: *newFakeController(),
		verifyErr:      errors.New("no devStatus reply from 192.168.7.75"),
		verified:       make(chan string, 1),
	}
	act := activity.GetLog()
	e := NewEngine(fake, act)
	defer e.Stop()

	e.Enqueue(testJob("m-unverified", "dev-v1"), 0)

	select {
	case <-fake.verified:
	case <-time.After(5 * time.Second):
		t.Fatal("verification was never attempted")
	}

	waitFor(t, func() bool {
		for _, r := range act.Recent(0) {
			if r.MappingID == "m-unverified" && r.Result != "queued" {
				return true
			}
		}
		return false
	})
	for _, r := range act.Recent(0) {
		if r.MappingID == "m-unverified" && r.Result == "effect ok" {
			t.Error("reported 'effect ok' for an unverified (silent) device")
		}
	}
}

// A verified effect reports ok as usual.
func TestEngine_VerifiedEffectReportsOK(t *testing.T) {
	fake := &verifyFake{
		fakeController: *newFakeController(),
		verified:       make(chan string, 1),
	}
	act := activity.GetLog()
	e := NewEngine(fake, act)
	defer e.Stop()

	e.Enqueue(testJob("m-verified", "dev-v2"), 0)
	waitFor(t, func() bool {
		for _, r := range act.Recent(0) {
			if r.MappingID == "m-verified" && r.Result == "effect ok" {
				return true
			}
		}
		return false
	})
}

// Manual controls take the device lock (no interleave with effects) but skip
// quiet hours — the operator is explicitly acting.
func TestEngine_ControlSerializedAndQuietHoursExempt(t *testing.T) {
	fake := newFakeController()
	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	// Quiet hours active for effects...
	e.now = func() time.Time { return time.Date(2026, 7, 11, 23, 0, 0, 0, time.Local) }
	e.SetQuietHours(QuietHours{Enabled: true, Start: "22:00", End: "07:00"})
	if got := e.Enqueue(testJob("m10", "dev-10"), 0); got != "quiet hours" {
		t.Fatalf("effect Enqueue = %q, want quiet hours", got)
	}

	// ...but the manual control still runs.
	e.EnqueueControl(Device{SKU: "H1", Device: "dev-10"}, "devices.capabilities.on_off", "powerSwitch", 1, "Manual control (powerSwitch)")
	waitFor(t, func() bool { return fake.callCount() >= 1 })

	// Serialization shares the same per-device lock as effects.
	e.EnqueueControl(Device{SKU: "H1", Device: "dev-10"}, "devices.capabilities.range", "brightness", 50, "Manual control (brightness)")
	e.EnqueueControl(Device{SKU: "H1", Device: "dev-10"}, "devices.capabilities.range", "brightness", 60, "Manual control (brightness)")
	waitFor(t, func() bool { return fake.callCount() >= 3 })
	if fake.overlap {
		t.Error("manual controls overlapped on the same device")
	}
}

// sceneFake extends fakeController with scene restore support.
type sceneFake struct {
	fakeController
	sceneRestored chan string // instance restored
}

func (s *sceneFake) ApplyScene(_ context.Context, _, device, instance string, _ json.RawMessage) error {
	s.enter(device, "scene")
	select {
	case s.sceneRestored <- instance:
	default:
	}
	return nil
}

// A configured after-effect scene is the deliberate final state: no state
// capture (even with Restore set), effect runs, then power-on + scene.
func TestEngine_AfterSceneAppliedAndSupersedesRestore(t *testing.T) {
	fake := &sceneFake{
		fakeController: *newFakeController(),
		sceneRestored:  make(chan string, 1),
	}

	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	job := testJob("m9", "dev-9")
	job.Params.Restore = true // must be ignored — afterScene supersedes it
	job.Devices[0].AfterScene = &SceneRef{
		Instance: govee.InstLightScene,
		Name:     "Sunrise",
		Value:    json.RawMessage(`{"paramId":16433,"id":9558}`),
	}
	e.Enqueue(job, 0)

	select {
	case <-fake.sceneRestored:
	case <-time.After(5 * time.Second):
		t.Fatal("after-effect scene was never applied")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, c := range fake.calls {
		if c == "dev-9:state" {
			t.Errorf("state was captured despite afterScene; calls: %v", fake.calls)
		}
	}
	last := fake.calls[len(fake.calls)-1]
	if last != "dev-9:scene" {
		t.Errorf("final call = %s, want dev-9:scene; calls: %v", last, fake.calls)
	}
}

// A reported scene must be re-applied on restore INSTEAD of solid color —
// setting colorRgb would kill the scene the lamp was playing.
func TestEngine_RestorePrefersSceneOverColor(t *testing.T) {
	fake := &sceneFake{
		fakeController: *newFakeController(),
		sceneRestored:  make(chan string, 1),
	}
	brightness, color, power := 40, 0x123456, 1
	fake.state = &govee.DeviceState{
		PowerOn: &power, Brightness: &brightness, ColorRgb: &color,
		SceneInstance: govee.InstLightScene,
		SceneValue:    json.RawMessage(`{"paramId":1234,"id":5678}`),
	}

	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	job := testJob("m8", "dev-8")
	job.Params.Restore = true
	e.Enqueue(job, 0)

	select {
	case instance := <-fake.sceneRestored:
		if instance != govee.InstLightScene {
			t.Errorf("restored instance = %q, want lightScene", instance)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scene was never restored")
	}

	// The scene path must not also push a solid color afterwards.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	sawScene := false
	for _, c := range fake.calls {
		if c == "dev-8:scene" {
			sawScene = true
		}
		if sawScene && c == "dev-8:color" {
			t.Errorf("solid color applied after scene restore; calls: %v", fake.calls)
		}
	}
}

func TestEngine_RestoreReappliesState(t *testing.T) {
	fake := newFakeController()
	brightness, color, power := 40, 0x123456, 1
	fake.state = &govee.DeviceState{PowerOn: &power, Brightness: &brightness, ColorRgb: &color}

	e := NewEngine(fake, activity.GetLog())
	defer e.Stop()

	job := testJob("m6", "dev-6")
	job.Params.Restore = true
	e.Enqueue(job, 0)

	// state + (power, brightness, color) + restore (color, brightness) = 6
	waitFor(t, func() bool { return fake.callCount() >= 6 })
}
