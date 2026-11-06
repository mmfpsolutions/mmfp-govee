/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * Licensed under the MIT License. See LICENSE for details.
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
