/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// Package hooks is the webhook listener (:8787). It accepts generic-webhook
// POSTs from GSS and GSSM, authenticates them by per-caller token, routes on
// the event type found in the BODY (GSSM `event_type`, GSS `type`), and hands
// matched mappings to the effects engine. It responds immediately — effects
// never run inline in the request (senders time out at 10s).
package hooks

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/dispatch"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	"github.com/mmfpsolutions/mmfp-govee/internal/security"
)

const (
	// TokenHeader is the preferred token transport — GSS and GSSM generic
	// webhooks both support custom headers, and headers keep secrets out of
	// access logs. ?token= works too for curl convenience.
	TokenHeader = "X-MMFP-Token"

	maxBodyBytes = 64 * 1024
)

// incomingPayload covers both sender shapes:
//   - GSSM FormatForGenericWebhook: event_type, entity, message, severity, ...
//   - GSS GenericFormatter: subject, message, type, timestamp
type incomingPayload struct {
	EventType string `json:"event_type"` // GSSM
	Type      string `json:"type"`       // GSS
	Entity    string `json:"entity"`     // GSSM only
}

// Server is the webhook listener.
type Server struct {
	cfgManager *config.Manager
	engine     *effects.Engine
	act        *activity.Log
	log        *logger.Logger
	httpServer *http.Server
}

// NewServer creates the webhook listener (not yet started).
func NewServer(cfgManager *config.Manager, engine *effects.Engine, act *activity.Log) *Server {
	return &Server{
		cfgManager: cfgManager,
		engine:     engine,
		act:        act,
		log:        logger.New(logger.ModuleHooks),
	}
}

// Router builds the chi router (exported for tests via httptest).
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	r.Post("/hook", s.handleHook)
	r.Get("/hook/{event}", s.handleHook)
	r.Post("/hook/{event}", s.handleHook)
	return r
}

// Start runs the listener on the configured webhook port.
func (s *Server) Start(port int) {
	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      s.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		s.log.Info("Webhook listener running on :%d", port)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("Webhook listener failed: %v", err)
		}
	}()
}

// Stop gracefully shuts the listener down.
func (s *Server) Stop(ctx context.Context) {
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			s.log.Warn("Webhook listener shutdown: %v", err)
		}
	}
}

// authenticate resolves the presented token to a configured token name.
// Constant-time comparison against every configured token — no early exit on
// name, no timing signal on which token matched.
func (s *Server) authenticate(r *http.Request) (string, bool) {
	presented := r.Header.Get(TokenHeader)
	if presented == "" {
		presented = r.URL.Query().Get("token")
	}
	if presented == "" {
		return "", false
	}

	cfg := s.cfgManager.GetConfig()
	matched := ""
	for _, t := range cfg.Tokens {
		secret, err := security.DecryptIfEncrypted(t.Token)
		if err != nil {
			s.log.Warn("Token %q could not be decrypted: %v", t.Name, err)
			continue
		}
		if len(secret) == len(presented) &&
			subtle.ConstantTimeCompare([]byte(secret), []byte(presented)) == 1 {
			matched = t.Name
		}
	}
	return matched, matched != ""
}

// handleHook is the single webhook entrypoint (body-routed, with an optional
// forced event in the path).
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	tokenName, ok := s.authenticate(r)
	if !ok {
		s.log.WarnWithRequest(r, "Webhook rejected: bad or missing token")
		s.act.Add(activity.Record{Source: "unknown", Event: "-", Result: "rejected"})
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	event := chi.URLParam(r, "event") // forced-event mode
	entity := ""
	source := "unknown"

	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if len(body) > 0 {
			var p incomingPayload
			if err := json.Unmarshal(body, &p); err == nil {
				switch {
				case p.EventType != "":
					source = "gssm"
					if event == "" {
						event = p.EventType
					}
					entity = p.Entity
				case p.Type != "":
					source = "gss"
					if event == "" {
						event = p.Type
					}
				}
			}
		}
	}

	if event == "" {
		s.act.Add(activity.Record{TokenName: tokenName, Source: source, Event: "-", Result: "rejected"})
		http.Error(w, "no event type: body must carry event_type (GSSM) or type (GSS), or use /hook/{event}", http.StatusBadRequest)
		return
	}

	s.log.InfoWithRequest(r, "Webhook: token=%s source=%s event=%s entity=%s", tokenName, source, event, entity)

	matched := s.dispatch(tokenName, source, event, entity)

	// Test buttons: GSSM sends event_type "test"; GSS's Test uses "startup".
	// A 200 keeps the sender's Test flow green whether or not a mapping is
	// wired to the test event.
	if matched == 0 {
		s.act.Add(activity.Record{TokenName: tokenName, Source: source, Event: event, Entity: entity, Result: "no match"})
		if event == "test" || event == "startup" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok (no mapping)\n"))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("no mapping\n"))
		return
	}

	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, "queued %d mapping(s)\n", matched)
}

// dispatch enqueues every enabled mapping matching (token, event, entity).
// Returns how many mappings matched (before cooldown/quiet-hours checks).
func (s *Server) dispatch(tokenName, source, event, entity string) int {
	cfg := s.cfgManager.GetConfig()
	matched := 0

	for _, m := range cfg.Mappings {
		if !m.Enabled {
			continue
		}
		if m.Token != "*" && m.Token != tokenName {
			continue
		}
		if !containsEvent(m.Events, event) {
			continue
		}
		if m.EntityFilter != "" && !strings.Contains(strings.ToLower(entity), strings.ToLower(m.EntityFilter)) {
			continue
		}
		matched++

		job := dispatch.JobFromMapping(cfg, m, dispatch.Trigger{
			TokenName: tokenName,
			Source:    source,
			Event:     event,
			Entity:    entity,
		})
		if err := effects.Validate(job.Effect, &job.Params); err != nil {
			s.log.Error("Mapping %s (%s) has invalid effect params: %v", m.ID, m.Name, err)
			s.act.Add(activity.Record{TokenName: tokenName, Source: source, Event: event, Entity: entity,
				MappingID: m.ID, MappingName: m.Name, Result: "effect failed: " + err.Error()})
			continue
		}

		s.engine.Enqueue(job, time.Duration(m.CooldownSeconds)*time.Second)
	}
	return matched
}

func containsEvent(events []string, event string) bool {
	for _, e := range events {
		if strings.EqualFold(e, event) {
			return true
		}
	}
	return false
}
