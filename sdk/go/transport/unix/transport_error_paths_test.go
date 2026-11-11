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

// Error-path round-trip tests: each test wires a real gRPC-over-UDS server
// whose mock service returns an error, and asserts the transport correctly
// propagates it as a RequestError. This covers the err != nil branches
// (83.3% → 100%) in most transport method implementations.

package unix

import (
	"context"
	"errors"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var errServiceFailed = errors.New("service error injected by test")

// ===========================================================================
// User service error paths
// ===========================================================================

func TestUnixRT_CreateUser_Error(t *testing.T) {
	usvc := &mockUserSvc{createFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	_, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "bob"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_GetUser_Error(t *testing.T) {
	usvc := &mockUserSvc{getFn: func(_ context.Context, _ uint64) (*user.User, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	_, err := tr.GetUser(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_GetUserByUsername_Error(t *testing.T) {
	usvc := &mockUserSvc{getByUserNameFn: func(_ context.Context, _, _ string) (*user.User, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	_, err := tr.GetUserByUsername(context.Background(), "t1", "ghost")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_UpdateUser_Error(t *testing.T) {
	usvc := &mockUserSvc{updateFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	_, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{ID: 1})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_DeleteUser_Error(t *testing.T) {
	usvc := &mockUserSvc{deleteFn: func(_ context.Context, _ uint64) error {
		return errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	err := tr.DeleteUser(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_EnableUser_Error(t *testing.T) {
	usvc := &mockUserSvc{enableFn: func(_ context.Context, _ uint64) error {
		return errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	if err := tr.EnableUser(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_DisableUser_Error(t *testing.T) {
	usvc := &mockUserSvc{disableFn: func(_ context.Context, _ uint64) error {
		return errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	if err := tr.DisableUser(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_SuspendUnsuspendUser_Error(t *testing.T) {
	fail := func(_ context.Context, _ uint64) error { return errServiceFailed }
	usvc := &mockUserSvc{suspendFn: fail, unsuspendFn: fail}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	if err := tr.SuspendUser(context.Background(), 1); err == nil {
		t.Fatal("SuspendUser: expected error")
	}
	if err := tr.UnsuspendUser(context.Background(), 1); err == nil {
		t.Fatal("UnsuspendUser: expected error")
	}
}

// ===========================================================================
// Role service error paths
// ===========================================================================

func TestUnixRT_CreateRole_Error(t *testing.T) {
	rsvc := &mockRoleSvc{createFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	_, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{Name: "admin"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_GetRole_Error(t *testing.T) {
	rsvc := &mockRoleSvc{getFn: func(_ context.Context, _ string) (*role.Role, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	_, err := tr.GetRole(context.Background(), "r1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_UpdateRole_Error(t *testing.T) {
	rsvc := &mockRoleSvc{updateFn: func(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	_, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{ID: "r1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_DeleteRole_Error(t *testing.T) {
	rsvc := &mockRoleSvc{deleteFn: func(_ context.Context, _ string) error {
		return errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	if err := tr.DeleteRole(context.Background(), "r1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_AssignRole_Error(t *testing.T) {
	// AssignRole calls UpdateUser internally on the gRPC side; a failing user service
	// is enough to trigger the error path on the client side.
	usvc := &mockUserSvc{updateFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	if err := tr.AssignRole(context.Background(), 1, "r1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_UnassignRole_Error(t *testing.T) {
	usvc := &mockUserSvc{updateFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	if err := tr.UnassignRole(context.Background(), 1, "r1"); err == nil {
		t.Fatal("expected error")
	}
}

// Ensure the permission import is used.
var _ permission.Permission = permission.Permission("kv.read")

// ===========================================================================
// Cert service error paths
// ===========================================================================

func TestUnixRT_ListCerts_Error(t *testing.T) {
	csvc := &mockCertProvider{listFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithCertProvider(csvc))
	defer cleanup()

	_, err := tr.ListCerts(context.Background(), "t1")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// KV service error paths (Put/Delete/Health/Status error path)
// ===========================================================================

func TestUnixRT_Put_Error(t *testing.T) {
	kvsvc := &mockKVService{putErr: errServiceFailed}
	tr, cleanup := startServer(t, kvsvc)
	defer cleanup()

	if err := tr.Put(context.Background(), "k", []byte("v")); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_Delete_Error(t *testing.T) {
	kvsvc := &mockKVService{delErr: errServiceFailed}
	tr, cleanup := startServer(t, kvsvc)
	defer cleanup()

	if err := tr.Delete(context.Background(), "k"); err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// Lease service error paths
// ===========================================================================

func TestUnixRT_LeaseGrant_Error(t *testing.T) {
	svc := &mockLeaseSvc{grantFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	_, err := tr.LeaseGrant(context.Background(), 60)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_LeaseRevoke_Error(t *testing.T) {
	svc := &mockLeaseSvc{revokeFn: func(_ context.Context, _ int64) error {
		return errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	if err := tr.LeaseRevoke(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_LeaseKeepAlive_Error(t *testing.T) {
	svc := &mockLeaseSvc{keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	_, err := tr.LeaseKeepAlive(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_LeaseTimeToLive_Error(t *testing.T) {
	svc := &mockLeaseSvc{ttlFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	_, err := tr.LeaseTimeToLive(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Lease not-found/expired typed-error round trips (go-qrdb#169, completing
// #141 for the lease family). The unix transport is gRPC over a domain
// socket talking to the identical pkg/api/grpc server the grpc package
// dials over TCP, so these mirror sdk/go/transport/grpc's equivalents; see
// mapLeaseAbsent's doc comment in transport_lease.go for the full rationale.
// ---------------------------------------------------------------------------

func TestUnixRT_LeaseRevoke_NotFound(t *testing.T) {
	svc := &mockLeaseSvc{revokeFn: func(_ context.Context, _ int64) error {
		return &service.LeaseNotFoundError{LeaseID: 42}
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	err := tr.LeaseRevoke(context.Background(), 42)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 42 {
		t.Errorf("LeaseID: got %d, want 42", notFound.LeaseID)
	}
}

func TestUnixRT_LeaseRevoke_Expired(t *testing.T) {
	svc := &mockLeaseSvc{revokeFn: func(_ context.Context, _ int64) error {
		return &service.LeaseExpiredError{LeaseID: 42}
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	err := tr.LeaseRevoke(context.Background(), 42)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 42 {
		t.Errorf("LeaseID: got %d, want 42", expired.LeaseID)
	}
}

func TestUnixRT_LeaseKeepAlive_NotFound(t *testing.T) {
	svc := &mockLeaseSvc{keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
		return nil, &service.LeaseNotFoundError{LeaseID: 7}
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	_, err := tr.LeaseKeepAlive(context.Background(), 7)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 7 {
		t.Errorf("LeaseID: got %d, want 7", notFound.LeaseID)
	}
}

func TestUnixRT_LeaseKeepAlive_Expired(t *testing.T) {
	svc := &mockLeaseSvc{keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
		return nil, &service.LeaseExpiredError{LeaseID: 7}
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	_, err := tr.LeaseKeepAlive(context.Background(), 7)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 7 {
		t.Errorf("LeaseID: got %d, want 7", expired.LeaseID)
	}
}

func TestUnixRT_LeaseTimeToLive_NotFound(t *testing.T) {
	svc := &mockLeaseSvc{ttlFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
		return nil, &service.LeaseNotFoundError{LeaseID: 99}
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 99 {
		t.Errorf("LeaseID: got %d, want 99", notFound.LeaseID)
	}
}

func TestUnixRT_LeaseTimeToLive_Expired(t *testing.T) {
	svc := &mockLeaseSvc{ttlFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
		return nil, &service.LeaseExpiredError{LeaseID: 99}
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 99 {
		t.Errorf("LeaseID: got %d, want 99", expired.LeaseID)
	}
}

// TestUnixRT_LeaseTimeToLive_NonNotFoundNotReclassified is the mutation-check
// companion (see the grpc package's equivalent for the full rationale):
// mapLeaseAbsent's codes.NotFound gate must actually gate, or a non-NotFound
// condition from the same call would be misreported as a lease error.
func TestUnixRT_LeaseTimeToLive_NonNotFoundNotReclassified(t *testing.T) {
	svc := &mockLeaseSvc{ttlFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
		return nil, &service.NotLeaderError{ShardID: 1}
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var notFound *transport.LeaseNotFoundError
	var expired *transport.LeaseExpiredError
	if errors.As(err, &notFound) || errors.As(err, &expired) {
		t.Fatalf("expected a non-lease error, got %T: %v", err, err)
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

// ===========================================================================
// Lock service error paths
// ===========================================================================

func TestUnixRT_LockAcquire_Error(t *testing.T) {
	svc := &mockLockSvc{lockFn: func(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	_, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_LockRelease_Error(t *testing.T) {
	svc := &mockLockSvc{unlockFn: func(_ context.Context, _ string, _ int64) error {
		return errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	if err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{Key: "k"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_LockExtend_Error(t *testing.T) {
	svc := &mockLockSvc{extendFn: func(_ context.Context, _ string, _ int64) (*lock.Lock, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	_, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_LockStatus_Error(t *testing.T) {
	svc := &mockLockSvc{statusFn: func(_ context.Context, _ string) (*lock.Lock, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	_, err := tr.LockStatus(context.Background(), "k")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// MVCC service error paths
// ===========================================================================

func TestUnixRT_PutWithLease_Error(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.putWithLeaseFn = func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
		return nil, errServiceFailed
	}
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	_, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_GetAtRevision_Error(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.getAtRevFn = func(_ context.Context, _ string, _ int64) (*kv.KeyValue, error) {
		return nil, errServiceFailed
	}
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	_, err := tr.GetAtRevision(context.Background(), "k", 5)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_CurrentRevision_Error(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.currentRevFn = func(_ context.Context) (int64, error) {
		return 0, errServiceFailed
	}
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	_, err := tr.CurrentRevision(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// TimeSeries error paths (using mockTimeSeriesSvc from transport_misc_roundtrip_test.go)
// ===========================================================================

func tsErrSvc() *mockTimeSeriesSvc {
	return &mockTimeSeriesSvc{
		writeFn: func(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
			return errServiceFailed
		},
		writeBatchFn: func(_ context.Context, _ []timeseries.DataPoint) error {
			return errServiceFailed
		},
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return nil, errServiceFailed
		},
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			return nil, errServiceFailed
		},
		writePointFn: func(_ context.Context, _ *timeseries.DataPoint) error {
			return errServiceFailed
		},
		queryRichFn: func(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			return nil, errServiceFailed
		},
		deleteFn: func(_ context.Context, _ *timeseries.DeleteRequest) error {
			return errServiceFailed
		},
		getRetFn: func(_ string) (*timeseries.RetentionPolicy, error) {
			return nil, errServiceFailed
		},
		setRetFn: func(_ string, _ *timeseries.RetentionPolicy) error {
			return errServiceFailed
		},
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			return nil, errServiceFailed
		},
	}
}

func TestUnixRT_TSWrite_Error(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(tsErrSvc()))
	defer cleanup()

	if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{Metric: "cpu"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_TSWriteBatch_Error(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(tsErrSvc()))
	defer cleanup()

	if err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{{Metric: "m"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_TSQuery_Error(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(tsErrSvc()))
	defer cleanup()

	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_TSDeleteSeries_Error(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(tsErrSvc()))
	defer cleanup()

	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_TSGetRetention_Error(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(tsErrSvc()))
	defer cleanup()

	_, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_TSListMetrics_Error(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(tsErrSvc()))
	defer cleanup()

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// Credential error paths (CredentialStore / CredentialRetrieve)
// ===========================================================================

func TestUnixRT_CredentialStore_Error(t *testing.T) {
	svc := &mockCredentialSvc{storeFn: func(_ context.Context, _ string, _ []byte) error {
		return credentials.ErrCredentialNotFound
	}}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	if err := tr.CredentialStore(context.Background(), "k", []byte("v")); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnixRT_CredentialRetrieve_Error(t *testing.T) {
	svc := &mockCredentialSvc{retrieveFn: func(_ context.Context, _ string) ([]byte, error) {
		return nil, credentials.ErrCredentialNotFound
	}}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	_, err := tr.retrieveBytes(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// Tenant error paths (no tenant service registered → Unimplemented from server)
// ===========================================================================

func TestUnixRT_CreateTenant_NoService(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "t1", Name: "Test"})
	if err == nil {
		t.Fatal("expected error when tenant service not configured")
	}
}

func TestUnixRT_GetTenant_NoService(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()
	_, err := tr.GetTenant(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error when tenant service not configured")
	}
}

// ===========================================================================
// Barrier error paths
// ===========================================================================

func TestUnixRT_BarrierInitializeShamir_Error(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	_, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{})
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// Role resolve error paths
// ===========================================================================

func TestUnixRT_GetRoleByName_Error(t *testing.T) {
	rsvc := &mockRoleSvc{getByNameFn: func(_ context.Context, _, _ string) (*role.Role, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRoleService(rsvc))
	defer cleanup()

	_, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{TenantID: "t1", Name: "admin"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// Scan + Put + Delete with write/read options (protoConsistency/protoWriteMode)
// ===========================================================================

func TestUnixRT_Put_WithWriteMode(t *testing.T) {
	kvsvc := newMockKVService()
	tr, cleanup := startServer(t, kvsvc)
	defer cleanup()

	// Put with all three write modes exercises the protoWriteMode switch.
	for _, mode := range []transport.WriteMode{
		transport.WriteModeSync,
		transport.WriteModeWALDurable,
		transport.WriteModeBatched,
	} {
		if err := tr.Put(context.Background(), "k", []byte("v"), transport.WithWriteMode(mode)); err != nil {
			t.Fatalf("Put with mode %v: %v", mode, err)
		}
	}
}

func TestUnixRT_Get_WithConsistency(t *testing.T) {
	kvsvc := newMockKVService()
	kvsvc.store["k"] = []byte("v")
	tr, cleanup := startServer(t, kvsvc)
	defer cleanup()

	// Get with all consistency levels exercises the protoConsistency switch.
	for _, level := range []transport.ConsistencyLevel{
		transport.ConsistencyStrong,
		transport.ConsistencyLinearizable,
		transport.ConsistencyEventual,
	} {
		_, err := tr.Get(context.Background(), "k", transport.WithConsistency(level))
		if err != nil {
			t.Fatalf("Get with consistency %v: %v", level, err)
		}
	}
}

// startServer is a minimal helper that starts a server with only the KV
// service (no auth). It reuses the existing newMockAdminService from
// transport_test.go. Only used in this file.
func startServer(t *testing.T, kvsvc service.KVServicer) (*Transport, func()) {
	t.Helper()
	socketPath := tempSocketPath(t)
	srv := unixserver.NewServer(kvsvc, newMockAdminService(), socketPath, unixserver.WithTLS(testUnixServerTLS))
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	tr, err := New(transport.WithAddress(socketPath), testClientTLSOption())
	if err != nil {
		srv.Stop()
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		srv.Stop()
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() {
		_ = tr.Close()
		srv.Stop()
	}
}

// Compile-time interface checks ensure mock types remain consistent.
var (
	_ consistency.ReadOptions = consistency.ReadOptions{}
	_ writemode.Mode          = writemode.Mode(0)
)
