/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// Package govee is the outbound Govee OpenAPI client: device listing (with an
// in-memory cache), device control, device state reads, and the API-call
// budget. All Govee traffic in the app flows through this one client so the
// daily budget and per-minute throttle see every call.
package govee

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
)

const (
	defaultBaseURL = "https://openapi.api.govee.com/router/api/v1"
	clientTimeout  = 10 * time.Second

	// Govee's published cloud API budget is 10,000 requests/day per key.
	// Refuse new effect work near the ceiling so a webhook storm can't
	// silently burn the key for the rest of the day.
	dailyCallLimit = 9500

	// Minimum spacing between control calls — a coarse token bucket that
	// keeps burst effects under Govee's per-minute limits.
	minCallInterval = 100 * time.Millisecond
)

// Capability type/instance constants (Govee OpenAPI capability model).
const (
	CapOnOff       = "devices.capabilities.on_off"
	CapColor       = "devices.capabilities.color_setting"
	CapRange       = "devices.capabilities.range"
	InstPower      = "powerSwitch"
	InstColorRgb   = "colorRgb"
	InstColorTemp  = "colorTemperatureK"
	InstBrightness = "brightness"

	CapDynamicScene = "devices.capabilities.dynamic_scene"
	InstLightScene  = "lightScene"
)

// Device is one entry from GET /user/devices.
type Device struct {
	SKU          string       `json:"sku"`
	Device       string       `json:"device"`
	DeviceName   string       `json:"deviceName"`
	Type         string       `json:"type"`
	Capabilities []Capability `json:"capabilities"`
}

