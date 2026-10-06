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

package user

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// CertBindingConflictError.Error coverage
// ---------------------------------------------------------------------------

func TestCertBindingConflictError_Error(t *testing.T) {
	e := &CertBindingConflictError{
		Thumbprint:   "abc123",
		BoundUserID:  42,
		TargetUserID: 99,
	}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error string")
	}
	if !containsStr(msg, "abc123") {
		t.Errorf("expected thumbprint in error message, got: %q", msg)
	}
	if !containsStr(msg, "42") {
		t.Errorf("expected bound user ID in error message, got: %q", msg)
	}
}

// StoreError with Err=nil still produces a message.
func TestStoreError_NilInner(t *testing.T) {
	e := &StoreError{Op: "get", Detail: "index lookup"}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error string")
	}
}

// ---------------------------------------------------------------------------
// store.go – NewStore nil-idgen path coverage
// ---------------------------------------------------------------------------

func TestNewStore_NilIDGen(t *testing.T) {
	_, err := NewStore(newMemKVStore(), nil)
	if err == nil {
		t.Fatal("expected error for nil id generator")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
	if se.Op != "new" {
		t.Errorf("Op: got %q, want new", se.Op)
	}
}

// ---------------------------------------------------------------------------
// store.go – GetUserByUsername / UserByCertThumbprint uncovered branches
// ---------------------------------------------------------------------------

// GetUserByUsername with a get error from the index.
func TestGetUserByUsername_IndexGetError(t *testing.T) {
	eStore := &errKVStore{getErr: errors.New("disk fault")}
	svc, err := NewStore(eStore, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	_, err = svc.GetUserByUsername(context.Background(), "t1", "alice")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
	if se.Op != "get_by_username" {
		t.Errorf("Op: got %q, want get_by_username", se.Op)
	}
}

// UserByCertThumbprint empty thumbprint.
func TestUserByCertThumbprint_EmptyThumbprint(t *testing.T) {
	svc := newTestStore(t)
	_, err := svc.UserByCertThumbprint(context.Background(), "")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

// UserByCertThumbprint with index get error.
func TestUserByCertThumbprint_IndexGetError(t *testing.T) {
	eStore := &errKVStore{getErr: errors.New("disk fault")}
	svc, err := NewStore(eStore, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	_, err = svc.UserByCertThumbprint(context.Background(), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// UserByCertThumbprint returns UserNotFoundError when cert not in store.
func TestUserByCertThumbprint_NotFound(t *testing.T) {
	svc := newTestStore(t)
	_, err := svc.UserByCertThumbprint(context.Background(), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	var nf *UserNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// store.go – AddCertBinding uncovered branches
// ---------------------------------------------------------------------------

// AddCertBinding rejects an invalid thumbprint format.
func TestAddCertBinding_InvalidThumbprint(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "alice", "acme")
	_, err := svc.AddCertBinding(ctx, u.ID, "not-a-valid-thumbprint", 0)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

// AddCertBinding detects a conflict when the thumbprint is already bound
// to a different user.
func TestAddCertBinding_Conflict(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	u1 := createTestUser(t, svc, "alice", "acme")
	u2 := createTestUser(t, svc, "bob", "acme")

	// A valid base64url-no-padding 32-byte (SPKI SHA-256) thumbprint.
	// 32 zero bytes encoded as base64url without padding = 43 chars.
	thumb := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	// Bind thumb to u1.
	_, err := svc.AddCertBinding(ctx, u1.ID, thumb, 0)
	if err != nil {
		t.Fatalf("first AddCertBinding: %v", err)
	}

	// Attempt to bind the same thumb to u2 — must detect conflict.
	_, err = svc.AddCertBinding(ctx, u2.ID, thumb, 0)
	var conflict *CertBindingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected *CertBindingConflictError, got %T: %v", err, err)
	}
	if conflict.BoundUserID != u1.ID {
		t.Errorf("BoundUserID: got %d, want %d", conflict.BoundUserID, u1.ID)
	}
}

// AddCertBinding empty thumbprint returns ValidationError.
func TestAddCertBinding_EmptyThumbprint(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "alice", "acme")
	_, err := svc.AddCertBinding(ctx, u.ID, "", 0)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError for empty thumbprint, got %T: %v", err, err)
	}
}

// AddCertBinding with notAfterUnix > 0 stores the expiry.
func TestAddCertBinding_WithExpiry(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "alice", "acme")
	thumb := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	updated, err := svc.AddCertBinding(ctx, u.ID, thumb, 9999999999)
	if err != nil {
		t.Fatalf("AddCertBinding with expiry: %v", err)
	}
	if len(updated.CertBindings) == 0 {
		t.Fatal("expected at least one cert binding")
	}
	if updated.CertBindings[0].NotAfter.IsZero() {
		t.Error("expected non-zero NotAfter when notAfterUnix > 0")
	}
}

// ---------------------------------------------------------------------------
// store.go – UpdateUser uncovered branches
// ---------------------------------------------------------------------------

// UpdateUser with a username change that conflicts with an existing username.
func TestUpdateUser_UsernameConflict(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestUser(t, svc, "alice", "acme")
	u2 := createTestUser(t, svc, "bob", "acme")

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u2.ID, Username: "alice"})
	var ue *UsernameExistsError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UsernameExistsError, got %T: %v", err, err)
	}
}

// UpdateUser with CertBindings updates the secondary cert index.
func TestUpdateUser_WithCertBindings(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "alice", "acme")
	thumb := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID: u.ID,
		CertBindings: []CertBinding{
			{Thumbprint: thumb, Kind: CertBindingSPKIThumbprint},
		},
	})
	if err != nil {
		t.Fatalf("UpdateUser with CertBindings: %v", err)
	}
	if len(updated.CertBindings) == 0 {
		t.Error("expected cert binding to be stored")
	}
}

