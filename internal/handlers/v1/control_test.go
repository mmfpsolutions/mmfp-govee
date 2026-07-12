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
	"encoding/json"
	"testing"

	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
)

func capWithParams(t *testing.T, params string) govee.Capability {
	t.Helper()
	return govee.Capability{
		Type:       "devices.capabilities.test",
		Instance:   "testInstance",
		Parameters: json.RawMessage(params),
	}
}

func TestValidateControlValue(t *testing.T) {
	intCap := capWithParams(t, `{"dataType":"INTEGER","range":{"min":1,"max":100,"precision":1}}`)
	enumCap := capWithParams(t, `{"dataType":"ENUM","options":[{"name":"on","value":1},{"name":"off","value":0}]}`)
	emptyEnumCap := capWithParams(t, `{"dataType":"ENUM","options":[]}`)
	structCap := capWithParams(t, `{"dataType":"STRUCT","fields":[
		{"fieldName":"musicMode","dataType":"ENUM","options":[{"name":"Rhythm","value":3}],"required":true},
		{"fieldName":"sensitivity","dataType":"INTEGER","range":{"min":0,"max":100},"required":true},
		{"fieldName":"autoColor","dataType":"ENUM","options":[{"name":"on","value":1},{"name":"off","value":0}],"required":false}]}`)

	tests := []struct {
		name    string
		cap     govee.Capability
		value   string
		wantErr bool
	}{
		{"integer in range", intCap, `50`, false},
		{"integer below range", intCap, `0`, true},
		{"integer above range", intCap, `101`, true},
		{"integer wrong type", intCap, `"fifty"`, true},
		{"enum member", enumCap, `1`, false},
		{"enum non-member", enumCap, `7`, true},
		{"empty enum passes through (dynamic scenes)", emptyEnumCap, `{"paramId":1,"id":2}`, false},
		{"struct valid", structCap, `{"musicMode":3,"sensitivity":80}`, false},
		{"struct missing required", structCap, `{"musicMode":3}`, true},
		{"struct field out of range", structCap, `{"musicMode":3,"sensitivity":150}`, true},
		{"struct field enum non-member", structCap, `{"musicMode":9,"sensitivity":50}`, true},
		{"struct optional field valid", structCap, `{"musicMode":3,"sensitivity":50,"autoColor":1}`, false},
		{"struct not an object", structCap, `42`, true},
		{"no parameters passes", govee.Capability{Type: "t", Instance: "i"}, `1`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateControlValue(tt.cap, json.RawMessage(tt.value))
			if (err != nil) != tt.wantErr {
				t.Errorf("validateControlValue() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFindCapability(t *testing.T) {
	devices := []govee.Device{{
		SKU: "H607C", Device: "AA:BB",
		Capabilities: []govee.Capability{
			{Type: "devices.capabilities.on_off", Instance: "powerSwitch"},
		},
	}}

	if _, ok := findCapability(devices, "AA:BB", "devices.capabilities.on_off", "powerSwitch"); !ok {
		t.Error("declared capability not found")
	}
	if _, ok := findCapability(devices, "AA:BB", "devices.capabilities.range", "brightness"); ok {
		t.Error("undeclared capability reported as found")
	}
	if _, ok := findCapability(devices, "CC:DD", "devices.capabilities.on_off", "powerSwitch"); ok {
		t.Error("unknown device reported as found")
	}
}
