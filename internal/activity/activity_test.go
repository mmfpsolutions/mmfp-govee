/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package activity

import (
	"fmt"
	"testing"
)

func TestRing_NewestFirstAndCap(t *testing.T) {
	l := &Log{}
	for i := 0; i < 250; i++ {
		l.Add(Record{Event: fmt.Sprintf("e%d", i)})
	}

	recent := l.Recent(0)
	if len(recent) != ringSize {
		t.Fatalf("len = %d, want %d (ring cap)", len(recent), ringSize)
	}
	if recent[0].Event != "e249" {
		t.Errorf("newest = %s, want e249", recent[0].Event)
	}
	if recent[ringSize-1].Event != fmt.Sprintf("e%d", 250-ringSize) {
		t.Errorf("oldest = %s, want e%d", recent[ringSize-1].Event, 250-ringSize)
	}

	limited := l.Recent(10)
	if len(limited) != 10 || limited[0].Event != "e249" {
		t.Errorf("Recent(10) = %d records, first %s", len(limited), limited[0].Event)
	}
}

func TestRing_TimestampStamped(t *testing.T) {
	l := &Log{}
	l.Add(Record{Event: "x"})
	if l.Recent(1)[0].Timestamp.IsZero() {
		t.Error("timestamp not stamped")
	}
}
