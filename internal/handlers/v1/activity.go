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
	"net/http"
	"strconv"

	"github.com/mmfpsolutions/mmfp-govee/internal/activity"
	v1types "github.com/mmfpsolutions/mmfp-govee/internal/types/v1"
)

// HandleActivityList handles GET /api/v1/activity?limit=N (newest first)
func HandleActivityList(act *activity.Log) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		v1types.RespondOK(w, map[string]interface{}{"activity": act.Recent(limit)}, nil)
	}
}
