/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/mmfpsolutions/mmfp-govee/internal/auth"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
)

type contextKey string

const UserContextKey contextKey = "user"

// User represents authenticated user information
type User struct {
	Username string
}

// AuthMiddleware creates a middleware that checks JWT authentication
func AuthMiddleware(cfgManager *config.Manager, requireJWT bool) func(http.Handler) http.Handler {
	log := logger.New(logger.ModuleAuth)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cfg := cfgManager.GetConfig()
			// Skip JWT check if authentication is disabled or not required.
			// MMFP Govee auth is OPT-IN: disableAuthentication defaults true.
			if !requireJWT || cfg.AuthDisabled() {
				next.ServeHTTP(w, r)
				return
			}

			// Get token from cookie
			cookie, err := r.Cookie("sessionToken")
			if err != nil {
				authDeny(w, r)
				return
			}

			// Verify token
			jwtService := auth.GetJWTService()
			if jwtService == nil {
				log.WarnWithRequest(r, "Auth enabled but JWT service not initialized (missing jsonWebTokenKey.json)")
				authDeny(w, r)
				return
			}
			claims, err := jwtService.VerifyToken(cookie.Value)
			if err != nil {
				log.WarnWithRequest(r, "JWT verification failed: %v", err)
				http.SetCookie(w, &http.Cookie{
					Name:     "sessionToken",
					Value:    "",
					Path:     "/",
					HttpOnly: true,
					MaxAge:   -1,
				})
				authDeny(w, r)
				return
			}

			// Add user to context
			user := &User{Username: claims.Username}
			ctx := context.WithValue(r.Context(), UserContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authDeny handles auth failure: 401 JSON for API routes, redirect for pages
func authDeny(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"Authentication required"}]}`))
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// GetUserFromContext retrieves the user from the request context
func GetUserFromContext(r *http.Request) *User {
	user, ok := r.Context().Value(UserContextKey).(*User)
	if !ok {
		return nil
	}
	return user
}

// LoggingMiddleware logs each request
func LoggingMiddleware(next http.Handler) http.Handler {
	log := logger.New(logger.ModuleMiddleware)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip logging for health check endpoint to avoid log clutter
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		// Log the request with client IP
		log.InfoWithRequest(r, "Request: %s %s", r.Method, r.URL.String())

		next.ServeHTTP(w, r)
	})
}
