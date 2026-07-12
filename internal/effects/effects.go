/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// Package effects executes canned light effects against Govee devices.
//
// Execution rules (locked in design-documents/mmfp-govee-plan-0.0.1.md §3.1):
//  1. Async — callers enqueue and return; nothing runs inline in a webhook
//     request.
//  2. Per-device serialization — one effect at a time per Govee device ID so
//     concurrent effects can't interleave color commands. Different devices
//     run in parallel.
//  3. Per-mapping cooldown — a webhook storm collapses to one effect.
//  4. Quiet hours — inside the configured window effects are skipped (still
//     logged to Activity).
package effects

import (
	"fmt"
	"strings"
)

// Effect names.
const (
	EffectFlash = "flash"
	EffectSolid = "solid"
	EffectOn    = "on"
	EffectOff   = "off"
)

// EffectNames lists the valid registry entries (for validation and the UI).
var EffectNames = []string{EffectFlash, EffectSolid, EffectOn, EffectOff}

// End states for flash.
const (
	EndHoldA = "holdA"
	EndHoldB = "holdB"
	EndOff   = "off"
)

// Params mirrors config.EffectParams without importing config (the engine is
// config-blind; the hooks/API layer projects one into the other).
type Params struct {
	ColorA     int
	ColorB     int
	Cycles     int
	DelayMs    int
	Brightness int
	EndState   string
	Restore    bool
}

// Validate checks an (effect, params) pair and fills defaults in place.
func Validate(effect string, p *Params) error {
	switch effect {
	case EffectFlash:
		if p.Cycles <= 0 {
			p.Cycles = 5
		}
		if p.Cycles > 20 {
			return fmt.Errorf("cycles must be 1-20")
		}
		if p.DelayMs <= 0 {
			p.DelayMs = 700
		}
		if p.DelayMs < 100 || p.DelayMs > 5000 {
			return fmt.Errorf("delayMs must be 100-5000")
		}
		if p.EndState == "" {
			p.EndState = EndHoldB
		}
		switch p.EndState {
		case EndHoldA, EndHoldB, EndOff:
		default:
			return fmt.Errorf("endState must be holdA, holdB, or off")
		}
		if err := validColor(p.ColorA); err != nil {
			return fmt.Errorf("colorA: %w", err)
		}
		if err := validColor(p.ColorB); err != nil {
			return fmt.Errorf("colorB: %w", err)
		}
	case EffectSolid:
		if err := validColor(p.ColorA); err != nil {
			return fmt.Errorf("colorA: %w", err)
		}
	case EffectOn, EffectOff:
		// no required params
	default:
		return fmt.Errorf("unknown effect %q (valid: %s)", effect, strings.Join(EffectNames, ", "))
	}

	if p.Brightness == 0 {
		p.Brightness = 100
	}
	if p.Brightness < 1 || p.Brightness > 100 {
		return fmt.Errorf("brightness must be 1-100")
	}
	return nil
}

func validColor(rgb int) error {
	if rgb < 0 || rgb > 0xFFFFFF {
		return fmt.Errorf("must be a 24-bit RGB integer (0-16777215)")
	}
	return nil
}
