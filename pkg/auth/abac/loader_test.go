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
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writePolicy(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "b.yaml", "apiVersion: abac.qrdb.io/v1\nkind: Policy\nmetadata:\n  name: policy-b\n  tenant: t1\nspec:\n  rules:\n    - name: r1\n      effect: allow\n      actions: [\"read\"]\n      resources: [\"/b/**\"]\n")
	writePolicy(t, dir, "a.yml", "apiVersion: abac.qrdb.io/v1\nkind: Policy\nmetadata:\n  name: policy-a\n  tenant: t1\nspec:\n  rules:\n    - name: r1\n      effect: deny\n      actions: [\"write\"]\n      resources: [\"/a/**\"]\n")
	writePolicy(t, dir, "ignored.txt", "not a policy")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	policies, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}
	if len(policies) != 2 {
		t.Fatalf("loaded %d policies, want 2", len(policies))
	}
	// Lexical file order: a.yml before b.yaml.
	if policies[0].Metadata.Name != "policy-a" || policies[1].Metadata.Name != "policy-b" {
		t.Errorf("load order = [%s, %s], want [policy-a, policy-b]",
			policies[0].Metadata.Name, policies[1].Metadata.Name)
	}
}

func TestLoadFile_MultiDocument(t *testing.T) {
	dir := t.TempDir()
	multi := "apiVersion: abac.qrdb.io/v1\nkind: Policy\nmetadata:\n  name: first\n  tenant: t1\nspec:\n  rules:\n    - name: r1\n      effect: allow\n      actions: [\"read\"]\n      resources: [\"/x/**\"]\n" +
		"---\n" +
		"apiVersion: abac.qrdb.io/v1\nkind: Policy\nmetadata:\n  name: second\n  tenant: t2\nspec:\n  rules:\n    - name: r1\n      effect: deny\n      actions: [\"write\"]\n      resources: [\"/y/**\"]\n"
	writePolicy(t, dir, "multi.yaml", multi)

	policies, err := LoadFile(filepath.Join(dir, "multi.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(policies) != 2 {
		t.Fatalf("loaded %d policies, want 2", len(policies))
	}
	if policies[0].Metadata.Name != "first" || policies[1].Metadata.Name != "second" {
		t.Errorf("names = [%s, %s], want [first, second]", policies[0].Metadata.Name, policies[1].Metadata.Name)
	}
	if policies[1].Metadata.Tenant != "t2" {
		t.Errorf("second tenant = %q, want t2", policies[1].Metadata.Tenant)
	}
}

func TestLoadFile_InvalidPolicy(t *testing.T) {
	dir := t.TempDir()
	// Parses fine but fails validation: no rules.
	writePolicy(t, dir, "invalid.yaml", "apiVersion: abac.qrdb.io/v1\nkind: Policy\nmetadata:\n  name: no-rules\n  tenant: t1\nspec: {}\n")

	_, err := LoadFile(filepath.Join(dir, "invalid.yaml"))
	if err == nil {
		t.Fatal("LoadFile on an invalid policy should fail")
	}
	var fileErr *PolicyFileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("error = %T, want *PolicyFileError", err)
	}
	var invalid *InvalidPolicyError
	if !errors.As(err, &invalid) {
		t.Errorf("PolicyFileError should wrap *InvalidPolicyError, got %v", err)
	}
}

func TestLoadFile_Missing(t *testing.T) {
	_, err := LoadFile("/nonexistent/policy.yaml")
	if err == nil {
		t.Fatal("LoadFile on a missing file should fail")
	}
	var fileErr *PolicyFileError
	if !errors.As(err, &fileErr) {
		t.Errorf("error = %T, want *PolicyFileError", err)
	}
}

func TestLoadFile_SkipsEmptyDocuments(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "trailing.yaml",
		"apiVersion: abac.qrdb.io/v1\nkind: Policy\nmetadata:\n  name: only\n  tenant: t1\nspec:\n  rules:\n    - name: r1\n      effect: allow\n      actions: [\"read\"]\n      resources: [\"/x/**\"]\n---\n")

	policies, err := LoadFile(filepath.Join(dir, "trailing.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(policies) != 1 {
		t.Errorf("loaded %d policies, want 1 (empty trailing doc skipped)", len(policies))
	}
}

func TestPolicyValidate(t *testing.T) {
	valid := func() *Policy {
		return &Policy{
			APIVersion: "abac.qrdb.io/v1",
			Kind:       PolicyKind,
			Metadata:   PolicyMetadata{Name: "p", Tenant: "t"},
			Spec: PolicySpec{
				Rules: []Rule{{Name: "r", Effect: EffectAllow, Actions: []string{"read"}, Resources: []string{"/x"}}},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Policy)
		wantErr bool
	}{
		{name: "valid", mutate: func(*Policy) {}, wantErr: false},
		{name: "missing apiVersion", mutate: func(p *Policy) { p.APIVersion = "" }, wantErr: true},
		{name: "wrong kind", mutate: func(p *Policy) { p.Kind = "Role" }, wantErr: true},
		{name: "missing name", mutate: func(p *Policy) { p.Metadata.Name = "" }, wantErr: true},
		{name: "missing tenant", mutate: func(p *Policy) { p.Metadata.Tenant = "" }, wantErr: true},
		{name: "no rules", mutate: func(p *Policy) { p.Spec.Rules = nil }, wantErr: true},
		{name: "rule missing name", mutate: func(p *Policy) { p.Spec.Rules[0].Name = "" }, wantErr: true},
		{name: "rule bad effect", mutate: func(p *Policy) { p.Spec.Rules[0].Effect = "block" }, wantErr: true},
		{name: "rule no actions", mutate: func(p *Policy) { p.Spec.Rules[0].Actions = nil }, wantErr: true},
		{name: "rule no resources", mutate: func(p *Policy) { p.Spec.Rules[0].Resources = nil }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid()
			tt.mutate(p)
			err := p.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				var invalid *InvalidPolicyError
				if !errors.As(err, &invalid) {
					t.Errorf("error = %T, want *InvalidPolicyError", err)
				}
			}
		})
	}
}
