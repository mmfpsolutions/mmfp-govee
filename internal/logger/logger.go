/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package logger

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level represents the logging level
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// Module represents the component that is logging
type Module string

const (
	ModuleMain       Module = "main"
	ModuleConfig     Module = "config"
	ModuleHandler    Module = "handler"
	ModuleMiddleware Module = "middleware"
	ModuleAuth       Module = "auth"
	ModuleWeb        Module = "web"
	ModuleGovee      Module = "govee"
	ModuleEffects    Module = "effects"
	ModuleHooks      Module = "hooks"
)

// Logger provides structured logging functionality
type Logger struct {
	module Module
	logger *log.Logger
}

var (
	// Global log level (can be set via config)
	globalLogLevel = LevelInfo

	// Global log output writer (can be file, stdout, or both)
	globalLogWriter io.Writer = os.Stdout

	// Global log file handle (to close on shutdown)
	globalLogFile *os.File

	// Mutex to protect globalLogWriter during rotation
	globalLogWriterMu sync.RWMutex
)

// delegatingWriter is a writer that always delegates to globalLogWriter
// This ensures all loggers automatically use the new file after rotation
type delegatingWriter struct{}

func (d *delegatingWriter) Write(p []byte) (n int, err error) {
	globalLogWriterMu.RLock()
	w := globalLogWriter
	globalLogWriterMu.RUnlock()
	return w.Write(p)
}

// Global delegating writer instance used by all loggers
var globalDelegatingWriter = &delegatingWriter{}

// New creates a new logger for the specified module
func New(module Module) *Logger {
	return &Logger{
		module: module,
		logger: log.New(globalDelegatingWriter, "", 0),
	}
}

// SetGlobalLevel sets the global logging level for all loggers
func SetGlobalLevel(level string) {
	switch strings.ToLower(level) {
	case "debug":
		globalLogLevel = LevelDebug
	case "info":
		globalLogLevel = LevelInfo
	case "warn", "warning":
		globalLogLevel = LevelWarn
	case "error":
		globalLogLevel = LevelError
	default:
		globalLogLevel = LevelInfo
	}
}

// CloseLogFile closes the log file if one is open
func CloseLogFile() error {
	globalLogWriterMu.Lock()
	defer globalLogWriterMu.Unlock()

	if globalLogFile != nil {
		err := globalLogFile.Close()
		globalLogFile = nil
		globalLogWriter = os.Stdout
		return err
	}
	return nil
}

// RotationConfig holds log rotation settings
type RotationConfig struct {
	MaxSizeMB  int  // Maximum log file size in MB before rotation
	MaxAgeDays int  // Maximum age in days before old logs are deleted
	MaxBackups int  // Maximum number of old log files to retain
	Compress   bool // Compress rotated log files with gzip
}

// rotationManager handles log file rotation
type rotationManager struct {
	mu          sync.Mutex
	logFilePath string
	config      RotationConfig
	stopChan    chan struct{}
	logger      *Logger
}

var globalRotationManager *rotationManager

// SetupFileLoggingWithRotation configures logging with rotation support
func SetupFileLoggingWithRotation(logToFile bool, logFilePath string, config RotationConfig) error {
	if !logToFile {
		globalLogWriterMu.Lock()
		globalLogWriter = os.Stdout
		globalLogWriterMu.Unlock()
		return nil
	}

	logDir := filepath.Dir(logFilePath)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	globalLogWriterMu.Lock()
	globalLogFile = file
	globalLogWriter = io.MultiWriter(os.Stdout, file)
	globalLogWriterMu.Unlock()

	globalRotationManager = &rotationManager{
		logFilePath: logFilePath,
		config:      config,
		stopChan:    make(chan struct{}),
		logger:      New(ModuleMain),
	}

	go globalRotationManager.startRotationChecker()

	return nil
}

// startRotationChecker periodically checks if rotation is needed
func (rm *rotationManager) startRotationChecker() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rm.checkAndRotate()
		case <-rm.stopChan:
			return
		}
	}
}

// checkAndRotate checks if the log file needs rotation and performs it
func (rm *rotationManager) checkAndRotate() {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	info, err := os.Stat(rm.logFilePath)
	if err != nil {
		return
	}

	maxBytes := int64(rm.config.MaxSizeMB) * 1024 * 1024
	if info.Size() >= maxBytes {
		rm.rotateLogFile()
	}

	rm.cleanupOldLogs()
}

// rotateLogFile performs the log rotation
func (rm *rotationManager) rotateLogFile() {
	if globalLogFile != nil {
		globalLogFile.Close()
	}

	timestamp := time.Now().Format("20060102-150405")
	dir := filepath.Dir(rm.logFilePath)
	base := filepath.Base(rm.logFilePath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)
	rotatedName := fmt.Sprintf("%s.%s%s", nameWithoutExt, timestamp, ext)
	rotatedPath := filepath.Join(dir, rotatedName)

	if err := os.Rename(rm.logFilePath, rotatedPath); err != nil {
		fmt.Printf("[%s] [main] [ERROR] Failed to rotate log file: %v\n",
			time.Now().Format("2006-01-02 15:04:05"), err)
		return
	}

	if rm.config.Compress {
		go rm.compressLogFile(rotatedPath)
	}

	file, err := os.OpenFile(rm.logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("[%s] [main] [ERROR] Failed to open new log file: %v\n",
			time.Now().Format("2006-01-02 15:04:05"), err)
		return
	}

	globalLogWriterMu.Lock()
	globalLogFile = file
	globalLogWriter = io.MultiWriter(os.Stdout, file)
	globalLogWriterMu.Unlock()

	fmt.Printf("[%s] [main] [INFO] Log rotated to %s\n",
		time.Now().Format("2006-01-02 15:04:05"), rotatedName)
}

