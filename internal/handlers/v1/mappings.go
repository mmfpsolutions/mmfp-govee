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

	"github.com/go-chi/chi/v5"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/dispatch"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
)

// mappingBody is the create/update request shape (Mapping minus ID).
type mappingBody struct {
	Name            string              `json:"name"`
	Token           string              `json:"token"`
	Events          []string            `json:"events"`
	EntityFilter    string              `json:"entityFilter,omitempty"`
	Devices         []config.DeviceRef  `json:"devices"`
	Effect          string              `json:"effect"`
	Params          config.EffectParams `json:"params"`
	CooldownSeconds int                 `json:"cooldownSeconds,omitempty"`
	Enabled         *bool               `json:"enabled,omitempty"`
}

func (b *mappingBody) validate(cfg *config.Config) (string, string) {
	if b.Name == "" {
		return "INVALID_NAME", "Name is required"
	}
	if len(b.Events) == 0 {
		return "INVALID_EVENTS", "At least one event is required"
	}
	if len(b.Devices) == 0 {
		return "INVALID_DEVICES", "At least one device is required"
	}
	for _, d := range b.Devices {
		if d.SKU == "" || d.Device == "" {
			return "INVALID_DEVICES", "Every device needs sku and device"
		}
		if s := d.AfterScene; s != nil {
			switch s.Instance {
			case "lightScene", "diyScene", "snapshot":
			default:
				return "INVALID_SCENE", "afterScene.instance must be lightScene, diyScene, or snapshot"
			}
			if len(s.Value) == 0 {
				return "INVALID_SCENE", "afterScene.value is required (pick a scene from the device's list)"
			}
		}
	}
	if b.Token == "" {
		b.Token = "*"
	}
	if b.Token != "*" {
		found := false
		for _, t := range cfg.Tokens {
			if t.Name == b.Token {
				found = true
				break
			}
		}
		if !found {
			return "INVALID_TOKEN", "Token name not found (use * for any token)"
		}
	}
	p := effects.Params{
		ColorA: b.Params.ColorA, ColorB: b.Params.ColorB, Cycles: b.Params.Cycles,
		DelayMs: b.Params.DelayMs, Brightness: b.Params.Brightness,
		EndState: b.Params.EndState, Restore: b.Params.Restore,
	}
	if err := effects.Validate(b.Effect, &p); err != nil {
		return "INVALID_EFFECT", err.Error()
	}
	// Write the filled defaults back so config.json carries explicit values.
	b.Params = config.EffectParams{
		ColorA: p.ColorA, ColorB: p.ColorB, Cycles: p.Cycles, DelayMs: p.DelayMs,
		Brightness: p.Brightness, EndState: p.EndState, Restore: p.Restore,
	}
	if b.CooldownSeconds < 0 || b.CooldownSeconds > 3600 {
		return "INVALID_COOLDOWN", "cooldownSeconds must be 0-3600"
	}
	return "", ""
}

func (b *mappingBody) toMapping(id string) config.Mapping {
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	cooldown := b.CooldownSeconds
	if cooldown == 0 {
		cooldown = config.DefaultCooldownSecs
	}
	return config.Mapping{
		ID:              id,
		Name:            b.Name,
		Token:           b.Token,
		Events:          b.Events,
		EntityFilter:    b.EntityFilter,
		Devices:         b.Devices,
		Effect:          b.Effect,
		Params:          b.Params,
		CooldownSeconds: cooldown,
		Enabled:         enabled,
	}
}

// cloneConfig deep-copies the parts of Config these handlers mutate so the
// live config (read lock-free by other goroutines) is never edited in place.
func cloneConfig(cfg *config.Config) *config.Config {
	clone := *cfg
	clone.Tokens = append([]config.Token(nil), cfg.Tokens...)
	clone.Mappings = append([]config.Mapping(nil), cfg.Mappings...)
	clone.DeviceScenes = make(map[string]*config.SceneRef, len(cfg.DeviceScenes))
	for k, v := range cfg.DeviceScenes {
		clone.DeviceScenes[k] = v
	}
	return &clone
}

// HandleMappingsList handles GET /api/v1/mappings
func HandleMappingsList(cfgManager *config.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v1types.RespondOK(w, map[string]interface{}{"mappings": cfgManager.GetConfig().Mappings}, nil)
	}
}

