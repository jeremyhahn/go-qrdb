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
	"net"
	"testing"
	"time"
)

func TestConditionParser_Parse(t *testing.T) {
	parser := NewConditionParser(nil)

	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{
			name:    "simple equality",
			expr:    "subject.id == \"user-123\"",
			wantErr: false,
		},
		{
			name:    "not equal",
			expr:    "subject.email != \"test@example.com\"",
			wantErr: false,
		},
		{
			name:    "greater than",
			expr:    "subject.attrs.age > 18",
			wantErr: false,
		},
		{
			name:    "less than or equal",
			expr:    "request.amount <= 1000",
			wantErr: false,
		},
		{
			name:    "and operator",
			expr:    "subject.authenticated and subject.mfa_verified",
			wantErr: false,
		},
		{
			name:    "or operator",
			expr:    "subject.id == \"admin\" or subject.roles contains \"admin\"",
			wantErr: false,
		},
		{
			name:    "not operator",
			expr:    "not subject.suspended",
			wantErr: false,
		},
		{
			name:    "contains operator",
			expr:    "subject.roles contains \"admin\"",
			wantErr: false,
		},
		{
			name:    "startswith operator",
			expr:    "resource startswith \"orders://\"",
			wantErr: false,
		},
		{
			name:    "endswith operator",
			expr:    "subject.email endswith \"@example.com\"",
			wantErr: false,
		},
		{
			name:    "in operator",
			expr:    "subject.id in [\"user-1\", \"user-2\"]",
			wantErr: false,
		},
		{
			name:    "between operator",
			expr:    "time.hour between 9..17",
			wantErr: false,
		},
		{
			name:    "exists operator",
			expr:    "subject.attrs.department exists",
			wantErr: false,
		},
		{
			name:    "not exists operator",
			expr:    "subject.attrs.temp notexists",
			wantErr: false,
		},
		{
			name:    "matches regex",
			expr:    "subject.email matches \"^[a-z]+@example\\.com$\"",
			wantErr: false,
		},
		{
			name:    "like pattern",
			expr:    "subject.name like \"John*\"",
			wantErr: false,
		},
		{
			name:    "empty expression",
			expr:    "",
			wantErr: true,
		},
		{
			name:    "eq operator",
			expr:    "subject.id eq \"user-123\"",
			wantErr: false,
		},
		{
			name:    "neq operator",
			expr:    "subject.id neq \"admin\"",
			wantErr: false,
		},
		{
			name:    "notcontains operator",
			expr:    "subject.roles notcontains \"banned\"",
			wantErr: false,
		},
		{
			name:    "overlaps operator",
			expr:    "subject.groups overlaps [\"eng\", \"ops\"]",
			wantErr: false,
		},
		{
			name:    "disjoint operator",
			expr:    "subject.groups disjoint [\"admin\", \"root\"]",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)

			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && cond == nil {
				t.Error("Parse() returned nil condition without error")
			}
		})
	}
}

func TestConditionParser_LogicalOperators(t *testing.T) {
	parser := NewConditionParser(nil)

	tests := []struct {
		name     string
		expr     string
		ctx      *EvalContext
		wantTrue bool
	}{
		{
			name: "and - both true",
			expr: "subject.authenticated and subject.mfa_verified",
			ctx: &EvalContext{
				Subject: &Subject{
					Authenticated: true,
					MFAVerified:   true,
				},
			},
			wantTrue: true,
		},
		{
			name: "and - one false",
			expr: "subject.authenticated and subject.mfa_verified",
			ctx: &EvalContext{
				Subject: &Subject{
					Authenticated: true,
					MFAVerified:   false,
				},
			},
			wantTrue: false,
		},
		{
			name: "or - one true",
			expr: "subject.id == \"admin\" or subject.id == \"user-1\"",
			ctx: &EvalContext{
				Subject: &Subject{
					ID: "user-1",
				},
			},
			wantTrue: true,
		},
		{
			name: "or - both false",
			expr: "subject.id == \"admin\" or subject.id == \"user-1\"",
			ctx: &EvalContext{
				Subject: &Subject{
					ID: "user-2",
				},
			},
			wantTrue: false,
		},
		{
			name: "not - negates true",
			expr: "not subject.authenticated",
			ctx: &EvalContext{
				Subject: &Subject{
					Authenticated: true,
				},
			},
			wantTrue: false,
		},
		{
			name: "not - negates false",
			expr: "not subject.authenticated",
			ctx: &EvalContext{
				Subject: &Subject{
					Authenticated: false,
				},
			},
			wantTrue: true,
		},
		{
			name: "complex and - multiple conditions",
			expr: "subject.authenticated and subject.mfa_verified and subject.id == \"user-1\"",
			ctx: &EvalContext{
				Subject: &Subject{
					Authenticated: true,
					MFAVerified:   true,
					ID:            "user-1",
				},
			},
			wantTrue: true,
		},
		{
			name: "complex or - multiple conditions",
			expr: "subject.id == \"admin\" or subject.id == \"user-1\" or subject.id == \"user-2\"",
			ctx: &EvalContext{
				Subject: &Subject{
					ID: "user-2",
				},
			},
			wantTrue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			result := cond.Evaluate(tt.ctx)
			if result != tt.wantTrue {
				t.Errorf("Evaluate() = %v, want %v", result, tt.wantTrue)
			}
		})
	}
}

