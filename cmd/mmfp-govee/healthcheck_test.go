/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmfpsolutions/mmfp-govee/internal/config"
)

func TestPortsFromFile(t *testing.T) {
	dir := t.TempDir()

	// Custom ports are honoured.
	custom := filepath.Join(dir, "custom.json")
	content := `{"webServerPort": 4108, "webhookPort": 9787, "goveeApiKey": "ENC:x"}`
	os.WriteFile(custom, []byte(content), 0600)
	if web, hook := portsFromFile(custom); web != 4108 || hook != 9787 {
		t.Errorf("custom = %d/%d, want 4108/9787", web, hook)
	}
	// The probe must never rewrite the file.
	if got, _ := os.ReadFile(custom); string(got) != content {
		t.Errorf("config.json was rewritten: %s", got)
	}

	// One port set, the other defaulted.
	partial := filepath.Join(dir, "partial.json")
	os.WriteFile(partial, []byte(`{"webhookPort": 9787}`), 0600)
	if web, hook := portsFromFile(partial); web != config.DefaultWebServerPort || hook != 9787 {
		t.Errorf("partial = %d/%d", web, hook)
	}

	// Missing and malformed files fall back to the defaults.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{not json`), 0600)
	for _, p := range []string{filepath.Join(dir, "missing.json"), bad} {
		if web, hook := portsFromFile(p); web != config.DefaultWebServerPort || hook != config.DefaultWebhookPort {
			t.Errorf("%s = %d/%d, want defaults", filepath.Base(p), web, hook)
		}
	}
}

func TestProbeAll(t *testing.T) {
	var methods []string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close() // nothing listening

	var errOut bytes.Buffer
	if code := probeAll(&errOut, []probe{{"web app", ok.URL}, {"webhook listener", ok.URL}}); code != 0 {
		t.Errorf("both up: exit %d (%s)", code, errOut.String())
	}
	for _, m := range methods {
		if m != http.MethodGet {
			t.Errorf("probe used %s; the /health routes are GET-only", m)
		}
	}

	// The webhook listener down → unhealthy, and the log names it.
	errOut.Reset()
	if code := probeAll(&errOut, []probe{{"web app", ok.URL}, {"webhook listener", downURL}}); code != 1 {
		t.Errorf("webhook down: exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "webhook listener") {
		t.Errorf("stderr = %q, want it to name the webhook listener", errOut.String())
	}

	// A non-200 is a failure too.
	errOut.Reset()
	if code := probeAll(&errOut, []probe{{"web app", notFound.URL}}); code != 1 || !strings.Contains(errOut.String(), "status 404") {
		t.Errorf("404: exit %d, stderr %q", code, errOut.String())
	}
}
