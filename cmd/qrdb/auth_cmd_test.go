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
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock auth transport
// ---------------------------------------------------------------------------

// mockAuthTransport wraps the embedded transport and overrides the User,
// Role, and Cert client methods with in-memory mock implementations.
type mockAuthTransport struct {
	*embedded.Transport
	users mockUserClient
	roles mockRoleClient
	certs mockCertClient
}

// newMockAuthTransport creates a mock transport with functional User,
// Role, and Cert clients backed by in-memory mock state.
func newMockAuthTransport() (*mockAuthTransport, error) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	base, err := embedded.New(kvSvc, adminSvc)
	if err != nil {
		return nil, err
	}
	return &mockAuthTransport{
		Transport: base,
		users:     newMockUserClient(),
		roles:     newMockRoleClient(),
		certs:     newMockCertClient(),
	}, nil
}

// User client delegation.
func (m *mockAuthTransport) CreateUser(ctx context.Context, req *transport.CreateUserRequest) (*transport.User, error) {
	return m.users.CreateUser(ctx, req)
}
func (m *mockAuthTransport) GetUser(ctx context.Context, id uint64) (*transport.User, error) {
	return m.users.GetUser(ctx, id)
}
func (m *mockAuthTransport) UpdateUser(ctx context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	return m.users.UpdateUser(ctx, req)
}
func (m *mockAuthTransport) DeleteUser(ctx context.Context, id uint64) error {
	return m.users.DeleteUser(ctx, id)
}
func (m *mockAuthTransport) ListUsers(ctx context.Context, req *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	return m.users.ListUsers(ctx, req)
}
func (m *mockAuthTransport) EnableUser(ctx context.Context, id uint64) error {
	return m.users.EnableUser(ctx, id)
}
func (m *mockAuthTransport) DisableUser(ctx context.Context, id uint64) error {
	return m.users.DisableUser(ctx, id)
}

// Role client delegation.
func (m *mockAuthTransport) CreateRole(ctx context.Context, req *transport.CreateRoleRequest) (*transport.Role, error) {
	return m.roles.CreateRole(ctx, req)
}
func (m *mockAuthTransport) GetRole(ctx context.Context, id string) (*transport.Role, error) {
	return m.roles.GetRole(ctx, id)
}
func (m *mockAuthTransport) GetRoleByName(ctx context.Context, req *transport.GetRoleByNameRequest) (*transport.Role, error) {
	return m.roles.GetRoleByName(ctx, req)
}
func (m *mockAuthTransport) UpdateRole(ctx context.Context, req *transport.UpdateRoleRequest) (*transport.Role, error) {
	return m.roles.UpdateRole(ctx, req)
}
func (m *mockAuthTransport) DeleteRole(ctx context.Context, id string) error {
	return m.roles.DeleteRole(ctx, id)
}
func (m *mockAuthTransport) ListRoles(ctx context.Context, req *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	return m.roles.ListRoles(ctx, req)
}
func (m *mockAuthTransport) AssignRole(ctx context.Context, userID uint64, roleID string) error {
	return m.roles.AssignRole(ctx, userID, roleID)
}
func (m *mockAuthTransport) UnassignRole(ctx context.Context, userID uint64, roleID string) error {
	return m.roles.UnassignRole(ctx, userID, roleID)
}
func (m *mockAuthTransport) ListPermissions(ctx context.Context) (*transport.ListPermissionsResponse, error) {
	return m.roles.ListPermissions(ctx)
}
func (m *mockAuthTransport) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	return m.roles.ResolvePermissions(ctx, req)
}

func (m *mockAuthTransport) ReconcileBuiltInRoles(ctx context.Context, req *transport.ReconcileRolesRequest) (*transport.ReconcileRolesResponse, error) {
	return m.roles.ReconcileBuiltInRoles(ctx, req)
}

// Cert client delegation.
func (m *mockAuthTransport) GenerateCert(ctx context.Context, req *transport.GenerateCertRequest) (*transport.CertBundle, error) {
	return m.certs.GenerateCert(ctx, req)
}
func (m *mockAuthTransport) ExportCert(ctx context.Context, req *transport.ExportCertRequest) (*transport.ExportCertResponse, error) {
	return m.certs.ExportCert(ctx, req)
}
func (m *mockAuthTransport) RevokeCert(ctx context.Context, serial string) (*transport.RevokeResult, error) {
	return m.certs.RevokeCert(ctx, serial)
}
func (m *mockAuthTransport) ListCerts(ctx context.Context, tenantID string) (*transport.ListCertsResponse, error) {
	return m.certs.ListCerts(ctx, tenantID)
}

