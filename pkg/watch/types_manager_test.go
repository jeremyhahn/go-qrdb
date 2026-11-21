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

package watch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestManagerEventType_String verifies the string representation of all event types.
func TestManagerEventType_String(t *testing.T) {
	tests := []struct {
		name     string
		et       ManagerEventType
		expected string
	}{
		{"PUT", PUT, "PUT"},
		{"DELETE", DELETE, "DELETE"},
		{"unknown positive", ManagerEventType(99), "UNKNOWN"},
		{"unknown negative", ManagerEventType(-1), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.et.String())
		})
	}
}

// TestManagerEventType_String_InvalidValue ensures unknown event types produce UNKNOWN.
