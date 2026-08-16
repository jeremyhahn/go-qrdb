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
	"testing"
)

func TestPolicyCompiler_Compile(t *testing.T) {
	compiler := NewPolicyCompiler()

	tests := []struct {
		name    string
		policy  *Policy
		wantErr bool
	}{
		{
			name: "valid policy with simple rule",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Name:     "test-policy",
					Tenant:   "test-tenant",
					Priority: 100,
				},
				Spec: PolicySpec{
					Rules: []Rule{
						{
							Name:      "allow-read",
							Effect:    EffectAllow,
							Actions:   []string{"read"},
							Resources: []string{"/orders/*"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "policy with variables",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Name:   "var-policy",
					Tenant: "test-tenant",
				},
				Spec: PolicySpec{
					Variables: map[string]string{
						"department": "engineering",
					},
					Rules: []Rule{
						{
							Name:      "allow-dept-access",
							Effect:    EffectAllow,
							Actions:   []string{"read"},
							Resources: []string{"/dept/${department}/*"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "policy with conditions",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Name:   "conditional-policy",
					Tenant: "test-tenant",
				},
				Spec: PolicySpec{
					Rules: []Rule{
						{
							Name:      "time-based-access",
							Effect:    EffectAllow,
							Actions:   []string{"read"},
							Resources: []string{"/data/*"},
							When: []Condition{
								{Expression: "time.hour >= 9"},
								{Expression: "time.hour < 17"},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "policy with unless conditions",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Name:   "unless-policy",
					Tenant: "test-tenant",
				},
				Spec: PolicySpec{
					Rules: []Rule{
						{
							Name:      "allow-unless-weekend",
							Effect:    EffectAllow,
							Actions:   []string{"write"},
							Resources: []string{"/data/*"},
							Unless: []Condition{
								{Expression: "time.weekday == \"sat\""},
								{Expression: "time.weekday == \"sun\""},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "policy with subject selectors",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Name:   "subject-policy",
					Tenant: "test-tenant",
				},
				Spec: PolicySpec{
					Subjects: SubjectSelector{
						Users:  []string{"user-1", "user-2"},
						Groups: []string{"admins"},
						Roles:  []string{"admin"},
					},
					Rules: []Rule{
						{
							Name:      "admin-access",
							Effect:    EffectAllow,
							Actions:   []string{"*"},
							Resources: []string{"/*"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "policy with attribute matching",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Name:   "attr-policy",
					Tenant: "test-tenant",
				},
				Spec: PolicySpec{
					Subjects: SubjectSelector{
						Match: map[string]interface{}{
							"department": "engineering",
							"level": map[string]interface{}{
								"gte": 5,
							},
						},
					},
					Rules: []Rule{
						{
							Name:      "senior-eng-access",
							Effect:    EffectAllow,
							Actions:   []string{"deploy"},
							Resources: []string{"/production/*"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid policy - missing name",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Tenant: "test-tenant",
				},
				Spec: PolicySpec{
					Rules: []Rule{
						{
							Name:      "test-rule",
							Effect:    EffectAllow,
							Actions:   []string{"read"},
							Resources: []string{"/data/*"},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid policy - no rules",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Name:   "empty-policy",
					Tenant: "test-tenant",
				},
				Spec: PolicySpec{
					Rules: []Rule{},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid policy - invalid condition",
			policy: &Policy{
				APIVersion: "abac.quicbaas.io/v1",
				Kind:       "Policy",
				Metadata: PolicyMetadata{
					Name:   "bad-condition-policy",
					Tenant: "test-tenant",
				},
				Spec: PolicySpec{
					Rules: []Rule{
						{
							Name:      "bad-rule",
							Effect:    EffectAllow,
							Actions:   []string{"read"},
							Resources: []string{"/data/*"},
							When: []Condition{
								{Expression: ""},
							},
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, err := compiler.Compile(tt.policy)

			if (err != nil) != tt.wantErr {
				t.Errorf("Compile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if compiled == nil {
					t.Error("Compile() returned nil without error")
					return
				}

				if compiled.Name != tt.policy.Metadata.Name {
					t.Errorf("Compiled policy name = %v, want %v", compiled.Name, tt.policy.Metadata.Name)
				}

				if len(compiled.Rules) != len(tt.policy.Spec.Rules) {
					t.Errorf("Compiled rules count = %v, want %v", len(compiled.Rules), len(tt.policy.Spec.Rules))
				}
			}
		})
	}
}

func TestPolicyCompiler_CachingBehavior(t *testing.T) {
	compiler := NewPolicyCompiler()

	policy := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata: PolicyMetadata{
			Name:   "cache-test-policy",
			Tenant: "test-tenant",
		},
		Spec: PolicySpec{
			Rules: []Rule{
				{
					Name:      "test-rule",
					Effect:    EffectAllow,
					Actions:   []string{"read"},
					Resources: []string{"/data/*"},
				},
			},
		},
	}

	// First compilation
	compiled1, err := compiler.Compile(policy)
	if err != nil {
		t.Fatalf("First Compile() error = %v", err)
	}

	// Second compilation - should use cache
	compiled2, err := compiler.Compile(policy)
	if err != nil {
		t.Fatalf("Second Compile() error = %v", err)
	}

	// Should return same instance from cache
	if compiled1 != compiled2 {
		t.Error("Compile() should return cached instance")
	}

	// Clear cache
	compiler.ClearCache()

	// Third compilation - should recompile
	compiled3, err := compiler.Compile(policy)
	if err != nil {
		t.Fatalf("Third Compile() error = %v", err)
	}

	// Should be different instance after cache clear
	if compiled1 == compiled3 {
		t.Error("Compile() should recompile after cache clear")
	}
}

func TestPolicyCompiler_RulePrioritization(t *testing.T) {
	compiler := NewPolicyCompiler()

	policy := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata: PolicyMetadata{
			Name:   "priority-test",
			Tenant: "test-tenant",
		},
		Spec: PolicySpec{
			Rules: []Rule{
				{
					Name:      "allow-rule",
					Effect:    EffectAllow,
					Priority:  10,
					Actions:   []string{"read"},
					Resources: []string{"/data/*"},
				},
				{
					Name:      "deny-rule",
					Effect:    EffectDeny,
					Priority:  5,
					Actions:   []string{"read"},
					Resources: []string{"/data/*"},
				},
				{
					Name:      "low-priority-allow",
					Effect:    EffectAllow,
					Priority:  1,
					Actions:   []string{"write"},
					Resources: []string{"/data/*"},
				},
			},
		},
	}

	compiled, err := compiler.Compile(policy)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	// Verify deny rules come first
	if len(compiled.Rules) < 1 {
		t.Fatal("Expected at least one compiled rule")
	}

	// First rule should be the deny rule (deny rules prioritized)
	if compiled.Rules[0].Effect != EffectDeny {
		t.Error("First rule should be deny rule")
	}
}

func TestCompileComplexAttributeMatcher(t *testing.T) {
	compiler := NewPolicyCompiler()

	tests := []struct {
		name     string
		key      string
		matchers map[string]interface{}
		wantType string
		wantErr  bool
	}{
		{
			name: "gte operator",
			key:  "level",
			matchers: map[string]interface{}{
				"gte": 5,
			},
			wantType: "numeric",
			wantErr:  false,
		},
		{
			name: ">= operator (symbol)",
			key:  "level",
			matchers: map[string]interface{}{
				">=": 10,
			},
			wantType: "numeric",
			wantErr:  false,
		},
		{
			name: "gt operator",
			key:  "age",
			matchers: map[string]interface{}{
				"gt": 18,
			},
			wantType: "numeric",
			wantErr:  false,
		},
		{
			name: "> operator (symbol)",
			key:  "age",
			matchers: map[string]interface{}{
				">": 21,
			},
			wantType: "numeric",
			wantErr:  false,
		},
		{
			name: "lte operator",
			key:  "score",
			matchers: map[string]interface{}{
				"lte": 100,
			},
			wantType: "numeric",
			wantErr:  false,
		},
		{
			name: "<= operator (symbol)",
			key:  "score",
			matchers: map[string]interface{}{
				"<=": 50,
			},
			wantType: "numeric",
			wantErr:  false,
		},
		{
			name: "lt operator",
			key:  "priority",
			matchers: map[string]interface{}{
				"lt": 10,
			},
			wantType: "numeric",
			wantErr:  false,
		},
		{
			name: "< operator (symbol)",
			key:  "priority",
			matchers: map[string]interface{}{
				"<": 5,
			},
			wantType: "numeric",
			wantErr:  false,
		},
		{
			name: "in operator with string slice",
			key:  "role",
			matchers: map[string]interface{}{
				"in": []string{"admin", "moderator", "superuser"},
			},
			wantType: "in",
			wantErr:  false,
		},
		{
			name: "in operator with interface slice",
			key:  "department",
			matchers: map[string]interface{}{
				"in": []interface{}{"eng", "sales", "ops"},
			},
			wantType: "in",
			wantErr:  false,
		},
		{
			name: "endswith operator",
			key:  "email",
			matchers: map[string]interface{}{
				"endswith": "@example.com",
			},
			wantType: "string",
			wantErr:  false,
		},
		{
			name: "startswith operator",
			key:  "email",
			matchers: map[string]interface{}{
				"startswith": "admin",
			},
			wantType: "string",
			wantErr:  false,
		},
		{
			name: "contains operator",
			key:  "description",
			matchers: map[string]interface{}{
				"contains": "urgent",
			},
			wantType: "string",
			wantErr:  false,
		},
		{
			name: "unsupported operator",
			key:  "field",
			matchers: map[string]interface{}{
				"regex": ".*pattern.*",
			},
			wantType: "",
			wantErr:  true,
		},
		{
			name:     "empty matchers map",
			key:      "field",
			matchers: map[string]interface{}{},
			wantType: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := compiler.compileComplexAttributeMatcher(tt.key, tt.matchers)

			if (err != nil) != tt.wantErr {
				t.Errorf("compileComplexAttributeMatcher() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if matcher == nil {
					t.Error("Expected non-nil matcher")
					return
				}

				// Verify matcher type by testing behavior
				switch tt.wantType {
				case "numeric":
					if _, ok := matcher.(*numericAttributeMatcher); !ok {
						t.Errorf("Expected numericAttributeMatcher, got %T", matcher)
					}
				case "string":
					if _, ok := matcher.(*stringAttributeMatcher); !ok {
						t.Errorf("Expected stringAttributeMatcher, got %T", matcher)
					}
				case "in":
					if _, ok := matcher.(*inAttributeMatcher); !ok {
						t.Errorf("Expected inAttributeMatcher, got %T", matcher)
					}
				}
			}
		})
	}
}

func TestToStringSlice(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  []string
	}{
		{
			name:  "string slice input",
			input: []string{"admin", "user", "guest"},
			want:  []string{"admin", "user", "guest"},
		},
		{
			name:  "interface slice input",
			input: []interface{}{"value1", "value2", "value3"},
			want:  []string{"value1", "value2", "value3"},
		},
		{
			name:  "interface slice with mixed types",
			input: []interface{}{"string", 123, true},
			want:  []string{"string", "123", "true"},
		},
		{
			name:  "single string input",
			input: "single-value",
			want:  []string{"single-value"},
		},
		{
			name:  "integer input",
			input: 42,
			want:  []string{"42"},
		},
		{
			name:  "boolean input",
			input: true,
			want:  []string{"true"},
		},
		{
			name:  "empty string slice",
			input: []string{},
			want:  []string{},
		},
		{
			name:  "empty interface slice",
			input: []interface{}{},
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toStringSlice(tt.input)

			if len(got) != len(tt.want) {
				t.Errorf("toStringSlice() length = %v, want %v", len(got), len(tt.want))
				return
			}

			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("toStringSlice()[%d] = %v, want %v", i, v, tt.want[i])
				}
			}
		})
	}
}

func TestAttributeMatcher_Exact(t *testing.T) {
	matcher := &exactAttributeMatcher{
		key:   "department",
		value: "engineering",
	}

	tests := []struct {
		name      string
		subject   *Subject
		wantMatch bool
	}{
		{
			name: "exact match",
			subject: &Subject{
				Attributes: map[string]interface{}{
					"department": "engineering",
				},
			},
			wantMatch: true,
		},
		{
			name: "no match",
			subject: &Subject{
				Attributes: map[string]interface{}{
					"department": "sales",
				},
			},
			wantMatch: false,
		},
		{
			name: "missing attribute",
			subject: &Subject{
				Attributes: map[string]interface{}{},
			},
			wantMatch: false,
		},
		{
			name:      "nil attributes",
			subject:   &Subject{},
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matcher.Match(tt.subject)
			if result != tt.wantMatch {
				t.Errorf("Match() = %v, want %v", result, tt.wantMatch)
			}
		})
	}
}

func TestAttributeMatcher_Numeric(t *testing.T) {
	tests := []struct {
		name      string
		matcher   *numericAttributeMatcher
		subject   *Subject
		wantMatch bool
	}{
		{
			name: "greater than - match",
			matcher: &numericAttributeMatcher{
				key:   "level",
				value: 5,
				op:    ">",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"level": 10,
				},
			},
			wantMatch: true,
		},
		{
			name: "greater than - no match",
			matcher: &numericAttributeMatcher{
				key:   "level",
				value: 5,
				op:    ">",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"level": 3,
				},
			},
			wantMatch: false,
		},
		{
			name: "greater than or equal - equal",
			matcher: &numericAttributeMatcher{
				key:   "level",
				value: 5,
				op:    ">=",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"level": 5,
				},
			},
			wantMatch: true,
		},
		{
			name: "less than - match",
			matcher: &numericAttributeMatcher{
				key:   "age",
				value: 65,
				op:    "<",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"age": 30,
				},
			},
			wantMatch: true,
		},
		{
			name: "less than or equal - equal",
			matcher: &numericAttributeMatcher{
				key:   "age",
				value: 65,
				op:    "<=",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"age": 65,
				},
			},
			wantMatch: true,
		},
		{
			name: "missing attribute",
			matcher: &numericAttributeMatcher{
				key:   "level",
				value: 5,
				op:    ">",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{},
			},
			wantMatch: false,
		},
		{
			name: "non-numeric attribute",
			matcher: &numericAttributeMatcher{
				key:   "level",
				value: 5,
				op:    ">",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"level": "not a number",
				},
			},
			wantMatch: false,
		},
		{
			name: "nil attributes",
			matcher: &numericAttributeMatcher{
				key:   "level",
				value: 5,
				op:    ">",
			},
			subject:   &Subject{},
			wantMatch: false,
		},
		{
			name: "unsupported operator",
			matcher: &numericAttributeMatcher{
				key:   "level",
				value: 5,
				op:    "!=",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"level": 10,
				},
			},
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.matcher.Match(tt.subject)
			if result != tt.wantMatch {
				t.Errorf("Match() = %v, want %v", result, tt.wantMatch)
			}
		})
	}
}

func TestAttributeMatcher_String(t *testing.T) {
	tests := []struct {
		name      string
		matcher   *stringAttributeMatcher
		subject   *Subject
		wantMatch bool
	}{
		{
			name: "startswith - match",
			matcher: &stringAttributeMatcher{
				key:   "email",
				value: "admin",
				op:    "startswith",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"email": "admin@example.com",
				},
			},
			wantMatch: true,
		},
		{
			name: "endswith - match",
			matcher: &stringAttributeMatcher{
				key:   "email",
				value: "@example.com",
				op:    "endswith",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"email": "user@example.com",
				},
			},
			wantMatch: true,
		},
		{
			name: "contains - match",
			matcher: &stringAttributeMatcher{
				key:   "email",
				value: "@example",
				op:    "contains",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"email": "user@example.com",
				},
			},
			wantMatch: true,
		},
		{
			name: "no match",
			matcher: &stringAttributeMatcher{
				key:   "email",
				value: "@test.com",
				op:    "endswith",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"email": "user@example.com",
				},
			},
			wantMatch: false,
		},
		{
			name: "missing attribute",
			matcher: &stringAttributeMatcher{
				key:   "email",
				value: "@example.com",
				op:    "endswith",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{},
			},
			wantMatch: false,
		},
		{
			name: "nil attributes",
			matcher: &stringAttributeMatcher{
				key:   "email",
				value: "@example.com",
				op:    "endswith",
			},
			subject:   &Subject{},
			wantMatch: false,
		},
		{
			name: "unsupported operator",
			matcher: &stringAttributeMatcher{
				key:   "email",
				value: "pattern",
				op:    "regex",
			},
			subject: &Subject{
				Attributes: map[string]interface{}{
					"email": "user@example.com",
				},
			},
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.matcher.Match(tt.subject)
			if result != tt.wantMatch {
				t.Errorf("Match() = %v, want %v", result, tt.wantMatch)
			}
		})
	}
}

func TestAttributeMatcher_In(t *testing.T) {
	matcher := &inAttributeMatcher{
		key:    "role",
		values: []string{"admin", "superuser", "moderator"},
	}

	tests := []struct {
		name      string
		subject   *Subject
		wantMatch bool
	}{
		{
			name: "value in list",
			subject: &Subject{
				Attributes: map[string]interface{}{
					"role": "admin",
				},
			},
			wantMatch: true,
		},
		{
			name: "value not in list",
			subject: &Subject{
				Attributes: map[string]interface{}{
					"role": "user",
				},
			},
			wantMatch: false,
		},
		{
			name: "missing attribute",
			subject: &Subject{
				Attributes: map[string]interface{}{},
			},
			wantMatch: false,
		},
		{
			name:      "nil attributes",
			subject:   &Subject{},
			wantMatch: false,
		},
		{
			name: "integer value converted to string",
			subject: &Subject{
				Attributes: map[string]interface{}{
					"role": 123,
				},
			},
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matcher.Match(tt.subject)
			if result != tt.wantMatch {
				t.Errorf("Match() = %v, want %v", result, tt.wantMatch)
			}
		})
	}
}

func TestSubstituteVariables(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		variables map[string]string
		want      string
	}{
		{
			name:    "single variable",
			pattern: "/dept/${department}/data",
			variables: map[string]string{
				"department": "engineering",
			},
			want: "/dept/engineering/data",
		},
		{
			name:    "multiple variables",
			pattern: "/${org}/${dept}/${resource}",
			variables: map[string]string{
				"org":      "acme",
				"dept":     "eng",
				"resource": "data",
			},
			want: "/acme/eng/data",
		},
		{
			name:      "no variables",
			pattern:   "/static/path",
			variables: map[string]string{},
			want:      "/static/path",
		},
		{
			name:    "undefined variable",
			pattern: "/dept/${unknown}/data",
			variables: map[string]string{
				"department": "engineering",
			},
			want: "/dept/${unknown}/data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := substituteVariables(tt.pattern, tt.variables)
			if result != tt.want {
				t.Errorf("substituteVariables() = %v, want %v", result, tt.want)
			}
		})
	}
}

