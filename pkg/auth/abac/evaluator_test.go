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

func TestPolicyEvaluator_Evaluate(t *testing.T) {
	evaluator := NewPolicyEvaluator()

	policy := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata: PolicyMetadata{
			Name:     "test-policy",
			Tenant:   "test-tenant",
			Priority: 100,
		},
		Spec: PolicySpec{
			Subjects: SubjectSelector{
				Authenticated: true,
			},
			Rules: []Rule{
				{
					Name:      "allow-read",
					Effect:    EffectAllow,
					Actions:   []string{"read"},
					Resources: []string{"/data/*"},
				},
				{
					Name:      "deny-delete",
					Effect:    EffectDeny,
					Actions:   []string{"delete"},
					Resources: []string{"/data/*"},
				},
			},
		},
	}

	if err := evaluator.AddPolicies(policy); err != nil {
		t.Fatalf("Failed to compile policy: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		req         *CheckRequest
		wantAllowed bool
		wantReason  string
	}{
		{
			name: "valid request - allowed",
			req: &CheckRequest{
				Subject: Subject{
					ID:            "user-123",
					TenantID:      "test-tenant",
					Authenticated: true,
				},
				Action:   "read",
				Resource: "/data/file.txt",
			},
			wantAllowed: true,
		},
		{
			name: "valid request - denied by explicit deny",
			req: &CheckRequest{
				Subject: Subject{
					ID:            "user-123",
					TenantID:      "test-tenant",
					Authenticated: true,
				},
				Action:   "delete",
				Resource: "/data/file.txt",
			},
			wantAllowed: false,
		},
		{
			name: "no matching rule - default deny",
			req: &CheckRequest{
				Subject: Subject{
					ID:            "user-123",
					TenantID:      "test-tenant",
					Authenticated: true,
				},
				Action:   "write",
				Resource: "/data/file.txt",
			},
			wantAllowed: false,
			wantReason:  "no matching allow policy",
		},
		{
			name:        "nil request",
			req:         nil,
			wantAllowed: false,
			wantReason:  "invalid request: subject required",
		},
		{
			name: "missing subject ID",
			req: &CheckRequest{
				Subject: Subject{
					TenantID:      "test-tenant",
					Authenticated: true,
				},
				Action:   "read",
				Resource: "/data/file.txt",
			},
			wantAllowed: false,
			wantReason:  "invalid request: subject required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := evaluator.Evaluate(ctx, tt.req)
			if err != nil {
				t.Errorf("Evaluate() error = %v", err)
				return
			}
			if resp.Allowed != tt.wantAllowed {
				t.Errorf("Evaluate() allowed = %v, want %v", resp.Allowed, tt.wantAllowed)
			}
			if tt.wantReason != "" && resp.Reason != tt.wantReason {
				t.Errorf("Evaluate() reason = %v, want %v", resp.Reason, tt.wantReason)
			}
			if resp.EvaluationTimeNs <= 0 {
				t.Error("EvaluationTimeNs should be > 0")
			}
		})
	}
}

func TestPolicyEvaluator_ActionMatching(t *testing.T) {
	evaluator := &PolicyEvaluator{}

	tests := []struct {
		name          string
		ruleActions   []string
		requestAction string
		wantMatch     bool
	}{
		{name: "exact match", ruleActions: []string{"read", "write"}, requestAction: "read", wantMatch: true},
		{name: "wildcard match", ruleActions: []string{"*"}, requestAction: "anything", wantMatch: true},
		{name: "prefix match", ruleActions: []string{"write*"}, requestAction: "write:key", wantMatch: true},
		{name: "prefix match exact", ruleActions: []string{"write*"}, requestAction: "write", wantMatch: true},
		{name: "no match", ruleActions: []string{"read", "list"}, requestAction: "delete", wantMatch: false},
		{name: "empty list", ruleActions: []string{}, requestAction: "read", wantMatch: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := evaluator.matchesAction(tt.ruleActions, tt.requestAction); result != tt.wantMatch {
				t.Errorf("matchesAction() = %v, want %v", result, tt.wantMatch)
			}
		})
	}
}

