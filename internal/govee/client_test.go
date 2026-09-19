/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package govee

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testKey() (string, error) { return "test-api-key", nil }

func newTestClient(handler http.HandlerFunc) (*Client, *httptest.Server) {
	srv := httptest.NewServer(handler)
	return NewClient(testKey, srv.URL), srv
}

func TestListDevices_FetchesAndCaches(t *testing.T) {
	var hits int32
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/user/devices" {
			t.Errorf("path = %s, want /user/devices", r.URL.Path)
		}
		if r.Header.Get("Govee-API-Key") != "test-api-key" {
			t.Errorf("missing Govee-API-Key header")
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200, "message": "success",
			"data": []map[string]interface{}{
				{"sku": "H6022", "device": "AA:BB", "deviceName": "Table Lamp", "type": "devices.types.light"},
			},
		})
	})
	defer srv.Close()

	devices, err := client.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceName != "Table Lamp" {
		t.Fatalf("devices = %+v", devices)
	}

	// Second call hits the cache — no extra request.
	if _, err := client.ListDevices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("upstream hits = %d, want 1 (cache)", hits)
	}

	// RefreshDevices forces a re-fetch.
	if _, err := client.RefreshDevices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("upstream hits = %d, want 2 (refresh)", hits)
	}
}

func TestControl_SendsCapability(t *testing.T) {
	var got controlRequest
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device/control" {
			t.Errorf("path = %s, want /device/control", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "message": "success"})
	})
	defer srv.Close()

	if err := client.Color(context.Background(), "H6022", "AA:BB", 0x00FF00); err != nil {
		t.Fatalf("Color: %v", err)
	}
	if got.Payload.SKU != "H6022" || got.Payload.Device != "AA:BB" {
		t.Errorf("payload = %+v", got.Payload)
	}
	if got.Payload.Capability.Type != CapColor || got.Payload.Capability.Instance != InstColorRgb {
		t.Errorf("capability = %+v", got.Payload.Capability)
	}
	if got.RequestID == "" {
		t.Error("requestId missing")
	}
}

func TestControl_GoveeErrorCodeSurfaces(t *testing.T) {
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 429, "message": "too many requests"})
	})
	defer srv.Close()

	err := client.Power(context.Background(), "H1", "AA", true)
	if err == nil {
		t.Fatal("expected error for code 429")
	}
}

func TestDailyBudget_Exhaustion(t *testing.T) {
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "message": "success"})
	})
	defer srv.Close()

	// Pretend the day's budget is already spent.
	client.mu.Lock()
	client.rollBudgetDayLocked()
	client.callsToday = dailyCallLimit
	client.mu.Unlock()

	if err := client.Power(context.Background(), "H1", "AA", true); err == nil {
		t.Fatal("expected budget-exhausted error")
	}

	used, limit := client.CallsToday()
	if used != dailyCallLimit || limit != dailyCallLimit {
		t.Errorf("CallsToday = %d/%d, want %d/%d", used, limit, dailyCallLimit, dailyCallLimit)
	}
}

func TestGenerateRequestID_UUIDShape(t *testing.T) {
	id := GenerateRequestID()
	if len(id) != 36 {
		t.Fatalf("length = %d, want 36 (%s)", len(id), id)
	}
	if id == GenerateRequestID() {
		t.Fatal("consecutive request IDs identical")
	}
}

// Govee signals backend failures with HTTP 200 + an in-body error code and
// `data:{}` — an OBJECT where an array belongs. Decoding the typed payload
// first turned that into "cannot unmarshal object into []govee.Device", which
// blamed our parser instead of naming Govee's outage. These bodies are
// VERBATIM from a real Govee cloud outage on 2026-09-19.
func TestCheckGoveeEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "outage: ConnectTimeoutException",
			body:    `{"code":400,"message":"request error: ConnectTimeoutException","data":{}}`,
			wantErr: "govee API error 400: request error: ConnectTimeoutException",
		},
		{
			name:    "outage: ChannelException",
			body:    `{"code":400,"message":"request error: ChannelException","data":{}}`,
			wantErr: "govee API error 400: request error: ChannelException",
		},
		{
			// /device/scenes spells it "msg", not "message".
			name:    "msg field instead of message",
			body:    `{"code":401,"msg":"invalid api key"}`,
			wantErr: "govee API error 401: invalid api key",
		},
		{name: "success passes", body: `{"code":200,"message":"success","data":[]}`},
		{name: "no code field passes", body: `{"foo":"bar"}`},
		{name: "non-JSON passes (typed decode reports it)", body: `<html>502</html>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGoveeEnvelope([]byte(tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("got error %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("got nil, want %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("got %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// End to end through do(): an outage body must surface Govee's message, NOT a
// Go type error about []govee.Device.
func TestListDevices_OutageSurfacesGoveeError(t *testing.T) {
	client, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // Govee answers 200 even when broken
		w.Write([]byte(`{"code":400,"message":"request error: ChannelException","data":{}}`))
	})
	defer srv.Close()

	_, err := client.RefreshDevices(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "unmarshal") || strings.Contains(err.Error(), "parse govee response") {
		t.Errorf("error blames our parser instead of Govee: %v", err)
	}
	if !strings.Contains(err.Error(), "ChannelException") {
		t.Errorf("error does not carry Govee's message: %v", err)
	}
}