func TestConditionParser_ComparisonOperators(t *testing.T) {
	parser := NewConditionParser(nil)

	tests := []struct {
		name     string
		expr     string
		ctx      *EvalContext
		wantTrue bool
	}{
		{
			name: "equal - strings",
			expr: "subject.id == \"user-123\"",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-123"},
			},
			wantTrue: true,
		},
		{
			name: "not equal - strings",
			expr: "subject.id != \"admin\"",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-123"},
			},
			wantTrue: true,
		},
		{
			name: "greater than - numbers",
			expr: "subject.attrs.age > 18",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{"age": 25},
				},
			},
			wantTrue: true,
		},
		{
			name: "greater than or equal",
			expr: "subject.attrs.age >= 18",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{"age": 18},
				},
			},
			wantTrue: true,
		},
		{
			name: "less than",
			expr: "request.amount < 100",
			ctx: &EvalContext{
				Request: map[string]interface{}{"amount": 50},
			},
			wantTrue: true,
		},
		{
			name: "less than or equal",
			expr: "request.amount <= 100",
			ctx: &EvalContext{
				Request: map[string]interface{}{"amount": 100},
			},
			wantTrue: true,
		},
		{
			name: "between - in range",
			expr: "subject.attrs.age between 18..65",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{"age": 30},
				},
			},
			wantTrue: true,
		},
		{
			name: "between - out of range",
			expr: "subject.attrs.age between 18..65",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{"age": 70},
				},
			},
			wantTrue: false,
		},
		{
			name: "eq operator - equal",
			expr: "subject.id eq \"user-123\"",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-123"},
			},
			wantTrue: true,
		},
		{
			name: "neq operator - not equal",
			expr: "subject.id neq \"admin\"",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-123"},
			},
			wantTrue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			result := cond.Evaluate(tt.ctx)
			if result != tt.wantTrue {
				t.Errorf("Evaluate() = %v, want %v", result, tt.wantTrue)
			}
		})
	}
}

func TestConditionParser_StringOperators(t *testing.T) {
	parser := NewConditionParser(nil)

	tests := []struct {
		name     string
		expr     string
		ctx      *EvalContext
		wantTrue bool
	}{
		{
			name: "startswith - match",
			expr: "subject.email startswith \"admin\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "admin@example.com"},
			},
			wantTrue: true,
		},
		{
			name: "startswith - no match",
			expr: "subject.email startswith \"user\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "admin@example.com"},
			},
			wantTrue: false,
		},
		{
			name: "endswith - match",
			expr: "subject.email endswith \"@example.com\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "admin@example.com"},
			},
			wantTrue: true,
		},
		{
			name: "contains - match",
			expr: "subject.email contains \"@example\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "admin@example.com"},
			},
			wantTrue: true,
		},
		{
			name: "matches - regex match",
			expr: "subject.email matches \"^[a-z]+@.*\\.com$\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "admin@example.com"},
			},
			wantTrue: true,
		},
		{
			name: "matches - regex no match",
			expr: "subject.email matches \"^[0-9]+@.*\\.com$\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "admin@example.com"},
			},
			wantTrue: false,
		},
		{
			name: "like - glob match",
			expr: "subject.name like \"John*\"",
			ctx: &EvalContext{
				Subject: &Subject{Name: "John Doe"},
			},
			wantTrue: true,
		},
		{
			name: "notcontains - not in string",
			expr: "subject.email notcontains \"test\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "admin@example.com"},
			},
			wantTrue: true,
		},
		{
			name: "notcontains - in string",
			expr: "subject.email notcontains \"admin\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "admin@example.com"},
			},
			wantTrue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			result := cond.Evaluate(tt.ctx)
			if result != tt.wantTrue {
				t.Errorf("Evaluate() = %v, want %v", result, tt.wantTrue)
			}
		})
	}
}