// ---------------------------------------------------------------------------
// Mock user client
// ---------------------------------------------------------------------------

// mockUserClient implements transport.UserClient with in-memory state.
type mockUserClient struct {
	users    map[uint64]*transport.User
	nextID   uint64
	disabled map[uint64]bool
}

func newMockUserClient() mockUserClient {
	users := map[uint64]*transport.User{
		1: {
			ID:        1,
			Username:  "alice",
			TenantID:  "tenant-1",
			RoleIDs:   []string{"role-admin"},
			Enabled:   true,
			System:    false,
			CreatedAt: "2026-01-01T00:00:00Z",
			UpdatedAt: "2026-01-01T00:00:00Z",
		},
		2: {
			ID:        2,
			Username:  "bob",
			TenantID:  "tenant-1",
			RoleIDs:   []string{"role-reader"},
			Enabled:   true,
			System:    false,
			CreatedAt: "2026-01-02T00:00:00Z",
			UpdatedAt: "2026-01-02T00:00:00Z",
		},
	}
	return mockUserClient{
		users:    users,
		nextID:   3,
		disabled: make(map[uint64]bool),
	}
}

func (m *mockUserClient) CreateUser(_ context.Context, req *transport.CreateUserRequest) (*transport.User, error) {
	id := m.nextID
	m.nextID++
	// Echo the identity fields. A double that drops them cannot show whether
	// the CLI forwarded --email/--name at all.
	u := &transport.User{
		ID:        id,
		Username:  req.Username,
		Email:     req.Email,
		Name:      req.Name,
		TenantID:  req.TenantID,
		RoleIDs:   req.RoleIDs,
		Enabled:   true,
		System:    false,
		CreatedAt: "2026-02-26T00:00:00Z",
		UpdatedAt: "2026-02-26T00:00:00Z",
	}
	m.users[id] = u
	return u, nil
}

func (m *mockUserClient) GetUser(_ context.Context, id uint64) (*transport.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, &transport.UserNotFoundError{ID: id}
	}
	return u, nil
}

func (m *mockUserClient) UpdateUser(_ context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	u, ok := m.users[req.ID]
	if !ok {
		return nil, &transport.UserNotFoundError{ID: req.ID}
	}
	if req.Username != "" {
		u.Username = req.Username
	}
	if req.Email != "" {
		u.Email = req.Email
	}
	if req.Name != "" {
		u.Name = req.Name
	}
	if len(req.RoleIDs) > 0 {
		u.RoleIDs = req.RoleIDs
	}
	u.UpdatedAt = "2026-02-26T12:00:00Z"
	return u, nil
}

func (m *mockUserClient) DeleteUser(_ context.Context, id uint64) error {
	if _, ok := m.users[id]; !ok {
		return &transport.UserNotFoundError{ID: id}
	}
	delete(m.users, id)
	return nil
}

func (m *mockUserClient) ListUsers(_ context.Context, _ *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	var users []*transport.User
	for _, u := range m.users {
		users = append(users, u)
	}
	return &transport.ListUsersResponse{
		Users:   users,
		Total:   len(users),
		HasMore: false,
	}, nil
}

func (m *mockUserClient) EnableUser(_ context.Context, id uint64) error {
	u, ok := m.users[id]
	if !ok {
		return &transport.UserNotFoundError{ID: id}
	}
	u.Enabled = true
	delete(m.disabled, id)
	return nil
}

func (m *mockUserClient) DisableUser(_ context.Context, id uint64) error {
	u, ok := m.users[id]
	if !ok {
		return &transport.UserNotFoundError{ID: id}
	}
	u.Enabled = false
	m.disabled[id] = true
	return nil
}

// ---------------------------------------------------------------------------
// Mock role client
// ---------------------------------------------------------------------------

// mockRoleClient implements transport.RoleClient with in-memory state.
type mockRoleClient struct {
	roles  map[string]*transport.Role
	nextID int
}

