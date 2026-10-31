package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// reconcileAllTransport is the auth mock with a fixed all-tenant reconcile
// result, so a test chooses the report (or the error) the CLI renders.
type reconcileAllTransport struct {
	*mockAuthTransport
	resp *transport.ReconcileAllTenantsRolesResponse
	err  error
}

func (r *reconcileAllTransport) ReconcileBuiltInRolesAllTenants(_ context.Context) (*transport.ReconcileAllTenantsRolesResponse, error) {
	return r.resp, r.err
}

// newTestRootCmdReconcileAll is newTestRootCmdAuth over a reconcileAllTransport.
func newTestRootCmdReconcileAll(resp *transport.ReconcileAllTenantsRolesResponse, rerr error) *cobra.Command {
	rootCmd := newRootCmd()
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		base, err := newMockAuthTransport()
		if err != nil {
			return err
		}
		c, err := sdk.NewClient(&reconcileAllTransport{mockAuthTransport: base, resp: resp, err: rerr})
		if err != nil {
			return err
		}
		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}
		client = c
		return nil
	}
	return rootCmd
}

func reconcileAllReport(failed bool) *transport.ReconcileAllTenantsRolesResponse {
	resp := &transport.ReconcileAllTenantsRolesResponse{
		Tenants: []transport.ReconcileRolesResponse{
			{TenantID: "system"},
			{
				TenantID: "acme",
				Roles: []transport.RoleReconcileResult{{
					RoleID:  "role-admin",
					Name:    "admin",
					Added:   []string{"certs:issue"},
					Removed: []string{"certs:generate"},
				}},
			},
		},
		Skipped: []transport.RoleReconcileSkippedTenant{
			{TenantID: "vault", Reason: "sealed", Detail: "barrier sealed"},
		},
	}
	if failed {
		resp.Failed = []transport.RoleReconcileFailedTenant{{TenantID: "broken", Error: "storage unavailable"}}
	}
	return resp
}

func TestRolesReconcileCmd_AllTenantsText(t *testing.T) {
	root := newTestRootCmdReconcileAll(reconcileAllReport(false), nil)

	out, err := executeCommand(root, "roles", "reconcile", "--all-tenants")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	for _, want := range []string{"system", "acme", "+ certs:issue", "- certs:generate", "Tenant vault: skipped (sealed): barrier sealed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got: %q", want, out)
		}
	}
	if strings.Contains(out, "FAILED") {
		t.Fatalf("expected no failure line, got: %q", out)
	}
}

func TestRolesReconcileCmd_AllTenantsFailedTenantExitsNonZero(t *testing.T) {
	root := newTestRootCmdReconcileAll(reconcileAllReport(true), nil)

	out, err := executeCommand(root, "roles", "reconcile", "--all-tenants")
	var failures *RoleReconcileFailuresError
	if !errors.As(err, &failures) {
		t.Fatalf("expected *RoleReconcileFailuresError, got: %v", err)
	}
	if len(failures.Failed) != 1 || failures.Failed[0].TenantID != "broken" {
		t.Fatalf("unexpected failures: %+v", failures.Failed)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("expected the failed tenant in the error, got: %v", err)
	}
	// The whole report is printed before the command fails.
	for _, want := range []string{"acme", "Tenant vault: skipped", "Tenant broken: FAILED: storage unavailable"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got: %q", want, out)
		}
	}
}

func TestRolesReconcileCmd_AllTenantsJSON(t *testing.T) {
	root := newTestRootCmdReconcileAll(reconcileAllReport(true), nil)

	out, err := executeCommand(root, "--output", "json", "roles", "reconcile", "--all-tenants")
	var failures *RoleReconcileFailuresError
	if !errors.As(err, &failures) {
		t.Fatalf("expected *RoleReconcileFailuresError, got: %v", err)
	}
	var got transport.ReconcileAllTenantsRolesResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v: %q", err, out)
	}
	if len(got.Tenants) != 2 || got.Tenants[1].TenantID != "acme" || len(got.Skipped) != 1 || len(got.Failed) != 1 {
		t.Fatalf("unexpected report: %+v", got)
	}
}

func TestRolesReconcileCmd_AllTenantsYAML(t *testing.T) {
	root := newTestRootCmdReconcileAll(reconcileAllReport(false), nil)

	out, err := executeCommand(root, "--output", "yaml", "roles", "reconcile", "--all-tenants")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	var got map[string]any
	if err := yaml.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not YAML: %v: %q", err, out)
	}
	for _, key := range []string{"tenants", "skipped", "failed"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("expected a %s key, got: %q", key, out)
		}
	}
	for _, want := range []string{"acme", "certs:generate", "sealed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got: %q", want, out)
		}
	}
}

func TestRolesReconcileCmd_AllTenantsTransportError(t *testing.T) {
	wantErr := &transport.AuthorizationDeniedError{Resource: "roles", Action: "reconcile_all"}
	root := newTestRootCmdReconcileAll(nil, wantErr)

	_, err := executeCommand(root, "roles", "reconcile", "--all-tenants")
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the transport error, got: %v", err)
	}
}

func TestRolesReconcileCmd_AllTenantsConflictsWithTenant(t *testing.T) {
	root := newTestRootCmdReconcileAll(reconcileAllReport(false), nil)

	_, err := executeCommand(root, "roles", "reconcile", "--all-tenants", "--tenant", "acme")
	if err == nil || !strings.Contains(err.Error(), "all-tenants") {
		t.Fatalf("expected a mutually-exclusive flag error, got: %v", err)
	}
}
