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

//go:build !integration

// roles_resolve_cmd_test.go exercises the "roles get-by-name" and
// "roles resolve-permissions" CLI subcommands.
package main

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// roles get-by-name
// ---------------------------------------------------------------------------

func TestRolesGetByNameCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "get-by-name", "admin")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "admin") {
		t.Fatalf("expected role name admin in output, got: %q", out)
	}
}

func TestRolesGetByNameCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "roles", "get-by-name", "reader", "--tenant-id", "tenant-1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"reader"`) {
		t.Fatalf("expected reader in JSON output, got: %q", out)
	}
}

func TestRolesGetByNameCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "get-by-name", "ghost")
	if err == nil {
		t.Fatal("expected error for unknown role name")
	}
}

func TestRolesGetByNameCmd_MissingArg(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "get-by-name")
	if err == nil {
		t.Fatal("expected error when name argument is missing")
	}
}

// ---------------------------------------------------------------------------
// roles resolve-permissions
// ---------------------------------------------------------------------------

func TestRolesResolvePermissionsCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "resolve-permissions", "role-admin", "role-reader")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	// role-admin grants kv:read/kv:write/admin:manage; role-reader grants
	// kv:read (deduped). All distinct permissions must appear.
	for _, want := range []string{"kv:read", "kv:write", "admin:manage"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected permission %q in output, got: %q", want, out)
		}
	}
}

func TestRolesResolvePermissionsCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "roles", "resolve-permissions", "role-reader")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"permissions"`) || !strings.Contains(out, "kv:read") {
		t.Fatalf("expected permissions JSON with kv:read, got: %q", out)
	}
}

func TestRolesResolvePermissionsCmd_UnknownRole(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "resolve-permissions", "role-missing")
	if err == nil {
		t.Fatal("expected error for unknown role ID")
	}
}

func TestRolesResolvePermissionsCmd_MissingArg(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "resolve-permissions")
	if err == nil {
		t.Fatal("expected error when no role IDs are provided")
	}
}

// ---------------------------------------------------------------------------
// roles reconcile
// ---------------------------------------------------------------------------

// TestRolesReconcileCmd_Success checks the human output names each changed role
// and both directions of the change: an operator running the repair must be
// able to read what it did without re-querying.
func TestRolesReconcileCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "reconcile", "--tenant", "t1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	for _, want := range []string{"t1", "admin", "updated", "+ sdlc:bind", "- sdlc:register", "operator", "created"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got: %q", want, out)
		}
	}
}

// TestRolesReconcileCmd_AlreadyCurrent covers the idempotent case: a converged
// tenant must say so rather than print an empty report an operator has to
// interpret.
func TestRolesReconcileCmd_AlreadyCurrent(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "reconcile", "--tenant", "converged")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "already current") {
		t.Fatalf("expected an already-current line, got: %q", out)
	}
}

func TestRolesReconcileCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "roles", "reconcile", "--tenant", "t1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"sdlc:register"`) || !strings.Contains(out, `"tenant_id"`) {
		t.Fatalf("expected the report in JSON output, got: %q", out)
	}
}

// TestRolesReconcileCmd_RejectsArgs: the tenant is a flag, so a positional
// argument is a mistake the command must refuse rather than ignore.
func TestRolesReconcileCmd_RejectsArgs(t *testing.T) {
	root := newTestRootCmdAuth()

	if _, err := executeCommand(root, "roles", "reconcile", "t1"); err == nil {
		t.Fatal("expected an error for a positional argument")
	}
}
