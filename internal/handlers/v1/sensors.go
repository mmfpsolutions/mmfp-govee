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
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
)

// sensorReading is one read-only measurement for the dashboard's readout.
type sensorReading struct {
	Device     string  `json:"device"`
	DeviceName string  `json:"deviceName"`
	Instance   string  `json:"instance"`
	Value      float64 `json:"value"`
	Online     bool    `json:"online"`
}

// isTemperatureSensor reports whether this device reports a temperature.
//
// These are exactly the devices isControllable removes from the dashboard —
// they command nothing, so they get no control row. But a thermometer still
// has something worth SEEING, which is a readout, not a control. Keeping the
// two surfaces separate is why a pool temperature can be shown without
// putting a dead power button next to it.
func isTemperatureSensor(d govee.Device) bool {
	for _, c := range d.Capabilities {
		if c.Type == govee.CapProperty && c.Instance == govee.InstSensorTemperature {
			return true
		}
	}
	return false
}

// HandleSensors handles GET /api/v1/sensors — current readings from the
// read-only sensors in the catalog (temperature only for now).
//
// Separate from /devices/status deliberately: that endpoint answers "can I
// see and toggle this device", and folding sensors back into it would undo
// the filtering that took the status sweep from ~7s to ~1s.
func HandleSensors(client *govee.Client) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		devices, err := client.ListDevices(r.Context())
		if err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}

		var (
			mu       sync.Mutex
			wg       sync.WaitGroup
			readings []sensorReading
		)

		for _, d := range devices {
			d := d
			if !isTemperatureSensor(d) {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				online, states, err := client.FullState(r.Context(), d.SKU, d.Device)
				if err != nil {
					log.Debug("Sensor read for %s failed: %v", d.DeviceName, err)
					return // omitted — the readout just doesn't render
				}
				for _, s := range states {
					if s.Type != govee.CapProperty || s.Instance != govee.InstSensorTemperature {
						continue
					}
					var v float64
					if json.Unmarshal(s.Value, &v) != nil {
						continue
					}
					mu.Lock()
					readings = append(readings, sensorReading{
						Device:     d.Device,
						DeviceName: d.DeviceName,
						Instance:   s.Instance,
						Value:      v,
						Online:     online,
					})
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		// Stable order so a multi-sensor readout doesn't shuffle per load.
		sort.Slice(readings, func(i, j int) bool {
			return readings[i].DeviceName < readings[j].DeviceName
		})

		v1types.RespondOK(w, map[string]interface{}{"sensors": readings}, nil)
	}
}
