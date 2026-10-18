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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func boolPtr(v bool) *bool { return &v }

// enabledPolicy is a valid policy whose Spec.Enabled is the given value
// (nil leaves the field absent).
func enabledPolicy(name string, enabled *bool, rules ...Rule) *Policy {
	p := testPolicy(name, "tenant-a", rules...)
	p.Spec.Enabled = enabled
	return p
}

var (
	denyReadRule  = Rule{Name: "deny-read", Effect: EffectDeny, Actions: []string{"read"}, Resources: []string{"/kv/**"}}
	allowReadRule = Rule{Name: "allow-read", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/kv/**"}}
)

func evaluateRead(t *testing.T, policies ...*Policy) *CheckResponse {
	t.Helper()
	e := NewPolicyEvaluator()
	if err := e.AddPolicies(policies...); err != nil {
		t.Fatalf("AddPolicies() error = %v", err)
	}
	resp, err := e.Evaluate(context.Background(), &CheckRequest{
		Subject:  Subject{ID: "alice", TenantID: "tenant-a", Authenticated: true},
		Action:   "read",
		Resource: "/kv/orders",
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	return resp
}

func TestPolicySpec_EnabledSet(t *testing.T) {
	tests := []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{"absent", nil, false},
		{"true", boolPtr(true), true},
		{"false", boolPtr(false), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := PolicySpec{Enabled: tt.enabled}
			if got := s.EnabledSet(); got != tt.want {
				t.Errorf("EnabledSet() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPolicySpec_IsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{"absent means enabled", nil, true},
		{"true", boolPtr(true), true},
		{"false", boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := PolicySpec{Enabled: tt.enabled}
			if got := s.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPolicySpec_EnabledJSONRoundTrip pins that the document is stored as
// written: an absent field stays absent and an explicit false stays false.
func TestPolicySpec_EnabledJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantSet  bool
		wantKey  bool
		wantJSON string
	}{
		{"absent", `{"subjects":{},"rules":null}`, false, false, ""},
		{"true", `{"enabled":true,"subjects":{},"rules":null}`, true, true, `"enabled":true`},
		{"false", `{"enabled":false,"subjects":{},"rules":null}`, true, true, `"enabled":false`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s PolicySpec
			if err := json.Unmarshal([]byte(tt.in), &s); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if s.EnabledSet() != tt.wantSet {
				t.Errorf("EnabledSet() = %v, want %v", s.EnabledSet(), tt.wantSet)
			}
			out, err := json.Marshal(&s)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if string(out) != tt.in {
				t.Errorf("round trip = %s, want %s", out, tt.in)
			}
			if got := bytes.Contains(out, []byte(`"enabled"`)); got != tt.wantKey {
				t.Errorf("enabled key present = %v, want %v", got, tt.wantKey)
			}
			if tt.wantJSON != "" && !bytes.Contains(out, []byte(tt.wantJSON)) {
				t.Errorf("output %s does not carry %s", out, tt.wantJSON)
			}
		})
	}
}

func TestPolicySpec_EnabledYAMLRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    *bool
		wantOut string
	}{
		{"absent", "", nil, ""},
		{"true", "  enabled: true\n", boolPtr(true), "enabled: true"},
		{"false", "  enabled: false\n", boolPtr(false), "enabled: false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			doc := "apiVersion: abac.qrdb.io/v1\nkind: Policy\nmetadata:\n  name: p\n  tenant: tenant-a\nspec:\n" + tt.line +
				"  rules:\n    - name: r\n      effect: deny\n      actions: [\"read\"]\n      resources: [\"/kv/**\"]\n"
			writePolicy(t, dir, "p.yaml", doc)
			policies, err := LoadFile(filepath.Join(dir, "p.yaml"))
			if err != nil {
				t.Fatalf("LoadFile() error = %v", err)
			}
			got := policies[0].Spec.Enabled
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("Enabled = %v, want %v", got, tt.want)
			}
			out, err := yaml.Marshal(&policies[0].Spec)
			if err != nil {
				t.Fatalf("yaml.Marshal() error = %v", err)
			}
			if tt.wantOut == "" {
				if strings.Contains(string(out), "enabled") {
					t.Errorf("absent field re-emitted:\n%s", out)
				}
			} else if !strings.Contains(string(out), tt.wantOut) {
				t.Errorf("output lacks %q:\n%s", tt.wantOut, out)
			}
		})
	}
}

func TestPolicyEvaluator_DisabledDenyDoesNotDeny(t *testing.T) {
	resp := evaluateRead(t,
		enabledPolicy("allow", nil, allowReadRule),
		enabledPolicy("guardrail", boolPtr(false), denyReadRule),
	)
	if !resp.Allowed {
		t.Fatalf("disabled deny denied: %s", resp.Reason)
	}
	if resp.MatchedPolicy != "allow" {
		t.Errorf("MatchedPolicy = %q, want allow", resp.MatchedPolicy)
	}
}

func TestPolicyEvaluator_DisabledAllowDoesNotAllow(t *testing.T) {
	resp := evaluateRead(t, enabledPolicy("grant", boolPtr(false), allowReadRule))
	if resp.Allowed {
		t.Fatalf("disabled allow allowed: %s", resp.Reason)
	}
	if resp.MatchedPolicy != "" {
		t.Errorf("MatchedPolicy = %q, want empty (no policy has an opinion)", resp.MatchedPolicy)
	}
}

func TestPolicyEvaluator_ExplicitAndAbsentEnabledApply(t *testing.T) {
	for name, enabled := range map[string]*bool{"absent": nil, "true": boolPtr(true)} {
		t.Run(name, func(t *testing.T) {
			resp := evaluateRead(t,
				enabledPolicy("allow", nil, allowReadRule),
				enabledPolicy("guardrail", enabled, denyReadRule),
			)
			if resp.Allowed || resp.MatchedPolicy != "guardrail" {
				t.Fatalf("enabled deny did not deny: allowed=%v policy=%q", resp.Allowed, resp.MatchedPolicy)
			}
		})
	}
}

func TestPolicyEvaluator_DisabledPolicyNotCountedButValidated(t *testing.T) {
	e := NewPolicyEvaluator()
	if err := e.AddPolicies(
		enabledPolicy("on", nil, allowReadRule),
		enabledPolicy("off", boolPtr(false), denyReadRule),
	); err != nil {
		t.Fatalf("AddPolicies() error = %v", err)
	}
	if e.Len() != 1 {
		t.Errorf("Len() = %d, want 1 (disabled policy not stored)", e.Len())
	}

	invalid := enabledPolicy("bad", boolPtr(false))
	err := NewPolicyEvaluator().AddPolicies(invalid)
	var compileErr *CompilationFailedError
	if !errors.As(err, &compileErr) {
		t.Fatalf("disabled invalid policy: error = %v, want *CompilationFailedError", err)
	}
}

func TestDecorate_OnlyDisabledPoliciesPassThrough(t *testing.T) {
	inner := &fakeAuthorizer{}
	authz, err := Decorate(inner, WithPolicies([]*Policy{enabledPolicy("guardrail", boolPtr(false), denyReadRule)}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	if authz != inner {
		t.Fatalf("Decorate() = %T, want the inner authorizer unwrapped", authz)
	}
}

func TestDecorate_PolicyDirDisabledPolicySkipped(t *testing.T) {
	dir := t.TempDir()
	doc := "apiVersion: abac.qrdb.io/v1\nkind: Policy\nmetadata:\n  name: dir-guardrail\n  tenant: tenant-a\nspec:\n" +
		"  enabled: false\n  subjects:\n    any: true\n  rules:\n    - name: no-purge\n      effect: deny\n" +
		"      actions: [\"purge\"]\n      resources: [\"/kv/**\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "guardrail.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := &fakeAuthorizer{}
	authz, err := Decorate(inner, WithPolicyDir(dir), WithLiveReload())
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	if err := authz.Authorize(ctx, testIdentity("alice", "tenant-a"), "/kv/orders", "purge"); err != nil {
		t.Fatalf("disabled dir policy denied: %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner consulted %d times, want 1", inner.calls)
	}
}
