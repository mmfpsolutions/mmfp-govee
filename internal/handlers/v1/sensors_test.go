/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * Licensed under the MIT License. See LICENSE for details.
 */

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
)

var (
	poolSensor = govee.Device{SKU: "H5310", Device: "POOL", DeviceName: "Pool Thermometer",
		Capabilities: []govee.Capability{{Type: govee.CapProperty, Instance: govee.InstSensorTemperature}}}
	leakSensor = govee.Device{SKU: "H5059", Device: "LEAK", DeviceName: "Attic Leak Detector-1",
		Capabilities: []govee.Capability{{Type: govee.CapEvent, Instance: "bodyAppearedEvent"}}}
	hygroSensor = govee.Device{SKU: "H5179", Device: "HYGRO", DeviceName: "Garage Thermo-Hygrometer",
		Capabilities: []govee.Capability{
			{Type: govee.CapProperty, Instance: govee.InstSensorTemperature},
			{Type: govee.CapProperty, Instance: govee.InstSensorHumidity},
		}}
	denLamp = govee.Device{SKU: "H607C", Device: "LAMP", DeviceName: "Den Floor Lamp",
		Capabilities: []govee.Capability{{Type: govee.CapOnOff, Instance: govee.InstPower}}}
)

// fakeSensorSource stands in for Govee: a fixed catalog, per-device state,
// optional per-device errors, and a count of state reads per device.
type fakeSensorSource struct {
	mu      sync.Mutex
	devices []govee.Device
	online  map[string]bool
	states  map[string][]govee.CapabilityState
	errs    map[string]error
	reads   map[string]int
}

func newFakeSensorSource(devices ...govee.Device) *fakeSensorSource {
	return &fakeSensorSource{
		devices: devices,
		online:  map[string]bool{},
		states:  map[string][]govee.CapabilityState{},
		errs:    map[string]error{},
		reads:   map[string]int{},
	}
}

func (f *fakeSensorSource) ListDevices(context.Context) ([]govee.Device, error) {
	return f.devices, nil
}

func (f *fakeSensorSource) FullState(_ context.Context, _, device string) (bool, []govee.CapabilityState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads[device]++
	if err := f.errs[device]; err != nil {
		return false, nil, err
	}
	return f.online[device], f.states[device], nil
}

func (f *fakeSensorSource) set(device string, online bool, kv ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online[device] = online
	var states []govee.CapabilityState
	for i := 0; i+1 < len(kv); i += 2 {
		states = append(states, govee.CapabilityState{
			Type: govee.CapProperty, Instance: kv[i], Value: json.RawMessage(kv[i+1]),
		})
	}
	f.states[device] = states
}

func (f *fakeSensorSource) readCount(device string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[device]
}

// resetPin puts the shared test config's pinnedSensor into a known state.
func resetPin(t *testing.T, pin *string) {
	t.Helper()
	clone := cloneConfig(testCfgManager.GetConfig())
	clone.PinnedSensor = pin
	if err := testCfgManager.Save(clone); err != nil {
		t.Fatal(err)
	}
}

func strPtr(s string) *string { return &s }

type listResponse struct {
	Data struct {
		Sensors []sensorView `json:"sensors"`
		Pinned  string       `json:"pinned"`
	} `json:"data"`
}