func TestConditionParser_SetOperators(t *testing.T) {
	parser := NewConditionParser(nil)

	tests := []struct {
		name     string
		expr     string
		ctx      *EvalContext
		wantTrue bool
	}{
		{
			name: "in - string in array",
			expr: "subject.id in [\"user-1\", \"user-2\", \"user-3\"]",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-2"},
			},
			wantTrue: true,
		},
		{
			name: "in - string not in array",
			expr: "subject.id in [\"user-1\", \"user-2\"]",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-3"},
			},
			wantTrue: false,
		},
		{
			name: "notin - string not in array",
			expr: "subject.id notin [\"admin\", \"root\"]",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-1"},
			},
			wantTrue: true,
		},
		{
			name: "containsall - all present",
			expr: "subject.roles containsall [\"read\", \"write\"]",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"read", "write", "admin"},
				},
			},
			wantTrue: true,
		},
		{
			name: "containsall - not all present",
			expr: "subject.roles containsall [\"read\", \"admin\"]",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"read", "write"},
				},
			},
			wantTrue: false,
		},
		{
			name: "containsany - at least one present",
			expr: "subject.roles containsany [\"admin\", \"superuser\"]",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"read", "admin"},
				},
			},
			wantTrue: true,
		},
		{
			name: "containsany - none present",
			expr: "subject.roles containsany [\"admin\", \"superuser\"]",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"read", "write"},
				},
			},
			wantTrue: false,
		},
		{
			name: "overlaps - has overlap",
			expr: "subject.groups overlaps [\"eng\", \"ops\"]",
			ctx: &EvalContext{
				Subject: &Subject{
					Groups: []string{"eng", "qa"},
				},
			},
			wantTrue: true,
		},
		{
			name: "disjoint - no overlap",
			expr: "subject.groups disjoint [\"admin\", \"root\"]",
			ctx: &EvalContext{
				Subject: &Subject{
					Groups: []string{"eng", "qa"},
				},
			},
			wantTrue: true,
		},
		{
			name: "contains - array contains value",
			expr: "subject.roles contains \"admin\"",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"read", "admin", "write"},
				},
			},
			wantTrue: true,
		},
		{
			name: "contains - array does not contain value",
			expr: "subject.roles contains \"superuser\"",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"read", "admin", "write"},
				},
			},
			wantTrue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			result := cond.Evaluate(tt.ctx)
			if result != tt.wantTrue {
				t.Errorf("Evaluate() = %v, want %v", result, tt.wantTrue)
			}
		})
	}
}

