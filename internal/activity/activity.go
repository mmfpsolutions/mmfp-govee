/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// Package activity is an in-memory ring buffer of recent webhook hits and
// effect outcomes — the Activity page's data source. No database: the log is
// wiped on restart, which is the right weight for a troubleshooting surface.
package activity

import (
	"sync"
	"time"
)

const ringSize = 200

// Record is one webhook hit or effect outcome.
type Record struct {
	Timestamp   time.Time `json:"timestamp"`
	TokenName   string    `json:"tokenName"`
	Source      string    `json:"source"` // "gss", "gssm", "unknown"
	Event       string    `json:"event"`
	Entity      string    `json:"entity,omitempty"`
	MappingID   string    `json:"mappingId,omitempty"`
	MappingName string    `json:"mappingName,omitempty"`
	Devices     []string  `json:"devices,omitempty"` // Govee device IDs involved (UI resolves names)
	// Transport is which path served this device: "lan" or "cloud". Empty on
	// job-level rows (a job's devices can differ) and on rows where nothing was
	// sent (queued / cooldown / quiet hours / rejected / no match).
	Transport string `json:"transport,omitempty"`
	Result    string `json:"result"` // "queued", "no match", "cooldown", "quiet hours", "rejected", "effect ok", "effect failed: ..."
}

// Log is a fixed-size thread-safe ring buffer.
type Log struct {
	mu      sync.RWMutex
	records [ringSize]Record
	next    int
	count   int
}

var (
	instance *Log
	once     sync.Once
)

// GetLog returns the singleton activity log.
func GetLog() *Log {
	once.Do(func() {
		instance = &Log{}
	})
	return instance
}

// Add appends a record (stamping the time if unset).
func (l *Log) Add(r Record) {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records[l.next] = r
	l.next = (l.next + 1) % ringSize
	if l.count < ringSize {
		l.count++
	}
}

// Recent returns up to limit records, newest first.
func (l *Log) Recent(limit int) []Record {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if limit <= 0 || limit > l.count {
		limit = l.count
	}
	out := make([]Record, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (l.next - 1 - i + ringSize*2) % ringSize
		out = append(out, l.records[idx])
	}
	return out
}
