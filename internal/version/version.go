/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
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
