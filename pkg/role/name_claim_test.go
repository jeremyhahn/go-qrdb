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

package role

import (
	"context"
	"errors"
	"strings"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// The non-atomic CreateRole claims a role's name with PutIfAbsent before it
// writes anything, and its rollback gives the claim back only while the
// name still holds this create's id: the shape of pkg/user's claimNewHandle
// and releaseClaims (go-qrdb#1312).

// builtInAuditorRequest is the canonical create request for the built-in
// auditor role in tenantID: a fixed, deterministic id.
func builtInAuditorRequest(t *testing.T, tenantID string) *CreateRoleRequest {
	t.Helper()
	c := canonicalSystemRole(tenantID, "auditor")
	if c == nil {
		t.Fatal(`canonicalSystemRole("auditor") = nil, want the built-in`)
	}
	return &CreateRoleRequest{ID: c.ID, Name: c.Name, TenantID: tenantID, Permissions: c.Permissions}
}

// customViewerRequest is a custom role whose id CreateRole generates.
func customViewerRequest() *CreateRoleRequest {
	return &CreateRoleRequest{Name: "viewer", TenantID: "acme", Permissions: []permission.Permission{permission.KVGet}}
}

func nameIndexValue(t *testing.T, kv *memKVStore, tenantID, name string) []byte {
	t.Helper()
	v, err := kv.Get(context.Background(), nameIndexKey(tenantID, name))
	if err != nil {
		t.Fatalf("read name index: %v", err)
	}
	return v
}

// A claim whose answer was lost but which committed is released by the
// rollback, and the retry then succeeds: for a built-in and a custom role.
func TestCreateRole_LostNameClaimIsReleasedAndRetrySucceeds(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(t *testing.T) *CreateRoleRequest
	}{
		{"built-in", func(t *testing.T) *CreateRoleRequest { return builtInAuditorRequest(t, "acme") }},
		{"custom", func(*testing.T) *CreateRoleRequest { return customViewerRequest() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := newMemKVStore()
			lost := errors.New("quicraft: timeout (it may still commit)")
			fkv := &faultKVStore{inner: inner, claimLostErr: lost}
			svc, err := NewStore(fkv)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			req := tc.req(t)

			_, err = svc.CreateRole(context.Background(), req)
			var se *StoreError
			if !errors.As(err, &se) || !errors.Is(err, lost) {
				t.Fatalf("CreateRole = %v, want a *StoreError wrapping the lost answer", err)
			}
			if v := nameIndexValue(t, inner, req.TenantID, req.Name); v != nil {
				t.Fatalf("name index after rollback = %q, want it released", v)
			}

			fkv.claimLostErr = nil
			created, err := svc.CreateRole(context.Background(), req)
			if err != nil {
				t.Fatalf("retry CreateRole: %v", err)
			}
			assertRoleFullyIndexed(t, svc, created)
		})
	}
}

// A claim that lost its answer and did not land, while another create took
// the name, leaves the other create's entry alone.
func TestCreateRole_NameClaimHeldByAnotherCreateIsNotReleased(t *testing.T) {
	inner := newMemKVStore()
	lost := errors.New("quicraft: timeout")
	fkv := &faultKVStore{inner: inner, claimLostErr: lost}
	fkv.onClaim = func() {
		if err := inner.Put(context.Background(), nameIndexKey("acme", "viewer"), []byte("winner-id")); err != nil {
			t.Fatalf("seed winner: %v", err)
		}
	}
	svc, err := NewStore(fkv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := svc.CreateRole(context.Background(), customViewerRequest()); !errors.Is(err, lost) {
		t.Fatalf("CreateRole = %v, want the lost answer", err)
	}
	if v := nameIndexValue(t, inner, "acme", "viewer"); string(v) != "winner-id" {
		t.Fatalf("name index = %q, want the other create's %q", v, "winner-id")
	}
}

// A claim refused because another create took the name after the lookup is
// reported as RoleNameExistsError, and the winner's entry stays.
func TestCreateRole_NameClaimRefusedWhenTakenAfterLookup(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(t *testing.T) *CreateRoleRequest
	}{
		{"built-in", func(t *testing.T) *CreateRoleRequest { return builtInAuditorRequest(t, "acme") }},
		{"custom", func(*testing.T) *CreateRoleRequest { return customViewerRequest() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := newMemKVStore()
			req := tc.req(t)
			fkv := &faultKVStore{inner: inner}
			fkv.onClaim = func() {
				if err := inner.Put(context.Background(), nameIndexKey(req.TenantID, req.Name), []byte("winner-id")); err != nil {
					t.Fatalf("seed winner: %v", err)
				}
			}
			svc, err := NewStore(fkv)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			_, err = svc.CreateRole(context.Background(), req)
			var nameExists *RoleNameExistsError
			if !errors.As(err, &nameExists) {
				t.Fatalf("CreateRole = %v, want *RoleNameExistsError", err)
			}
			if v := nameIndexValue(t, inner, req.TenantID, req.Name); string(v) != "winner-id" {
				t.Fatalf("name index = %q, want the winner's", v)
			}
		})
	}
}