func TestComputeFingerprint(t *testing.T) {
	policy1 := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata: PolicyMetadata{
			Name:   "test-policy",
			Tenant: "test-tenant",
		},
		Spec: PolicySpec{
			Rules: []Rule{
				{
					Name:      "test-rule",
					Effect:    EffectAllow,
					Actions:   []string{"read"},
					Resources: []string{"/data/*"},
				},
			},
		},
	}

	policy2 := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata: PolicyMetadata{
			Name:   "test-policy",
			Tenant: "test-tenant",
		},
		Spec: PolicySpec{
			Rules: []Rule{
				{
					Name:      "test-rule",
					Effect:    EffectAllow,
					Actions:   []string{"read"},
					Resources: []string{"/data/*"},
				},
			},
		},
	}

	policy3 := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata: PolicyMetadata{
			Name:   "different-policy",
			Tenant: "test-tenant",
		},
		Spec: PolicySpec{
			Rules: []Rule{
				{
					Name:      "test-rule",
					Effect:    EffectAllow,
					Actions:   []string{"write"},
					Resources: []string{"/data/*"},
				},
			},
		},
	}

	fp1 := computeFingerprint(policy1)
	fp2 := computeFingerprint(policy2)
	fp3 := computeFingerprint(policy3)

	if fp1 == "" {
		t.Error("computeFingerprint() returned empty string")
	}

	if fp1 != fp2 {
		t.Error("computeFingerprint() should return same hash for identical policies")
	}

	if fp1 == fp3 {
		t.Error("computeFingerprint() should return different hash for different policies")
	}
}

