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

// Bulk unmarshal-error path coverage for SDK transport methods. Each test
// installs a responder that returns a syntactically invalid payload, then
// invokes the matching SDK method and asserts a non-nil error. This drives
// the proto.Unmarshal / json.Unmarshal failure branches of the response
// decoder, lifting per-method coverage from ~75% to ~85+%.

package quic

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// MVCC unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_PutWithLease_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.PutWithLease", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 0); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetRevision_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.GetRevision", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.GetRevision(context.Background(), "k"); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetAtRevision_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.GetAtRevision", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.GetAtRevision(context.Background(), "k", 1); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_Range_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.Range", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CurrentRevision_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.CurrentRevision", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CurrentRevision(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ExecuteTransaction_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tx.Transaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ExecuteBatch_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("batch.Batch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.ExecuteBatch(context.Background(), nil); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Lease unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_LeaseGrant_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.Grant", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LeaseGrant(context.Background(), 60); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_LeaseKeepAlive_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.KeepAlive", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LeaseKeepAlive(context.Background(), 1); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_LeaseTimeToLive_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.TimeToLive", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LeaseTimeToLive(context.Background(), 1); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_LeaseList_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LeaseList(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Lock unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_LockAcquire_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lock.Acquire", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{Key: "k"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_LockTryLock_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lock.TryLock", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{Key: "k"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_LockExtend_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lock.Extend", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{Key: "k"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_LockStatus_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lock.Status", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LockStatus(context.Background(), "k"); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_LockList_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lock.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.LockList(context.Background(), ""); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Index unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_IndexQuery_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("index.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.IndexQuery(context.Background(), "idx", "v"); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_IndexScan_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("index.Scan", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.IndexScan(context.Background(), "idx", spIdx("a"), spIdx("z")); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_IndexList_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("index.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.IndexList(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Barrier unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_BarrierStatus_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.Status", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BarrierStatus(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_BarrierInitializeShamir_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.ShamirInitialize", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_BarrierUnsealWithShare_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.ShamirUnsealShare", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_BarrierRekey_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.Rekey", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_BarrierGenerateRecoveryKeys_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RecoveryGenerate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_BarrierHasRecoveryKeys_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RecoveryStatus", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BarrierHasRecoveryKeys(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_BarrierGenerateRootToken_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("barrier.RootToken", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Ceremony unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_CeremonyInitialize_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.Initialize", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CeremonyStatus_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.Status", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CeremonyStatus(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CeremonyClaimCertBegin_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.ClaimCertBegin", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CeremonyClaimCertBegin(context.Background(), &transport.ClaimCertBeginRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CeremonyClaimCertComplete_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.ClaimCertComplete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CeremonyClaimCertComplete(context.Background(), &transport.ClaimCertCompleteRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CeremonyClaimShare_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.BeginClaimShare", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	srv.SetResponder("ceremony.CompleteClaimShare", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CeremonyBeginClaimShare(context.Background(), &transport.BeginClaimShareRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
	if _, err := tr.CeremonyCompleteClaimShare(context.Background(), &transport.CompleteClaimShareRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CeremonySignCSR_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.SignCSR", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CeremonyEnrollExternalOfficer_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ceremony.EnrollExternalOfficer", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CeremonyEnrollExternalOfficer(context.Background(), &transport.EnrollExternalOfficerRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Credential unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_CredentialStrategy_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Strategy", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CredentialStrategy(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CredentialList_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CredentialRotate_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Rotate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "k", NewValue: []byte("v")}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CredentialRetrieveVersion_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.RetrieveVersion", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CredentialRetrieveVersion(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "k", Version: 1}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CredentialListVersions_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.ListVersions", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "k"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CredentialGCExpired_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.GCExpired", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "k"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Tenant unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_CreateTenant_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "t1"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetTenant_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.GetTenant(context.Background(), "t1"); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListTenants_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.ListTenants(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateTenant_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.Update", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "t1"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateTenantQuotas_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.UpdateQuotas", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{TenantID: "t1"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Vector unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_CreateNamespace_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.CreateNamespace", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListNamespaces_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.ListNamespaces", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.ListNamespaces(context.Background(), &transport.ListNamespacesRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetNamespaceInfo_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.GetNamespaceInfo", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpsertVector_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.Upsert", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_BatchUpsert_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.BatchUpsert", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BatchUpsert(context.Background(), &transport.BatchUpsertRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_BatchDeleteVectors_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.BatchDelete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListVectors_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.ListVectors(context.Background(), &transport.ListVectorsRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_SearchByMetadata_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.SearchByMetadata", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_HybridSearch_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.HybridSearch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_SearchByID_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.SearchByID", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_RebuildIndex_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.RebuildIndex", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.RebuildIndex(context.Background(), &transport.RebuildIndexRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetRebuildProgress_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.GetRebuildProgress", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.GetRebuildProgress(context.Background(), &transport.GetRebuildProgressRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetIndexStats_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.GetIndexStats", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.GetIndexStats(context.Background(), &transport.GetIndexStatsRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CompactNamespace_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.CompactNamespace", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CompactNamespace(context.Background(), &transport.CompactNamespaceRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetCompactionProgress_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.GetCompactionProgress", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.GetCompactionProgress(context.Background(), &transport.GetCompactionProgressRequest{Namespace: "ns"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// TS unmarshal errors (proto-based)
// ---------------------------------------------------------------------------

func TestE2E_TSQuery_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{Metric: "m"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_TSAggregate_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.Aggregate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{Metric: "m"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_TSListMetrics_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.ListMetrics", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Collection unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_CreateCollection_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListCollections_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.ListCollections(context.Background(), "db"); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_InsertDocument_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.InsertDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListDocuments_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.ListDocuments", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	if _, err := tr.ListDocuments(context.Background(), transport.ListDocumentsRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// User/Role/Cert unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_CreateUser_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetUserByUsername_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.GetByUsername", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.GetUserByUsername(context.Background(), "t1", "alice"); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateUser_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Update", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{ID: 1}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListUsers_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_CreateRole_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{Name: "admin"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateRole_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Update", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{ID: "r1"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListRoles_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ExportCert_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Export", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.ExportCert(context.Background(), &transport.ExportCertRequest{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// AuthFactor unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_GetAuthFactor_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.GetAuthFactor(context.Background(), &transport.AuthFactorGetRequest{FactorID: "f1"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_RemoveAuthFactor_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Remove", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.RemoveAuthFactor(context.Background(), &transport.AuthFactorRemoveRequest{FactorID: "f1"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_RotateAuthFactor_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Rotate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{OldFactorID: "f1"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_RelabelAuthFactor_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Relabel", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.RelabelAuthFactor(context.Background(), &transport.AuthFactorRelabelRequest{FactorID: "f1"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_LoginAuthFactor_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Login", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.LoginAuthFactor(context.Background(), &transport.AuthFactorLoginRequest{Type: "mtls"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Database unmarshal errors
// ---------------------------------------------------------------------------

func TestE2E_CreateDatabase_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{Name: "db"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateDatabaseProtocol_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateProtocol", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{Name: "db"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateDatabaseEtcd_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateEtcd", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{Name: "db"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateDatabaseRaft_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateRaft", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{Name: "db"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateDatabaseBarrier_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateBarrier", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{Name: "db"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_UpdateDatabaseConnection_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateConnection", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	if _, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{Name: "db"}); err == nil {
		t.Fatal("expected unmarshal error")
	}
}
