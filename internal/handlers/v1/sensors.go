/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
)

// Sensors tab + the Devices-page pinned readout.
// Plan: design-documents/sensors/sensors-plan.md.

// sensorTTL is how long a reading is served without asking Govee again.
//
// Sensors are cloud-only devices (BLE pucks behind a gateway — they will
// never have a LAN route), measured at 1.6-4.5s per read. Temperature and
// humidity move over minutes to hours, so re-reading on every page load would
// put that latency in front of the user for a number that had not changed.
const sensorTTL = 5 * time.Minute

// sensorSource is the slice of govee.Client the sensor handlers use (an
// interface so tests can stand in a fake Govee).
type sensorSource interface {
	ListDevices(ctx context.Context) ([]govee.Device, error)
	FullState(ctx context.Context, sku, device string) (bool, []govee.CapabilityState, error)
}

var _ sensorSource = (*govee.Client)(nil)

// sensorReading is one measurement. Value is set when Govee sends a bare
// number; anything else is passed through as Raw text rather than dropped.
type sensorReading struct {
	Instance string   `json:"instance"`
	Label    string   `json:"label"`
	Unit     string   `json:"unit,omitempty"`
	Decimals *int     `json:"decimals,omitempty"` // nil = show as received
	Value    *float64 `json:"value,omitempty"`
	Raw      string   `json:"raw,omitempty"`
}

// sensorView is one row of the Sensors tab.
type sensorView struct {
	Device     string `json:"device"`
	DeviceName string `json:"deviceName"`
	SKU        string `json:"sku"`
	// Online is null for events-only sensors and for sensors never read
	// successfully. Govee reports the H5059 leak detector as offline while its
	// connectivity test passes (verified 2026-11-06), so for events-only
	// devices the flag is never sent — the UI cannot show it even by accident.
	Online     *bool           `json:"online"`
	EventsOnly bool            `json:"eventsOnly"`
	Readings   []sensorReading `json:"readings"`
	AgeSecs    *int            `json:"ageSecs"` // since the last SUCCESSFUL read
	LastError  string          `json:"lastError,omitempty"`
}

// readingMeta is the label/unit table for known reading instances. Go owns
// it so the JS stays dumb; adding a known instance is one line.
type readingMeta struct {
	label    string
	unit     string
	decimals int
	order    int
}

var knownReadings = map[string]readingMeta{
	// No unit field exists anywhere in Govee's payload; the value follows the
	// Govee account's setting. Scott's account is °F.
	govee.InstSensorTemperature: {label: "Temperature", unit: "°F", decimals: 1, order: 0},
	govee.InstSensorHumidity:    {label: "Humidity", unit: "%", decimals: 0, order: 1},
}

// hasReadings reports whether the device declares anything we can read.
// A device that declares only events (the leak detector) has nothing for
// /device/state to return.
func hasReadings(d govee.Device) bool {
	for _, c := range d.Capabilities {
		if c.Type == govee.CapProperty {
			return true
		}
	}
	return false
}

