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

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func newClosedTransport() *Transport {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	return tr
}

func assertClosed(t *testing.T, err error) {
	t.Helper()
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// transport_credentials.go
// ===========================================================================

func TestQUICCredentials_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{}); !errors.As(err, &ce) {
		t.Errorf("CredentialSubmit: %v", err)
	}
	if _, err := tr.CredentialStrategy(context.Background()); !errors.As(err, &ce) {
		t.Errorf("CredentialStrategy: %v", err)
	}
	if err := tr.CredentialDelete(context.Background(), "k"); !errors.As(err, &ce) {
		t.Errorf("CredentialDelete: %v", err)
	}
	if _, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{}); !errors.As(err, &ce) {
		t.Errorf("CredentialList: %v", err)
	}
	if _, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{}); !errors.As(err, &ce) {
		t.Errorf("CredentialRotate: %v", err)
	}
	if _, err := tr.CredentialRetrieveVersion(context.Background(), &transport.CredentialRetrieveVersionRequest{}); !errors.As(err, &ce) {
		t.Errorf("CredentialRetrieveVersion: %v", err)
	}
	if _, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{}); !errors.As(err, &ce) {
		t.Errorf("CredentialListVersions: %v", err)
	}
	if _, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{}); !errors.As(err, &ce) {
		t.Errorf("CredentialGCExpired: %v", err)
	}
}

// ===========================================================================
// transport_dek.go
// ===========================================================================

func TestQUICDEK_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.DEKInitialize(context.Background(), &transport.DEKInitializeRequest{}); !errors.As(err, &ce) {
		t.Errorf("DEKInitialize: %v", err)
	}
	if _, err := tr.DEKGetStatus(context.Background()); !errors.As(err, &ce) {
		t.Errorf("DEKGetStatus: %v", err)
	}
	if err := tr.DEKRotate(context.Background()); !errors.As(err, &ce) {
		t.Errorf("DEKRotate: %v", err)
	}
	if _, err := tr.DEKGetCurrent(context.Background()); !errors.As(err, &ce) {
		t.Errorf("DEKGetCurrent: %v", err)
	}
	if err := tr.DEKSyncToNode(context.Background(), &transport.DEKSyncRequest{}); !errors.As(err, &ce) {
		t.Errorf("DEKSyncToNode: %v", err)
	}
	if _, err := tr.TenantDEKGetOrCreate(context.Background(), &transport.TenantDEKRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKGetOrCreate: %v", err)
	}
	if err := tr.TenantDEKRotate(context.Background(), &transport.TenantDEKRotateRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKRotate: %v", err)
	}
	if err := tr.TenantDEKDelete(context.Background(), &transport.TenantDEKDeleteRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKDelete: %v", err)
	}
	if _, err := tr.TenantDEKList(context.Background()); !errors.As(err, &ce) {
		t.Errorf("TenantDEKList: %v", err)
	}
	if _, err := tr.TenantDEKGetStatus(context.Background(), &transport.TenantDEKRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKGetStatus: %v", err)
	}
}

// ===========================================================================
// transport_barrier.go
// ===========================================================================

func TestQUICBarrier_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierInitialize: %v", err)
	}
	if err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierUnseal: %v", err)
	}
	if err := tr.BarrierSeal(context.Background()); !errors.As(err, &ce) {
		t.Errorf("BarrierSeal: %v", err)
	}
	if _, err := tr.BarrierStatus(context.Background()); !errors.As(err, &ce) {
		t.Errorf("BarrierStatus: %v", err)
	}
}

// ===========================================================================
// transport_database.go
// ===========================================================================

func TestQUICDatabase_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateDatabase: %v", err)
	}
	if _, err := tr.GetDatabase(context.Background(), "n"); !errors.As(err, &ce) {
		t.Errorf("GetDatabase: %v", err)
	}
	if _, err := tr.ListDatabases(context.Background()); !errors.As(err, &ce) {
		t.Errorf("ListDatabases: %v", err)
	}
	if err := tr.DropDatabase(context.Background(), "n"); !errors.As(err, &ce) {
		t.Errorf("DropDatabase: %v", err)
	}
	if _, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseProtocol: %v", err)
	}
	if _, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseEtcd: %v", err)
	}
	if _, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseRaft: %v", err)
	}
	if _, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseBarrier: %v", err)
	}
	if _, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDatabaseConnection: %v", err)
	}
}

