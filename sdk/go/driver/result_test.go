//go:build qrdb_sql

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

package driver

import (
	"database/sql/driver"
	"testing"
)

// Compile-time interface assertion for result.
var _ driver.Result = (*result)(nil)

// TestResult_LastInsertId verifies LastInsertId returns the correct value.
func TestResult_LastInsertId(t *testing.T) {
	tests := []struct {
		name string
		id   int64
	}{
		{
			name: "zero",
			id:   0,
		},
		{
			name: "non-zero",
			id:   42,
		},
		{
			name: "negative",
			id:   -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &result{lastInsertID: tt.id}
			got, err := r.LastInsertId()
			if err != nil {
				t.Fatalf("LastInsertId() unexpected error: %v", err)
			}
			if got != tt.id {
				t.Errorf("LastInsertId() = %d, want %d", got, tt.id)
			}
		})
	}
}
