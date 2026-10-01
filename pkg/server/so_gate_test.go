// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// TestSOGate_OnUnderExactlyTheRESTConditions: the orchestrator builds the gate
// the gRPC, Unix, QUIC and MCP transports install under the same conditions
// the REST transport installs SOMiddleware -- auth.so_dual_factor set and an SO
// authenticator wired (soDualFactorEnabled). An auth middleware is not one of
// them: without one the gate is still on, and refuses every SO-gated request.
func TestSOGate_OnUnderExactlyTheRESTConditions(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "t1")
	mw := authmw.New(rbac.New(roles))
	verifier := authtest.SOVerifier(t, "pin", "t1")
	cases := []struct {
		name     string
		flag     bool
		opts     []ServerOption
		wantGate bool
	}{
		{"flag, verifier and middleware", true, []ServerOption{WithSOAuthenticator(verifier), WithAuthMiddleware(mw), WithRoleService(roles)}, true},
		{"flag off", false, []ServerOption{WithSOAuthenticator(verifier), WithAuthMiddleware(mw)}, false},
		{"no verifier", true, []ServerOption{WithAuthMiddleware(mw)}, false},
		{"no auth middleware: on, and fails closed", true, []ServerOption{WithSOAuthenticator(verifier)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, false, false)
			cfg.Auth.SODualFactor = tc.flag
			srv := New(cfg, newMockKVService(), &mockAdminService{}, tc.opts...)
			gate, err := srv.soGate()
			if err != nil {
				t.Fatalf("soGate: %v", err)
			}
			if (gate != nil) != tc.wantGate {
				t.Fatalf("gate = %v, want present=%v", gate, tc.wantGate)
			}
		})
	}
}

// TestSOGate_UnrecognisedAuditPolicyFailsTheStart: a node that cannot tell
// which audit posture was asked for does not choose one, exactly as REST.
func TestSOGate_UnrecognisedAuditPolicyFailsTheStart(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "t1")
	cfg := testConfig(t, false, false)
	cfg.Auth.SODualFactor = true
	cfg.Auth.AuditFailurePolicy = "sometimes"
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithSOAuthenticator(authtest.SOVerifier(t, "pin", "t1")), WithAuthMiddleware(authmw.New(rbac.New(roles))))
	if gate, err := srv.soGate(); err == nil || gate != nil {
		t.Fatalf("soGate = (%v, %v), want an error and no gate", gate, err)
	}
}
