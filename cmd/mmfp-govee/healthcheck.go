/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * Licensed under the MIT License. See LICENSE for details.
 */

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/config"
)

// healthcheckTimeout bounds each probe. Docker's own --timeout is 5s and
// there are two probes, run one after the other.
const healthcheckTimeout = 2 * time.Second

// healthcheck probes BOTH of this instance's listeners and returns a process
// exit code: 0 when both answer GET /health with 200, else 1.
//
// Both, because the webhook listener is the app's reason to exist: if :8787
// fails to bind, the process keeps running (by design; see run()) and the web
// UI still answers, so probing only the web port would report healthy for an
// app that cannot receive a webhook.
//
// The Govee cloud is deliberately NOT probed. It's external: an outage is
// surfaced on the Health page, never as container ill-health.
func healthcheck() int {
	webPort, hookPort := healthcheckPorts()
	return probeAll(os.Stderr, []probe{
		{name: "web app", url: fmt.Sprintf("http://localhost:%d/health", webPort)},
		{name: "webhook listener", url: fmt.Sprintf("http://localhost:%d/health", hookPort)},
	})
}

// healthcheckPorts reads both ports from config.json directly — never through
// config.Manager, whose load path can rewrite the file (encryption migration).
// A missing or unreadable file, or an absent port, falls back to the defaults
// run() would use.
func healthcheckPorts() (web, hook int) {
	web, hook = config.DefaultWebServerPort, config.DefaultWebhookPort
	baseDir, err := resolveBaseDir()
	if err != nil {
		return web, hook
	}
	return portsFromFile(filepath.Join(baseDir, "config", "config.json"))
}

func portsFromFile(path string) (web, hook int) {
	web, hook = config.DefaultWebServerPort, config.DefaultWebhookPort
	data, err := os.ReadFile(path)
	if err != nil {
		return web, hook
	}
	var c struct {
		WebServerPort int `json:"webServerPort"`
		WebhookPort   int `json:"webhookPort"`
	}
	if json.Unmarshal(data, &c) != nil {
		return web, hook
	}
	if c.WebServerPort != 0 {
		web = c.WebServerPort
	}
	if c.WebhookPort != 0 {
		hook = c.WebhookPort
	}
	return web, hook
}

type probe struct {
	name, url string
}

// probeAll GETs every URL (GET, never HEAD — the routes are GET-only) and
// reports each failure by name, so Docker's health log says which listener
// is down.
func probeAll(errOut io.Writer, probes []probe) int {
	client := &http.Client{Timeout: healthcheckTimeout}
	code := 0
	for _, p := range probes {
		resp, err := client.Get(p.url)
		if err != nil {
			fmt.Fprintf(errOut, "healthcheck: %s: %v\n", p.name, err)
			code = 1
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(errOut, "healthcheck: %s: status %d\n", p.name, resp.StatusCode)
			code = 1
		}
	}
	return code
}