// ===========================================================================
// transport_index.go
// ===========================================================================

func TestQUICIndex_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.IndexCreate(context.Background(), "i", "f", transport.IndexConfig{}); !errors.As(err, &ce) {
		t.Errorf("IndexCreate: %v", err)
	}
	if err := tr.IndexDrop(context.Background(), "i"); !errors.As(err, &ce) {
		t.Errorf("IndexDrop: %v", err)
	}
	if _, err := tr.IndexQuery(context.Background(), "i", "v"); !errors.As(err, &ce) {
		t.Errorf("IndexQuery: %v", err)
	}
	if _, err := tr.IndexScan(context.Background(), "i", "a", "z"); !errors.As(err, &ce) {
		t.Errorf("IndexScan: %v", err)
	}
	if _, err := tr.IndexList(context.Background()); !errors.As(err, &ce) {
		t.Errorf("IndexList: %v", err)
	}
}

// ===========================================================================
// transport_lock.go
// ===========================================================================

func TestQUICLock_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{}); !errors.As(err, &ce) {
		t.Errorf("LockAcquire: %v", err)
	}
	if _, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{}); !errors.As(err, &ce) {
		t.Errorf("LockTryLock: %v", err)
	}
	if err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{}); !errors.As(err, &ce) {
		t.Errorf("LockRelease: %v", err)
	}
	if _, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{}); !errors.As(err, &ce) {
		t.Errorf("LockExtend: %v", err)
	}
	if _, err := tr.LockStatus(context.Background(), "k"); !errors.As(err, &ce) {
		t.Errorf("LockStatus: %v", err)
	}
	if _, err := tr.LockList(context.Background(), ""); !errors.As(err, &ce) {
		t.Errorf("LockList: %v", err)
	}
}

// ===========================================================================
// transport_mvcc.go
// ===========================================================================

func TestQUICMVCC_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 0); !errors.As(err, &ce) {
		t.Errorf("PutWithLease: %v", err)
	}
	if _, err := tr.GetRevision(context.Background(), "k"); !errors.As(err, &ce) {
		t.Errorf("GetRevision: %v", err)
	}
	if _, err := tr.GetAtRevision(context.Background(), "k", 1); !errors.As(err, &ce) {
		t.Errorf("GetAtRevision: %v", err)
	}
	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{}); !errors.As(err, &ce) {
		t.Errorf("Range: %v", err)
	}
	if err := tr.Compact(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("Compact: %v", err)
	}
	if _, err := tr.CurrentRevision(context.Background()); !errors.As(err, &ce) {
		t.Errorf("CurrentRevision: %v", err)
	}
}

// ===========================================================================
// transport_tenant.go
// ===========================================================================

func TestQUICTenant_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateTenant: %v", err)
	}
	if _, err := tr.GetTenant(context.Background(), "t"); !errors.As(err, &ce) {
		t.Errorf("GetTenant: %v", err)
	}
	if _, err := tr.ListTenants(context.Background()); !errors.As(err, &ce) {
		t.Errorf("ListTenants: %v", err)
	}
	if err := tr.DeleteTenant(context.Background(), "t"); !errors.As(err, &ce) {
		t.Errorf("DeleteTenant: %v", err)
	}
	if _, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateTenant: %v", err)
	}
	if _, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateTenantQuotas: %v", err)
	}
	if err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantBarrierInit: %v", err)
	}
	if err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantBarrierUnseal: %v", err)
	}
	if err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantBarrierSeal: %v", err)
	}
	if _, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantBarrierStatus: %v", err)
	}
}

// ===========================================================================
// transport_timeseries.go (only some methods have closed checks)
// ===========================================================================

func TestQUICTimeSeries_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{}); !errors.As(err, &ce) {
		t.Errorf("TSWrite: %v", err)
	}
	if err := tr.TSWriteBatch(context.Background(), nil); !errors.As(err, &ce) {
		t.Errorf("TSWriteBatch: %v", err)
	}
	if _, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSQuery: %v", err)
	}
	if _, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{}); !errors.As(err, &ce) {
		t.Errorf("TSAggregate: %v", err)
	}
}

