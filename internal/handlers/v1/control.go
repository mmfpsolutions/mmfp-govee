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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
)

// HandleDeviceState handles GET /api/v1/devices/{device}/state?sku=... —
// the device's full reported state for the controller page. Manual-refresh
// only by design (Govee: 30 state reads/min per device).
func HandleDeviceState(client *govee.Client) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		device, err := url.PathUnescape(chi.URLParam(r, "device"))
		if err != nil || device == "" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_DEVICE", "Invalid device id")
			return
		}
		sku := r.URL.Query().Get("sku")
		if sku == "" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_SKU", "sku query parameter is required")
			return
		}

		online, states, err := client.FullState(r.Context(), sku, device)
		if err != nil {
			log.Error("State read for %s failed: %v", device, err)
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}
		v1types.RespondOK(w, map[string]interface{}{
			"online": online,
			"states": states,
		}, nil)
	}
}

// deviceStatus is one row of the dashboard Status column.
type deviceStatus struct {
	Online  bool `json:"online"`
	PowerOn *int `json:"powerOn,omitempty"` // 1/0; nil = not reported
}

// HandleDevicesStatus handles GET /api/v1/devices/status — one state read
// per cached device, fanned out concurrently (WaitGroup; the govee client's
// throttle paces the actual calls). The dashboard fills its Status column
// and power toggles from this. Manual refresh only — ~1 Govee call per
// device per request.
func HandleDevicesStatus(client *govee.Client) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		devices, err := client.ListDevices(r.Context())
		if err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}

		statuses := make(map[string]deviceStatus, len(devices))
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, d := range devices {
			d := d
			wg.Add(1)
			go func() {
				defer wg.Done()
				online, states, err := client.FullState(r.Context(), d.SKU, d.Device)
				if err != nil {
					log.Debug("Status read for %s failed: %v", d.Device, err)
					return // row stays absent — the UI shows "?"
				}
				st := deviceStatus{Online: online}
				for _, s := range states {
					if s.Type == govee.CapOnOff && s.Instance == govee.InstPower {
						var n int
						if json.Unmarshal(s.Value, &n) == nil {
							st.PowerOn = &n
						}
					}
				}
				mu.Lock()
				statuses[d.Device] = st
				mu.Unlock()
			}()
		}
		wg.Wait()

		v1types.RespondOK(w, map[string]interface{}{"statuses": statuses}, nil)
	}
}

// controlBody is the manual-control request.
type controlBody struct {
	SKU      string          `json:"sku"`
	Type     string          `json:"type"`
	Instance string          `json:"instance"`
	Value    json.RawMessage `json:"value"`
}

// HandleDeviceControl handles POST /api/v1/devices/{device}/control — one
// capability write, validated against the device's own capability
// declarations, then queued through the engine (per-device serialization,
// budget; deliberately NO cooldown and NO quiet-hours check — the operator
// is explicitly acting).
func HandleDeviceControl(client *govee.Client, engine *effects.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		device, err := url.PathUnescape(chi.URLParam(r, "device"))
		if err != nil || device == "" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_DEVICE", "Invalid device id")
			return
		}

		var req controlBody
		if err := decodeBody(r, &req); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
			return
		}
		if req.SKU == "" || req.Type == "" || req.Instance == "" || len(req.Value) == 0 {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_CONTROL", "sku, type, instance, and value are required")
			return
		}

		// Validate against the device's own capability declarations from the
		// cache. A device the cache doesn't know can't be controlled.
		devices, err := client.ListDevices(r.Context())
		if err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}
		capability, found := findCapability(devices, device, req.Type, req.Instance)
		if !found {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "UNKNOWN_CAPABILITY",
				fmt.Sprintf("Device does not declare %s/%s (refresh the device list?)", req.Type, req.Instance))
			return
		}
		if err := validateControlValue(capability, req.Value); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_VALUE", err.Error())
			return
		}

		var value interface{}
		if err := json.Unmarshal(req.Value, &value); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_VALUE", "value is not valid JSON")
			return
		}

		engine.EnqueueControl(
			effects.Device{SKU: req.SKU, Device: device},
			req.Type, req.Instance, value,
			"Manual control ("+req.Instance+")",
		)
		v1types.RespondOK(w, map[string]string{"result": "queued"}, nil)
	}
}

func findCapability(devices []govee.Device, deviceID, capType, instance string) (govee.Capability, bool) {
	for _, d := range devices {
		if d.Device != deviceID {
			continue
		}
		for _, c := range d.Capabilities {
			if c.Type == capType && c.Instance == instance {
				return c, true
			}
		}
	}
	return govee.Capability{}, false
}

// capParameters is the subset of Govee capability parameter declarations the
// validator understands.
type capParameters struct {
	DataType string `json:"dataType"`
	Range    *struct {
		Min int `json:"min"`
		Max int `json:"max"`
	} `json:"range"`
	Options []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"options"`
	Fields []struct {
		FieldName string `json:"fieldName"`
		DataType  string `json:"dataType"`
		Required  bool   `json:"required"`
		Range     *struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"range"`
		Options []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"options"`
	} `json:"fields"`
}

// validateControlValue checks a control value against the capability's own
// parameter declarations: INTEGER ranges, ENUM membership, STRUCT fields.
// Declarations the validator doesn't model (arrays, empty ENUM option lists
// like dynamic scenes) pass through — Govee is the final authority.
func validateControlValue(capability govee.Capability, value json.RawMessage) error {
	if len(capability.Parameters) == 0 {
		return nil
	}
	var params capParameters
	if err := json.Unmarshal(capability.Parameters, &params); err != nil {
		return nil // undeclared/odd parameters — pass through
	}

	switch params.DataType {
	case "INTEGER":
		var n int
		if err := json.Unmarshal(value, &n); err != nil {
			return fmt.Errorf("%s expects an integer", capability.Instance)
		}
		if params.Range != nil && (n < params.Range.Min || n > params.Range.Max) {
			return fmt.Errorf("%s must be %d-%d", capability.Instance, params.Range.Min, params.Range.Max)
		}
	case "ENUM":
		if len(params.Options) == 0 {
			return nil // e.g. dynamic scenes — options live in the scene catalog
		}
		for _, opt := range params.Options {
			if jsonEqual(opt.Value, value) {
				return nil
			}
		}
		return fmt.Errorf("%s: value is not one of the declared options", capability.Instance)
	case "STRUCT":
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(value, &obj); err != nil {
			return fmt.Errorf("%s expects an object", capability.Instance)
		}
		for _, f := range params.Fields {
			fv, present := obj[f.FieldName]
			if !present {
				if f.Required {
					return fmt.Errorf("%s: field %q is required", capability.Instance, f.FieldName)
				}
				continue
			}
			switch f.DataType {
			case "INTEGER":
				var n int
				if err := json.Unmarshal(fv, &n); err != nil {
					return fmt.Errorf("%s.%s expects an integer", capability.Instance, f.FieldName)
				}
				if f.Range != nil && (n < f.Range.Min || n > f.Range.Max) {
					return fmt.Errorf("%s.%s must be %d-%d", capability.Instance, f.FieldName, f.Range.Min, f.Range.Max)
				}
			case "ENUM":
				if len(f.Options) == 0 {
					continue
				}
				ok := false
				for _, opt := range f.Options {
					if jsonEqual(opt.Value, fv) {
						ok = true
						break
					}
				}
				if !ok {
					return fmt.Errorf("%s.%s: value is not one of the declared options", capability.Instance, f.FieldName)
				}
			}
		}
	}
	return nil
}

// jsonEqual compares two JSON values by compacted bytes.
func jsonEqual(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