func newMockRoleClient() mockRoleClient {
	roles := map[string]*transport.Role{
		"role-admin": {
			ID:          "role-admin",
			Name:        "admin",
			TenantID:    "tenant-1",
			Permissions: []string{"kv:read", "kv:write", "admin:manage"},
			System:      true,
			CreatedAt:   "2026-01-01T00:00:00Z",
			UpdatedAt:   "2026-01-01T00:00:00Z",
		},
		"role-reader": {
			ID:          "role-reader",
			Name:        "reader",
			TenantID:    "tenant-1",
			Permissions: []string{"kv:read"},
			System:      false,
			CreatedAt:   "2026-01-02T00:00:00Z",
			UpdatedAt:   "2026-01-02T00:00:00Z",
		},
	}
	return mockRoleClient{roles: roles, nextID: 3}
}

func (m *mockRoleClient) CreateRole(_ context.Context, req *transport.CreateRoleRequest) (*transport.Role, error) {
	id := "role-" + strings.Repeat("0", m.nextID)
	m.nextID++
	r := &transport.Role{
		ID:          id,
		Name:        req.Name,
		TenantID:    req.TenantID,
		Permissions: req.Permissions,
		System:      false,
		CreatedAt:   "2026-02-26T00:00:00Z",
		UpdatedAt:   "2026-02-26T00:00:00Z",
	}
	m.roles[id] = r
	return r, nil
}

func (m *mockRoleClient) GetRole(_ context.Context, id string) (*transport.Role, error) {
	r, ok := m.roles[id]
	if !ok {
		return nil, &transport.RoleNotFoundError{ID: id}
	}
	return r, nil
}

func (m *mockRoleClient) GetRoleByName(_ context.Context, req *transport.GetRoleByNameRequest) (*transport.Role, error) {
	for _, r := range m.roles {
		if r.Name == req.Name {
			return r, nil
		}
	}
	return nil, &transport.RoleNotFoundError{ID: req.Name}
}

// ReconcileBuiltInRoles reports a fixed repair: one built-in that gained a
// verb and lost a retired one, and one that was seeded — the go-qrdb#354 shape.
func (m *mockRoleClient) ReconcileBuiltInRoles(_ context.Context, req *transport.ReconcileRolesRequest) (*transport.ReconcileRolesResponse, error) {
	tenantID := ""
	if req != nil {
		tenantID = req.TenantID
	}
	if tenantID == "converged" {
		return &transport.ReconcileRolesResponse{TenantID: tenantID}, nil
	}
	return &transport.ReconcileRolesResponse{
		TenantID: tenantID,
		Roles: []transport.RoleReconcileResult{
			{
				RoleID:  "role-admin",
				Name:    "admin",
				Added:   []string{"sdlc:bind"},
				Removed: []string{"sdlc:register"},
			},
			{
				RoleID:  "role-operator",
				Name:    "operator",
				Created: true,
				Added:   []string{"orchestrator:task_block"},
			},
		},
	}, nil
}

