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
	"runtime"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	"github.com/mmfpsolutions/mmfp-govee/internal/security"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
	"github.com/mmfpsolutions/mmfp-govee/internal/version"
)

// settingsView is the Settings page projection (API key never leaves the
// server; only whether one is set).
type settingsView struct {
	WebServerPort         int                      `json:"webServerPort"`
	WebhookPort           int                      `json:"webhookPort"`
	GoveeAPIKeySet        bool                     `json:"goveeApiKeySet"`
	DisableAuthentication bool                     `json:"disableAuthentication"`
	QuietHours            *config.QuietHoursConfig `json:"quietHours,omitempty"`
	LogLevel              string                   `json:"logLevel"`
	GoveeCallsToday       int                      `json:"goveeCallsToday"`
	GoveeCallsLimit       int                      `json:"goveeCallsLimit"`
	// LAN fast path (see design-documents/lan-transport/). Running=false means
	// disabled in config or the UDP port could not be bound; either way every
	// device falls back to the cloud API.
	LANEnabled    bool  `json:"lanEnabled"`    // config intent
	LANRunning    bool  `json:"lanRunning"`    // actually bound + scanning
	LANDiscovered int   `json:"lanDiscovered"` // devices with a live LAN route
	LANLastScan   int64 `json:"lanLastScan,omitempty"`
	// Quiet-hours diagnostics — the app's OWN clock and whether the window is
	// active right now. Surfaced because a container/host timezone mismatch is
	// otherwise invisible until an alert fires at 5am.
	AppTime        string `json:"appTime"`        // "15:04" as the app sees it
	AppTimezone    string `json:"appTimezone"`    // zone the window is evaluated in
	QuietHoursNow  bool   `json:"quietHoursNow"`  // suppressing right now?
	QuietHoursDesc string `json:"quietHoursDesc"` // human summary
}

// HandleSettingsGet handles GET /api/v1/settings
func HandleSettingsGet(cfgManager *config.Manager, client *govee.Client, engine *effects.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := cfgManager.GetConfig()
		used, limit := client.CallsToday()
		view := settingsView{
			WebServerPort:         cfg.WebServerPort,
			WebhookPort:           cfg.WebhookPort,
			GoveeAPIKeySet:        cfg.GoveeAPIKey != "",
			DisableAuthentication: cfg.AuthDisabled(),
			QuietHours:            cfg.QuietHours,
			GoveeCallsToday:       used,
			GoveeCallsLimit:       limit,
		}
		if cfg.Logging != nil {
			view.LogLevel = cfg.Logging.Level
		}
		// Quiet-hours diagnostics: report the app's own view of the clock.
		qh := effects.QuietHours{}
		if cfg.QuietHours != nil {
			qh = effects.QuietHours{
				Enabled:  cfg.QuietHours.Enabled,
				Start:    cfg.QuietHours.Start,
				End:      cfg.QuietHours.End,
				Timezone: cfg.QuietHours.Timezone,
			}
		}
		now := time.Now()
		loc, _ := qh.Location()
		view.AppTime = now.In(loc).Format("15:04")
		view.AppTimezone = loc.String()
		view.QuietHoursNow = engine.InQuietHours()
		view.QuietHoursDesc = qh.Describe(now)

		view.LANEnabled = cfg.LANEnabled()
		view.LANRunning = client.LANEnabled()
		view.LANDiscovered = len(client.LANRoutes())
		if t := client.LANLastScan(); !t.IsZero() {
			view.LANLastScan = t.Unix()
		}
		v1types.RespondOK(w, view, nil)
	}
}

// settingsUpdate carries the writable settings. Pointer fields: nil = leave
// unchanged.
type settingsUpdate struct {
	GoveeAPIKey           *string                  `json:"goveeApiKey,omitempty"`
	DisableAuthentication *bool                    `json:"disableAuthentication,omitempty"`
	QuietHours            *config.QuietHoursConfig `json:"quietHours,omitempty"`
	LogLevel              *string                  `json:"logLevel,omitempty"`
}

// HandleSettingsUpdate handles PUT /api/v1/settings. Ports are file-only
// (restart required) so they are deliberately not writable here.
func HandleSettingsUpdate(cfgManager *config.Manager, engine *effects.Engine) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		var req settingsUpdate
		if err := decodeBody(r, &req); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
			return
		}

		clone := cloneConfig(cfgManager.GetConfig())

		if req.GoveeAPIKey != nil && *req.GoveeAPIKey != "" {
			encrypted, err := security.Encrypt(*req.GoveeAPIKey)
			if err != nil {
				log.Error("Encrypt Govee API key failed: %v", err)
				v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to encrypt API key")
				return
			}
			clone.GoveeAPIKey = encrypted
		}
		if req.DisableAuthentication != nil {
			clone.DisableAuthentication = req.DisableAuthentication
		}
		if req.QuietHours != nil {
			// Preserve a hand-edited timezone: the Settings form does not
			// expose it, so an empty value from the UI must not wipe it.
			if req.QuietHours.Timezone == "" && clone.QuietHours != nil {
				req.QuietHours.Timezone = clone.QuietHours.Timezone
			}
			clone.QuietHours = req.QuietHours
		}
		if req.LogLevel != nil {
			if clone.Logging == nil {
				clone.Logging = &config.LoggingConfig{}
			}
			clone.Logging.Level = *req.LogLevel
			logger.SetGlobalLevel(*req.LogLevel)
		}

		if err := cfgManager.Save(clone); err != nil {
			log.Error("Save config failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to save configuration")
			return
		}

		// Hot-apply quiet hours to the running engine.
		if clone.QuietHours != nil {
			engine.SetQuietHours(effects.QuietHours{
				Enabled:  clone.QuietHours.Enabled,
				Start:    clone.QuietHours.Start,
				End:      clone.QuietHours.End,
				Timezone: clone.QuietHours.Timezone,
			})
		} else {
			engine.SetQuietHours(effects.QuietHours{})
		}

		v1types.RespondOK(w, map[string]string{"message": "Settings saved"}, nil)
	}
}

// HandleStatus handles GET /api/v1/status (footer version + basics)
func HandleStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v1types.RespondOK(w, map[string]interface{}{
			"version":    version.Version,
			"buildDate":  version.BuildDate,
			"commit":     version.Commit,
			"uptime":     version.Uptime().Round(1e9).String(),
			"goVersion":  runtime.Version(),
			"goroutines": runtime.NumGoroutine(),
		}, nil)
	}
}

// HandleHealth handles GET /health (Docker healthcheck)
func HandleHealth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
}
