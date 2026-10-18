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

//go:build !integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ownPermsRBAC is an RBACClient answering ListOwnPermissions with perms or
// err; the other methods are not reached by roles own-permissions.
type ownPermsRBAC struct {
	perms []transport.PermissionInfo
	err   error
}

var _ transport.RBACClient = (*ownPermsRBAC)(nil)

func (r *ownPermsRBAC) Authorize(context.Context, *transport.Identity, string, string) error {
	return auth.ErrUnauthorized
}

func (r *ownPermsRBAC) ListPermissions(context.Context, *transport.Identity) ([]transport.PermissionInfo, error) {
	return nil, auth.ErrUnauthorized
}

func (r *ownPermsRBAC) ListOwnPermissions(context.Context) ([]transport.PermissionInfo, error) {
	return r.perms, r.err
}

// rbacMockTransport is the mock auth transport with an RBAC client.
type rbacMockTransport struct {
	*mockAuthTransport
	rbac transport.RBACClient
}

var _ transport.RBACProvider = (*rbacMockTransport)(nil)

func (t *rbacMockTransport) RBACClient() transport.RBACClient { return t.rbac }

// newTestRootCmdRBAC is newTestRootCmdAuth over a transport whose RBAC
// client is rc.
func newTestRootCmdRBAC(rc transport.RBACClient) *cobra.Command {
	rootCmd := newRootCmd()
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format
		mt, err := newMockAuthTransport()
		if err != nil {
			return err
		}
		c, err := sdk.NewClient(&rbacMockTransport{mockAuthTransport: mt, rbac: rc})
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

var ownPerms = []transport.PermissionInfo{
	{Name: "kv:get", Resource: "kv", Action: "get", Scope: transport.PermissionScopeSelf},
	{Name: "permissions:resolve", Resource: "permissions", Action: "resolve", Scope: transport.PermissionScopeTenantOnly},
}

func TestRolesOwnPermissionsCmd_Table(t *testing.T) {
	out, err := executeCommand(newTestRootCmdRBAC(&ownPermsRBAC{perms: ownPerms}), "roles", "own-permissions")
	if err != nil {
		t.Fatalf("roles own-permissions: %v", err)
	}
	for _, want := range []string{"NAME", "SCOPE", "kv:get", "self", "permissions:resolve", "tenant-only"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q: %q", want, out)
		}
	}
}

func TestRolesOwnPermissionsCmd_JSON(t *testing.T) {
	out, err := executeCommand(newTestRootCmdRBAC(&ownPermsRBAC{perms: ownPerms}), "--output", "json", "roles", "own-permissions")
	if err != nil {
		t.Fatalf("roles own-permissions: %v", err)
	}
	var got ownPermissionsOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(got.Permissions) != len(ownPerms) || got.Permissions[1] != ownPerms[1] {
		t.Fatalf("permissions = %+v, want %+v", got.Permissions, ownPerms)
	}
}

func TestRolesOwnPermissionsCmd_YAML(t *testing.T) {
	out, err := executeCommand(newTestRootCmdRBAC(&ownPermsRBAC{perms: ownPerms}), "--output", "yaml", "roles", "own-permissions")
	if err != nil || !strings.Contains(out, "permissions:") || !strings.Contains(out, "kv:get") {
		t.Fatalf("yaml output = %q, %v", out, err)
	}
}

func TestRolesOwnPermissionsCmd_ServerErrorPropagates(t *testing.T) {
	_, err := executeCommand(newTestRootCmdRBAC(&ownPermsRBAC{err: auth.ErrUnauthenticated}), "roles", "own-permissions")
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestRolesOwnPermissionsCmd_NoRBACClient(t *testing.T) {
	_, err := executeCommand(newTestRootCmdRBAC(nil), "roles", "own-permissions")
	var unavailable *RBACClientUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want *RBACClientUnavailableError", err)
	}
	if unavailable.Error() == "" {
		t.Fatal("empty error message")
	}
}

func TestRolesOwnPermissionsCmd_RejectsArguments(t *testing.T) {
	if _, err := executeCommand(newTestRootCmdRBAC(&ownPermsRBAC{}), "roles", "own-permissions", "extra"); err == nil {
		t.Fatal("expected an error for a positional argument")
	}
}