// ===========================================================================
// transport_vector.go (closed-path coverage)
// ===========================================================================

func TestQUICVector_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateNamespace: %v", err)
	}
	if _, err := tr.DeleteNamespace(context.Background(), &transport.DeleteNamespaceRequest{}); !errors.As(err, &ce) {
		t.Errorf("DeleteNamespace: %v", err)
	}
	if _, err := tr.ListNamespaces(context.Background(), &transport.ListNamespacesRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListNamespaces: %v", err)
	}
}

// ===========================================================================
// transport_auth.go (closed-paths)
// ===========================================================================

func TestQUICAuth_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateUser: %v", err)
	}
	if _, err := tr.GetUser(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("GetUser: %v", err)
	}
	if _, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateUser: %v", err)
	}
	if err := tr.DeleteUser(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("DeleteUser: %v", err)
	}
	if _, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListUsers: %v", err)
	}
	if err := tr.EnableUser(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("EnableUser: %v", err)
	}
	if err := tr.DisableUser(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("DisableUser: %v", err)
	}
	if _, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateRole: %v", err)
	}
	if _, err := tr.GetRole(context.Background(), "r"); !errors.As(err, &ce) {
		t.Errorf("GetRole: %v", err)
	}
	if _, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateRole: %v", err)
	}
	if err := tr.DeleteRole(context.Background(), "r"); !errors.As(err, &ce) {
		t.Errorf("DeleteRole: %v", err)
	}
	if _, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListRoles: %v", err)
	}
	if _, err := tr.ListPermissions(context.Background()); !errors.As(err, &ce) {
		t.Errorf("ListPermissions: %v", err)
	}
}

// ===========================================================================
// transport_authfactor.go
// ===========================================================================

func TestQUICAuthFactor_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{}); !errors.As(err, &ce) {
		t.Errorf("EnrollFactor: %v", err)
	}
	if _, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{}); !errors.As(err, &ce) {
		t.Errorf("ChallengeFactor: %v", err)
	}
	if err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{}); !errors.As(err, &ce) {
		t.Errorf("VerifyFactor: %v", err)
	}
	if _, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListFactors: %v", err)
	}
	if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{}); !errors.As(err, &ce) {
		t.Errorf("RevokeFactor: %v", err)
	}
}

// ===========================================================================
// transport_ceremony.go
// ===========================================================================

func TestQUICCeremony_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyInitialize: %v", err)
	}
	if _, err := tr.CeremonyStatus(context.Background()); !errors.As(err, &ce) {
		t.Errorf("CeremonyStatus: %v", err)
	}
}

// ===========================================================================
// transport_batch.go - NotImplemented
// ===========================================================================

func TestQUICBatch_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ExecuteBatch(context.Background(), nil)
	assertClosed(t, err)
}

// ===========================================================================
// transport_watch.go
// ===========================================================================

func TestQUICWatch_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.Watch(context.Background(), "k", transport.WatchOptions{})
	assertClosed(t, err)
}

// ===========================================================================
// transport_transaction.go
// ===========================================================================

func TestQUICTransaction_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.ExecuteTransaction(context.Background(), nil)
	assertClosed(t, err)
}

// ===========================================================================
// transport_lease.go
// ===========================================================================

func TestQUICLease_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.LeaseGrant(context.Background(), 60); !errors.As(err, &ce) {
		t.Errorf("LeaseGrant: %v", err)
	}
	if err := tr.LeaseRevoke(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("LeaseRevoke: %v", err)
	}
	if _, err := tr.LeaseKeepAlive(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("LeaseKeepAlive: %v", err)
	}
	if _, err := tr.LeaseTimeToLive(context.Background(), 1); !errors.As(err, &ce) {
		t.Errorf("LeaseTimeToLive: %v", err)
	}
	if _, err := tr.LeaseList(context.Background()); !errors.As(err, &ce) {
		t.Errorf("LeaseList: %v", err)
	}
}