func (m *mockRoleClient) ResolvePermissions(_ context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, id := range req.RoleIDs {
		r, ok := m.roles[id]
		if !ok {
			return nil, &transport.RoleNotFoundError{ID: id}
		}
		for _, p := range r.Permissions {
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return &transport.ResolvePermissionsResponse{Permissions: out}, nil
}

func (m *mockRoleClient) UpdateRole(_ context.Context, req *transport.UpdateRoleRequest) (*transport.Role, error) {
	r, ok := m.roles[req.ID]
	if !ok {
		return nil, &transport.RoleNotFoundError{ID: req.ID}
	}
	if req.Name != "" {
		r.Name = req.Name
	}
	if len(req.Permissions) > 0 {
		r.Permissions = req.Permissions
	}
	r.UpdatedAt = "2026-02-26T12:00:00Z"
	return r, nil
}

func (m *mockRoleClient) DeleteRole(_ context.Context, id string) error {
	if _, ok := m.roles[id]; !ok {
		return &transport.RoleNotFoundError{ID: id}
	}
	delete(m.roles, id)
	return nil
}

func (m *mockRoleClient) ListRoles(_ context.Context, _ *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	var roles []*transport.Role
	for _, r := range m.roles {
		roles = append(roles, r)
	}
	return &transport.ListRolesResponse{
		Roles:   roles,
		Total:   len(roles),
		HasMore: false,
	}, nil
}

func (m *mockRoleClient) AssignRole(_ context.Context, _ uint64, _ string) error {
	return nil
}

func (m *mockRoleClient) UnassignRole(_ context.Context, _ uint64, _ string) error {
	return nil
}

func (m *mockRoleClient) ListPermissions(_ context.Context) (*transport.ListPermissionsResponse, error) {
	return &transport.ListPermissionsResponse{
		Categories: []*transport.PermissionCategory{
			{
				Name:        "Key-Value",
				Group:       "data",
				Permissions: []string{"kv:read", "kv:write", "kv:delete"},
			},
			{
				Name:        "Admin",
				Group:       "control",
				Permissions: []string{"admin:manage", "admin:drain"},
			},
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Mock cert client
// ---------------------------------------------------------------------------

// mockCertClient implements transport.CertClient with in-memory state.
type mockCertClient struct {
	certs []*transport.CertRecord
}

func newMockCertClient() mockCertClient {
	return mockCertClient{
		certs: []*transport.CertRecord{
			{
				Serial:    "ABC123",
				Username:  "alice",
				TenantID:  "tenant-1",
				IssuedAt:  "2026-01-01T00:00:00Z",
				ExpiresAt: "2027-01-01T00:00:00Z",
				Revoked:   false,
			},
			{
				Serial:    "DEF456",
				Username:  "bob",
				TenantID:  "tenant-1",
				IssuedAt:  "2026-01-02T00:00:00Z",
				ExpiresAt: "2027-01-02T00:00:00Z",
				Revoked:   false,
			},
		},
	}
}

func (m *mockCertClient) GenerateCert(_ context.Context, req *transport.GenerateCertRequest) (*transport.CertBundle, error) {
	if req.Username == "" {
		return nil, errors.New("username required")
	}
	return &transport.CertBundle{
		CertPEM: "-----BEGIN CERTIFICATE-----\nMOCKCERT\n-----END CERTIFICATE-----\n",
		KeyPEM:  "-----BEGIN PRIVATE KEY-----\nMOCKKEY\n-----END PRIVATE KEY-----\n",
		Serial:  "SERIAL-001",
	}, nil
}

func (m *mockCertClient) ExportCert(_ context.Context, _ *transport.ExportCertRequest) (*transport.ExportCertResponse, error) {
	data := base64.StdEncoding.EncodeToString([]byte("mock-pkcs12-data"))
	return &transport.ExportCertResponse{
		PKCS12: data,
	}, nil
}

func (m *mockCertClient) RevokeCert(_ context.Context, serial string) (*transport.RevokeResult, error) {
	for _, c := range m.certs {
		if c.Serial == serial {
			c.Revoked = true
			return &transport.RevokeResult{Serial: serial, RecordFound: true}, nil
		}
	}
	return nil, errors.New("certificate not found: " + serial)
}

func (m *mockCertClient) ListCerts(_ context.Context, _ string) (*transport.ListCertsResponse, error) {
	return &transport.ListCertsResponse{
		Certs: m.certs,
	}, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// newTestRootCmdAuth creates a root command wired to a mock auth transport
// for testing users, roles, and certs CLI commands.
func newTestRootCmdAuth() *cobra.Command {
	rootCmd := newRootCmd()

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}

		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		t, err := newMockAuthTransport()
		if err != nil {
			return err
		}

		c, err := sdk.NewClient(t)
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

// ===========================================================================
// User command tests
// ===========================================================================

func TestUsersListCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "ID") || !strings.Contains(out, "USERNAME") {
		t.Fatalf("expected table headers ID and USERNAME, got: %q", out)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("expected user alice in output, got: %q", out)
	}
	if !strings.Contains(out, "bob") {
		t.Fatalf("expected user bob in output, got: %q", out)
	}
}

func TestUsersListCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "users", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"users"`) {
		t.Fatalf("expected JSON users field, got: %q", out)
	}
	if !strings.Contains(out, `"alice"`) {
		t.Fatalf("expected alice in JSON output, got: %q", out)
	}
}

func TestUsersCreateCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "create", "--without-email-login-key", "--username", "charlie")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "charlie") {
		t.Fatalf("expected username charlie in output, got: %q", out)
	}
	if !strings.Contains(out, "ID:") {
		t.Fatalf("expected ID field in output, got: %q", out)
	}
}

func TestUsersCreateCmd_SuccessWithRoles(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "create", "--without-email-login-key", "--username", "dave", "--roles", "role-admin, role-reader")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "dave") {
		t.Fatalf("expected username dave in output, got: %q", out)
	}
	if !strings.Contains(out, "role-admin") {
		t.Fatalf("expected role-admin in output, got: %q", out)
	}
}

func TestUsersCreateCmd_MissingUsername(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "users", "create", "--without-email-login-key")
	if err == nil {
		t.Fatal("expected error for missing --username flag")
	}
}

func TestUsersGetCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "get", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("expected username alice in output, got: %q", out)
	}
	if !strings.Contains(out, "ID:        1") {
		t.Fatalf("expected user ID 1 in output, got: %q", out)
	}
}

func TestUsersGetCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "users", "get", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"username"`) {
		t.Fatalf("expected JSON username field, got: %q", out)
	}
	if !strings.Contains(out, `"alice"`) {
		t.Fatalf("expected alice in JSON output, got: %q", out)
	}
}

func TestUsersGetCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "users", "get", "999999")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
	var nfe *transport.UserNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected UserNotFoundError, got: %T: %v", err, err)
	}
}

func TestUsersUpdateCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "update", "1", "--username", "alice-updated")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "alice-updated") {
		t.Fatalf("expected updated username in output, got: %q", out)
	}
}

func TestUsersUpdateCmd_SuccessWithRoles(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "update", "2", "--roles", "role-admin")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "role-admin") {
		t.Fatalf("expected role-admin in output, got: %q", out)
	}
}

func TestUsersDeleteCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "delete", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestUsersDeleteCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "users", "delete", "999999")
	if err == nil {
		t.Fatal("expected error for deleting nonexistent user")
	}
	var nfe *transport.UserNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected UserNotFoundError, got: %T: %v", err, err)
	}
}

func TestUsersEnableCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "enable", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestUsersEnableCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "users", "enable", "999999")
	if err == nil {
		t.Fatal("expected error for enabling nonexistent user")
	}
	var nfe *transport.UserNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected UserNotFoundError, got: %T: %v", err, err)
	}
}

func TestUsersDisableCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "users", "disable", "2")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestUsersDisableCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "users", "disable", "999999")
	if err == nil {
		t.Fatal("expected error for disabling nonexistent user")
	}
	var nfe *transport.UserNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected UserNotFoundError, got: %T: %v", err, err)
	}
}

func TestUsersListCmd_SuccessYAML(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "yaml", "users", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "users:") {
		t.Fatalf("expected YAML users field, got: %q", out)
	}
}

func TestUsersCreateCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "users", "create", "--without-email-login-key", "--username", "eve")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"username"`) {
		t.Fatalf("expected JSON username field, got: %q", out)
	}
	if !strings.Contains(out, `"eve"`) {
		t.Fatalf("expected eve in JSON output, got: %q", out)
	}
}

func TestUsersGetCmd_MissingArg(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "users", "get")
	if err == nil {
		t.Fatal("expected error for missing user ID argument")
	}
}

func TestUsersDeleteCmd_MissingArg(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "users", "delete")
	if err == nil {
		t.Fatal("expected error for missing user ID argument")
	}
}

// ===========================================================================
// Role command tests
// ===========================================================================

func TestRolesListCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "ID") || !strings.Contains(out, "NAME") {
		t.Fatalf("expected table headers ID and NAME, got: %q", out)
	}
	if !strings.Contains(out, "admin") {
		t.Fatalf("expected role admin in output, got: %q", out)
	}
	if !strings.Contains(out, "reader") {
		t.Fatalf("expected role reader in output, got: %q", out)
	}
}

func TestRolesListCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "roles", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"roles"`) {
		t.Fatalf("expected JSON roles field, got: %q", out)
	}
	if !strings.Contains(out, `"admin"`) {
		t.Fatalf("expected admin in JSON output, got: %q", out)
	}
}

func TestRolesListCmd_SuccessYAML(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "yaml", "roles", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "roles:") {
		t.Fatalf("expected YAML roles field, got: %q", out)
	}
}

func TestRolesCreateCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "create", "--name", "writer", "--permissions", "kv:read,kv:write")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "writer") {
		t.Fatalf("expected role name writer in output, got: %q", out)
	}
	if !strings.Contains(out, "kv:read") {
		t.Fatalf("expected permission kv:read in output, got: %q", out)
	}
}

func TestRolesCreateCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "roles", "create", "--name", "editor", "--permissions", "kv:read,kv:write")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"name"`) {
		t.Fatalf("expected JSON name field, got: %q", out)
	}
	if !strings.Contains(out, `"editor"`) {
		t.Fatalf("expected editor in JSON output, got: %q", out)
	}
}

func TestRolesCreateCmd_MissingFlags(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "create")
	if err == nil {
		t.Fatal("expected error for missing required flags --name and --permissions")
	}
}

func TestRolesCreateCmd_MissingName(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "create", "--permissions", "kv:read")
	if err == nil {
		t.Fatal("expected error for missing --name flag")
	}
}

func TestRolesCreateCmd_MissingPermissions(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "create", "--name", "incomplete")
	if err == nil {
		t.Fatal("expected error for missing --permissions flag")
	}
}

func TestRolesGetCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "get", "role-admin")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "admin") {
		t.Fatalf("expected role name admin in output, got: %q", out)
	}
	if !strings.Contains(out, "role-admin") {
		t.Fatalf("expected role ID role-admin in output, got: %q", out)
	}
}

func TestRolesGetCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "roles", "get", "role-admin")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"name"`) {
		t.Fatalf("expected JSON name field, got: %q", out)
	}
}

func TestRolesGetCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "get", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
	var nfe *transport.RoleNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected RoleNotFoundError, got: %T: %v", err, err)
	}
}

func TestRolesUpdateCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "update", "role-reader", "--name", "reader-updated")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "reader-updated") {
		t.Fatalf("expected updated name in output, got: %q", out)
	}
}

func TestRolesUpdateCmd_SuccessWithPermissions(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "update", "role-reader", "--permissions", "kv:read,kv:write")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "kv:write") {
		t.Fatalf("expected kv:write permission in output, got: %q", out)
	}
}

func TestRolesUpdateCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "update", "nonexistent", "--name", "nope")
	if err == nil {
		t.Fatal("expected error for updating nonexistent role")
	}
	var nfe *transport.RoleNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected RoleNotFoundError, got: %T: %v", err, err)
	}
}

func TestRolesDeleteCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "delete", "role-reader")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestRolesDeleteCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "delete", "nonexistent")
	if err == nil {
		t.Fatal("expected error for deleting nonexistent role")
	}
	var nfe *transport.RoleNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected RoleNotFoundError, got: %T: %v", err, err)
	}
}

func TestRolesAssignCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "assign", "--user", "1", "--role", "role-reader")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestRolesAssignCmd_MissingUser(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "assign", "--role", "role-reader")
	if err == nil {
		t.Fatal("expected error for missing --user flag")
	}
}

func TestRolesAssignCmd_MissingRole(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "assign", "--user", "1")
	if err == nil {
		t.Fatal("expected error for missing --role flag")
	}
}

func TestRolesUnassignCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "unassign", "--user", "1", "--role", "role-admin")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestRolesUnassignCmd_MissingFlags(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "unassign")
	if err == nil {
		t.Fatal("expected error for missing --user and --role flags")
	}
}

func TestRolesPermissionsCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "roles", "permissions")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "Key-Value") {
		t.Fatalf("expected Key-Value category in output, got: %q", out)
	}
	if !strings.Contains(out, "kv:read") {
		t.Fatalf("expected kv:read permission in output, got: %q", out)
	}
	if !strings.Contains(out, "Admin") {
		t.Fatalf("expected Admin category in output, got: %q", out)
	}
}

func TestRolesPermissionsCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "roles", "permissions")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"categories"`) {
		t.Fatalf("expected JSON categories field, got: %q", out)
	}
	if !strings.Contains(out, `"kv:read"`) {
		t.Fatalf("expected kv:read in JSON output, got: %q", out)
	}
}

func TestRolesPermissionsCmd_SuccessYAML(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "yaml", "roles", "permissions")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "categories:") {
		t.Fatalf("expected YAML categories field, got: %q", out)
	}
}

func TestRolesGetCmd_MissingArg(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "get")
	if err == nil {
		t.Fatal("expected error for missing role ID argument")
	}
}

func TestRolesDeleteCmd_MissingArg(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "roles", "delete")
	if err == nil {
		t.Fatal("expected error for missing role ID argument")
	}
}

// ===========================================================================
// Cert command tests
// ===========================================================================

func TestCertsGenerateCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "ca", "issue", "--username", "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "SERIAL-001") {
		t.Fatalf("expected serial number in output, got: %q", out)
	}
	if !strings.Contains(out, "CERTIFICATE") {
		t.Fatalf("expected certificate PEM in output, got: %q", out)
	}
	if !strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("expected private key PEM in output, got: %q", out)
	}
}

func TestCertsGenerateCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "ca", "issue", "--username", "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"serial"`) {
		t.Fatalf("expected JSON serial field, got: %q", out)
	}
	if !strings.Contains(out, `"certPEM"`) {
		t.Fatalf("expected JSON certPEM field, got: %q", out)
	}
}

func TestCertsGenerateCmd_SuccessYAML(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "yaml", "ca", "issue", "--username", "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "serial:") {
		t.Fatalf("expected YAML serial field, got: %q", out)
	}
}

func TestCertsGenerateCmd_MissingUsername(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "ca", "issue")
	if err == nil {
		t.Fatal("expected error for missing --username flag")
	}
}

func TestCertsGenerateCmd_WithOutDir(t *testing.T) {
	root := newTestRootCmdAuth()

	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "certout")

	out, err := executeCommand(root, "ca", "issue", "--username", "alice", "--out", outDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "cert.pem") {
		t.Fatalf("expected cert.pem path in output, got: %q", out)
	}
	if !strings.Contains(out, "key.pem") {
		t.Fatalf("expected key.pem path in output, got: %q", out)
	}
	if !strings.Contains(out, "SERIAL-001") {
		t.Fatalf("expected serial number in output, got: %q", out)
	}

	// Verify files were written.
	certData, err := os.ReadFile(filepath.Join(outDir, "cert.pem"))
	if err != nil {
		t.Fatalf("expected cert.pem to exist: %v", err)
	}
	if !strings.Contains(string(certData), "CERTIFICATE") {
		t.Fatalf("expected certificate PEM content, got: %q", certData)
	}

	keyData, err := os.ReadFile(filepath.Join(outDir, "key.pem"))
	if err != nil {
		t.Fatalf("expected key.pem to exist: %v", err)
	}
	if !strings.Contains(string(keyData), "PRIVATE KEY") {
		t.Fatalf("expected private key PEM content, got: %q", keyData)
	}
}

func TestCertsExportCmd_Success(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mock PEM files for the export command to read.
	certPEM := "-----BEGIN CERTIFICATE-----\nMOCKCERT\n-----END CERTIFICATE-----\n"
	keyPEM := "-----BEGIN PRIVATE KEY-----\nMOCKKEY\n-----END PRIVATE KEY-----\n"

	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	outFile := filepath.Join(tmpDir, "client.p12")

	if err := os.WriteFile(certFile, []byte(certPEM), 0644); err != nil {
		t.Fatalf("failed to write cert file: %v", err)
	}
	if err := os.WriteFile(keyFile, []byte(keyPEM), 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "ca", "export-bundle",
		"--cert-pem", certFile,
		"--key-pem", keyFile,
		"--passphrase", "test-pass",
		"--out", outFile,
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "PKCS#12") {
		t.Fatalf("expected PKCS#12 message in output, got: %q", out)
	}
	if !strings.Contains(out, outFile) {
		t.Fatalf("expected output file path in output, got: %q", out)
	}

	// Verify PKCS#12 file was written.
	p12Data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("expected PKCS#12 file to exist: %v", err)
	}
	if len(p12Data) == 0 {
		t.Fatal("expected non-empty PKCS#12 file")
	}
}

func TestCertsExportCmd_MissingCertPEM(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "ca", "export-bundle",
		"--key-pem", "/tmp/key.pem",
		"--passphrase", "test",
		"--out", "/tmp/out.p12",
	)
	if err == nil {
		t.Fatal("expected error for missing --cert-pem flag")
	}
}

func TestCertsExportCmd_MissingKeyPEM(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "ca", "export-bundle",
		"--cert-pem", "/tmp/cert.pem",
		"--passphrase", "test",
		"--out", "/tmp/out.p12",
	)
	if err == nil {
		t.Fatal("expected error for missing --key-pem flag")
	}
}

