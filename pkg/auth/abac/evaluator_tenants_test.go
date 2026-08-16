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

package abac

import (
	"context"
	"testing"
)

// TestPolicyEvaluator_DenyOverridesAcrossPolicies proves the two-pass
// deny-overrides combining: an allow rule in one policy can never shadow a
// deny rule in another applicable policy, regardless of policy priority.
func TestPolicyEvaluator_DenyOverridesAcrossPolicies(t *testing.T) {
	evaluator := NewPolicyEvaluator()

	allow := testPolicy("allow-first", "t1", Rule{
		Name: "allow-all", Effect: EffectAllow, Actions: []string{"*"}, Resources: []string{"/**"},
	})
	allow.Metadata.Priority = 100
	deny := testPolicy("deny-second", "t1", Rule{
		Name: "deny-write", Effect: EffectDeny, Actions: []string{"write"}, Resources: []string{"/kv/**"},
	})
	deny.Metadata.Priority = 1

	if err := evaluator.AddPolicies(allow, deny); err != nil {
		t.Fatalf("AddPolicies() error = %v", err)
	}

	resp, err := evaluator.Evaluate(context.Background(), &CheckRequest{
		Subject:  Subject{ID: "u1", TenantID: "t1", Authenticated: true},
		Action:   "write",
		Resource: "/kv/x",
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if resp.Allowed {
		t.Error("deny in a lower-priority policy must override the allow")
	}
	if resp.MatchedPolicy != "deny-second" || resp.MatchedRule != "deny-write" {
		t.Errorf("matched %q/%q, want deny-second/deny-write", resp.MatchedPolicy, resp.MatchedRule)
	}
}

// TestPolicyEvaluator_EvaluateTenants proves the tenant union semantics
// used by the decorator: the target tenant's policies and the system
// tenant's policies both apply, other tenants' policies never do, and
// duplicate tenant IDs are evaluated once.
func TestPolicyEvaluator_EvaluateTenants(t *testing.T) {
	evaluator := NewPolicyEvaluator()

	if err := evaluator.AddPolicies(
		testPolicy("t1-allow", "t1", Rule{
			Name: "r", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/t1/**"},
		}),
		testPolicy("sys-deny", "system", Rule{
			Name: "r", Effect: EffectDeny, Actions: []string{"read"}, Resources: []string{"/t1/secret/**"},
		}),
		testPolicy("t2-deny", "t2", Rule{
			Name: "r", Effect: EffectDeny, Actions: []string{"*"}, Resources: []string{"/**"},
		}),
	); err != nil {
		t.Fatalf("AddPolicies() error = %v", err)
	}

	subject := Subject{ID: "u1", TenantID: "t1", Authenticated: true}

	t.Run("target tenant allow applies", func(t *testing.T) {
		resp, err := evaluator.EvaluateTenants(context.Background(), &CheckRequest{
			Subject: subject, Action: "read", Resource: "/t1/public/doc",
		}, []string{"t1", "system"})
		if err != nil {
			t.Fatalf("EvaluateTenants() error = %v", err)
		}
		if !resp.Allowed || resp.MatchedPolicy != "t1-allow" {
			t.Errorf("got allowed=%v policy=%q, want allow by t1-allow", resp.Allowed, resp.MatchedPolicy)
		}
	})

	t.Run("system deny overrides tenant allow", func(t *testing.T) {
		resp, err := evaluator.EvaluateTenants(context.Background(), &CheckRequest{
			Subject: subject, Action: "read", Resource: "/t1/secret/key",
		}, []string{"t1", "system"})
		if err != nil {
			t.Fatalf("EvaluateTenants() error = %v", err)
		}
		if resp.Allowed || resp.MatchedPolicy != "sys-deny" {
			t.Errorf("got allowed=%v policy=%q, want deny by sys-deny", resp.Allowed, resp.MatchedPolicy)
		}
	})

	t.Run("foreign tenant policies excluded", func(t *testing.T) {
		resp, err := evaluator.EvaluateTenants(context.Background(), &CheckRequest{
			Subject: subject, Action: "read", Resource: "/t1/public/doc",
		}, []string{"t1", "system"})
		if err != nil {
			t.Fatalf("EvaluateTenants() error = %v", err)
		}
		if !resp.Allowed {
			t.Errorf("t2's deny-all must not affect a t1 evaluation: %+v", resp)
		}
	})

	t.Run("duplicate tenant ids evaluated once", func(t *testing.T) {
		resp, err := evaluator.EvaluateTenants(context.Background(), &CheckRequest{
			Subject: subject, Action: "read", Resource: "/t1/public/doc",
		}, []string{"t1", "t1", "system", "system"})
		if err != nil {
			t.Fatalf("EvaluateTenants() error = %v", err)
		}
		if !resp.Allowed {
			t.Errorf("duplicate tenant ids should not change the decision: %+v", resp)
		}
	})
}

// TestPolicyEvaluator_AddPoliciesInvalid proves that a compilation failure
// rejects the whole batch.
func TestPolicyEvaluator_AddPoliciesInvalid(t *testing.T) {
	evaluator := NewPolicyEvaluator()
	bad := testPolicy("bad", "t1") // no rules → validation failure
	if err := evaluator.AddPolicies(bad); err == nil {
		t.Fatal("AddPolicies with an invalid policy should fail")
	}
	if evaluator.Len() != 0 {
		t.Errorf("Len() = %d after failed AddPolicies, want 0", evaluator.Len())
	}
}
