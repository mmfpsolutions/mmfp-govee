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
	"fmt"
	"io"
	"net/http"
)

// decodeBody decodes a JSON request body with a size cap.
func decodeBody(r *http.Request, v interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return fmt.Errorf("empty body")
	}
	return json.Unmarshal(body, v)
}