// Capability describes one controllable aspect of a device.
type Capability struct {
	Type       string          `json:"type"`
	Instance   string          `json:"instance"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

// devicesResponse is the wire shape of GET /user/devices.
type devicesResponse struct {
	Code    int      `json:"code"`
	Message string   `json:"message"`
	Data    []Device `json:"data"`
}

// controlRequest is the wire shape of POST /device/control.
type controlRequest struct {
	RequestID string         `json:"requestId"`
	Payload   controlPayload `json:"payload"`
}

type controlPayload struct {
	SKU        string          `json:"sku"`
	Device     string          `json:"device"`
	Capability capabilityValue `json:"capability"`
}

type capabilityValue struct {
	Type     string      `json:"type"`
	Instance string      `json:"instance"`
	Value    interface{} `json:"value"`
}

// stateRequest is the wire shape of POST /device/state.
type stateRequest struct {
	RequestID string      `json:"requestId"`
	Payload   stateDevice `json:"payload"`
}

type stateDevice struct {
	SKU    string `json:"sku"`
	Device string `json:"device"`
}

// stateResponse carries the current capability values of a device.
type stateResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Payload struct {
		SKU          string `json:"sku"`
		Device       string `json:"device"`
		Capabilities []struct {
			Type     string `json:"type"`
			Instance string `json:"instance"`
			State    struct {
				Value json.RawMessage `json:"value"`
			} `json:"state"`
		} `json:"capabilities"`
	} `json:"payload"`
}

// DeviceState is the restorable snapshot of a device (Phase 3 capture &
// restore). Fields are pointers: nil = the device didn't report that
// capability, so restore skips it.
type DeviceState struct {
	PowerOn    *int // 1 on, 0 off
	Brightness *int // 1-100
	ColorRgb   *int // 24-bit RGB
	// Active scene, when the device reports one. Most Govee devices do NOT
	// expose the running scene through the state endpoint (dynamic_scene is
	// effectively write-only), so this is best-effort: the raw state value is
	// kept verbatim and replayed to /device/control on restore.
	SceneInstance string          // "lightScene" | "diyScene" | "snapshot"
	SceneValue    json.RawMessage // nil = no scene reported
}

// KeyProvider returns the decrypted Govee API key at call time. Reading it
// per-call (instead of caching plaintext in the client) keeps hot-reloaded
// key changes live and plaintext lifetime short.
type KeyProvider func() (string, error)

// Client is the Govee OpenAPI client. One instance per process.
type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     KeyProvider
	log        *logger.Logger

	mu         sync.Mutex
	lastCall   time.Time
	callsToday int
	budgetDay  string // "2006-01-02" the counter belongs to

	cacheMu     sync.RWMutex
	devices     []Device
	devicesFrom time.Time
	scenes      map[string][]Scene // device ID → scene catalog

	// lan is the optional fast path (nil = cloud-only). See transport.go.
	lan *lanService
}

// NewClient creates the Govee client. baseURL "" means production.
func NewClient(apiKey KeyProvider, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: clientTimeout},
		apiKey:     apiKey,
		log:        logger.New(logger.ModuleGovee),
	}
}

// CallsToday returns the API calls consumed since local midnight and the
// budget ceiling (for the Settings page counter).
func (c *Client) CallsToday() (used, limit int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollBudgetDayLocked()
	return c.callsToday, dailyCallLimit
}

func (c *Client) rollBudgetDayLocked() {
	today := time.Now().Format("2006-01-02")
	if c.budgetDay != today {
		c.budgetDay = today
		c.callsToday = 0
	}
}

// spend reserves one API call against the budget and enforces the minimum
// call spacing. Returns an error when the daily ceiling is reached.
func (c *Client) spend() error {
	c.mu.Lock()
	c.rollBudgetDayLocked()
	if c.callsToday >= dailyCallLimit {
		c.mu.Unlock()
		return fmt.Errorf("govee daily API budget exhausted (%d calls)", dailyCallLimit)
	}
	c.callsToday++
	wait := minCallInterval - time.Since(c.lastCall)
	if wait > 0 {
		c.lastCall = c.lastCall.Add(minCallInterval)
	} else {
		c.lastCall = time.Now()
	}
	c.mu.Unlock()

	if wait > 0 {
		time.Sleep(wait)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	if err := c.spend(); err != nil {
		return err
	}

	key, err := c.apiKey()
	if err != nil {
		return fmt.Errorf("govee API key unavailable: %w", err)
	}
	if key == "" {
		return fmt.Errorf("govee API key not configured")
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal govee request: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("create govee request: %w", err)
	}
	req.Header.Set("Govee-API-Key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("govee request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("govee returned status %d: %s", resp.StatusCode, string(respBody))
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("parse govee response: %w", err)
		}
	}
	return nil
}

// ListDevices returns the device list, from cache when available. Call
// RefreshDevices to force a re-fetch (the UI Refresh button).
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	c.cacheMu.RLock()
	if c.devices != nil {
		devices := c.devices
		c.cacheMu.RUnlock()
		return devices, nil
	}
	c.cacheMu.RUnlock()
	return c.RefreshDevices(ctx)
}

// CachedAt returns when the device cache was last filled (zero = never).
func (c *Client) CachedAt() time.Time {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	return c.devicesFrom
}

// RefreshDevices re-fetches the device list from Govee and updates the cache.
func (c *Client) RefreshDevices(ctx context.Context) ([]Device, error) {
	var resp devicesResponse
	if err := c.do(ctx, http.MethodGet, "/user/devices", nil, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 200 {
		return nil, fmt.Errorf("govee devices error %d: %s", resp.Code, resp.Message)
	}

	c.cacheMu.Lock()
	c.devices = resp.Data
	c.devicesFrom = time.Now()
	c.cacheMu.Unlock()

	c.log.Info("Device cache refreshed: %d devices", len(resp.Data))
	return resp.Data, nil
}

// Control sends one capability write to a device. Capabilities LAN speaks
// (power/brightness/colorRgb/colorTemperatureK on a routed device) take the
// fast path; everything else — scenes, segments, music, toggles — is cloud.
func (c *Client) Control(ctx context.Context, sku, device, capType, instance string, value interface{}) error {
	if handled, err := c.lanServe(device, capType, instance, value); handled {
		return err
	}
	req := controlRequest{
		RequestID: GenerateRequestID(),
		Payload: controlPayload{
			SKU:    sku,
			Device: device,
			Capability: capabilityValue{
				Type:     capType,
				Instance: instance,
				Value:    value,
			},
		},
	}
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := c.do(ctx, http.MethodPost, "/device/control", req, &resp); err != nil {
		return err
	}
	if resp.Code != 200 {
		return fmt.Errorf("govee control error %d: %s", resp.Code, resp.Message)
	}
	return nil
}

// Convenience wrappers matching the capability set the effects engine uses.

// Power, Color, and Brightness are the hot ops — they take the LAN fast path
// when the device has a route (free, ~26ms, no budget), else cloud. Transport
// resolution lives in transport.go; callers stay transport-blind.

func (c *Client) Power(ctx context.Context, sku, device string, on bool) error {
	if route, ok := c.lanRouteFor(device); ok {
		return c.lan.Turn(route.IP, on)
	}
	v := 0
	if on {
		v = 1
	}
	return c.Control(ctx, sku, device, CapOnOff, InstPower, v)
}

func (c *Client) Color(ctx context.Context, sku, device string, rgb int) error {
	if route, ok := c.lanRouteFor(device); ok {
		return c.lan.ColorRGB(route.IP, rgb)
	}
	return c.Control(ctx, sku, device, CapColor, InstColorRgb, rgb)
}

func (c *Client) Brightness(ctx context.Context, sku, device string, percent int) error {
	if route, ok := c.lanRouteFor(device); ok {
		return c.lan.Brightness(route.IP, percent)
	}
	return c.Control(ctx, sku, device, CapRange, InstBrightness, percent)
}

// CapabilityState is one capability's current value as reported by the
// device (raw — an empty value means the instance does not support query).
type CapabilityState struct {
	Type     string          `json:"type"`
	Instance string          `json:"instance"`
	Value    json.RawMessage `json:"value"`
}

// FullState reads the device's complete reported state: the online flag plus
// every capability value, unprojected. The device-controller state panel
// reads this; the effects engine keeps using State (the restorable subset).
// Govee limit: 30 state reads/min per device — callers must not poll hot.
func (c *Client) FullState(ctx context.Context, sku, device string) (online bool, states []CapabilityState, err error) {
	resp, err := c.fetchState(ctx, sku, device)
	if err != nil {
		return false, nil, err
	}
	for _, cap := range resp.Payload.Capabilities {
		if cap.Type == "devices.capabilities.online" {
			var v bool
			if json.Unmarshal(cap.State.Value, &v) == nil {
				online = v
			}
			continue
		}
		states = append(states, CapabilityState{Type: cap.Type, Instance: cap.Instance, Value: cap.State.Value})
	}
	return online, states, nil
}

// lanState reads devStatus over LAN and projects it into the cloud-shaped
// stateResponse so callers cannot tell the difference. On timeout it drops the
// stale route, kicks a re-scan, and reports failure so the caller falls back
// to cloud. Note the projection is honest about LAN's blindness: it emits ONLY
// the four fields LAN reports, plus online (a reply IS the online signal).
func (c *Client) lanState(ctx context.Context, device string) (*stateResponse, bool) {
	route, ok := c.lanRouteFor(device)
	if !ok {
		return nil, false
	}
	d, err := c.lan.Status(ctx, route.IP)
	if err != nil {
		c.lan.dropRoute(device)
		return nil, false
	}

	rgb := (d.Color.R << 16) | (d.Color.G << 8) | d.Color.B
	var resp stateResponse
	resp.Code = 200
	add := func(capType, instance string, v interface{}) {
		raw, _ := json.Marshal(v)
		resp.Payload.Capabilities = append(resp.Payload.Capabilities, struct {
			Type     string `json:"type"`
			Instance string `json:"instance"`
			State    struct {
				Value json.RawMessage `json:"value"`
			} `json:"state"`
		}{Type: capType, Instance: instance, State: struct {
			Value json.RawMessage `json:"value"`
		}{Value: raw}})
	}
	add("devices.capabilities.online", "online", true)
	add(CapOnOff, InstPower, d.OnOff)
	add(CapRange, InstBrightness, d.Brightness)
	add(CapColor, InstColorRgb, rgb)
	add(CapColor, InstColorTemp, d.ColorTemInKelvin)
	return &resp, true
}

func (c *Client) fetchState(ctx context.Context, sku, device string) (*stateResponse, error) {
	// LAN fast path (free, ~26ms). Falls through to cloud when there is no
	// route or the route just went stale.
	if resp, ok := c.lanState(ctx, device); ok {
		return resp, nil
	}
	req := stateRequest{
		RequestID: GenerateRequestID(),
		Payload:   stateDevice{SKU: sku, Device: device},
	}
	var resp stateResponse
	if err := c.do(ctx, http.MethodPost, "/device/state", req, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 200 {
		return nil, fmt.Errorf("govee state error %d: %s", resp.Code, resp.Message)
	}
	return &resp, nil
}

// State reads the current device state and projects the restorable subset.
func (c *Client) State(ctx context.Context, sku, device string) (*DeviceState, error) {
	resp, err := c.fetchState(ctx, sku, device)
	if err != nil {
		return nil, err
	}

	state := &DeviceState{}
	for _, cap := range resp.Payload.Capabilities {
		// Scene state (rarely reported — see DeviceState): keep the raw value
		// verbatim so whatever shape the device uses round-trips to control.
		// lightScene wins over diyScene/snapshot when several are present.
		if cap.Type == CapDynamicScene {
			if sceneValueReported(cap.State.Value) &&
				(state.SceneValue == nil || cap.Instance == InstLightScene) {
				state.SceneInstance = cap.Instance
				state.SceneValue = cap.State.Value
			}
			continue
		}

		var n int
		if err := json.Unmarshal(cap.State.Value, &n); err != nil {
			continue // non-numeric state (structs, strings) — not restorable here
		}
		v := n
		switch {
		case cap.Type == CapOnOff && cap.Instance == InstPower:
			state.PowerOn = &v
		case cap.Type == CapRange && cap.Instance == InstBrightness:
			state.Brightness = &v
		case cap.Type == CapColor && cap.Instance == InstColorRgb:
			state.ColorRgb = &v
		}
	}
	return state, nil
}

// sceneValueReported filters out the "no scene" placeholders devices return:
// null, empty string, empty object, or 0.
func sceneValueReported(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	switch s {
	case "", "null", `""`, "{}", "0":
		return false
	}
	return true
}

// ApplyScene applies a dynamic-scene value (from State capture or the scene
// catalog) via device control.
func (c *Client) ApplyScene(ctx context.Context, sku, device, instance string, value json.RawMessage) error {
	var v interface{}
	if err := json.Unmarshal(value, &v); err != nil {
		return fmt.Errorf("scene value round-trip: %w", err)
	}
	return c.Control(ctx, sku, device, CapDynamicScene, instance, v)
}

// Scene is one entry from the per-device scene catalog.
type Scene struct {
	Name     string          `json:"name"`
	Instance string          `json:"instance"` // "lightScene" | "diyScene"
	Value    json.RawMessage `json:"value"`    // controllable value, e.g. {"paramId":16433,"id":9558}
}

// scenesResponse is the wire shape of POST /device/scenes and
// /device/diy-scenes.
type scenesResponse struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Payload struct {
		Capabilities []struct {
			Type       string `json:"type"`
			Instance   string `json:"instance"`
			Parameters struct {
				Options []struct {
					Name  string          `json:"name"`
					Value json.RawMessage `json:"value"`
				} `json:"options"`
			} `json:"parameters"`
		} `json:"capabilities"`
	} `json:"payload"`
}

// ListScenes returns the device's scene catalog (dynamic + DIY scenes),
// cached per device. refresh forces a re-fetch. Scene values are per-device
// (paramId differs across devices) — never reuse one device's values on
// another.
func (c *Client) ListScenes(ctx context.Context, sku, device string, refresh bool) ([]Scene, error) {
	c.cacheMu.RLock()
	cached, ok := c.scenes[device]
	c.cacheMu.RUnlock()
	if ok && !refresh {
		return cached, nil
	}

	var scenes []Scene
	for _, path := range []string{"/device/scenes", "/device/diy-scenes"} {
		req := stateRequest{
			RequestID: GenerateRequestID(),
			Payload:   stateDevice{SKU: sku, Device: device},
		}
		var resp scenesResponse
		if err := c.do(ctx, http.MethodPost, path, req, &resp); err != nil {
			// DIY scenes are optional — a device with none may error; the
			// dynamic-scene list alone is still useful.
			if path == "/device/diy-scenes" && len(scenes) > 0 {
				c.log.Debug("DIY scene list for %s unavailable: %v", device, err)
				continue
			}
			return nil, err
		}
		if resp.Code != 200 {
			if path == "/device/diy-scenes" && len(scenes) > 0 {
				continue
			}
			return nil, fmt.Errorf("govee scenes error %d: %s", resp.Code, resp.Message)
		}
		for _, cap := range resp.Payload.Capabilities {
			if cap.Type != CapDynamicScene {
				continue
			}
			for _, opt := range cap.Parameters.Options {
				scenes = append(scenes, Scene{Name: opt.Name, Instance: cap.Instance, Value: opt.Value})
			}
		}
	}

	c.cacheMu.Lock()
	if c.scenes == nil {
		c.scenes = make(map[string][]Scene)
	}
	c.scenes[device] = scenes
	c.cacheMu.Unlock()

	c.log.Info("Scene catalog for %s: %d scenes", device, len(scenes))
	return scenes, nil
}