func TestCompileCondition(t *testing.T) {
	compiler := NewPolicyCompiler()

	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{
			name:    "valid simple condition",
			expr:    "subject.authenticated",
			wantErr: false,
		},
		{
			name:    "valid comparison",
			expr:    "subject.id == \"user-123\"",
			wantErr: false,
		},
		{
			name:    "valid complex condition",
			expr:    "subject.authenticated and time.hour >= 9",
			wantErr: false,
		},
		{
			name:    "empty expression",
			expr:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := compiler.CompileCondition(tt.expr)

			if (err != nil) != tt.wantErr {
				t.Errorf("CompileCondition() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && cond == nil {
				t.Error("CompileCondition() returned nil without error")
			}
		})
	}
}

func BenchmarkPolicyCompiler_Compile(b *testing.B) {
	compiler := NewPolicyCompiler()

	policy := &Policy{
		APIVersion: "abac.quicbaas.io/v1",
		Kind:       "Policy",
		Metadata: PolicyMetadata{
			Name:     "bench-policy",
			Tenant:   "test-tenant",
			Priority: 100,
		},
		Spec: PolicySpec{
			Subjects: SubjectSelector{
				Groups: []string{"admins", "engineers"},
			},
			Rules: []Rule{
				{
					Name:      "allow-read",
					Effect:    EffectAllow,
					Priority:  10,
					Actions:   []string{"read", "list"},
					Resources: []string{"/data/*", "/reports/*"},
					When: []Condition{
						{Expression: "time.hour >= 9"},
						{Expression: "time.hour < 17"},
					},
				},
			},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := compiler.Compile(policy)
		if err != nil {
			b.Fatalf("Compile() error = %v", err)
		}
	}
}
