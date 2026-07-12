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
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	"github.com/mmfpsolutions/mmfp-govee/internal/security"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
)

// tokenView is a token with the secret masked (list view).
type tokenView struct {
	Name   string `json:"name"`
	Masked string `json:"masked"` // last 4 chars only
	InUse  int    `json:"inUse"`  // mappings referencing this token by name
}

var tokenNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

func maskSecret(encrypted string) string {
	secret, err := security.DecryptIfEncrypted(encrypted)
	if err != nil || len(secret) < 4 {
		return "••••"
	}
	return "••••" + secret[len(secret)-4:]
}

// HandleTokensList handles GET /api/v1/tokens (secrets masked)
func HandleTokensList(cfgManager *config.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := cfgManager.GetConfig()
		views := make([]tokenView, 0, len(cfg.Tokens))
		for _, t := range cfg.Tokens {
			inUse := 0
			for _, m := range cfg.Mappings {
				if m.Token == t.Name {
					inUse++
				}
			}
			views = append(views, tokenView{Name: t.Name, Masked: maskSecret(t.Token), InUse: inUse})
		}
		v1types.RespondOK(w, map[string]interface{}{"tokens": views}, nil)
	}
}

// HandleTokenCreate handles POST /api/v1/tokens. The generated secret is
// returned ONCE in this response; afterwards it is only available masked.
func HandleTokenCreate(cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeBody(r, &req); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
			return
		}
		if !tokenNameRe.MatchString(req.Name) {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_NAME", "Name must be 1-40 chars: letters, digits, dot, dash, underscore")
			return
		}
		if req.Name == "*" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_NAME", "* is reserved")
			return
		}

		cfg := cfgManager.GetConfig()
		for _, t := range cfg.Tokens {
			if t.Name == req.Name {
				v1types.RespondErrorMsg(w, http.StatusConflict, "DUPLICATE", "A token with that name already exists")
				return
			}
		}

		secret := config.GenerateSecret()
		encrypted, err := security.Encrypt(secret)
		if err != nil {
			log.Error("Encrypt token failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to encrypt token")
			return
		}

		clone := cloneConfig(cfg)
		clone.Tokens = append(clone.Tokens, config.Token{Name: req.Name, Token: encrypted})
		if err := cfgManager.Save(clone); err != nil {
			log.Error("Save config failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to save configuration")
			return
		}

		v1types.RespondCreated(w, map[string]string{
			"name":   req.Name,
			"secret": secret, // shown once
		})
	}
}

// HandleTokenDelete handles DELETE /api/v1/tokens/{name}
func HandleTokenDelete(cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")

		cfg := cfgManager.GetConfig()
		for _, m := range cfg.Mappings {
			if m.Token == name {
				v1types.RespondErrorMsg(w, http.StatusConflict, "IN_USE",
					"Token is referenced by mapping \""+m.Name+"\" — repoint or delete that mapping first")
				return
			}
		}

		clone := cloneConfig(cfg)
		kept := clone.Tokens[:0]
		found := false
		for _, t := range clone.Tokens {
			if t.Name == name {
				found = true
				continue
			}
			kept = append(kept, t)
		}
		if !found {
			v1types.RespondErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "Token not found")
			return
		}
		clone.Tokens = kept
		if err := cfgManager.Save(clone); err != nil {
			log.Error("Save config failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to save configuration")
			return
		}
		v1types.RespondOK(w, map[string]string{"message": "Token revoked"}, nil)
	}
}