// A name held by an existing role is refused: for a built-in and a custom
// role.
func TestCreateRole_NameHeldByExistingRoleRefused(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second func(t *testing.T) *CreateRoleRequest
	}{
		{
			"built-in",
			func(t *testing.T) *CreateRoleRequest { return builtInAuditorRequest(t, "acme") },
			func(t *testing.T) *CreateRoleRequest { return builtInAuditorRequest(t, "acme") },
		},
		{
			"custom",
			func(*testing.T) *CreateRoleRequest { return customViewerRequest() },
			func(*testing.T) *CreateRoleRequest { return customViewerRequest() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := NewStore(newMemKVStore())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			ctx := context.Background()
			first, err := svc.CreateRole(ctx, tc.first(t))
			if err != nil {
				t.Fatalf("first CreateRole: %v", err)
			}

			_, err = svc.CreateRole(ctx, tc.second(t))
			var nameExists *RoleNameExistsError
			if !errors.As(err, &nameExists) {
				t.Fatalf("second CreateRole = %v, want *RoleNameExistsError", err)
			}
			assertRoleFullyIndexed(t, svc, first)
		})
	}
}

// A name entry left by a create that is not this one is never overwritten,
// even when no role record backs it: only its own claim is ever removed.
func TestCreateRole_ForeignNameEntryIsNotOverwritten(t *testing.T) {
	inner := newMemKVStore()
	if err := inner.Put(context.Background(), nameIndexKey("acme", "viewer"), []byte("other-id")); err != nil {
		t.Fatalf("seed name index: %v", err)
	}
	svc, err := NewStore(inner)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = svc.CreateRole(context.Background(), customViewerRequest())
	var nameExists *RoleNameExistsError
	if !errors.As(err, &nameExists) {
		t.Fatalf("CreateRole = %v, want *RoleNameExistsError", err)
	}
	if v := nameIndexValue(t, inner, "acme", "viewer"); string(v) != "other-id" {
		t.Fatalf("name index = %q, want %q", v, "other-id")
	}
}

// A release that fails is reported as IndexCompensationError, a
// non-retryable ErrDataLoss, rather than hidden behind the create's cause.
func TestCreateRole_FailedNameReleaseIsReported(t *testing.T) {
	lost := errors.New("quicraft: timeout")
	fkv := &faultKVStore{
		inner:        newMemKVStore(),
		claimLostErr: lost,
		delFailAt:    1, // the guarded release is the rollback's first delete
		delErr:       errors.New("delete error"),
	}
	svc, err := NewStore(fkv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = svc.CreateRole(context.Background(), customViewerRequest())
	var ice *IndexCompensationError
	if !errors.As(err, &ice) {
		t.Fatalf("CreateRole = %v, want *IndexCompensationError", err)
	}
	if ice.Op != "create" || ice.TenantID != "acme" || len(ice.Failures) != 1 {
		t.Fatalf("IndexCompensationError = %+v, want op create, tenant acme, one failure", ice)
	}
	if !errors.Is(err, lost) {
		t.Fatalf("IndexCompensationError does not unwrap to the create's cause: %v", err)
	}
	if !strings.Contains(err.Error(), "name claim rollback was incomplete") {
		t.Fatalf("Error() = %q, want it to name the incomplete rollback", err.Error())
	}
	var qe *dberrors.QRDBError
	if !errors.As(err, &qe) || qe.Code != dberrors.ErrDataLoss {
		t.Fatalf("errors.As QRDBError = %+v, want ErrDataLoss", qe)
	}
}

// IndexCompensationError matches no target but a QRDBError through As.
func TestIndexCompensationError_AsOtherTarget(t *testing.T) {
	e := &IndexCompensationError{Op: "create", ID: "x", TenantID: "acme", Cause: errors.New("c")}
	var se *StoreError
	if e.As(&se) {
		t.Fatal("As(*StoreError) = true, want false")
	}
}

// nonAtomicKVStore hides memKVStore's atomic verbs: a plain KVStore.
type nonAtomicKVStore struct{ kvstore.KVStore }

// A store that cannot claim atomically fails closed and writes nothing.
func TestCreateRole_NonAtomicStoreFailsClosed(t *testing.T) {
	inner := newMemKVStore()
	svc, err := NewStore(nonAtomicKVStore{KVStore: inner})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = svc.CreateRole(context.Background(), customViewerRequest())
	if !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("CreateRole = %v, want ErrStoreNotAtomic", err)
	}
	if v := nameIndexValue(t, inner, "acme", "viewer"); v != nil {
		t.Fatalf("name index = %q, want nothing written", v)
	}
}

