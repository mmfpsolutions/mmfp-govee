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
}

// HandleSettingsGet handles GET /api/v1/settings
func HandleSettingsGet(cfgManager *config.Manager, client *govee.Client) http.HandlerFunc {
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
				Enabled: clone.QuietHours.Enabled,
				Start:   clone.QuietHours.Start,
				End:     clone.QuietHours.End,
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
