/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package router

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	"github.com/mmfpsolutions/mmfp-govee/internal/middleware"
	"github.com/mmfpsolutions/mmfp-govee/internal/web"
)

// setupRedirectMiddleware redirects all non-setup routes to /settings when no
// Govee API key is configured yet (first-time setup).
func setupRedirectMiddleware(cfgManager *config.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfgManager.SetupRequired() {
				path := r.URL.Path
				if path == "/settings" || path == "/login" ||
					strings.HasPrefix(path, "/static/") ||
					strings.HasPrefix(path, "/api/") ||
					path == "/health" {
					next.ServeHTTP(w, r)
					return
				}
				http.Redirect(w, r, "/settings", http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SetupRouter configures all routes for the web application (:3008).
// The webhook listener (:8787) has its own router in internal/hooks.
func SetupRouter(cfgManager *config.Manager, client *govee.Client, engine *effects.Engine, act *activity.Log) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.LoggingMiddleware)
	r.Use(setupRedirectMiddleware(cfgManager))

	log := logger.New(logger.ModuleWeb)

	// Register web UI routes (templates + static assets via go:embed)
	if err := web.RegisterRoutes(r, cfgManager); err != nil {
		log.Error("Failed to register web UI routes: %v", err)
	}

	// Register API v1 routes
	registerV1Routes(r, cfgManager, client, engine, act)

	return r
}