// humanizeInstance turns an unknown instance into a label:
// "sensorAirQuality" → "Air Quality".
func humanizeInstance(instance string) string {
	s := strings.TrimPrefix(instance, "sensor")
	if s == "" {
		s = instance
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte(' ')
		}
		if i == 0 {
			r = unicode.ToUpper(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// toReadings projects Govee state into ordered readings: known instances in
// table order, then the rest alphabetically, so a row never reshuffles.
func toReadings(states []govee.CapabilityState) []sensorReading {
	out := []sensorReading{}
	for _, s := range states {
		if s.Type != govee.CapProperty {
			continue
		}
		r := sensorReading{Instance: s.Instance, Label: humanizeInstance(s.Instance)}
		if meta, ok := knownReadings[s.Instance]; ok {
			r.Label, r.Unit = meta.label, meta.unit
			d := meta.decimals
			r.Decimals = &d
		}
		var v float64
		if err := json.Unmarshal(s.Value, &v); err == nil {
			r.Value = &v
		} else {
			// Not a bare number (e.g. a struct). Show it rather than hide it;
			// the H5179's humidity shape is unverified.
			var compact bytes.Buffer
			if json.Compact(&compact, s.Value) == nil {
				r.Raw = compact.String()
			} else {
				r.Raw = string(s.Value)
			}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		mi, iKnown := knownReadings[out[i].Instance]
		mj, jKnown := knownReadings[out[j].Instance]
		switch {
		case iKnown && jKnown:
			return mi.order < mj.order
		case iKnown != jKnown:
			return iKnown
		default:
			return out[i].Instance < out[j].Instance
		}
	})
	return out
}

// sensorEntry is the cached state of one sensor.
type sensorEntry struct {
	online    bool
	readings  []sensorReading
	readAt    time.Time // last SUCCESSFUL read; zero = never
	lastError string    // most recent read's error; "" after a success
}

// Sensors serves the Sensors tab and the pinned readout from ONE per-device
// cache, so opening one never doubles the other's Govee calls.
type Sensors struct {
	src        sensorSource
	cfgManager *config.Manager
	log        *logger.Logger
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*sensorEntry
}

// NewSensors builds the sensor handlers around a Govee client.
func NewSensors(client *govee.Client, cfgManager *config.Manager) *Sensors {
	return newSensors(client, cfgManager)
}

func newSensors(src sensorSource, cfgManager *config.Manager) *Sensors {
	return &Sensors{
		src:        src,
		cfgManager: cfgManager,
		log:        logger.New(logger.ModuleHandler),
		now:        time.Now,
		entries:    map[string]*sensorEntry{},
	}
}

// sensorsIn returns the catalog's sensors, sorted by name.
func sensorsIn(devices []govee.Device) []govee.Device {
	var out []govee.Device
	for _, d := range devices {
		if isSensor(d) {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].DeviceName) < strings.ToLower(out[j].DeviceName)
	})
	return out
}

// read returns a view per sensor, reading from Govee only where the cache is
// stale (or force is set). Reads fan out in parallel, so the wait is bounded
// by the slowest sensor. Events-only sensors are never read — /device/state
// has nothing for them but an online flag we know to be wrong.
func (s *Sensors) read(ctx context.Context, sensors []govee.Device, force bool) []sensorView {
	var wg sync.WaitGroup
	for _, d := range sensors {
		if !hasReadings(d) {
			continue
		}
		s.mu.Lock()
		e := s.entries[d.Device]
		fresh := e != nil && !e.readAt.IsZero() && s.now().Sub(e.readAt) < sensorTTL
		s.mu.Unlock()
		if fresh && !force {
			continue
		}

		d := d
		wg.Add(1)
		go func() {
			defer wg.Done()
			online, states, err := s.src.FullState(ctx, d.SKU, d.Device)

			s.mu.Lock()
			defer s.mu.Unlock()
			e := s.entries[d.Device]
			if e == nil {
				e = &sensorEntry{}
				s.entries[d.Device] = e
			}
			if err != nil {
				// Keep the last good reading: the UI shows it with its real
				// age and a "read failed" cue instead of making it vanish.
				s.log.Debug("Sensor read for %s failed: %v", d.DeviceName, err)
				e.lastError = err.Error()
				return
			}
			e.online = online
			e.readings = toReadings(states)
			e.readAt = s.now()
			e.lastError = ""
		}()
	}
	wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	views := make([]sensorView, 0, len(sensors))
	for _, d := range sensors {
		v := sensorView{
			Device:     d.Device,
			DeviceName: d.DeviceName,
			SKU:        d.SKU,
			EventsOnly: !hasReadings(d),
			Readings:   []sensorReading{},
		}
		if e := s.entries[d.Device]; e != nil && !v.EventsOnly {
			v.LastError = e.lastError
			if !e.readAt.IsZero() {
				online := e.online
				v.Online = &online
				v.Readings = append(v.Readings, e.readings...)
				age := int(s.now().Sub(e.readAt).Seconds())
				v.AgeSecs = &age
			}
		}
		views = append(views, v)
	}
	return views
}

// pinnedID resolves the pinned sensor, applying the one-time default: when
// nothing was ever chosen and exactly ONE sensor reports a reading, pin it and
// save, so the choice survives more sensors being added later. A pin to a
// device no longer in the catalog resolves to "" but is NOT cleared, so a
// sensor that drops out during a cloud blip gets its pin back.
func (s *Sensors) pinnedID(sensors []govee.Device) string {
	cfg := s.cfgManager.GetConfig()
	if cfg.PinnedSensor == nil {
		var candidates []govee.Device
		for _, d := range sensors {
			if hasReadings(d) {
				candidates = append(candidates, d)
			}
		}
		if len(candidates) != 1 {
			return ""
		}
		id := candidates[0].Device
		clone := cloneConfig(cfg)
		clone.PinnedSensor = &id
		if err := s.cfgManager.Save(clone); err != nil {
			s.log.Error("Auto-pin sensor: save config failed: %v", err)
		} else {
			s.log.Info("Pinned sensor defaulted to %s (the only sensor with a reading)", candidates[0].DeviceName)
		}
		return id
	}
	for _, d := range sensors {
		if d.Device == *cfg.PinnedSensor && hasReadings(d) {
			return d.Device
		}
	}
	return ""
}

// HandleList handles GET /api/v1/sensors — every sensor in the catalog.
// ?refresh=1 bypasses the TTL (the Sensors tab's Refresh button).
func (s *Sensors) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		devices, err := s.src.ListDevices(r.Context())
		if err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}
		sensors := sensorsIn(devices)
		views := s.read(r.Context(), sensors, r.URL.Query().Get("refresh") == "1")
		v1types.RespondOK(w, map[string]interface{}{
			"sensors": views,
			"pinned":  s.pinnedID(sensors),
		}, nil)
	}
}

// HandlePinnedGet handles GET /api/v1/sensors/pinned — the Devices-page
// readout. Reads only the pinned sensor; sensor is null when none is pinned.
func (s *Sensors) HandlePinnedGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		devices, err := s.src.ListDevices(r.Context())
		if err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}
		sensors := sensorsIn(devices)
		id := s.pinnedID(sensors)
		var pinned *sensorView
		for _, d := range sensors {
			if d.Device == id {
				views := s.read(r.Context(), []govee.Device{d}, false)
				pinned = &views[0]
				break
			}
		}
		v1types.RespondOK(w, map[string]interface{}{"sensor": pinned}, nil)
	}
}

// HandlePinnedSet handles PUT /api/v1/sensors/pinned — body {"device": id}
// pins a sensor, {"device": ""} unpins (stored as "", hiding the readout).
func (s *Sensors) HandlePinnedSet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Device string `json:"device"`
		}
		if err := decodeBody(r, &req); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
			return
		}

		if req.Device != "" {
			devices, err := s.src.ListDevices(r.Context())
			if err != nil {
				v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
				return
			}
			ok := false
			for _, d := range sensorsIn(devices) {
				if d.Device == req.Device && hasReadings(d) {
					ok = true
					break
				}
			}
			if !ok {
				v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_SENSOR", "Not a sensor with a reading")
				return
			}
		}

		clone := cloneConfig(s.cfgManager.GetConfig())
		pinned := req.Device
		clone.PinnedSensor = &pinned
		if err := s.cfgManager.Save(clone); err != nil {
			s.log.Error("Save config failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to save configuration")
			return
		}
		v1types.RespondOK(w, map[string]string{"pinned": pinned}, nil)
	}
}