// compressLogFile compresses a rotated log file with gzip
func (rm *rotationManager) compressLogFile(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	gzPath := filePath + ".gz"
	gzFile, err := os.Create(gzPath)
	if err != nil {
		return
	}
	defer gzFile.Close()

	gzWriter := gzip.NewWriter(gzFile)
	if _, err := gzWriter.Write(data); err != nil {
		os.Remove(gzPath)
		return
	}
	if err := gzWriter.Close(); err != nil {
		os.Remove(gzPath)
		return
	}

	os.Remove(filePath)
}

// cleanupOldLogs removes old log files based on age and count limits
func (rm *rotationManager) cleanupOldLogs() {
	dir := filepath.Dir(rm.logFilePath)
	base := filepath.Base(rm.logFilePath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	pattern := filepath.Join(dir, nameWithoutExt+".*"+ext)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}

	gzPattern := filepath.Join(dir, nameWithoutExt+".*"+ext+".gz")
	gzMatches, err := filepath.Glob(gzPattern)
	if err == nil {
		matches = append(matches, gzMatches...)
	}

	type logFile struct {
		path    string
		modTime time.Time
	}
	var logFiles []logFile

	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		logFiles = append(logFiles, logFile{path: match, modTime: info.ModTime()})
	}

	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].modTime.After(logFiles[j].modTime)
	})

	for i := rm.config.MaxBackups; i < len(logFiles); i++ {
		os.Remove(logFiles[i].path)
	}

	cutoff := time.Now().AddDate(0, 0, -rm.config.MaxAgeDays)
	for _, lf := range logFiles {
		if lf.modTime.Before(cutoff) {
			os.Remove(lf.path)
		}
	}
}

// StopRotation stops the rotation checker goroutine
func StopRotation() {
	if globalRotationManager != nil && globalRotationManager.stopChan != nil {
		close(globalRotationManager.stopChan)
	}
}

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}

// formatMessage formats the log message with standard format:
// [timestamp] [client_ip] [module] [level] action
func (l *Logger) formatMessage(level, clientIP, action string) string {
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	if clientIP != "" {
		return fmt.Sprintf("[%s] [%s] [%s] [%s] %s", timestamp, clientIP, l.module, level, action)
	}
	return fmt.Sprintf("[%s] [system] [%s] [%s] %s", timestamp, l.module, level, action)
}

// Info logs an informational message (system-level, no client IP)
func (l *Logger) Info(format string, args ...interface{}) {
	if globalLogLevel > LevelInfo {
		return
	}
	action := fmt.Sprintf(format, args...)
	msg := l.formatMessage("INFO", "", action)
	l.logger.Println(msg)
}

// InfoWithRequest logs an informational message with client IP from request
func (l *Logger) InfoWithRequest(r *http.Request, format string, args ...interface{}) {
	if globalLogLevel > LevelInfo {
		return
	}
	action := fmt.Sprintf(format, args...)
	clientIP := getClientIP(r)
	msg := l.formatMessage("INFO", clientIP, action)
	l.logger.Println(msg)
}

// Error logs an error message (system-level, no client IP)
func (l *Logger) Error(format string, args ...interface{}) {
	if globalLogLevel > LevelError {
		return
	}
	action := fmt.Sprintf(format, args...)
	msg := l.formatMessage("ERROR", "", action)
	l.logger.Println(msg)
}

// ErrorWithRequest logs an error message with client IP from request
func (l *Logger) ErrorWithRequest(r *http.Request, format string, args ...interface{}) {
	if globalLogLevel > LevelError {
		return
	}
	action := fmt.Sprintf(format, args...)
	clientIP := getClientIP(r)
	msg := l.formatMessage("ERROR", clientIP, action)
	l.logger.Println(msg)
}

// Fatal logs a fatal error and exits the program
func (l *Logger) Fatal(format string, args ...interface{}) {
	action := fmt.Sprintf(format, args...)
	msg := l.formatMessage("FATAL", "", action)
	l.logger.Fatal(msg)
}

// Warn logs a warning message (system-level, no client IP)
func (l *Logger) Warn(format string, args ...interface{}) {
	if globalLogLevel > LevelWarn {
		return
	}
	action := fmt.Sprintf(format, args...)
	msg := l.formatMessage("WARN", "", action)
	l.logger.Println(msg)
}

// WarnWithRequest logs a warning message with client IP from request
func (l *Logger) WarnWithRequest(r *http.Request, format string, args ...interface{}) {
	if globalLogLevel > LevelWarn {
		return
	}
	action := fmt.Sprintf(format, args...)
	clientIP := getClientIP(r)
	msg := l.formatMessage("WARN", clientIP, action)
	l.logger.Println(msg)
}

// Debug logs a debug message (system-level, no client IP)
func (l *Logger) Debug(format string, args ...interface{}) {
	if globalLogLevel > LevelDebug {
		return
	}
	action := fmt.Sprintf(format, args...)
	msg := l.formatMessage("DEBUG", "", action)
	l.logger.Println(msg)
}
