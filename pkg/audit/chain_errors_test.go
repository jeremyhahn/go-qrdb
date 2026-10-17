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

package audit

import (
	"errors"
	"strings"
	"testing"
)

func TestEncodeEventError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("unsupported type")
	e := &EncodeEventError{Err: cause}
	if !strings.Contains(e.Error(), "unsupported type") {
		t.Fatalf("Error() = %q, want it to mention the cause", e.Error())
	}
	if e.Unwrap() != cause {
		t.Fatal("Unwrap() did not return the wrapped cause")
	}
}

func TestMissingTimestampError_Error(t *testing.T) {
	e := &MissingTimestampError{}
	if e.Error() == "" {
		t.Fatal("Error() returned an empty string")
	}
}

func TestInvalidCursorError_Error(t *testing.T) {
	e := &InvalidCursorError{Cursor: "garbage"}
	if !strings.Contains(e.Error(), "garbage") {
		t.Fatalf("Error() = %q, want it to mention the offending cursor", e.Error())
	}
}

func TestTenantMismatchError_Error(t *testing.T) {
	e := &TenantMismatchError{EventTenantID: "acme", RoutingTenantID: "other"}
	msg := e.Error()
	if !strings.Contains(msg, "acme") || !strings.Contains(msg, "other") {
		t.Fatalf("Error() = %q, want it to mention both tenant ids", msg)
	}
}
