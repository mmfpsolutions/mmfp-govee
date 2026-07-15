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
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/dispatch"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
)

// deviceView is the UI projection of a Govee device: identity plus a summary
// of the capabilities the effects engine can drive.
type deviceView struct {
	SKU          string   `json:"sku"`
	Device       string   `json:"device"`
	DeviceName   string   `json:"deviceName"`
	Type         string   `json:"type"`
	Capabilities []string `json:"capabilities"` // "power", "color", "brightness"
	Controllable bool     `json:"controllable"` // has power + color
	// AssignedScene is the device's configured "normal" scene — every effect
	// on this device ends by re-applying it (config.DeviceScenes).
	AssignedScene *config.SceneRef `json:"assignedScene,omitempty"`
	// CapabilityDetails is the device's full capability declarations
	// (type/instance/parameters) — the controller page's widget registry
	// renders from these.
	CapabilityDetails []govee.Capability `json:"capabilityDetails,omitempty"`
	// LANControl reports a live LAN route: this device is served over UDP
	// (fast, free, offline) instead of the cloud API. Drives the dashboard's
	// LAN Control column — which is also the operator's feedback loop for
	// toggling LAN Control on more devices in the Govee Home app.
	LANControl bool   `json:"lanControl"`
	LANIP      string `json:"lanIP,omitempty"`
}

func toDeviceViews(devices []govee.Device, cfg *config.Config, lanRoutes map[string]govee.LANRoute) []deviceView {
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		v := deviceView{
			SKU:        d.SKU,
			Device:     d.Device,
			DeviceName: d.DeviceName,
			Type:       d.Type,
		}
		var hasPower, hasColor bool
		for _, c := range d.Capabilities {
			switch {
			case c.Type == govee.CapOnOff && c.Instance == govee.InstPower:
				v.Capabilities = append(v.Capabilities, "power")
				hasPower = true
			case c.Type == govee.CapColor && c.Instance == govee.InstColorRgb:
				v.Capabilities = append(v.Capabilities, "color")
				hasColor = true
			case c.Type == govee.CapRange && c.Instance == govee.InstBrightness:
				v.Capabilities = append(v.Capabilities, "brightness")
			}
		}
		v.Controllable = hasPower && hasColor
		if cfg.DeviceScenes != nil {
			v.AssignedScene = cfg.DeviceScenes[d.Device]
		}
		v.CapabilityDetails = d.Capabilities
		if route, ok := lanRoutes[d.Device]; ok {
			v.LANControl = true
			v.LANIP = route.IP
		}
		views = append(views, v)
	}
	return views
}

type devicesData struct {
	Devices  []deviceView `json:"devices"`
	CachedAt int64        `json:"cachedAt,omitempty"` // unix seconds; 0 = never fetched
}

