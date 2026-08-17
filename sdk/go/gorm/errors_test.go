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
	err := &UnsupportedTransportError{Scheme: "rest"}
	got := err.Error()

	if got == "" {
		t.Fatal("Error() is empty")
	}
	if !strings.Contains(got, "rest") {
		t.Errorf("Error() = %q, want it to mention the scheme %q", got, "rest")
	}
}

func TestInvalidSavePointNameError_Error(t *testing.T) {
	err := &InvalidSavePointNameError{Name: "1; DROP TABLE users"}
	got := err.Error()

	if got == "" {
		t.Fatal("Error() is empty")
	}
	if !strings.Contains(got, "1; DROP TABLE users") {
		t.Errorf("Error() = %q, want it to mention the rejected name", got)
	}
}