func TestPolicyEvaluator_SubjectMatchingEdgeCases(t *testing.T) {
	evaluator := NewPolicyEvaluator()
	ctx := context.Background()

	tests := []struct {
		name        string
		policy      *Policy
		subject     Subject
		action      string
		resource    string
		wantAllowed bool
	}{
		{
			name: "service matching",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "svc", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Services: []string{"api-service"}},
					Rules:    []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"*"}, Resources: []string{"/**"}}},
				},
			},
			subject:     Subject{ID: "api-service", TenantID: "test-tenant", Authenticated: true},
			action:      "write",
			resource:    "/data/file.txt",
			wantAllowed: true,
		},
		{
			name: "empty matcher matches all",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "empty", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{},
					Rules:    []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/public/*"}}},
				},
			},
			subject:     Subject{ID: "anyone", TenantID: "test-tenant"},
			action:      "read",
			resource:    "/public/data.txt",
			wantAllowed: true,
		},
		{
			name: "authenticated only",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "auth", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Authenticated: true},
					Rules:    []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/data/*"}}},
				},
			},
			subject:     Subject{ID: "user", TenantID: "test-tenant", Authenticated: true},
			action:      "read",
			resource:    "/data/file.txt",
			wantAllowed: true,
		},
		{
			name: "group match",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "grp", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Groups: []string{"editors"}},
					Rules:    []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/data/*"}}},
				},
			},
			subject:     Subject{ID: "user", TenantID: "test-tenant", Groups: []string{"users", "editors"}, Authenticated: true},
			action:      "read",
			resource:    "/data/file.txt",
			wantAllowed: true,
		},
		{
			name: "role match",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "role", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Roles: []string{"admin"}},
					Rules:    []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"delete"}, Resources: []string{"/data/*"}}},
				},
			},
			subject:     Subject{ID: "user", TenantID: "test-tenant", Roles: []string{"admin"}, Authenticated: true},
			action:      "delete",
			resource:    "/data/file.txt",
			wantAllowed: true,
		},
		{
			name: "no match with constraints",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "nomatch", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Users: []string{"admin"}, Groups: []string{"admins"}},
					Rules:    []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/data/*"}}},
				},
			},
			subject:     Subject{ID: "user", TenantID: "test-tenant", Groups: []string{"users"}, Authenticated: true},
			action:      "read",
			resource:    "/data/file.txt",
			wantAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := evaluator.AddPolicies(tt.policy); err != nil {
				t.Fatalf("Failed to compile: %v", err)
			}
			resp, err := evaluator.Evaluate(ctx, &CheckRequest{Subject: tt.subject, Action: tt.action, Resource: tt.resource})
			if err != nil {
				t.Errorf("Evaluate() error = %v", err)
			}
			if resp.Allowed != tt.wantAllowed {
				t.Errorf("allowed = %v, want %v", resp.Allowed, tt.wantAllowed)
			}
			evaluator.Reset()
		})
	}
}

func TestPolicyEvaluator_NoPolicies(t *testing.T) {
	evaluator := NewPolicyEvaluator()
	resp, err := evaluator.Evaluate(context.Background(), &CheckRequest{
		Subject:  Subject{ID: "user", TenantID: "test", Authenticated: true},
		Action:   "read",
		Resource: "/data/file.txt",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Error("should deny with no policies")
	}
	if resp.Reason != "no matching allow policy" {
		t.Errorf("reason = %v, want 'no matching allow policy'", resp.Reason)
	}
}

func TestPolicyEvaluator_ResourceTrieNoMatch(t *testing.T) {
	evaluator := NewPolicyEvaluator()

	policy := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata:   PolicyMetadata{Name: "p1", Tenant: "test-tenant"},
		Spec: PolicySpec{
			Rules: []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/data/*"}}},
		},
	}

	if err := evaluator.AddPolicies(policy); err != nil {
		t.Fatalf("Failed to compile policy: %v", err)
	}

	resp, _ := evaluator.Evaluate(context.Background(), &CheckRequest{
		Subject:  Subject{ID: "user", TenantID: "test-tenant", Authenticated: true},
		Action:   "read",
		Resource: "/admin/config.txt",
	})

	if resp.Allowed {
		t.Error("should deny when resource doesn't match")
	}
}

func TestPolicyEvaluator_AttributeMatchers(t *testing.T) {
	evaluator := NewPolicyEvaluator()

	tests := []struct {
		name        string
		match       map[string]interface{}
		attrs       map[string]interface{}
		wantAllowed bool
	}{
		{
			name:        "exact match passes",
			match:       map[string]interface{}{"department": "engineering"},
			attrs:       map[string]interface{}{"department": "engineering"},
			wantAllowed: true,
		},
		{
			name:        "exact match fails",
			match:       map[string]interface{}{"department": "engineering"},
			attrs:       map[string]interface{}{"department": "sales"},
			wantAllowed: false,
		},
		{
			name:        "numeric gte passes",
			match:       map[string]interface{}{"level": map[string]interface{}{"gte": 3}},
			attrs:       map[string]interface{}{"level": 5},
			wantAllowed: true,
		},
		{
			name:        "numeric gte fails",
			match:       map[string]interface{}{"level": map[string]interface{}{"gte": 5}},
			attrs:       map[string]interface{}{"level": 3},
			wantAllowed: false,
		},
		{
			name: "multiple matchers all pass",
			match: map[string]interface{}{
				"department": "engineering",
				"level":      map[string]interface{}{"gte": 3},
			},
			attrs:       map[string]interface{}{"department": "engineering", "level": 5},
			wantAllowed: true,
		},
		{
			name: "multiple matchers one fails",
			match: map[string]interface{}{
				"department": "engineering",
				"level":      map[string]interface{}{"gte": 10},
			},
			attrs:       map[string]interface{}{"department": "engineering", "level": 5},
			wantAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "attr", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Subjects: SubjectSelector{Match: tt.match},
					Rules:    []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/data/*"}}},
				},
			}

			if err := evaluator.AddPolicies(policy); err != nil {
				t.Fatalf("compile error: %v", err)
			}

			resp, err := evaluator.Evaluate(context.Background(), &CheckRequest{
				Subject:  Subject{ID: "user", TenantID: "test-tenant", Authenticated: true, Attributes: tt.attrs},
				Action:   "read",
				Resource: "/data/file.txt",
			})

			if err != nil {
				t.Errorf("Evaluate error: %v", err)
			}
			if resp.Allowed != tt.wantAllowed {
				t.Errorf("allowed = %v, want %v", resp.Allowed, tt.wantAllowed)
			}

			evaluator.Reset()
		})
	}
}

func BenchmarkPolicyEvaluator_Evaluate(b *testing.B) {
	evaluator := NewPolicyEvaluator()

	policy := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata:   PolicyMetadata{Name: "bench", Tenant: "test-tenant", Priority: 100},
		Spec: PolicySpec{
			Subjects: SubjectSelector{Authenticated: true},
			Rules:    []Rule{{Name: "r1", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/data/*"}}},
		},
	}

	if err := evaluator.AddPolicies(policy); err != nil {
		b.Fatalf("Failed to compile policy: %v", err)
	}

	req := &CheckRequest{
		Subject:  Subject{ID: "user-123", TenantID: "test-tenant", Authenticated: true},
		Action:   "read",
		Resource: "/data/file.txt",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = evaluator.Evaluate(context.Background(), req)
	}
}

func TestPolicyEvaluator_ConditionPaths(t *testing.T) {
	evaluator := NewPolicyEvaluator()
	ctx := context.Background()

	tests := []struct {
		name        string
		policy      *Policy
		subject     Subject
		wantAllowed bool
	}{
		{
			name: "unless condition blocks rule",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "unless", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Rules: []Rule{{
						Name:      "r1",
						Effect:    EffectAllow,
						Actions:   []string{"read"},
						Resources: []string{"/data/*"},
						Unless:    []Condition{{Expression: "subject.attrs.blocked"}},
					}},
				},
			},
			subject:     Subject{ID: "user", TenantID: "test-tenant", Authenticated: true, Attributes: map[string]interface{}{"blocked": true}},
			wantAllowed: false,
		},
		{
			name: "when condition passes",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "when", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Rules: []Rule{{
						Name:      "r1",
						Effect:    EffectAllow,
						Actions:   []string{"read"},
						Resources: []string{"/data/*"},
						When:      []Condition{{Expression: "subject.attrs.active"}},
					}},
				},
			},
			subject:     Subject{ID: "user", TenantID: "test-tenant", Authenticated: true, Attributes: map[string]interface{}{"active": true}},
			wantAllowed: true,
		},
		{
			name: "when condition fails",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata:   PolicyMetadata{Name: "when-fail", Tenant: "test-tenant"},
				Spec: PolicySpec{
					Rules: []Rule{{
						Name:      "r1",
						Effect:    EffectAllow,
						Actions:   []string{"read"},
						Resources: []string{"/data/*"},
						When:      []Condition{{Expression: "subject.attrs.premium"}},
					}},
				},
			},
			subject:     Subject{ID: "user", TenantID: "test-tenant", Authenticated: true, Attributes: map[string]interface{}{"premium": false}},
			wantAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := evaluator.AddPolicies(tt.policy); err != nil {
				t.Fatalf("compile error: %v", err)
			}

			resp, err := evaluator.Evaluate(ctx, &CheckRequest{
				Subject:  tt.subject,
				Action:   "read",
				Resource: "/data/file.txt",
			})

			if err != nil {
				t.Errorf("Evaluate error: %v", err)
			}
			if resp.Allowed != tt.wantAllowed {
				t.Errorf("allowed = %v, want %v", resp.Allowed, tt.wantAllowed)
			}

			evaluator.Reset()
		})
	}
}

// TestValidateCapturedVars tests the validateCapturedVars function
// which validates captured variables from resource matching.
func TestValidateCapturedVars(t *testing.T) {
	evaluator := &PolicyEvaluator{}

	tests := []struct {
		name         string
		capturedVars map[string]string
		subject      *Subject
		wantResult   bool
	}{
		{
			name:         "empty captured vars",
			capturedVars: map[string]string{},
			subject:      &Subject{ID: "user1"},
			wantResult:   true,
		},
		{
			name:         "non-subject variable",
			capturedVars: map[string]string{"resource_id": "123"},
			subject:      &Subject{ID: "user1"},
			wantResult:   true,
		},
		{
			name:         "subject.id matches",
			capturedVars: map[string]string{"subject.id": "user1"},
			subject:      &Subject{ID: "user1"},
			wantResult:   true,
		},
		{
			name:         "subject.id does not match",
			capturedVars: map[string]string{"subject.id": "user2"},
			subject:      &Subject{ID: "user1"},
			wantResult:   false,
		},
		{
			name:         "subject.name matches",
			capturedVars: map[string]string{"subject.name": "John Doe"},
			subject:      &Subject{ID: "user1", Name: "John Doe"},
			wantResult:   true,
		},
		{
			name:         "subject.name does not match",
			capturedVars: map[string]string{"subject.name": "Jane Doe"},
			subject:      &Subject{ID: "user1", Name: "John Doe"},
			wantResult:   false,
		},
		{
			name:         "subject.email matches",
			capturedVars: map[string]string{"subject.email": "user@example.com"},
			subject:      &Subject{ID: "user1", Email: "user@example.com"},
			wantResult:   true,
		},
		{
			name:         "subject.email does not match",
			capturedVars: map[string]string{"subject.email": "other@example.com"},
			subject:      &Subject{ID: "user1", Email: "user@example.com"},
			wantResult:   false,
		},
		{
			name:         "subject.tenant matches",
			capturedVars: map[string]string{"subject.tenant": "tenant1"},
			subject:      &Subject{ID: "user1", TenantID: "tenant1"},
			wantResult:   true,
		},
		{
			name:         "subject.tenant does not match",
			capturedVars: map[string]string{"subject.tenant": "tenant2"},
			subject:      &Subject{ID: "user1", TenantID: "tenant1"},
			wantResult:   false,
		},
		{
			name:         "subject.tenant_id matches",
			capturedVars: map[string]string{"subject.tenant_id": "tenant1"},
			subject:      &Subject{ID: "user1", TenantID: "tenant1"},
			wantResult:   true,
		},
		{
			name:         "subject.tenant_id does not match",
			capturedVars: map[string]string{"subject.tenant_id": "tenant2"},
			subject:      &Subject{ID: "user1", TenantID: "tenant1"},
			wantResult:   false,
		},
		{
			name:         "subject attribute from map matches",
			capturedVars: map[string]string{"subject.department": "engineering"},
			subject:      &Subject{ID: "user1", Attributes: map[string]interface{}{"department": "engineering"}},
			wantResult:   true,
		},
		{
			name:         "subject attribute from map does not match",
			capturedVars: map[string]string{"subject.department": "sales"},
			subject:      &Subject{ID: "user1", Attributes: map[string]interface{}{"department": "engineering"}},
			wantResult:   false,
		},
		{
			name:         "subject attribute not in map - passes",
			capturedVars: map[string]string{"subject.department": "engineering"},
			subject:      &Subject{ID: "user1", Attributes: nil},
			wantResult:   true,
		},
		{
			name:         "subject attribute not a string - passes",
			capturedVars: map[string]string{"subject.level": "5"},
			subject:      &Subject{ID: "user1", Attributes: map[string]interface{}{"level": 5}},
			wantResult:   true,
		},
		{
			name: "multiple captured vars all match",
			capturedVars: map[string]string{
				"subject.id":        "user1",
				"subject.tenant_id": "tenant1",
			},
			subject:    &Subject{ID: "user1", TenantID: "tenant1"},
			wantResult: true,
		},
		{
			name: "multiple captured vars one does not match",
			capturedVars: map[string]string{
				"subject.id":        "user1",
				"subject.tenant_id": "tenant2",
			},
			subject:    &Subject{ID: "user1", TenantID: "tenant1"},
			wantResult: false,
		},
		{
			name:         "short subject variable name",
			capturedVars: map[string]string{"subject": "value"},
			subject:      &Subject{ID: "user1"},
			wantResult:   true,
		},
		{
			name:         "unknown subject attribute",
			capturedVars: map[string]string{"subject.unknown_field": "value"},
			subject:      &Subject{ID: "user1"},
			wantResult:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluator.validateCapturedVars(tt.capturedVars, tt.subject)
			if result != tt.wantResult {
				t.Errorf("validateCapturedVars() = %v, want %v", result, tt.wantResult)
			}
		})
	}
}

// TestResourceTrieExtractVariableName tests the extractVariableName function
func TestResourceTrieExtractVariableName(t *testing.T) {
	tests := []struct {
		name     string
		segment  string
		wantName string
	}{
		{
			name:     "dollar-brace format",
			segment:  "${userId}",
			wantName: "userId",
		},
		{
			name:     "colon format",
			segment:  ":userId",
			wantName: "userId",
		},
		{
			name:     "brace format",
			segment:  "{userId}",
			wantName: "userId",
		},
		{
			name:     "plain segment - returned as is",
			segment:  "userId",
			wantName: "userId",
		},
		{
			name:     "empty dollar-brace",
			segment:  "${}",
			wantName: "",
		},
		{
			name:     "empty brace",
			segment:  "{}",
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractVariableName(tt.segment)
			if result != tt.wantName {
				t.Errorf("extractVariableName(%q) = %q, want %q", tt.segment, result, tt.wantName)
			}
		})
	}
}

// TestResourceTrieSplitPath tests the splitPath function
func TestResourceTrieSplitPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantResult []string
	}{
		{
			name:       "empty path",
			path:       "",
			wantResult: []string{},
		},
		{
			name:       "single segment",
			path:       "users",
			wantResult: []string{"users"},
		},
		{
			name:       "multiple segments",
			path:       "users/123/profile",
			wantResult: []string{"users", "123", "profile"},
		},
		{
			name:       "leading slash",
			path:       "/users/123",
			wantResult: []string{"users", "123"},
		},
		{
			name:       "trailing slash",
			path:       "users/123/",
			wantResult: []string{"users", "123"},
		},
		{
			name:       "both leading and trailing slash",
			path:       "/users/123/",
			wantResult: []string{"users", "123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitPath(tt.path)
			if len(result) != len(tt.wantResult) {
				t.Errorf("splitPath(%q) returned %d segments, want %d", tt.path, len(result), len(tt.wantResult))
				return
			}
			for i := range result {
				if result[i] != tt.wantResult[i] {
					t.Errorf("splitPath(%q)[%d] = %q, want %q", tt.path, i, result[i], tt.wantResult[i])
				}
			}
		})
	}
}

func TestResourceTrieCopyVars(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := copyVars(nil)
		if result == nil {
			t.Error("expected non-nil map")
		}
		if len(result) != 0 {
			t.Errorf("expected empty map, got %d entries", len(result))
		}
	})

	t.Run("non-nil input", func(t *testing.T) {
		original := map[string]string{"key": "value"}
		result := copyVars(original)
		if result == nil {
			t.Error("expected non-nil map")
		}
		if result["key"] != "value" {
			t.Error("value not copied")
		}
		// Modify original to ensure copy is independent
		original["key"] = "modified"
		if result["key"] == "modified" {
			t.Error("copy is not independent")
		}
	})
}

// TestResourceTrieIsVariable tests the isVariable helper function
func TestResourceTrieIsVariable(t *testing.T) {
	tests := []struct {
		name    string
		segment string
		want    bool
	}{
		{"dollar-brace format", "${userId}", true},
		{"colon format", ":userId", true},
		{"brace format", "{userId}", true},
		{"plain segment", "users", false},
		{"empty segment", "", false},
		{"single colon", ":", false},
		{"incomplete dollar-brace", "${", false},
		{"incomplete brace", "{", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isVariable(tt.segment)
			if result != tt.want {
				t.Errorf("isVariable(%q) = %v, want %v", tt.segment, result, tt.want)
			}
		})
	}
}

// TestResourceTrieDeleteSegments tests the deleteSegments path
func TestResourceTrieDeleteSegments(t *testing.T) {
	trie := NewResourceTrie()

	// Insert patterns including variables
	trie.Insert("/users/${userId}/profile", "user-profile")
	trie.Insert("/users/:id/settings", "user-settings")
	trie.Insert("/admin/**", "admin-all")

	t.Run("delete with dollar-brace variable", func(t *testing.T) {
		deleted := trie.Delete("/users/${userId}/profile")
		if !deleted {
			t.Error("expected deletion to succeed")
		}
	})

	t.Run("delete with colon variable", func(t *testing.T) {
		deleted := trie.Delete("/users/:id/settings")
		if !deleted {
			t.Error("expected deletion to succeed")
		}
	})

	t.Run("delete non-existent pattern", func(t *testing.T) {
		deleted := trie.Delete("/nonexistent/path")
		if deleted {
			t.Error("expected deletion to fail for non-existent")
		}
	})
}

// TestPEPWithNilConfig tests PEP creation with nil config