func TestCertsExportCmd_NonexistentCertFile(t *testing.T) {
	tmpDir := t.TempDir()
	keyFile := filepath.Join(tmpDir, "key.pem")
	if err := os.WriteFile(keyFile, []byte("key"), 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "ca", "export-bundle",
		"--cert-pem", "/nonexistent/cert.pem",
		"--key-pem", keyFile,
		"--passphrase", "test",
		"--out", filepath.Join(tmpDir, "out.p12"),
	)
	if err == nil {
		t.Fatal("expected error for nonexistent cert file")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected FileReadError, got: %T: %v", err, err)
	}
}

func TestCertsRevokeCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "ca", "revoke", "--serial", "ABC123")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestCertsRevokeCmd_MissingSerial(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "ca", "revoke")
	if err == nil {
		t.Fatal("expected error for missing --serial flag")
	}
}

func TestCertsRevokeCmd_NotFound(t *testing.T) {
	root := newTestRootCmdAuth()

	_, err := executeCommand(root, "ca", "revoke", "--serial", "UNKNOWN-SERIAL")
	if err == nil {
		t.Fatal("expected error for revoking nonexistent certificate")
	}
}

func TestCertsListCmd_Success(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "ca", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "SERIAL") || !strings.Contains(out, "USERNAME") {
		t.Fatalf("expected table headers SERIAL and USERNAME, got: %q", out)
	}
	if !strings.Contains(out, "ABC123") {
		t.Fatalf("expected cert serial ABC123 in output, got: %q", out)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("expected username alice in output, got: %q", out)
	}
}

func TestCertsListCmd_SuccessJSON(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "json", "ca", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"certs"`) {
		t.Fatalf("expected JSON certs field, got: %q", out)
	}
	if !strings.Contains(out, `"ABC123"`) {
		t.Fatalf("expected ABC123 in JSON output, got: %q", out)
	}
}

func TestCertsListCmd_SuccessYAML(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "--output", "yaml", "ca", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "certs:") {
		t.Fatalf("expected YAML certs field, got: %q", out)
	}
}

func TestCertsListCmd_WithTenantFilter(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "ca", "list", "--tenant-id", "tenant-1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "SERIAL") {
		t.Fatalf("expected table header SERIAL, got: %q", out)
	}
}

func TestCertsGenerateCmd_WithTTL(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "ca", "issue", "--username", "alice", "--ttl", "8760h")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "SERIAL-001") {
		t.Fatalf("expected serial number in output, got: %q", out)
	}
}

func TestCertsGenerateCmd_WithTenantID(t *testing.T) {
	root := newTestRootCmdAuth()

	out, err := executeCommand(root, "ca", "issue", "--username", "alice", "--tenant-id", "tenant-1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "SERIAL-001") {
		t.Fatalf("expected serial number in output, got: %q", out)
	}
}

// The users create/update commands must actually forward the identity flags.
//
// The flags were added to the CLI in this PR and the integration catalogue
// listed them for create but not update, so a dropped or miswired --email on
// update would not have been caught anywhere (Codex, PR #191).
func TestUsersCreate_ForwardsIdentityFlags(t *testing.T) {
	root := newTestRootCmdAuth()
	out, err := executeCommand(root, "--output", "json",
		"users", "create", "--username", "flagtest", "--email", "flag@example.com", "--name", "Flag Test")
	if err != nil {
		t.Fatalf("users create: %v (out=%s)", err, out)
	}
	if !strings.Contains(out, "flag@example.com") {
		t.Fatalf("--email did not reach the request/response path: %s", out)
	}
}

func TestUsersUpdate_ForwardsIdentityFlags(t *testing.T) {
	root := newTestRootCmdAuth()

	// User 1 is pre-seeded by the mock, as the other update tests rely on.
	out, err := executeCommand(root, "users", "update", "1",
		"--email", "updated@example.com", "--name", "Updated")
	if err != nil {
		t.Fatalf("users update: %v (out=%s)", err, out)
	}
	if !strings.Contains(out, "updated@example.com") {
		t.Fatalf("--email did not reach the request/response path: %s", out)
	}
	if !strings.Contains(out, "Updated") {
		t.Fatalf("--name did not reach the request/response path: %s", out)
	}
}
