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

// The orchestrator resolves the audit failure posture from CONFIG, not from a
// ServerOption (go-qrdb#445).
//
// It matters which: an embedder that sets auth.audit_failure_policy in YAML
// and forgets an option would otherwise end up with the authorization gate on
// one posture and the security-officer gate on the other -- exactly the split
// the single-lever ruling exists to remove. Reading it from the same config
// key the authorization middleware reads makes that impossible by
// construction.
//
// NO BUILD CONSTRAINT, matching server_test.go, whose testConfig helper this
// uses.

package server

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// TestStartREST_InvalidAuditFailurePolicyFailsTheStart: a typo must not
// resolve to EITHER posture. Guessing fail-closed denies a fleet the operator
// meant to keep serving; guessing fail-open weakens the posture on a typo. A
// node that cannot tell which was asked for must not choose one.
func TestStartREST_InvalidAuditFailurePolicyFailsTheStart(t *testing.T) {
	cfg := testConfig(t, true, false) // REST only
	cfg.Auth.AuditFailurePolicy = "fail_open"

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("an unrecognised audit_failure_policy must fail the start")
	}
	var invalid *audit.InvalidFailurePolicyError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *audit.InvalidFailurePolicyError, got %T: %v", err, err)
	}
	if invalid.Value != "fail_open" {
		t.Errorf("InvalidFailurePolicyError.Value = %q, want %q", invalid.Value, "fail_open")
	}
}

// TestStartREST_ValidAuditFailurePolicyStarts is the control: a recognised
// value -- and the omitted one, which means fail-closed -- starts normally.
// Without it a check that rejected every value would pass the test above.
func TestStartREST_ValidAuditFailurePolicyStarts(t *testing.T) {
	for _, value := range []string{"", "fail-closed", "fail-open"} {
		t.Run("policy="+value, func(t *testing.T) {
			cfg := testConfig(t, true, false)
			cfg.Auth.AuditFailurePolicy = value

			srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
			if err := srv.Start(); err != nil {
				t.Fatalf("Start with audit_failure_policy=%q: %v", value, err)
			}
			defer srv.Stop()

			if srv.RESTAddr() == "" {
				t.Fatal("expected non-empty RESTAddr")
			}
		})
	}
}
