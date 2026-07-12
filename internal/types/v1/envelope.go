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
	"net/http"
	"time"
)

// APIResponse is the standard response envelope for all API endpoints.
type APIResponse struct {
	Status string      `json:"status"` // "ok", "error", "partial"
	Data   interface{} `json:"data,omitempty"`
	Errors []APIError  `json:"errors,omitempty"`
	Meta   *Meta       `json:"meta,omitempty"`
}

// APIError represents a single error in the response.
type APIError struct {
	Code    string `json:"code"` // "DEVICE_TIMEOUT", "RPC_FAILED", etc.
	Message string `json:"message"`
	Target  string `json:"target,omitempty"` // miner/node ID that failed
}

// Meta holds request metadata.
type Meta struct {
	RequestDuration string `json:"requestDuration,omitempty"`
	Timestamp       int64  `json:"timestamp"`
	PartialResults  bool   `json:"partialResults,omitempty"`
}

// NewMeta creates a Meta with the current timestamp and elapsed duration.
func NewMeta(start time.Time) *Meta {
	return &Meta{
		RequestDuration: time.Since(start).Round(time.Millisecond).String(),
		Timestamp:       time.Now().Unix(),
	}
}

// --- Response helpers ---

func writeJSON(w http.ResponseWriter, statusCode int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(statusCode)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// RespondOK writes a 200 response with status "ok".
func RespondOK(w http.ResponseWriter, data interface{}, meta *Meta) {
	writeJSON(w, http.StatusOK, APIResponse{
		Status: "ok",
		Data:   data,
		Meta:   meta,
	})
}

// RespondCreated writes a 201 response with status "ok".
func RespondCreated(w http.ResponseWriter, data interface{}) {
	writeJSON(w, http.StatusCreated, APIResponse{
		Status: "ok",
		Data:   data,
	})
}

// RespondPartial writes a 200 response with status "partial" and error details.
func RespondPartial(w http.ResponseWriter, data interface{}, errors []APIError, meta *Meta) {
	if meta != nil {
		meta.PartialResults = true
	}
	writeJSON(w, http.StatusOK, APIResponse{
		Status: "partial",
		Data:   data,
		Errors: errors,
		Meta:   meta,
	})
}

// RespondError writes an error response with the given HTTP status code.
func RespondError(w http.ResponseWriter, statusCode int, errors []APIError) {
	writeJSON(w, statusCode, APIResponse{
		Status: "error",
		Errors: errors,
	})
}

// RespondErrorMsg is a convenience for a single error message.
func RespondErrorMsg(w http.ResponseWriter, statusCode int, code, message string) {
	RespondError(w, statusCode, []APIError{{Code: code, Message: message}})
}
