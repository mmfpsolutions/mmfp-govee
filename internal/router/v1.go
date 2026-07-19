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
	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	v1 "github.com/mmfpsolutions/mmfp-govee/internal/handlers/v1"
	"github.com/mmfpsolutions/mmfp-govee/internal/middleware"
)

// registerV1Routes registers all /api/v1 routes.
func registerV1Routes(r chi.Router, cfgManager *config.Manager, client *govee.Client, engine *effects.Engine, act *activity.Log) {
	// Public
	r.Get("/health", v1.HandleHealth())
	r.Post("/api/v1/auth/login", v1.HandleAuthLogin(cfgManager))
	r.Post("/api/v1/auth/logout", v1.HandleAuthLogout())

	// Protected (JWT cookie when auth is enabled; open when disabled — the default)
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(cfgManager, true))

		r.Get("/api/v1/status", v1.HandleStatus())

		r.Get("/api/v1/devices", v1.HandleDevicesList(client, cfgManager))
		r.Get("/api/v1/devices/status", v1.HandleDevicesStatus(client))
		r.Post("/api/v1/devices/refresh", v1.HandleDevicesRefresh(client, cfgManager))
		r.Post("/api/v1/devices/{device}/test", v1.HandleDeviceTest(engine, cfgManager))
		r.Get("/api/v1/devices/{device}/scenes", v1.HandleDeviceScenes(client))
		r.Put("/api/v1/devices/{device}/scene", v1.HandleDeviceSceneSet(cfgManager))
		r.Get("/api/v1/devices/{device}/state", v1.HandleDeviceState(client))
		r.Post("/api/v1/devices/{device}/control", v1.HandleDeviceControl(client, engine))

		r.Get("/api/v1/events", v1.HandleEventCatalog())

		r.Get("/api/v1/mappings", v1.HandleMappingsList(cfgManager))
		r.Post("/api/v1/mappings", v1.HandleMappingCreate(cfgManager))
		r.Put("/api/v1/mappings/{id}", v1.HandleMappingUpdate(cfgManager))
		r.Delete("/api/v1/mappings/{id}", v1.HandleMappingDelete(cfgManager))
		r.Post("/api/v1/mappings/{id}/test", v1.HandleMappingTest(cfgManager, engine))

		r.Get("/api/v1/tokens", v1.HandleTokensList(cfgManager))
		r.Post("/api/v1/tokens", v1.HandleTokenCreate(cfgManager))
		r.Delete("/api/v1/tokens/{name}", v1.HandleTokenDelete(cfgManager))

		r.Get("/api/v1/settings", v1.HandleSettingsGet(cfgManager, client, engine))
		r.Put("/api/v1/settings", v1.HandleSettingsUpdate(cfgManager, engine))

		r.Post("/api/v1/lan/rescan", v1.HandleLANRescan(client))

		r.Get("/api/v1/activity", v1.HandleActivityList(act))
	})
}
