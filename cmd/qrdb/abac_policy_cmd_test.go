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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

const testABACPolicyYAML = `apiVersion: abac.qrdb.io/v1
kind: Policy
metadata:
  name: deny-secrets
  priority: 10
spec:
  enabled: %s
  subjects:
    roles: [auditor]
  rules:
    - name: deny-secrets
      effect: deny
      actions: [read]
      resources: ["/secrets/**"]
      when:
        - expression: 'subject.department == "finance"'
`

// writeABACPolicy writes a policy document into the test's temporary
// directory and returns its path.
func writeABACPolicy(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newABACPolicyCLIService(t *testing.T) service.PolicyServicer {
	t.Helper()
	svc, err := service.NewPolicyService(dekCLIStores{})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// executeABACPolicyCmd runs a qrdb command against svc over the embedded
// transport and returns its output.
func executeABACPolicyCmd(t *testing.T, svc service.PolicyServicer, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	oldWriter, oldFormat := outputWriter, outputFormat
	t.Cleanup(func() { outputWriter, outputFormat = oldWriter, oldFormat })
	outputWriter = &buf

	root := newRootCmd()
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		format, err := cmd.Root().PersistentFlags().GetString("output")
		if err != nil {
			return err
		}
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format
		tr, err := embedded.New(newMockKVService(), newMockAdminService(), embedded.WithPolicyService(svc))
		if err != nil {
			return err
		}
		c, err := sdk.NewClient(tr)
		if err != nil {
			return err
		}
		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}
		client = c
		return nil
	}
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestABACPolicyCmd_Lifecycle(t *testing.T) {
	svc := newABACPolicyCLIService(t)
	enabled := writeABACPolicy(t, "p.yaml", strings.Replace(testABACPolicyYAML, "%s", "true", 1))
	disabled := writeABACPolicy(t, "p.yaml", strings.Replace(testABACPolicyYAML, "%s", "false", 1))

	out, err := executeABACPolicyCmd(t, svc, "abac", "policy", "create", "-f", enabled)
	if err != nil || !strings.Contains(out, "deny-secrets") || !strings.Contains(out, "true") {
		t.Fatalf("create: %v\n%s", err, out)
	}
	out, err = executeABACPolicyCmd(t, svc, "abac", "policy", "update", "--file", disabled)
	if err != nil || !strings.Contains(out, "false") {
		t.Fatalf("update: %v\n%s", err, out)
	}
	out, err = executeABACPolicyCmd(t, svc, "--output", "json", "abac", "policy", "get", "deny-secrets")
	if err != nil {
		t.Fatalf("get: %v\n%s", err, out)
	}
	var got abac.Policy
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Spec.IsEnabled() || len(got.Spec.Rules[0].When) != 1 || got.Metadata.Priority != 10 {
		t.Fatalf("get json = %+v, %v\n%s", got, err, out)
	}
	out, err = executeABACPolicyCmd(t, svc, "--output", "yaml", "abac", "policy", "get", "deny-secrets")
	if err != nil || !strings.Contains(out, "name: deny-secrets") {
		t.Fatalf("get yaml: %v\n%s", err, out)
	}
	out, err = executeABACPolicyCmd(t, svc, "abac", "policy", "list")
	if err != nil || !strings.Contains(out, "NAME") || !strings.Contains(out, "deny-secrets") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = executeABACPolicyCmd(t, svc, "abac", "policy", "delete", "deny-secrets")
	if err != nil || !strings.Contains(out, "Deleted policy deny-secrets") {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	for format, want := range map[string]string{"json": "[]", "yaml": "[]"} {
		out, err = executeABACPolicyCmd(t, svc, "--output", format, "abac", "policy", "list")
		if err != nil || strings.TrimSpace(out) != want {
			t.Fatalf("list -o %s after delete = %q, %v, want %s", format, out, err, want)
		}
	}
}

func TestABACPolicyCmd_TenantFlag(t *testing.T) {
	svc := newABACPolicyCLIService(t)
	doc := strings.Replace(testABACPolicyYAML, "%s", "true", 1)
	path := writeABACPolicy(t, "p.yaml", doc)

	out, err := executeABACPolicyCmd(t, svc, "--output", "json", "abac", "policy", "create", "--tenant", "system", "-f", path)
	if err != nil {
		t.Fatalf("create --tenant system: %v\n%s", err, out)
	}
	var got abac.Policy
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Metadata.Tenant != "system" {
		t.Fatalf("created = %+v, %v", got, err)
	}

	named := writeABACPolicy(t, "named.yaml", strings.Replace(doc, "  priority: 10\n", "  priority: 10\n  tenant: acme\n", 1))
	_, err = executeABACPolicyCmd(t, svc, "abac", "policy", "update", "--tenant", "system", "-f", named)
	var iae *InvalidArgError
	if !errors.As(err, &iae) || iae.Arg != "tenant" {
		t.Fatalf("a --tenant the document contradicts: err = %v, want InvalidArgError{tenant}", err)
	}
	if _, err := executeABACPolicyCmd(t, svc, "abac", "policy", "list", "--tenant", "globex"); err == nil {
		t.Fatal("a cross-tenant list must fail")
	}
}

func TestABACPolicyCmd_Refusals(t *testing.T) {
	svc := newABACPolicyCLIService(t)
	doc := strings.Replace(testABACPolicyYAML, "%s", "true", 1)
	path := writeABACPolicy(t, "p.yaml", doc)
	if _, err := executeABACPolicyCmd(t, svc, "abac", "policy", "create", "-f", path); err != nil {
		t.Fatal(err)
	}

	two := writeABACPolicy(t, "two.yaml", doc+"---\n"+strings.Replace(doc, "name: deny-secrets\n  priority", "name: other\n  priority", 1))
	_, err := executeABACPolicyCmd(t, svc, "abac", "policy", "create", "-f", two)
	var iae *InvalidArgError
	if !errors.As(err, &iae) || iae.Arg != "file" {
		t.Errorf("two documents: err = %v, want InvalidArgError{file}", err)
	}
	var pfe *ABACPolicyFileError
	if _, err := executeABACPolicyCmd(t, svc, "abac", "policy", "create", "-f", filepath.Join(t.TempDir(), "absent.yaml")); !errors.As(err, &pfe) {
		t.Errorf("missing file: err = %v, want *ABACPolicyFileError", err)
	}
	cases := map[string][]string{
		"duplicate":     {"create", "-f", path},
		"missing get":   {"get", "absent"},
		"missing del":   {"delete", "absent"},
		"no file flag":  {"create"},
		"no name":       {"get"},
		"extra arg":     {"list", "x"},
		"update absent": {"update", "-f", writeABACPolicy(t, "u.yaml", strings.Replace(doc, "name: deny-secrets\n  priority", "name: absent\n  priority", 1))},
	}
	for name, args := range cases {
		if _, err := executeABACPolicyCmd(t, svc, append([]string{"abac", "policy"}, args...)...); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestABACPolicyFileError(t *testing.T) {
	cause := errors.New("no such file")
	err := &ABACPolicyFileError{Path: "p.yaml", Err: cause}
	if got, want := err.Error(), `abac policy file "p.yaml": no such file`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is(err, cause) = false, want true through Unwrap")
	}
}

func TestABACPolicyFileError_Decode(t *testing.T) {
	bad := writeABACPolicy(t, "bad.yaml", "spec: [unclosed\n")
	_, err := loadABACPolicy(newABACPolicyCmd(), bad)
	var pfe *ABACPolicyFileError
	if !errors.As(err, &pfe) || pfe.Path != bad {
		t.Errorf("undecodable document: err = %v, want *ABACPolicyFileError{%s}", err, bad)
	}
}
