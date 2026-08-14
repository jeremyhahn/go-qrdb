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