// releaseClaims removes only keys that still hold this create's id, and has
// nothing to do for no keys.
func TestReleaseClaims(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	s := &kvRoleStore{store: kv}
	mine, theirs := nameIndexKey("acme", "mine"), nameIndexKey("acme", "theirs")
	if err := kv.Put(ctx, mine, []byte("me")); err != nil {
		t.Fatal(err)
	}
	if err := kv.Put(ctx, theirs, []byte("them")); err != nil {
		t.Fatal(err)
	}

	if failures := s.releaseClaims(ctx, nil, []byte("me")); failures != nil {
		t.Fatalf("releaseClaims(no keys) = %v, want nil", failures)
	}
	if failures := s.releaseClaims(ctx, []string{mine, theirs}, []byte("me")); len(failures) != 0 {
		t.Fatalf("releaseClaims = %v, want no failures", failures)
	}
	if v, _ := kv.Get(ctx, mine); v != nil {
		t.Fatalf("own claim = %q, want released", v)
	}
	if v, _ := kv.Get(ctx, theirs); string(v) != "them" {
		t.Fatalf("other claim = %q, want kept", v)
	}
}

// releaseClaims over a store without atomic verbs reports that it could not
// release rather than dropping the claim silently.
func TestReleaseClaims_NonAtomicStore(t *testing.T) {
	s := &kvRoleStore{store: nonAtomicKVStore{KVStore: newMemKVStore()}}
	failures := s.releaseClaims(context.Background(), []string{"k"}, []byte("me"))
	if len(failures) != 1 || !errors.Is(failures[0], ErrStoreNotAtomic) {
		t.Fatalf("releaseClaims = %v, want [ErrStoreNotAtomic]", failures)
	}
}

// claimName records the key when it claimed it and when the answer was
// lost, but not when the name was taken or the store cannot claim.
func TestClaimName_RecordsOnlyClaimsThatMayBeOurs(t *testing.T) {
	ctx := context.Background()
	inner := newMemKVStore()
	key := nameIndexKey("acme", "viewer")

	var claimed []string
	s := &kvRoleStore{store: inner}
	if taken, err := s.claimName(ctx, key, []byte("a"), &claimed); taken || err != nil || len(claimed) != 1 {
		t.Fatalf("first claim: taken=%v err=%v claimed=%v", taken, err, claimed)
	}
	if taken, err := s.claimName(ctx, key, []byte("b"), &claimed); !taken || err != nil || len(claimed) != 1 {
		t.Fatalf("taken claim: taken=%v err=%v claimed=%v", taken, err, claimed)
	}

	lost := errors.New("lost")
	s = &kvRoleStore{store: &faultKVStore{inner: newMemKVStore(), claimLostErr: lost}}
	claimed = nil
	if _, err := s.claimName(ctx, key, []byte("a"), &claimed); !errors.Is(err, lost) || len(claimed) != 1 {
		t.Fatalf("lost claim: err=%v claimed=%v", err, claimed)
	}

	s = &kvRoleStore{store: nonAtomicKVStore{KVStore: newMemKVStore()}}
	claimed = nil
	if _, err := s.claimName(ctx, key, []byte("a"), &claimed); !errors.Is(err, ErrStoreNotAtomic) || len(claimed) != 0 {
		t.Fatalf("non-atomic claim: err=%v claimed=%v", err, claimed)
	}
}
