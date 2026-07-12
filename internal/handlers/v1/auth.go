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

	"github.com/mmfpsolutions/mmfp-govee/internal/auth"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
)

// LoginRequest represents the login request body
type LoginRequest struct {
	Username       string `json:"username"`
	HashedPassword string `json:"hashedPassword"`
}

// HandleAuthLogin handles POST /api/v1/auth/login
func HandleAuthLogin(cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		var req LoginRequest
		if err := decodeBody(r, &req); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
			return
		}

		accessData, err := auth.LoadAccessCredentials(cfgManager.GetConfigDir())
		if err != nil {
			log.Error("Error reading access.json: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Server configuration error")
			return
		}

		hashedPassword, exists := accessData[req.Username]
		if !exists || hashedPassword != req.HashedPassword {
			v1types.RespondErrorMsg(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid username or password")
			return
		}

		jwtService := auth.GetJWTService()
		if jwtService == nil {
			log.Error("JWT service not initialized (missing jsonWebTokenKey.json)")
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Server configuration error")
			return
		}
		token, err := jwtService.CreateToken(req.Username)
		if err != nil {
			log.Error("Error creating JWT: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to create session")
			return
		}

		maxAge := cfgManager.GetConfig().CookieMaxAge
		if maxAge == 0 {
			maxAge = 3600
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "sessionToken",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			MaxAge:   maxAge,
			SameSite: http.SameSiteStrictMode,
		})

		v1types.RespondOK(w, map[string]string{"message": "Login successful"}, nil)
	}
}

// HandleAuthLogout handles POST /api/v1/auth/logout
func HandleAuthLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "sessionToken",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})

		v1types.RespondOK(w, map[string]string{"message": "Logout successful"}, nil)
	}
}
