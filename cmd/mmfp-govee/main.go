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
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	"github.com/mmfpsolutions/mmfp-govee/internal/auth"
	"github.com/mmfpsolutions/mmfp-govee/internal/config"
	"github.com/mmfpsolutions/mmfp-govee/internal/effects"
	"github.com/mmfpsolutions/mmfp-govee/internal/govee"
	"github.com/mmfpsolutions/mmfp-govee/internal/hooks"
	"github.com/mmfpsolutions/mmfp-govee/internal/logger"
	"github.com/mmfpsolutions/mmfp-govee/internal/router"
	"github.com/mmfpsolutions/mmfp-govee/internal/security"
	"github.com/mmfpsolutions/mmfp-govee/internal/version"
)

func main() {
	log := logger.New(logger.ModuleMain)
	if err := run(); err != nil {
		log.Fatal("FAILED TO START SERVER: %v", err)
	}
}

func run() error {
	log := logger.New(logger.ModuleMain)

	log.Info("MMFP Govee v%s (built %s, commit %s)", version.Version, version.BuildDate, version.Commit)

	// Determine paths
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	baseDir := filepath.Dir(execPath)

	// For development, use current directory
	if _, err := os.Stat("./config"); err == nil {
		baseDir, _ = os.Getwd()
	}

	configDir := filepath.Join(baseDir, "config")
	logsDir := filepath.Join(baseDir, "logs")

	log.Info("Base directory: %s", baseDir)
	log.Info("Config directory: %s", configDir)

	for _, dir := range []string{configDir, logsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Warn("Could not create directory %s: %v", dir, err)
		}
	}

	// Initialize JWT service (only matters when auth is enabled — opt-in)
	if err := auth.InitJWTService(configDir); err != nil {
		log.Info("JWT service not initialized (auth is opt-in; provide jsonWebTokenKey.json to enable): %v", err)
	}

	// Load configuration
	cfgManager := config.GetManager(configDir)
	cfg, err := cfgManager.LoadConfig()
	if err != nil {
		// A baseline is written ONLY when there's truly no config — if the
		// file EXISTS but failed to load, overwriting it would silently
		// destroy the operator's configuration. Fail loud instead.
		if cfgManager.ConfigFileExists() {
			log.Error("config.json exists but could not be loaded; refusing to start so it is not overwritten — fix it (e.g. JSON syntax) and restart. Error: %v", err)
			os.Exit(1)
		}
		log.Info("No configuration file found, starting first-time setup")
		cfg, err = cfgManager.WriteDefaultConfig()
		if err != nil {
			log.Error("Failed to write default config: %v", err)
			os.Exit(1)
		}
	}

	// Setup logging from config
	if cfg.Logging != nil {
		logger.SetGlobalLevel(cfg.Logging.Level)
		logFilePath := cfg.Logging.LogFilePath
		if logFilePath == "" {
			logFilePath = filepath.Join(logsDir, "mmfp-govee.log")
		}
		if err := logger.SetupFileLoggingWithRotation(cfg.Logging.LogToFile, logFilePath, logger.RotationConfig{
			MaxSizeMB:  cfg.Logging.MaxSizeMb,
			MaxAgeDays: cfg.Logging.MaxAgeDays,
			MaxBackups: cfg.Logging.MaxBackups,
			Compress:   cfg.Logging.Compress,
		}); err != nil {
			log.Error("Failed to setup file logging: %v", err)
		} else if cfg.Logging.LogToFile {
			log.Info("File logging enabled: %s", logFilePath)
		}
	}

	// Govee client — the API key is decrypted at call time from the live
	// config so key changes hot-reload and plaintext stays short-lived.
	client := govee.NewClient(func() (string, error) {
		return security.DecryptIfEncrypted(cfgManager.GetConfig().GoveeAPIKey)
	}, "")

	// LAN Control fast path (optional; defaults on). Discovery is startup +
	// bounded retries + manual Refresh + self-heal — never a polling timer.
	// A bind failure or an empty scan is non-fatal: every device falls back to
	// the cloud API per-device, so this is safe on any network.
	if cfg.LANEnabled() {
		client.EnableLAN()
	} else {
		log.Info("LAN control disabled by config — using the cloud API only")
	}

	// Activity log + effects engine
	act := activity.GetLog()
	engine := effects.NewEngine(client, act)
	if cfg.QuietHours != nil {
		engine.SetQuietHours(effects.QuietHours{
			Enabled: cfg.QuietHours.Enabled,
			Start:   cfg.QuietHours.Start,
			End:     cfg.QuietHours.End,
		})
	}

	// Webhook listener (:8787)
	hookServer := hooks.NewServer(cfgManager, engine, act)
	hookServer.Start(cfg.WebhookPort)

	// Web app (:3008)
	handler := router.SetupRouter(cfgManager, client, engine, act)
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.WebServerPort),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Info("Web app running on http://localhost:%d", cfg.WebServerPort)
	log.Info("Webhook listener on :%d (POST /hook)", cfg.WebhookPort)

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-quit:
		log.Info("Shutdown signal received, gracefully shutting down...")
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stop accepting webhooks first, then let in-flight effects finish, then
	// close the LAN socket (effects may still be using it).
	hookServer.Stop(ctx)
	engine.Stop()
	client.StopLAN()

	logger.StopRotation()
	defer logger.CloseLogFile()

	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("server forced to shutdown: %w", err)
	}

	log.Info("Server stopped gracefully")
	return nil
}
