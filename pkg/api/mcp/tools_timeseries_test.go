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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd && !integration

package mcp

import (
	"encoding/json"
	"math"
	"testing"
)

// TestParseExactInt64 checks the decoding of each argument type an MCP
// client can send for a nanosecond timestamp. The missing-key, int64, int
// and non-integer default cases are checked by TestHandleTSQuery_MissingStart
// and the TestCovParseExactInt64_* tests.
func TestParseExactInt64(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int64
	}{
		// 1.7e18 is exactly representable as a float64, so the conversion
		// through its decimal form must be lossless.
		{name: "float64", value: float64(1700000000000000000), want: 1700000000000000000},
		{name: "float64 above MaxInt64 clamps", value: math.MaxFloat64, want: math.MaxInt64},
		{name: "float64 below MinInt64 clamps", value: -math.MaxFloat64, want: math.MinInt64},
		{name: "string", value: "1234567890", want: 1234567890},
		{name: "json.Number", value: json.Number("9876543210"), want: 9876543210},
		{name: "int64", value: int64(math.MaxInt64), want: math.MaxInt64},
		{name: "int", value: int(-99), want: -99},
		{name: "other integer type", value: uint64(7), want: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildCallToolRequest(t, "test", map[string]any{"start": tt.value})
			got, err := parseExactInt64(req, "start")
			if err != nil {
				t.Fatalf("parseExactInt64: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseExactInt64 = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestParseExactInt64_DefaultCaseNonInteger checks that a value of another
// type whose %v form is not an integer is rejected.
func TestParseExactInt64_DefaultCaseNonInteger(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{"start": true})
	if _, err := parseExactInt64(req, "start"); err == nil {
		t.Fatal("parseExactInt64(true): want error, got nil")
	}
}