// HandleMappingCreate handles POST /api/v1/mappings
func HandleMappingCreate(cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		var body mappingBody
		if err := decodeBody(r, &body); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
			return
		}
		cfg := cfgManager.GetConfig()
		if code, msg := body.validate(cfg); code != "" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, code, msg)
			return
		}

		mapping := body.toMapping(config.GenerateID())
		clone := cloneConfig(cfg)
		clone.Mappings = append(clone.Mappings, mapping)
		if err := cfgManager.Save(clone); err != nil {
			log.Error("Save config failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to save configuration")
			return
		}
		v1types.RespondCreated(w, mapping)
	}
}

// HandleMappingUpdate handles PUT /api/v1/mappings/{id}
func HandleMappingUpdate(cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		var body mappingBody
		if err := decodeBody(r, &body); err != nil {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
			return
		}
		cfg := cfgManager.GetConfig()
		if code, msg := body.validate(cfg); code != "" {
			v1types.RespondErrorMsg(w, http.StatusBadRequest, code, msg)
			return
		}

		clone := cloneConfig(cfg)
		found := false
		for i := range clone.Mappings {
			if clone.Mappings[i].ID == id {
				clone.Mappings[i] = body.toMapping(id)
				found = true
				break
			}
		}
		if !found {
			v1types.RespondErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "Mapping not found")
			return
		}
		if err := cfgManager.Save(clone); err != nil {
			log.Error("Save config failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to save configuration")
			return
		}
		v1types.RespondOK(w, body.toMapping(id), nil)
	}
}

// HandleMappingDelete handles DELETE /api/v1/mappings/{id}
func HandleMappingDelete(cfgManager *config.Manager) http.HandlerFunc {
	log := logger.New(logger.ModuleHandler)

	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		clone := cloneConfig(cfgManager.GetConfig())
		kept := clone.Mappings[:0]
		found := false
		for _, m := range clone.Mappings {
			if m.ID == id {
				found = true
				continue
			}
			kept = append(kept, m)
		}
		if !found {
			v1types.RespondErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "Mapping not found")
			return
		}
		clone.Mappings = kept
		if err := cfgManager.Save(clone); err != nil {
			log.Error("Save config failed: %v", err)
			v1types.RespondErrorMsg(w, http.StatusInternalServerError, "SERVER_ERROR", "Failed to save configuration")
			return
		}
		v1types.RespondOK(w, map[string]string{"message": "Mapping deleted"}, nil)
	}
}

// HandleMappingTest handles POST /api/v1/mappings/{id}/test — runs the
// mapping's real effect now (no cooldown), validating the full chain.
func HandleMappingTest(cfgManager *config.Manager, engine *effects.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		cfg := cfgManager.GetConfig()
		for _, m := range cfg.Mappings {
			if m.ID != id {
				continue
			}
			job := dispatch.JobFromMapping(cfg, m, dispatch.Trigger{
				TokenName: "ui",
				Source:    "ui",
				Event:     "mapping_test",
			})
			if err := effects.Validate(job.Effect, &job.Params); err != nil {
				v1types.RespondErrorMsg(w, http.StatusBadRequest, "INVALID_EFFECT", err.Error())
				return
			}
			result := engine.Enqueue(job, 0)
			v1types.RespondOK(w, map[string]string{"result": result}, nil)
			return
		}
		v1types.RespondErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "Mapping not found")
	}
}

// HandleEventCatalog handles GET /api/v1/events — the known GSS/GSSM event
// types for the mapping editor's picker (free text is still allowed).
func HandleEventCatalog() http.HandlerFunc {
	catalog := map[string][]string{
		"gss": {
			"block_found", "block_matured", "block_orphaned",
			"payment_pending", "payment_complete", "payment_failed",
			"node_offline", "node_online", "node_failover", "node_failback",
			"miner_connect", "miner_disconnect",
			"best_share", "notable_share", "rejected_shares",
			"network_difficulty_below", "network_difficulty_above", "network_difficulty_recovered",
			"startup", "shutdown", "cleanup",
		},
		"gssm": {
			"miner_offline", "miner_online", "miner_failover", "miner_zero_hashrate",
			"miner_temp_high", "miner_temp_normal",
			"miner_fan_emergency", "miner_fan_emergency_off",
			"pool_offline", "pool_online",
			"node_offline", "node_online",
			"system_startup", "system_shutdown", "test",
		},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		v1types.RespondOK(w, catalog, nil)
	}
}
