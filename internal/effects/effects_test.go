/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package effects

import (
	"strings"
	"testing"
)

func TestValidate_FlashDefaults(t *testing.T) {
	p := Params{ColorA: 0xFFB000, ColorB: 0x00FF00}
	if err := Validate(EffectFlash, &p); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if p.Cycles != 5 {
		t.Errorf("Cycles default = %d, want 5", p.Cycles)
	}
	if p.DelayMs != 700 {
		t.Errorf("DelayMs default = %d, want 700", p.DelayMs)
	}
	if p.Brightness != 100 {
		t.Errorf("Brightness default = %d, want 100", p.Brightness)
	}
	if p.EndState != EndHoldB {
		t.Errorf("EndState default = %q, want %q", p.EndState, EndHoldB)
	}
}

func TestValidate_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		effect string
		params Params
		want   string
	}{
		{"unknown effect", "disco", Params{}, "unknown effect"},
		{"bad color", EffectFlash, Params{ColorA: 0x1FFFFFF}, "colorA"},
		{"negative color", EffectSolid, Params{ColorA: -1}, "colorA"},
		{"too many cycles", EffectFlash, Params{Cycles: 21}, "cycles"},
		{"delay too small", EffectFlash, Params{DelayMs: 50}, "delayMs"},
		{"bad end state", EffectFlash, Params{EndState: "sideways"}, "endState"},
		{"bad brightness", EffectOn, Params{Brightness: 101}, "brightness"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.effect, &tt.params)
			if err == nil {
				t.Fatalf("Validate accepted invalid input")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidate_OffNeedsNoParams(t *testing.T) {
	p := Params{}
	if err := Validate(EffectOff, &p); err != nil {
		t.Fatalf("Validate(off): %v", err)
	}
}