func TestConditionParser_FieldResolution(t *testing.T) {
	parser := NewConditionParser(nil)

	now := time.Now()
	lastLogin := now.Add(-1 * time.Hour)
	mfaTimestamp := now.Add(-30 * time.Minute)

	tests := []struct {
		name     string
		expr     string
		ctx      *EvalContext
		wantTrue bool
	}{
		{
			name: "subject.id",
			expr: "subject.id == \"user-123\"",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-123"},
			},
			wantTrue: true,
		},
		{
			name: "subject.name",
			expr: "subject.name == \"John Doe\"",
			ctx: &EvalContext{
				Subject: &Subject{Name: "John Doe"},
			},
			wantTrue: true,
		},
		{
			name: "subject.email",
			expr: "subject.email == \"john@example.com\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "john@example.com"},
			},
			wantTrue: true,
		},
		{
			name: "subject.tenant",
			expr: "subject.tenant == \"acme-corp\"",
			ctx: &EvalContext{
				Subject: &Subject{TenantID: "acme-corp"},
			},
			wantTrue: true,
		},
		{
			name: "subject.authenticated",
			expr: "subject.authenticated",
			ctx: &EvalContext{
				Subject: &Subject{Authenticated: true},
			},
			wantTrue: true,
		},
		{
			name: "subject.mfa_verified",
			expr: "subject.mfa_verified",
			ctx: &EvalContext{
				Subject: &Subject{MFAVerified: true},
			},
			wantTrue: true,
		},
		{
			name: "subject.mfa_timestamp",
			expr: "subject.mfa_timestamp exists",
			ctx: &EvalContext{
				Subject: &Subject{MFATimestamp: &mfaTimestamp},
			},
			wantTrue: true,
		},
		{
			name: "subject.last_login",
			expr: "subject.last_login exists",
			ctx: &EvalContext{
				Subject: &Subject{LastLogin: &lastLogin},
			},
			wantTrue: true,
		},
		{
			// password_changed is no longer supported (passwordless auth only).
			// The expression always evaluates to false/nil since the field is removed.
			name: "subject.password_changed returns nil",
			expr: "subject.password_changed exists",
			ctx: &EvalContext{
				Subject: &Subject{},
			},
			wantTrue: false,
		},
		{
			name: "subject.attributes (using attrs)",
			expr: "subject.attrs.department == \"engineering\"",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{
						"department": "engineering",
					},
				},
			},
			wantTrue: true,
		},
		{
			name: "subject.attributes (using attributes keyword)",
			expr: "subject.attributes.department == \"engineering\"",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{
						"department": "engineering",
					},
				},
			},
			wantTrue: true,
		},
		{
			name: "request field",
			expr: "request.ip_address == \"192.168.1.1\"",
			ctx: &EvalContext{
				Request: map[string]interface{}{
					"ip_address": "192.168.1.1",
				},
			},
			wantTrue: true,
		},
		{
			name: "resource field",
			expr: "resource == \"orders://order-123\"",
			ctx: &EvalContext{
				Resource: "orders://order-123",
			},
			wantTrue: true,
		},
		{
			name: "resource with request context",
			expr: "resource.owner == \"user-123\"",
			ctx: &EvalContext{
				Resource: "orders://order-123",
				Request: map[string]interface{}{
					"resource_owner": "user-123",
				},
			},
			wantTrue: true,
		},
		{
			name: "time.hour",
			expr: "time.hour >= 9",
			ctx: &EvalContext{
				Time: time.Date(2024, 1, 1, 14, 0, 0, 0, time.UTC),
			},
			wantTrue: true,
		},
		{
			name: "time.minute",
			expr: "time.minute == 30",
			ctx: &EvalContext{
				Time: time.Date(2024, 1, 1, 14, 30, 0, 0, time.UTC),
			},
			wantTrue: true,
		},
		{
			name: "time.weekday",
			expr: "time.weekday == \"mon\"",
			ctx: &EvalContext{
				Time: time.Date(2024, 1, 1, 14, 0, 0, 0, time.UTC), // Monday
			},
			wantTrue: true,
		},
		{
			name: "time.date",
			expr: "time.date == \"2024-01-01\"",
			ctx: &EvalContext{
				Time: time.Date(2024, 1, 1, 14, 0, 0, 0, time.UTC),
			},
			wantTrue: true,
		},
		{
			name: "variable substitution",
			expr: "${user_type} == \"admin\"",
			ctx: &EvalContext{
				Variables: map[string]interface{}{
					"user_type": "admin",
				},
			},
			wantTrue: true,
		},
		{
			name: "variable substitution - missing variable",
			expr: "${missing_var} exists",
			ctx: &EvalContext{
				Variables: map[string]interface{}{
					"user_type": "admin",
				},
			},
			wantTrue: false,
		},
		{
			name: "exists - attribute present",
			expr: "subject.attrs.department exists",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{
						"department": "engineering",
					},
				},
			},
			wantTrue: true,
		},
		{
			name: "exists - attribute missing",
			expr: "subject.attrs.temp exists",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{
						"department": "engineering",
					},
				},
			},
			wantTrue: false,
		},
		{
			name: "notexists - attribute missing",
			expr: "subject.attrs.temp notexists",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{
						"department": "engineering",
					},
				},
			},
			wantTrue: true,
		},
		{
			name: "field condition - boolean true",
			expr: "subject.authenticated",
			ctx: &EvalContext{
				Subject: &Subject{Authenticated: true},
			},
			wantTrue: true,
		},
		{
			name: "field condition - boolean false",
			expr: "subject.authenticated",
			ctx: &EvalContext{
				Subject: &Subject{Authenticated: false},
			},
			wantTrue: false,
		},
		{
			name: "field condition - non-nil value",
			expr: "subject.id",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-123"},
			},
			wantTrue: true,
		},
		{
			name: "quoted string with double quotes",
			expr: "subject.id == \"user-123\"",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-123"},
			},
			wantTrue: true,
		},
		{
			name: "quoted string with single quotes",
			expr: "subject.id == 'user-123'",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-123"},
			},
			wantTrue: true,
		},
		{
			name:     "literal integer",
			expr:     "42 == 42",
			ctx:      &EvalContext{},
			wantTrue: true,
		},
		{
			name:     "literal float",
			expr:     "3.14 > 3.0",
			ctx:      &EvalContext{},
			wantTrue: true,
		},
		{
			name:     "literal boolean true",
			expr:     "true",
			ctx:      &EvalContext{},
			wantTrue: true,
		},
		{
			name:     "literal boolean false",
			expr:     "false == false",
			ctx:      &EvalContext{},
			wantTrue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			result := cond.Evaluate(tt.ctx)
			if result != tt.wantTrue {
				t.Errorf("Evaluate() = %v, want %v", result, tt.wantTrue)
			}
		})
	}
}

