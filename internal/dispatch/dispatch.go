/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// Package dispatch is the ONE config→engine projection. Every trigger — the
// webhook listener, the mapping Test button, the device Test Blink — builds
// its effects.Job here, so after-scene resolution and device conversion can
// never drift between paths.
package dispatch

import (
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
)

// Trigger is the who/why context stamped onto Activity records.
type Trigger struct {
	TokenName string
	Source    string
	Event     string
	Entity    string
}

// DeviceFor projects a config device ref into an engine device, resolving
// the after-effect scene canonically: mapping-level override first, then the
// device's assigned scene (config.DeviceScenes), then none.
func DeviceFor(cfg *config.Config, ref config.DeviceRef) effects.Device {
	dev := effects.Device{SKU: ref.SKU, Device: ref.Device}
	if s := cfg.AfterSceneFor(ref); s != nil {
		dev.AfterScene = &effects.SceneRef{Instance: s.Instance, Name: s.Name, Value: s.Value}
	}
	return dev
}

// JobFromMapping builds the engine job for one mapping and trigger context.
func JobFromMapping(cfg *config.Config, m config.Mapping, t Trigger) effects.Job {
	job := effects.Job{
		MappingID:   m.ID,
		MappingName: m.Name,
		Effect:      m.Effect,
		Params: effects.Params{
			ColorA:     m.Params.ColorA,
			ColorB:     m.Params.ColorB,
			Cycles:     m.Params.Cycles,
			DelayMs:    m.Params.DelayMs,
			Brightness: m.Params.Brightness,
			EndState:   m.Params.EndState,
			Restore:    m.Params.Restore,
		},
		TokenName: t.TokenName,
		Source:    t.Source,
		Event:     t.Event,
		Entity:    t.Entity,
	}
	for _, d := range m.Devices {
		job.Devices = append(job.Devices, DeviceFor(cfg, d))
	}
	return job
}