// UpdateUser on a non-existent user returns UserNotFoundError.
func TestUpdateUser_UserNotFound(t *testing.T) {
	svc := newTestStore(t)
	_, err := svc.UpdateUser(context.Background(), &UpdateUserRequest{ID: 99999})
	var nf *UserNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// store.go – ListUsers pagination & edge cases
// ---------------------------------------------------------------------------

// ListUsers with page > 1 that overshoots the total.
func TestListUsers_PageBeyondEnd(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	createTestUser(t, svc, "alice", "acme")

	resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme", Page: 100, PageSize: 10})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(resp.Users) != 0 {
		t.Errorf("expected 0 users for page beyond end, got %d", len(resp.Users))
	}
}

// ListUsers default page/pageSize branches.
func TestListUsers_DefaultPageSize(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		createTestUser(t, svc, []string{"user1", "user2", "user3"}[i], "test")
	}

	// Page 0 and PageSize 0 should both default to sensible values.
	resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "test", Page: 0, PageSize: 0})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("Total: got %d, want 3", resp.Total)
	}
}

// ---------------------------------------------------------------------------
// routing.go – routing methods when resolver returns error (sealed)
// ---------------------------------------------------------------------------

func TestRouting_GetUser_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	_, err = svc.GetUser(ctx, 12345)
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetUser(sealed) = %v, want ErrTenantSealed", err)
	}
}

func TestRouting_GetUserByUsername_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	_, err = svc.GetUserByUsername(context.Background(), "vault", "alice")
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetUserByUsername(sealed) = %v, want ErrTenantSealed", err)
	}
}

func TestRouting_UserByCertThumbprint_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	_, err = svc.UserByCertThumbprint(ctx, "somethumb")
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("UserByCertThumbprint(sealed) = %v, want ErrTenantSealed", err)
	}
}

func TestRouting_AddCertBinding_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	_, err = svc.AddCertBinding(ctx, 1, "somethumb", 0)
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("AddCertBinding(sealed) = %v, want ErrTenantSealed", err)
	}
}

func TestRouting_ActivateWithCertBinding_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	activator, ok := svc.(CertActivator)
	if !ok {
		t.Fatal("the routing service does not implement CertActivator")
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	_, err = activator.ActivateWithCertBinding(ctx, 1, "somethumb", 0)
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("ActivateWithCertBinding(sealed) = %v, want ErrTenantSealed", err)
	}
}

func TestRouting_UpdateUser_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	_, err = svc.UpdateUser(ctx, &UpdateUserRequest{ID: 1})
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("UpdateUser(sealed) = %v, want ErrTenantSealed", err)
	}
}

func TestRouting_DeleteUser_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	err = svc.DeleteUser(ctx, 1)
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("DeleteUser(sealed) = %v, want ErrTenantSealed", err)
	}
}

