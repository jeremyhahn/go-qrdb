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

package embedded

import (
	"testing"
)

func TestMissingAuditLoggerError_Message(t *testing.T) {
	err := &MissingAuditLoggerError{}
	want := "embedded: audit logger is not configured"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestUnregisteredOperationError_Message(t *testing.T) {
	err := &UnregisteredOperationError{Method: "Frobnicate"}
	want := "embedded: Frobnicate has no registered permission; refusing rather than admitting an uncontrolled operation"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