func TestConditionParser_IPCIDRMatching(t *testing.T) {
	parser := NewConditionParser(nil)

	tests := []struct {
		name     string
		expr     string
		ctx      *EvalContext
		wantTrue bool
	}{
		{
			name: "IP in CIDR",
			expr: "request.ip in \"192.168.1.0/24\"",
			ctx: &EvalContext{
				Request: map[string]interface{}{
					"ip": "192.168.1.100",
				},
			},
			wantTrue: true,
		},
		{
			name: "IP not in CIDR",
			expr: "request.ip in \"192.168.1.0/24\"",
			ctx: &EvalContext{
				Request: map[string]interface{}{
					"ip": "10.0.0.1",
				},
			},
			wantTrue: false,
		},
		{
			name: "invalid IP string",
			expr: "request.ip in \"192.168.1.0/24\"",
			ctx: &EvalContext{
				Request: map[string]interface{}{
					"ip": "not-an-ip",
				},
			},
			wantTrue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			result := cond.Evaluate(tt.ctx)
			if result != tt.wantTrue {
				t.Errorf("Evaluate() = %v, want %v", result, tt.wantTrue)
			}
		})
	}
}

func TestInCIDR(t *testing.T) {
	tests := []struct {
		name     string
		ip       net.IP
		cidr     string
		wantTrue bool
	}{
		{
			name:     "IPv4 in range",
			ip:       net.ParseIP("192.168.1.100"),
			cidr:     "192.168.1.0/24",
			wantTrue: true,
		},
		{
			name:     "IPv4 out of range",
			ip:       net.ParseIP("10.0.0.1"),
			cidr:     "192.168.1.0/24",
			wantTrue: false,
		},
		{
			name:     "invalid CIDR",
			ip:       net.ParseIP("192.168.1.100"),
			cidr:     "invalid",
			wantTrue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := inCIDR(tt.ip, tt.cidr)
			if result != tt.wantTrue {
				t.Errorf("inCIDR() = %v, want %v", result, tt.wantTrue)
			}
		})
	}
}

