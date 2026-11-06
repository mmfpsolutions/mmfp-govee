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
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
	"github.com/mmfpsolutions/mmfp-govee/internal/version"
)

// Health page. Ledger's port of GSSM's metrics page (runtime, memory, GC),
// without the database, plus a Services card. Plan:
// design-documents/health/health-plan.md.

// ListenerStatus is the webhook listener as the Health page sees it
// (hooks.Server satisfies it).
type ListenerStatus interface {
	Running() bool
	Port() int
}

// goveeStatus is the slice of govee.Client the Services card reads. Every
// method is an in-memory read — none of them makes a Govee call.
type goveeStatus interface {
	LANEnabled() bool // the LAN transport is bound and scanning
	LANRoutes() map[string]govee.LANRoute
	LANLastScan() time.Time
	BreakerState() (open bool, until time.Time, lastErr error)
	CallsToday() (used, limit int)
	CachedAt() time.Time
}

// webhookService is the Services card's webhook listener row.
type webhookService struct {
	Running bool `json:"running"`
	Port    int  `json:"port"`
}

// lanService is the LAN transport row. Enabled = config intent, Running =
// actually bound; enabled-but-not-running means UDP 4002 could not be bound
// and every device is on the cloud.
type lanService struct {
	Enabled      bool `json:"enabled"`
	Running      bool `json:"running"`
	Devices      int  `json:"devices"`
	LastScanSecs *int `json:"lastScanSecs"` // age; null = never scanned
}

// cloudService is the Govee cloud row. Paused = the circuit breaker is open:
// a run of OUTAGE failures (not ordinary rejections) and calls are being
// refused without being made. Not paused is "Normal", never "Reachable" —
// this page makes no call to prove that.
type cloudService struct {
	Paused      bool   `json:"paused"`
	RetryInSecs int    `json:"retryInSecs,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	CallsToday  int    `json:"callsToday"`
	CallsLimit  int    `json:"callsLimit"`
}

var _ goveeStatus = (*govee.Client)(nil)

// catalogService is the device catalog row.
type catalogService struct {
	FetchedSecs *int `json:"fetchedSecs"` // age; null = never fetched
}

type servicesView struct {
	Webhook webhookService `json:"webhook"`
	LAN     lanService     `json:"lan"`
	Cloud   cloudService   `json:"cloud"`
	Catalog catalogService `json:"catalog"`
}

// ageSecs is the whole seconds since t, or nil when t is zero.
func ageSecs(now, t time.Time) *int {
	if t.IsZero() {
		return nil
	}
	s := int(now.Sub(t).Seconds())
	if s < 0 {
		s = 0
	}
	return &s
}

// buildServices projects Govee's in-memory state into the Services card.
func buildServices(cfg *config.Config, listener ListenerStatus, g goveeStatus, now time.Time) servicesView {
	var v servicesView
	if listener != nil {
		v.Webhook = webhookService{Running: listener.Running(), Port: listener.Port()}
	}

	v.LAN = lanService{
		Enabled:      cfg.LANEnabled(),
		Running:      g.LANEnabled(),
		Devices:      len(g.LANRoutes()),
		LastScanSecs: ageSecs(now, g.LANLastScan()),
	}

	open, until, lastErr := g.BreakerState()
	used, limit := g.CallsToday()
	v.Cloud = cloudService{Paused: open, CallsToday: used, CallsLimit: limit}
	if open {
		if d := until.Sub(now); d > 0 {
			v.Cloud.RetryInSecs = int(d.Round(time.Second).Seconds())
		}
		if lastErr != nil {
			v.Cloud.LastError = lastErr.Error()
		}
	}

	v.Catalog = catalogService{FetchedSecs: ageSecs(now, g.CachedAt())}
	return v
}

// HandleMetrics handles GET /api/v1/metrics, the Health page: Go runtime,
// memory and GC (as Ledger and GSSM), plus Govee's services. Raw bytes are
// returned; the page formats them. It calls nothing outside Govee.
func HandleMetrics(cfgManager *config.Manager, g goveeStatus, listener ListenerStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		resp := map[string]interface{}{
			"runtime": map[string]interface{}{
				"uptime":     formatDuration(version.Uptime()),
				"goroutines": runtime.NumGoroutine(),
				"numCPU":     runtime.NumCPU(),
				"goVersion":  runtime.Version(),
			},
			"memory": map[string]interface{}{
				"inUseBytes":  m.Alloc,
				"fromOsBytes": m.Sys,
				// The breakdown adds up to fromOsBytes (Sys).
				"heapInUse":    m.HeapInuse,
				"heapReserved": m.HeapSys - m.HeapInuse,
				"stack":        m.StackSys,
				"other":        m.Sys - m.HeapSys - m.StackSys,
				"heapObjects":  m.HeapObjects,
				"logFileBytes": logFileSize(),
			},
			"gc": map[string]interface{}{
				"numGC":        m.NumGC,
				"numForcedGC":  m.NumForcedGC,
				"pauseTotalMs": float64(m.PauseTotalNs) / 1e6,
				"cpuPercent":   m.GCCPUFraction * 100,
			},
			"services": buildServices(cfgManager.GetConfig(), listener, g, time.Now()),
		}

		v1types.RespondOK(w, resp, nil)
	}
}

// formatDuration renders an uptime as "1d 2h 3m 4s" (Ledger's, unchanged).
func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// logFileSize returns the current log file size in bytes, or 0 when file
// logging is off or the file is unavailable.
func logFileSize() int64 {
	path := logger.FilePath()
	if path == "" {
		return 0
	}
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}