func getSensors(t *testing.T, s *Sensors, query string) listResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	s.HandleList()(rec, httptest.NewRequest("GET", "/api/v1/sensors"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /sensors = %d: %s", rec.Code, rec.Body.String())
	}
	var out listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestToReadings(t *testing.T) {
	got := toReadings([]govee.CapabilityState{
		{Type: govee.CapProperty, Instance: "sensorAirQuality", Value: json.RawMessage(`12`)},
		{Type: govee.CapProperty, Instance: govee.InstSensorHumidity, Value: json.RawMessage(`{"currentHumidity": 41}`)},
		{Type: "devices.capabilities.online", Instance: "online", Value: json.RawMessage(`true`)},
		{Type: govee.CapProperty, Instance: govee.InstSensorTemperature, Value: json.RawMessage(`80.96`)},
	})
	if len(got) != 3 {
		t.Fatalf("got %d readings, want 3 (non-property states dropped): %+v", len(got), got)
	}
	// Known instances first in table order, then the rest.
	if got[0].Instance != govee.InstSensorTemperature || got[1].Instance != govee.InstSensorHumidity || got[2].Instance != "sensorAirQuality" {
		t.Errorf("order = %s, %s, %s", got[0].Instance, got[1].Instance, got[2].Instance)
	}
	if got[0].Value == nil || *got[0].Value != 80.96 || got[0].Unit != "°F" || got[0].Label != "Temperature" {
		t.Errorf("temperature = %+v", got[0])
	}
	// A non-number shape is passed through, not dropped.
	if got[1].Value != nil || got[1].Raw != `{"currentHumidity":41}` || got[1].Unit != "%" {
		t.Errorf("humidity = %+v", got[1])
	}
	if got[2].Label != "Air Quality" || got[2].Unit != "" || got[2].Decimals != nil {
		t.Errorf("unknown instance = %+v", got[2])
	}
}

// The leak detector reports online:false while its connectivity test passes
// (verified 2026-11-06), so events-only sensors are listed with nothing: no
// state read, no online flag.
func TestSensors_EventsOnlyNeverReadNoOnline(t *testing.T) {
	resetPin(t, strPtr(""))
	src := newFakeSensorSource(poolSensor, leakSensor, denLamp)
	src.set("POOL", true, govee.InstSensorTemperature, `80.96`)
	s := newSensors(src, testCfgManager)

	resp := getSensors(t, s, "")
	if len(resp.Data.Sensors) != 2 {
		t.Fatalf("got %d sensors, want 2 (lamp excluded): %+v", len(resp.Data.Sensors), resp.Data.Sensors)
	}
	leak := resp.Data.Sensors[0] // sorted by name: Attic before Pool
	if leak.Device != "LEAK" || !leak.EventsOnly || leak.Online != nil || len(leak.Readings) != 0 || leak.AgeSecs != nil {
		t.Errorf("leak view = %+v", leak)
	}
	if src.readCount("LEAK") != 0 {
		t.Error("events-only sensor was read from Govee")
	}
	pool := resp.Data.Sensors[1]
	if pool.Online == nil || !*pool.Online || len(pool.Readings) != 1 || *pool.Readings[0].Value != 80.96 {
		t.Errorf("pool view = %+v", pool)
	}
}

func TestSensors_CacheTTLRefreshAndLastGood(t *testing.T) {
	resetPin(t, strPtr(""))
	src := newFakeSensorSource(poolSensor)
	src.set("POOL", true, govee.InstSensorTemperature, `80.96`)
	s := newSensors(src, testCfgManager)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	getSensors(t, s, "")
	getSensors(t, s, "")
	if n := src.readCount("POOL"); n != 1 {
		t.Fatalf("reads within TTL = %d, want 1", n)
	}
	getSensors(t, s, "?refresh=1")
	if n := src.readCount("POOL"); n != 2 {
		t.Fatalf("refresh=1 did not bypass the TTL (reads = %d)", n)
	}

	// A failed read after the TTL keeps the last good value with its real age.
	now = now.Add(sensorTTL + time.Minute)
	src.errs["POOL"] = errors.New("govee API error 400: request error: ChannelException")
	pool := getSensors(t, s, "").Data.Sensors[0]
	if pool.LastError == "" || len(pool.Readings) != 1 || *pool.Readings[0].Value != 80.96 {
		t.Errorf("failed read lost the last good reading: %+v", pool)
	}
	if pool.AgeSecs == nil || *pool.AgeSecs != int((sensorTTL+time.Minute).Seconds()) {
		t.Errorf("age = %v, want the age of the last GOOD read", pool.AgeSecs)
	}

	// Recovery clears the error.
	delete(src.errs, "POOL")
	pool = getSensors(t, s, "").Data.Sensors[0]
	if pool.LastError != "" || *pool.AgeSecs != 0 {
		t.Errorf("after recovery: %+v", pool)
	}
}

func TestSensors_AutoPinDefault(t *testing.T) {
	// Never chosen + exactly one sensor with a reading (the leak detector
	// has none) → the pool is pinned and SAVED.
	resetPin(t, nil)
	src := newFakeSensorSource(poolSensor, leakSensor)
	src.set("POOL", true, govee.InstSensorTemperature, `80.96`)
	resp := getSensors(t, newSensors(src, testCfgManager), "")
	if resp.Data.Pinned != "POOL" {
		t.Errorf("pinned = %q, want POOL", resp.Data.Pinned)
	}
	if p := testCfgManager.GetConfig().PinnedSensor; p == nil || *p != "POOL" {
		t.Errorf("auto-pin not saved: %v", p)
	}

	// Saved choice survives the H5179s arriving.
	src2 := newFakeSensorSource(poolSensor, leakSensor, hygroSensor)
	if got := getSensors(t, newSensors(src2, testCfgManager), "").Data.Pinned; got != "POOL" {
		t.Errorf("after more sensors: pinned = %q, want POOL", got)
	}

	// Never chosen + more than one candidate → nothing pinned, nothing saved.
	resetPin(t, nil)
	if got := getSensors(t, newSensors(src2, testCfgManager), "").Data.Pinned; got != "" {
		t.Errorf("two candidates: pinned = %q, want none", got)
	}
	if testCfgManager.GetConfig().PinnedSensor != nil {
		t.Error("two candidates: a pin was saved")
	}

	// Explicit "none" is never overridden by the default.
	resetPin(t, strPtr(""))
	if got := getSensors(t, newSensors(src, testCfgManager), "").Data.Pinned; got != "" {
		t.Errorf("explicit none overridden: pinned = %q", got)
	}
}

func TestSensors_PinnedGetReadsOnlyPinned(t *testing.T) {
	resetPin(t, strPtr("POOL"))
	src := newFakeSensorSource(poolSensor, hygroSensor)
	src.set("POOL", true, govee.InstSensorTemperature, `80.96`)
	s := newSensors(src, testCfgManager)

	rec := httptest.NewRecorder()
	s.HandlePinnedGet()(rec, httptest.NewRequest("GET", "/api/v1/sensors/pinned", nil))
	var out struct {
		Data struct {
			Sensor *sensorView `json:"sensor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Sensor == nil || out.Data.Sensor.Device != "POOL" {
		t.Fatalf("pinned sensor = %+v", out.Data.Sensor)
	}
	if src.readCount("HYGRO") != 0 {
		t.Error("pinned endpoint read an unpinned sensor")
	}

	// A pin to a device no longer in the catalog acts as unpinned but is kept.
	resetPin(t, strPtr("GONE"))
	rec = httptest.NewRecorder()
	s.HandlePinnedGet()(rec, httptest.NewRequest("GET", "/api/v1/sensors/pinned", nil))
	out.Data.Sensor = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Sensor != nil {
		t.Errorf("stale pin: sensor = %+v, want null", out.Data.Sensor)
	}
	if p := testCfgManager.GetConfig().PinnedSensor; p == nil || *p != "GONE" {
		t.Errorf("stale pin was cleared: %v", p)
	}
}

func TestSensors_PinnedSet(t *testing.T) {
	resetPin(t, nil)
	s := newSensors(newFakeSensorSource(poolSensor, leakSensor, denLamp), testCfgManager)
	put := func(body string) int {
		rec := httptest.NewRecorder()
		s.HandlePinnedSet()(rec, httptest.NewRequest("PUT", "/api/v1/sensors/pinned", strings.NewReader(body)))
		return rec.Code
	}

	for _, bad := range []string{"LAMP", "LEAK", "NOPE"} {
		if code := put(`{"device":"` + bad + `"}`); code != http.StatusBadRequest {
			t.Errorf("pin %s = %d, want 400", bad, code)
		}
	}
	if code := put(`{"device":"POOL"}`); code != http.StatusOK {
		t.Fatalf("pin POOL = %d", code)
	}
	if p := testCfgManager.GetConfig().PinnedSensor; p == nil || *p != "POOL" {
		t.Errorf("pin not saved: %v", p)
	}
	if code := put(`{"device":""}`); code != http.StatusOK {
		t.Fatalf("unpin = %d", code)
	}
	if p := testCfgManager.GetConfig().PinnedSensor; p == nil || *p != "" {
		t.Errorf("unpin should store explicit none, got %v", p)
	}
}