func TestRouting_EnableUser_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	err = svc.EnableUser(ctx, 1)
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("EnableUser(sealed) = %v, want ErrTenantSealed", err)
	}
}

func TestRouting_DisableUser_SealedTenant(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"vault": newFakeKVStore()},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	err = svc.DisableUser(ctx, 1)
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("DisableUser(sealed) = %v, want ErrTenantSealed", err)
	}
}

// ListUsers with nil request to cover nil-req guard.
func TestRouting_ListUsers_NilRequest(t *testing.T) {
	resolver := &fakeResolver{shared: newFakeKVStore(), tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	// nil request → routed to system tenant → inside kvUserStore
	// kvUserStore.ListUsers returns ValidationError for nil req.
	_, err = svc.ListUsers(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil ListUsersRequest")
	}
}

// CreateUser with nil request routes to system tenant then hits validation.
func TestRouting_CreateUser_NilRequest(t *testing.T) {
	resolver := &fakeResolver{shared: newFakeKVStore(), tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	_, err = svc.CreateUser(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil CreateUserRequest")
	}
}

// ---------------------------------------------------------------------------
// routing.go – success paths (non-sealed) to cover the success return branch
// ---------------------------------------------------------------------------

func TestRouting_GetUserByUsername_Success(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()
	// Create user and then look up by username.
	_, createErr := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "routeuser", TenantID: "rt"})
	if createErr != nil {
		t.Fatalf("CreateUser: %v", createErr)
	}
	got, err := svc.GetUserByUsername(ctx, "rt", "routeuser")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.Username != "routeuser" {
		t.Errorf("Username = %q, want routeuser", got.Username)
	}
}

func TestRouting_UserByCertThumbprint_Success(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "thumbuser"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	thumb := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	tctx := tenant.WithTenantID(ctx, u.TenantID)
	_, err = svc.AddCertBinding(tctx, u.ID, thumb, 0)
	if err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	got, err := svc.UserByCertThumbprint(tctx, thumb)
	if err != nil {
		t.Fatalf("UserByCertThumbprint: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("ID = %d, want %d", got.ID, u.ID)
	}
}

func TestRouting_UpdateUser_Success(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "updateroute"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, u.TenantID)
	updated, err := svc.UpdateUser(tctx, &UpdateUserRequest{ID: u.ID, RoleIDs: []string{"admin"}})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if len(updated.RoleIDs) == 0 || updated.RoleIDs[0] != "admin" {
		t.Errorf("RoleIDs = %v, want [admin]", updated.RoleIDs)
	}
}

func TestRouting_DeleteUser_Success(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "deleteroute"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, u.TenantID)
	if err := svc.DeleteUser(tctx, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
}

func TestRouting_EnableDisableUser_Success(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "enroute"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, u.TenantID)
	if err := svc.DisableUser(tctx, u.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if err := svc.EnableUser(tctx, u.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
}

func TestRouting_AddCertBinding_Success(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "certroute"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, u.TenantID)
	thumb := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	updated, err := svc.AddCertBinding(tctx, u.ID, thumb, 0)
	if err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if len(updated.CertBindings) == 0 {
		t.Error("expected cert binding in result")
	}
}

// EffectiveTenant honors an explicit tenant for a system-tenant caller.
func TestEffectiveTenant_SystemExplicit(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	got, err := tenant.EffectiveTenant(ctx, "my-tenant", "create user")
	if err != nil {
		t.Fatalf("EffectiveTenant: %v", err)
	}
	if got != "my-tenant" {
		t.Errorf("EffectiveTenant(system, %q) = %q, want my-tenant", "my-tenant", got)
	}
}

// storeForKV caching: two calls for the same KV pointer return the same store.
func TestRouting_StoreForKV_Caching(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	r := svc.(*routingUserService)

	s1, err := r.storeForKV(shared, &r.stores, r.opts)
	if err != nil {
		t.Fatalf("storeForKV: %v", err)
	}
	s2, err := r.storeForKV(shared, &r.stores, r.opts)
	if err != nil {
		t.Fatalf("storeForKV (2nd call): %v", err)
	}
	if s1 != s2 {
		t.Error("expected the same UserServicer instance for the same KVStore key")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		findSub(s, sub))
}

func findSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