// HandleDevicesList handles GET /api/v1/devices (cached)
func HandleDevicesList(client *govee.Client, cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		devices, err := client.ListDevices(r.Context())
		if err != nil {
			log.Error("Device list failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}
		data := devicesData{Devices: toDeviceViews(devices, cfgManager.GetConfig(), client.LANRoutes())}
		if t := client.CachedAt(); !t.IsZero() {
			data.CachedAt = t.Unix()
		}
		v1types.RespondOK(w, data, v1types.NewMeta(start))
	}
}

// HandleDevicesRefresh handles POST /api/v1/devices/refresh
func HandleDevicesRefresh(client *govee.Client, cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Manual refresh re-scans the LAN too — this is the operator's
		// feedback loop after enabling LAN Control on a device in the app.
		client.LANRescan(r.Context())
		devices, err := client.RefreshDevices(r.Context())
		if err != nil {
			log.Error("Device refresh failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}
		data := devicesData{Devices: toDeviceViews(devices, cfgManager.GetConfig(), client.LANRoutes())}
		if t := client.CachedAt(); !t.IsZero() {
			data.CachedAt = t.Unix()
		}
		v1types.RespondOK(w, data, v1types.NewMeta(start))
	}
}

// HandleDeviceScenes handles GET /api/v1/devices/{device}/scenes?sku=...
// — the device's scene catalog (cached; ?refresh=1 re-fetches). Used by the
// mapping editor's per-device "after effect" scene dropdown.
func HandleDeviceScenes(client *govee.Client) http.HandlerFunc {
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
		refresh := r.URL.Query().Get("refresh") == "1"

		scenes, err := client.ListScenes(r.Context(), sku, device, refresh)
		if err != nil {
			log.Error("Scene list for %s failed: %v", device, err)
			v1types.RespondErrorMsg(w, http.StatusBadGateway, "GOVEE_ERROR", err.Error())
			return
		}
		v1types.RespondOK(w, map[string]interface{}{"scenes": scenes}, nil)
	}
}

// HandleDeviceTest handles POST /api/v1/devices/{device}/test — a short
// white/blue blink to visually confirm which physical light this row is.
// sku comes in the body because device IDs alone don't address the API.
func HandleDeviceTest(engine *effects.Engine, cfgManager *config.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// chi.URLParam returns the still-escaped path segment; Govee device
		// IDs are MAC-style (colons), so %3A must be decoded before use.
		device, err := url.PathUnescape(chi.URLParam(r, "device"))
		if err != nil || device == "" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_DEVICE", "Invalid device id")
			return
		}

		var req struct {
			SKU string `json:"sku"`
		}
		if err := decodeBody(r, &req); err != nil || req.SKU == "" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Body must include sku")
			return
		}

		// Canonical config→engine projection (resolves the assigned scene) —
		// same path the webhook dispatcher uses. Without an assigned scene it
		// falls back to state capture/restore (best-effort — Govee can't
		// report an active scene, so scene lamps come back as solid color).
		dev := dispatch.DeviceFor(cfgManager.GetConfig(), config.DeviceRef{SKU: req.SKU, Device: device})

		job := effects.Job{
			MappingID:   "device-test-" + device,
			MappingName: "Device test",
			Effect:      effects.EffectFlash,
			Params: effects.Params{
				ColorA:   0xFFFFFF, // white
				ColorB:   0x3B82F6, // MMFP blue
				Cycles:   2,
				DelayMs:  500,
				EndState: effects.EndOff,
				// Put the light back how it was — a test blink must not leave
				// an already-on lamp dark (or change its color).
				Restore: true,
			},
			Devices:   []effects.Device{dev},
			TokenName: "ui",
			Source:    "ui",
			Event:     "device_test",
		}
		if err := effects.Validate(job.Effect, &job.Params); err != nil {
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", err.Error())
			return
		}
		result := engine.Enqueue(job, 0)
		v1types.RespondOK(w, map[string]string{"result": result}, nil)
	}
}

// HandleDeviceSceneSet handles PUT /api/v1/devices/{device}/scene — assigns
// (or clears, with an empty body value) the device's normal scene.
func HandleDeviceSceneSet(cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		device, err := url.PathUnescape(chi.URLParam(r, "device"))
		if err != nil || device == "" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_DEVICE", "Invalid device id")
			return
		}

		var req config.SceneRef
		if err := decodeBody(r, &req); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
			return
		}

		clone := cloneConfig(cfgManager.GetConfig())
		if len(req.Value) == 0 || string(req.Value) == "null" {
			delete(clone.DeviceScenes, device)
		} else {
			switch req.Instance {
			case "lightScene", "diyScene", "snapshot":
			default:
				v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_SCENE", "instance must be lightScene, diyScene, or snapshot")
				return
			}
			clone.DeviceScenes[device] = &config.SceneRef{Instance: req.Instance, Name: req.Name, Value: req.Value}
		}

		if err := cfgManager.Save(clone); err != nil {
			log.Error("Save config failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to save configuration")
			return
		}
		v1types.RespondOK(w, map[string]string{"message": "Scene assignment saved"}, nil)
	}
}
