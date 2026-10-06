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

package gorm

import (
	"strings"
	"testing"
)

func TestUnsupportedTransportError_Error(t *testing.T) {
	tests := []string{"rest", "grpc"}
	for _, scheme := range tests {
		t.Run(scheme, func(t *testing.T) {
			got := (&UnsupportedTransportError{Scheme: scheme}).Error()
			if !strings.Contains(got, `"`+scheme+`"`) {
				t.Errorf("Error() = %q, want it to name the scheme %q", got, scheme)
			}
			if !strings.Contains(got, "node://") {
				t.Errorf("Error() = %q, want it to name node:// as the alternative", got)
			}
		})
	}
}