func TestToSlice(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		wantOk  bool
		wantLen int
	}{
		{
			name:    "slice of interfaces",
			input:   []interface{}{"a", "b", "c"},
			wantOk:  true,
			wantLen: 3,
		},
		{
			name:    "slice of strings",
			input:   []string{"a", "b", "c"},
			wantOk:  true,
			wantLen: 3,
		},
		{
			name:    "array literal string",
			input:   "[a, b, c]",
			wantOk:  true,
			wantLen: 3,
		},
		{
			name:    "array literal with quotes",
			input:   "[\"a\", \"b\", \"c\"]",
			wantOk:  true,
			wantLen: 3,
		},
		{
			name:    "array literal with single quotes",
			input:   "['a', 'b', 'c']",
			wantOk:  true,
			wantLen: 3,
		},
		{
			name:    "array literal with mixed spacing",
			input:   "[ a , b , c ]",
			wantOk:  true,
			wantLen: 3,
		},
		{
			name:    "empty array literal",
			input:   "[]",
			wantOk:  true,
			wantLen: 1,
		},
		{
			name:   "invalid input - integer",
			input:  123,
			wantOk: false,
		},
		{
			name:   "invalid input - boolean",
			input:  true,
			wantOk: false,
		},
		{
			name:   "invalid input - non-array string",
			input:  "not an array",
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := toSlice(tt.input)
			if ok != tt.wantOk {
				t.Errorf("toSlice() ok = %v, want %v", ok, tt.wantOk)
			}
			if ok && len(result) != tt.wantLen {
				t.Errorf("toSlice() len = %v, want %v", len(result), tt.wantLen)
			}
		})
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name   string
		input  interface{}
		wantOk bool
		wantV  float64
	}{
		{
			name:   "int",
			input:  42,
			wantOk: true,
			wantV:  42.0,
		},
		{
			name:   "int64",
			input:  int64(42),
			wantOk: true,
			wantV:  42.0,
		},
		{
			name:   "float64",
			input:  42.5,
			wantOk: true,
			wantV:  42.5,
		},
		{
			name:   "string number",
			input:  "42.5",
			wantOk: true,
			wantV:  42.5,
		},
		{
			name:   "invalid string",
			input:  "not a number",
			wantOk: false,
		},
		{
			name:   "boolean",
			input:  true,
			wantOk: false,
		},
		{
			name:   "nil",
			input:  nil,
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := toFloat64(tt.input)
			if ok != tt.wantOk {
				t.Errorf("toFloat64() ok = %v, want %v", ok, tt.wantOk)
			}
			if ok && result != tt.wantV {
				t.Errorf("toFloat64() = %v, want %v", result, tt.wantV)
			}
		})
	}
}

func TestSplitByOperator(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		op       string
		wantLen  int
		wantPart string
	}{
		{
			name:     "simple split",
			expr:     "a and b and c",
			op:       " and ",
			wantLen:  3,
			wantPart: "a",
		},
		{
			name:     "with parentheses",
			expr:     "(a and b) or c",
			op:       " or ",
			wantLen:  2,
			wantPart: "(a and b)",
		},
		{
			name:    "no operator",
			expr:    "simple expression",
			op:      " and ",
			wantLen: 1,
		},
		{
			name:     "nested parentheses",
			expr:     "((a and b) or c) and d",
			op:       " and ",
			wantLen:  2,
			wantPart: "((a and b) or c)",
		},
		{
			name:    "empty expression",
			expr:    "",
			op:      " and ",
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitByOperator(tt.expr, tt.op)
			if len(result) != tt.wantLen {
				t.Errorf("splitByOperator() len = %v, want %v", len(result), tt.wantLen)
			}
			if tt.wantPart != "" && len(result) > 0 {
				if result[0] != tt.wantPart {
					t.Errorf("splitByOperator()[0] = %v, want %v", result[0], tt.wantPart)
				}
			}
		})
	}
}

