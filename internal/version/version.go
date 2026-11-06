/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * Licensed under the MIT License. See LICENSE for details.
 */

package version

import "time"

// Build-time variables injected via ldflags
var (
	Version   = "dev"
	BuildDate = "unknown"
	Commit    = "unknown"
)

// StartTime is set when the package is initialized
var StartTime = time.Now()

// Uptime returns how long the application has been running
func Uptime() time.Duration {
	return time.Since(StartTime)
}
