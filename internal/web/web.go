/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	"github.com/mmfpsolutions/mmfp-govee/internal/middleware"
)

// Template functions available in all templates
var templateFuncs = template.FuncMap{
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
}

//go:embed templates/* static/js/* static/css/* static/img/*
var embedFS embed.FS

// PageData represents data passed to all templates
type PageData struct {
	Title      string
	ActivePage string
	Username   string
	DeviceID   string
}

// RegisterRoutes registers all web UI routes (pages + static assets)
func RegisterRoutes(r chi.Router, cfgManager *config.Manager) error {
	log := logger.New(logger.ModuleWeb)

	// Parse templates
	tmpl, err := template.New("").Funcs(templateFuncs).ParseFS(embedFS,
		"templates/layout/*.html",
		"templates/pages/*.html",
	)
	if err != nil {
		return err
	}
	log.Info("Loaded %d web templates", len(tmpl.Templates()))

	// Serve static files from embedded FS
	staticFS, err := fs.Sub(embedFS, "static")
	if err != nil {
		return err
	}
	fileServer := http.FileServer(http.FS(staticFS))
	r.Handle("/static/*", http.StripPrefix("/static/", fileServer))

	// Page renderer (extracts username from auth context when available)
	servePage := func(page string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			data := PageData{
				Title:      cfgManager.GetConfig().Title,
				ActivePage: page,
			}
			if user := middleware.GetUserFromContext(r); user != nil {
				data.Username = user.Username
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
				log.Error("Template render error (%s): %v", page, err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}
	}

	// Public routes (no auth)
	r.Get("/login", servePage("login"))

	// Protected page routes (auth middleware)
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(cfgManager, true))

		r.Get("/", servePage("devices"))
		r.Get("/devices", servePage("devices"))
		r.Get("/devices/{device}/details", func(w http.ResponseWriter, r *http.Request) {
			// chi returns the still-escaped segment; the page re-encodes for
			// API calls, so hand it the decoded ID.
			device, err := url.PathUnescape(chi.URLParam(r, "device"))
			if err != nil || device == "" {
				http.NotFound(w, r)
				return
			}
			data := PageData{
				Title:      cfgManager.GetConfig().Title,
				ActivePage: "device-detail",
				DeviceID:   device,
			}
			if user := middleware.GetUserFromContext(r); user != nil {
				data.Username = user.Username
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
				log.Error("Template render error (device-detail): %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		})
		r.Get("/mappings", servePage("mappings"))
		r.Get("/tokens", servePage("tokens"))
		r.Get("/activity", servePage("activity"))
		r.Get("/settings", servePage("settings"))
	})

	return nil
}