// Test edge cases and error paths
func TestConditionParser_EdgeCases(t *testing.T) {
	parser := NewConditionParser(nil)

	tests := []struct {
		name     string
		expr     string
		ctx      *EvalContext
		wantTrue bool
	}{
		{
			name: "between - invalid range format",
			expr: "subject.attrs.age between invalid",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{"age": 30},
				},
			},
			wantTrue: false,
		},
		{
			name: "between - non-numeric value",
			expr: "subject.name between 18..65",
			ctx: &EvalContext{
				Subject: &Subject{Name: "John"},
			},
			wantTrue: false,
		},
		{
			name: "between - non-string right operand",
			expr: "subject.attrs.age between 42",
			ctx: &EvalContext{
				Subject: &Subject{
					Attributes: map[string]interface{}{"age": 30},
				},
			},
			wantTrue: false,
		},
		{
			name:     "endswith - non-string operands",
			expr:     "42 endswith 2",
			ctx:      &EvalContext{},
			wantTrue: false,
		},
		{
			name:     "startswith - non-string operands",
			expr:     "42 startswith 4",
			ctx:      &EvalContext{},
			wantTrue: false,
		},
		{
			name: "matches - invalid regex",
			expr: "subject.email matches \"[invalid(regex\"",
			ctx: &EvalContext{
				Subject: &Subject{Email: "test@example.com"},
			},
			wantTrue: false,
		},
		{
			name:     "matches - non-string operands",
			expr:     "42 matches \"\\d+\"",
			ctx:      &EvalContext{},
			wantTrue: false,
		},
		{
			name:     "like - non-string operands",
			expr:     "42 like \"4*\"",
			ctx:      &EvalContext{},
			wantTrue: false,
		},
		{
			name: "in - non-slice right operand",
			expr: "subject.id in invalid",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-1"},
			},
			wantTrue: false,
		},
		{
			name: "containsall - left not a slice",
			expr: "subject.id containsall [\"user\", \"1\"]",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-1"},
			},
			wantTrue: false,
		},
		{
			name: "containsall - right not a slice",
			expr: "subject.roles containsall invalid",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"read", "write"},
				},
			},
			wantTrue: false,
		},
		{
			name: "containsany - left not a slice",
			expr: "subject.id containsany [\"user\", \"admin\"]",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-1"},
			},
			wantTrue: false,
		},
		{
			name: "containsany - right not a slice",
			expr: "subject.roles containsany invalid",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"read", "write"},
				},
			},
			wantTrue: false,
		},
		{
			name: "greater than - non-numeric operands",
			expr: "subject.name > \"John\"",
			ctx: &EvalContext{
				Subject: &Subject{Name: "John Doe"},
			},
			wantTrue: false,
		},
		{
			name: "less than - non-numeric operands",
			expr: "subject.name < \"John\"",
			ctx: &EvalContext{
				Subject: &Subject{Name: "John Doe"},
			},
			wantTrue: false,
		},
		{
			name: "nil subject",
			expr: "subject.id == \"user-123\"",
			ctx: &EvalContext{
				Subject: nil,
			},
			wantTrue: false,
		},
		{
			name: "nil request",
			expr: "request.ip == \"192.168.1.1\"",
			ctx: &EvalContext{
				Request: nil,
			},
			wantTrue: false,
		},
		{
			name:     "unknown field path",
			expr:     "unknown.field == \"value\"",
			ctx:      &EvalContext{},
			wantTrue: false,
		},
		{
			name:     "empty field parts",
			expr:     " == \"value\"",
			ctx:      &EvalContext{},
			wantTrue: false,
		},
		{
			name: "time.since - not implemented",
			expr: "time.since exists",
			ctx: &EvalContext{
				Time: time.Now(),
			},
			wantTrue: false,
		},
		{
			name: "time.until - not implemented",
			expr: "time.until exists",
			ctx: &EvalContext{
				Time: time.Now(),
			},
			wantTrue: false,
		},
		{
			name: "field condition - empty string is truthy",
			expr: "subject.id",
			ctx: &EvalContext{
				Subject: &Subject{ID: ""},
			},
			wantTrue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			result := cond.Evaluate(tt.ctx)
			if result != tt.wantTrue {
				t.Errorf("Evaluate() = %v, want %v", result, tt.wantTrue)
			}
		})
	}
}

// Test error handling in parser
func TestConditionParser_ParseErrors(t *testing.T) {
	parser := NewConditionParser(nil)

	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{
			name:    "empty expression",
			expr:    "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			expr:    "   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parser.Parse(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Test resolveValue edge cases
func TestResolveValue_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		ctx     *EvalContext
		wantNil bool
	}{
		{name: "unknown subject field",
			expr: "subject.unknown_field == \"value\"",
			ctx: &EvalContext{
				Subject: &Subject{ID: "user-1"},
			},
			wantNil: false,
		},
		{
			name: "subject.roles field",
			expr: "subject.roles contains \"admin\"",
			ctx: &EvalContext{
				Subject: &Subject{
					Roles: []string{"admin", "user"},
				},
			},
			wantNil: false,
		},
		{
			name: "subject.groups field",
			expr: "subject.groups contains \"eng\"",
			ctx: &EvalContext{
				Subject: &Subject{
					Groups: []string{"eng", "ops"},
				},
			},
			wantNil: false,
		},
		{
			name: "resource without request context",
			expr: "resource.owner exists",
			ctx: &EvalContext{
				Resource: "orders://order-123",
				Request:  nil,
			},
			wantNil: false,
		},
		{
			name: "request field - nil request",
			expr: "request.ip exists",
			ctx: &EvalContext{
				Request: nil,
			},
			wantNil: false,
		},
		{
			name: "time field - unknown field",
			expr: "time.unknown exists",
			ctx: &EvalContext{
				Time: time.Now(),
			},
			wantNil: false,
		},
	}

	parser := NewConditionParser(nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			result := cond.Evaluate(tt.ctx)
			_ = result // We're just testing that evaluation doesn't panic
		})
	}
}
