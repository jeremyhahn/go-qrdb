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
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestBuildQuotaAccountant_NilTenantService verifies the nil-guard: no tenant
// service means no storage-byte enforcement.
func TestBuildQuotaAccountant_NilTenantService(t *testing.T) {
	if acct := buildQuotaAccountant(nil); acct != nil {
		t.Errorf("expected nil accountant for nil tenant service, got %v", acct)
	}
}

// TestBuildQuotaAccountant_NotQuotaProvider verifies that a TenantServicer that
// does not satisfy quota.QuotaProvider (no Quotas(ctx,id) method) yields a nil
// accountant — there is no quota lookup to enforce against.
func TestBuildQuotaAccountant_NotQuotaProvider(t *testing.T) {
	if acct := buildQuotaAccountant(&mockTenantServicer{}); acct != nil {
		t.Errorf("expected nil accountant for non-QuotaProvider tenant service, got %v", acct)
	}
}

// TestBuildQuotaAccountant_RealService verifies the success path: a concrete
// TenantService is a QuotaProvider and yields a non-nil accountant.
func TestBuildQuotaAccountant_RealService(t *testing.T) {
	svc, _ := newReattachFixture(t)
	if acct := buildQuotaAccountant(svc); acct == nil {
		t.Error("expected non-nil accountant for a QuotaProvider tenant service")
	}
}

// TestBuildQuotaReconciler_NotSetter verifies that a TenantServicer lacking
// SetQuotaUsage disables the reconciler (returns nil).
func TestBuildQuotaReconciler_NotSetter(t *testing.T) {
	svc, _ := newReattachFixture(t)
	acct := buildQuotaAccountant(svc)
	if acct == nil {
		t.Fatal("precondition: accountant must be non-nil")
	}
	if rec := buildQuotaReconciler(acct, &mockTenantServicer{}, qrdbconfig.TenantConfig{}, nil); rec != nil {
		t.Errorf("expected nil reconciler for non-setter tenant service, got %v", rec)
	}
}

// TestBuildQuotaReconciler_RealService verifies the success path: a concrete
// TenantService supports SetQuotaUsage and yields a non-nil reconciler.
func TestBuildQuotaReconciler_RealService(t *testing.T) {
	svc, _ := newReattachFixture(t)
	acct := buildQuotaAccountant(svc)
	if acct == nil {
		t.Fatal("precondition: accountant must be non-nil")
	}
	rec := buildQuotaReconciler(acct, svc, qrdbconfig.TenantConfig{}, nil)
	if rec == nil {
		t.Fatal("expected non-nil reconciler for a SetQuotaUsage-capable tenant service")
	}
}

// TestBuildQuotaReconciler_NilAccountant_Error verifies that passing a nil
// accountant to buildQuotaReconciler triggers the quota.NewReconciler error
// path (start.go:1742-1745): slog.Error is emitted and nil is returned.
// quotaErrTenantSvc satisfies quotaUsageSetter so the function gets past
// the early-return and reaches quota.NewReconciler with a nil acct.
func TestBuildQuotaReconciler_NilAccountant_Error(t *testing.T) {
	svc := &quotaErrTenantSvc{mockTenantServicer: &mockTenantServicer{}}
	rec := buildQuotaReconciler(nil, svc, qrdbconfig.TenantConfig{}, nil)
	if rec != nil {
		t.Errorf("expected nil reconciler when accountant is nil, got %v", rec)
	}
}

// TestBuildQuotaReconciler_InvalidInterval verifies the invalid-interval
// fallback branch: an unparseable quota_reconcile_interval falls back to the
// default and still produces a non-nil reconciler.
func TestBuildQuotaReconciler_InvalidInterval(t *testing.T) {
	svc, _ := newReattachFixture(t)
	acct := buildQuotaAccountant(svc)
	if acct == nil {
		t.Fatal("precondition: accountant must be non-nil")
	}
	cfg := qrdbconfig.TenantConfig{QuotaReconcileInterval: "not-a-duration"}
	rec := buildQuotaReconciler(acct, svc, cfg, nil)
	if rec == nil {
		t.Fatal("expected non-nil reconciler despite invalid interval (default fallback)")
	}
}

// ---------------------------------------------------------------------------
// VolumeReattachError / RaftReattachError string + unwrap
// ---------------------------------------------------------------------------

func TestVolumeReattachError_ErrorAndUnwrap(t *testing.T) {
	inner := &mockError{msg: "boom"}
	e := &VolumeReattachError{TenantID: "acme", Err: inner}
	if got := e.Error(); got != "server: re-attach volume tenant acme: boom" {
		t.Errorf("unexpected message: %q", got)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap did not return the wrapped error")
	}
}

func TestRaftReattachError_WithDatabase(t *testing.T) {
	inner := &mockError{msg: "boom"}
	e := &RaftReattachError{TenantID: "acme", Database: "app", Err: inner}
	if got := e.Error(); got != "server: re-attach raft tenant acme/app: boom" {
		t.Errorf("unexpected message: %q", got)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap did not return the wrapped error")
	}
}

func TestRaftReattachError_WithoutDatabase(t *testing.T) {
	inner := &mockError{msg: "boom"}
	e := &RaftReattachError{TenantID: "acme", Err: inner}
	if got := e.Error(); got != "server: re-attach raft tenant acme: boom" {
		t.Errorf("unexpected message: %q", got)
	}
}

// mockError is a trivial error used to assert wrapping behaviour.
type mockError struct{ msg string }

func (m *mockError) Error() string { return m.msg }
