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

// The audit failure posture reaching the SO dual-factor middleware
// (go-qrdb#445).
//
// NO BUILD CONSTRAINT, deliberately, unlike server_options_test.go beside it.
// This is the wiring half of a security property that ships in every build,
// and it uses nothing but NewServer and pkg/audit, so it straddles no tag.

package rest

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// TestWithAuditFailurePolicy_Applied: the operator's posture reaches the
// server that installs SOMiddleware.
func TestWithAuditFailurePolicy_Applied(t *testing.T) {
	srv := NewServer(nil, nil, ":0", WithAuditFailurePolicy(audit.FailOpen))
	if srv.auditFailure != audit.FailOpen {
		t.Fatalf("auditFailure = %v, want audit.FailOpen", srv.auditFailure)
	}
}

// TestWithAuditFailurePolicy_DefaultIsFailClosed asserts the DEFAULT directly
// rather than inheriting it from the option that sets it. A server built
// without the option -- by an embedder, a test, or a future constructor nobody
// remembered to update -- must gate rather than serve unrecorded.
func TestWithAuditFailurePolicy_DefaultIsFailClosed(t *testing.T) {
	srv := NewServer(nil, nil, ":0")
	if srv.auditFailure != audit.FailClosed {
		t.Fatalf("an unset posture is %v, want audit.FailClosed", srv.auditFailure)
	}
}
