// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport

import (
	"testing"
)

// ---------------------------------------------------------------------------
// WriteMode tests
// ---------------------------------------------------------------------------

func TestWriteMode_String(t *testing.T) {
	tests := []struct {
		mode WriteMode
		want string
	}{
		{WriteModeSync, "sync"},
		{WriteModeWALDurable, "wal_durable"},
		{WriteModeBatched, "batched"},
		{WriteMode(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.want {
			t.Errorf("WriteMode(%d).String() = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func TestWriteMode_IsValid(t *testing.T) {
	tests := []struct {
		mode WriteMode
		want bool
	}{
		{WriteModeSync, true},
		{WriteModeWALDurable, true},
		{WriteModeBatched, true},
		{WriteMode(3), false},
		{WriteMode(255), false},
	}
	for _, tt := range tests {
		if got := tt.mode.IsValid(); got != tt.want {
			t.Errorf("WriteMode(%d).IsValid() = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestParseWriteMode(t *testing.T) {
	tests := []struct {
		input   string
		want    WriteMode
		wantErr bool
	}{
		{"sync", WriteModeSync, false},
		{"", WriteModeSync, false},
		{"wal_durable", WriteModeWALDurable, false},
		{"batched", WriteModeBatched, false},
		{"invalid", 0, true},
		{"SYNC", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseWriteMode(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseWriteMode(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseWriteMode(%q) = %v, want %v", tt.input, got, tt.want)
		}
		if tt.wantErr {
			var target *InvalidWriteModeError
			if err == nil {
				t.Errorf("ParseWriteMode(%q) expected InvalidWriteModeError, got nil", tt.input)
			} else if !isInvalidWriteModeError(err, &target) {
				t.Errorf("ParseWriteMode(%q) error type = %T, want *InvalidWriteModeError", tt.input, err)
			}
		}
	}
}

func isInvalidWriteModeError(err error, target **InvalidWriteModeError) bool {
	e, ok := err.(*InvalidWriteModeError)
	if ok {
		*target = e
	}
	return ok
}

// ---------------------------------------------------------------------------
// ConsistencyLevel tests
// ---------------------------------------------------------------------------

func TestConsistencyLevel_String(t *testing.T) {
	tests := []struct {
		level ConsistencyLevel
		want  string
	}{
		{ConsistencyStrong, "strong"},
		{ConsistencyLinearizable, "linearizable"},
		{ConsistencyEventual, "eventual"},
		{ConsistencyLevel(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("ConsistencyLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestConsistencyLevel_IsValid(t *testing.T) {
	tests := []struct {
		level ConsistencyLevel
		want  bool
	}{
		{ConsistencyStrong, true},
		{ConsistencyLinearizable, true},
		{ConsistencyEventual, true},
		{ConsistencyLevel(3), false},
		{ConsistencyLevel(255), false},
	}
	for _, tt := range tests {
		if got := tt.level.IsValid(); got != tt.want {
			t.Errorf("ConsistencyLevel(%d).IsValid() = %v, want %v", tt.level, got, tt.want)
		}
	}
}

func TestParseConsistencyLevel(t *testing.T) {
	tests := []struct {
		input   string
		want    ConsistencyLevel
		wantErr bool
	}{
		{"strong", ConsistencyStrong, false},
		{"linearizable", ConsistencyLinearizable, false},
		{"", ConsistencyLinearizable, false},
		{"eventual", ConsistencyEventual, false},
		{"stale", ConsistencyEventual, false},
		{"invalid", 0, true},
		{"STRONG", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseConsistencyLevel(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseConsistencyLevel(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseConsistencyLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
		if tt.wantErr {
			var target *InvalidConsistencyLevelError
			if err == nil {
				t.Errorf("ParseConsistencyLevel(%q) expected InvalidConsistencyLevelError, got nil", tt.input)
			} else if !isInvalidConsistencyLevelError(err, &target) {
				t.Errorf("ParseConsistencyLevel(%q) error type = %T, want *InvalidConsistencyLevelError", tt.input, err)
			}
		}
	}
}

func isInvalidConsistencyLevelError(err error, target **InvalidConsistencyLevelError) bool {
	e, ok := err.(*InvalidConsistencyLevelError)
	if ok {
		*target = e
	}
	return ok
}

// ---------------------------------------------------------------------------
// WriteOption tests
// ---------------------------------------------------------------------------

func TestResolveWriteOptions_Empty(t *testing.T) {
	wo := ResolveWriteOptions()
	if wo.ModeSet {
		t.Error("expected ModeSet=false with no options")
	}
	if wo.Mode != WriteModeSync {
		t.Errorf("expected default Mode=WriteModeSync, got %v", wo.Mode)
	}
}

func TestResolveWriteOptions_WithWriteMode(t *testing.T) {
	wo := ResolveWriteOptions(WithWriteMode(WriteModeWALDurable))
	if !wo.ModeSet {
		t.Error("expected ModeSet=true")
	}
	if wo.Mode != WriteModeWALDurable {
		t.Errorf("expected Mode=WriteModeWALDurable, got %v", wo.Mode)
	}
}

func TestResolveWriteOptions_LastWins(t *testing.T) {
	wo := ResolveWriteOptions(
		WithWriteMode(WriteModeWALDurable),
		WithWriteMode(WriteModeBatched),
	)
	if wo.Mode != WriteModeBatched {
		t.Errorf("expected last option to win: got %v, want WriteModeBatched", wo.Mode)
	}
}

// ---------------------------------------------------------------------------
// ReadOption tests
// ---------------------------------------------------------------------------

func TestResolveReadOptions_Empty(t *testing.T) {
	ro := ResolveReadOptions()
	if ro.ConsistencySet {
		t.Error("expected ConsistencySet=false with no options")
	}
	if ro.Consistency != ConsistencyStrong {
		t.Errorf("expected default Consistency=ConsistencyStrong, got %v", ro.Consistency)
	}
	if ro.TimeoutMs != 0 {
		t.Errorf("expected default TimeoutMs=0, got %v", ro.TimeoutMs)
	}
	if ro.AllowStale {
		t.Error("expected default AllowStale=false")
	}
}

func TestResolveReadOptions_WithConsistency(t *testing.T) {
	ro := ResolveReadOptions(WithConsistency(ConsistencyEventual))
	if !ro.ConsistencySet {
		t.Error("expected ConsistencySet=true")
	}
	if ro.Consistency != ConsistencyEventual {
		t.Errorf("expected Consistency=ConsistencyEventual, got %v", ro.Consistency)
	}
}

func TestResolveReadOptions_WithReadTimeout(t *testing.T) {
	ro := ResolveReadOptions(WithReadTimeout(5000))
	if ro.TimeoutMs != 5000 {
		t.Errorf("expected TimeoutMs=5000, got %v", ro.TimeoutMs)
	}
}

func TestResolveReadOptions_WithAllowStale(t *testing.T) {
	ro := ResolveReadOptions(WithAllowStale())
	if !ro.AllowStale {
		t.Error("expected AllowStale=true")
	}
}

func TestResolveReadOptions_Combined(t *testing.T) {
	ro := ResolveReadOptions(
		WithConsistency(ConsistencyEventual),
		WithReadTimeout(3000),
		WithAllowStale(),
	)
	if !ro.ConsistencySet {
		t.Error("expected ConsistencySet=true")
	}
	if ro.Consistency != ConsistencyEventual {
		t.Errorf("expected ConsistencyEventual, got %v", ro.Consistency)
	}
	if ro.TimeoutMs != 3000 {
		t.Errorf("expected TimeoutMs=3000, got %v", ro.TimeoutMs)
	}
	if !ro.AllowStale {
		t.Error("expected AllowStale=true")
	}
}

func TestResolveReadOptions_LastWins(t *testing.T) {
	ro := ResolveReadOptions(
		WithConsistency(ConsistencyStrong),
		WithConsistency(ConsistencyEventual),
	)
	if ro.Consistency != ConsistencyEventual {
		t.Errorf("expected last option to win: got %v, want ConsistencyEventual", ro.Consistency)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestInvalidWriteModeError(t *testing.T) {
	err := &InvalidWriteModeError{Mode: "badmode"}
	want := "transport: invalid write mode: badmode"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestInvalidConsistencyLevelError(t *testing.T) {
	err := &InvalidConsistencyLevelError{Level: "badlevel"}
	want := "transport: invalid consistency level: badlevel"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
