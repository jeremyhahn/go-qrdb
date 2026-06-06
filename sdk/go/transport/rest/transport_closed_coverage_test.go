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

package rest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// MVCC: nil/closed paths
// ===========================================================================

func TestRESTMVCC_AllMethods_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	ctx := context.Background()
	var ce *transport.ClosedError

	cases := []struct {
		name string
		call func() error
	}{
		{"PutWithLease", func() error { _, e := rt.PutWithLease(ctx, "k", []byte("v"), 1); return e }},
		{"GetRevision", func() error { _, e := rt.GetRevision(ctx, "k"); return e }},
		{"GetAtRevision", func() error { _, e := rt.GetAtRevision(ctx, "k", 1); return e }},
		{"Range", func() error {
			_, e := rt.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{})
			return e
		}},
		{"Compact", func() error { return rt.Compact(ctx, 1) }},
		{"CurrentRevision", func() error { _, e := rt.CurrentRevision(ctx); return e }},
		{"ExecuteTransaction", func() error {
			_, e := rt.ExecuteTransaction(ctx, &transport.TransactionRequest{})
			return e
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); !errors.As(err, &ce) {
				t.Fatalf("expected ClosedError, got %T: %v", err, err)
			}
		})
	}
}

// ===========================================================================
// Vector: closed paths
// ===========================================================================

func TestRESTVector_AllMethods_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	ctx := context.Background()
	var ce *transport.ClosedError

	cases := []struct {
		name string
		call func() error
	}{
		{"CreateNamespace", func() error { _, e := rt.CreateNamespace(ctx, &transport.CreateNamespaceRequest{}); return e }},
		{"DeleteNamespace", func() error { _, e := rt.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{}); return e }},
		{"ListNamespaces", func() error { _, e := rt.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); return e }},
		{"GetNamespaceInfo", func() error { _, e := rt.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{}); return e }},
		{"UpsertVector", func() error { _, e := rt.UpsertVector(ctx, &transport.UpsertVectorRequest{}); return e }},
		{"GetVector", func() error { _, e := rt.GetVector(ctx, &transport.GetVectorRequest{}); return e }},
		{"DeleteVector", func() error { _, e := rt.DeleteVector(ctx, &transport.DeleteVectorRequest{}); return e }},
		{"BatchUpsert", func() error { _, e := rt.BatchUpsert(ctx, &transport.BatchUpsertRequest{}); return e }},
		{"BatchDeleteVectors", func() error {
			_, e := rt.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{})
			return e
		}},
		{"ListVectors", func() error { _, e := rt.ListVectors(ctx, &transport.ListVectorsRequest{}); return e }},
		{"UpdateMetadata", func() error {
			_, e := rt.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{})
			return e
		}},
		{"SearchSimilar", func() error { _, e := rt.SearchSimilar(ctx, &transport.SearchSimilarRequest{}); return e }},
		{"SearchByMetadata", func() error {
			_, e := rt.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{})
			return e
		}},
		{"HybridSearch", func() error { _, e := rt.HybridSearch(ctx, &transport.HybridSearchRequest{}); return e }},
		{"SearchByID", func() error { _, e := rt.SearchByID(ctx, &transport.SearchByIDRequest{}); return e }},
		{"RebuildIndex", func() error { _, e := rt.RebuildIndex(ctx, &transport.RebuildIndexRequest{}); return e }},
		{"GetRebuildProgress", func() error {
			_, e := rt.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{})
			return e
		}},
		{"GetIndexStats", func() error { _, e := rt.GetIndexStats(ctx, &transport.GetIndexStatsRequest{}); return e }},
		{"CompactNamespace", func() error {
			_, e := rt.CompactNamespace(ctx, &transport.CompactNamespaceRequest{})
			return e
		}},
		{"GetCompactionProgress", func() error {
			_, e := rt.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{})
			return e
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); !errors.As(err, &ce) {
				t.Fatalf("expected ClosedError, got %T: %v", err, err)
			}
		})
	}
}

// ===========================================================================
// Tenant: closed paths
// ===========================================================================

func TestRESTTenant_AllMethods_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	ctx := context.Background()
	var ce *transport.ClosedError

	cases := []struct {
		name string
		call func() error
	}{
		{"CreateTenant", func() error { _, e := rt.CreateTenant(ctx, &transport.CreateTenantRequest{}); return e }},
		{"GetTenant", func() error { _, e := rt.GetTenant(ctx, "t1"); return e }},
		{"ListTenants", func() error { _, e := rt.ListTenants(ctx); return e }},
		{"DeleteTenant", func() error { return rt.DeleteTenant(ctx, "t1") }},
		{"UpdateTenant", func() error { _, e := rt.UpdateTenant(ctx, &transport.UpdateTenantRequest{}); return e }},
		{"UpdateTenantQuotas", func() error {
			_, e := rt.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{})
			return e
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); !errors.As(err, &ce) {
				t.Fatalf("expected ClosedError, got %T: %v", err, err)
			}
		})
	}
}

// ===========================================================================
// Database update methods: closed paths
// ===========================================================================

func TestRESTDatabase_UpdateMethods_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	ctx := context.Background()
	var ce *transport.ClosedError

	cases := []struct {
		name string
		call func() error
	}{
		{"UpdateDatabaseProtocol", func() error {
			_, e := rt.UpdateDatabaseProtocol(ctx, transport.UpdateDatabaseProtocolRequest{})
			return e
		}},
		{"UpdateDatabaseEtcd", func() error {
			_, e := rt.UpdateDatabaseEtcd(ctx, transport.UpdateDatabaseEtcdRequest{})
			return e
		}},
		{"UpdateDatabaseRaft", func() error {
			_, e := rt.UpdateDatabaseRaft(ctx, transport.UpdateDatabaseRaftRequest{})
			return e
		}},
		{"UpdateDatabaseBarrier", func() error {
			_, e := rt.UpdateDatabaseBarrier(ctx, transport.UpdateDatabaseBarrierRequest{})
			return e
		}},
		{"UpdateDatabaseConnection", func() error {
			_, e := rt.UpdateDatabaseConnection(ctx, transport.UpdateDatabaseConnectionRequest{})
			return e
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); !errors.As(err, &ce) {
				t.Fatalf("expected ClosedError, got %T: %v", err, err)
			}
		})
	}
}

// ===========================================================================
// UserAuthFactor: closed paths
// ===========================================================================

func TestRESTUserAuthFactor_AllMethods_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	ctx := context.Background()
	var ce *transport.ClosedError

	cases := []struct {
		name string
		call func() error
	}{
		{"AddAuthFactor", func() error { _, e := rt.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{}); return e }},
		{"ListAuthFactors", func() error { _, e := rt.ListAuthFactors(ctx, &transport.AuthFactorListRequest{}); return e }},
		{"GetAuthFactor", func() error { _, e := rt.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{}); return e }},
		{"RemoveAuthFactor", func() error {
			return func() error { _, e := rt.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{}); return e }()
		}},
		{"RotateAuthFactor", func() error {
			_, e := rt.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{})
			return e
		}},
		{"RelabelAuthFactor", func() error {
			_, e := rt.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{})
			return e
		}},
		{"LoginAuthFactor", func() error {
			_, e := rt.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{})
			return e
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); !errors.As(err, &ce) {
				t.Fatalf("expected ClosedError, got %T: %v", err, err)
			}
		})
	}
}

// ===========================================================================
// Database update success paths (best-effort coverage of happy path)
// ===========================================================================

func TestRESTDatabase_UpdateMethods_Success(t *testing.T) {
	t.Run("UpdateDatabaseProtocol", func(t *testing.T) {
		rt, rs, _ := newExtTransport(t)
		rs.handle(pathPrefix(http.MethodPatch, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, &transport.DatabaseInfo{Name: "db"})
		})
		if _, err := rt.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{Name: "db"}); err != nil {
			t.Fatalf("UpdateDatabaseProtocol: %v", err)
		}
	})
	t.Run("UpdateDatabaseEtcd", func(t *testing.T) {
		rt, rs, _ := newExtTransport(t)
		rs.handle(pathPrefix(http.MethodPatch, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, &transport.DatabaseInfo{})
		})
		if _, err := rt.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{Name: "db"}); err != nil {
			t.Fatalf("UpdateDatabaseEtcd: %v", err)
		}
	})
	t.Run("UpdateDatabaseRaft", func(t *testing.T) {
		rt, rs, _ := newExtTransport(t)
		rs.handle(pathPrefix(http.MethodPatch, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, &transport.DatabaseInfo{})
		})
		if _, err := rt.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{Name: "db"}); err != nil {
			t.Fatalf("UpdateDatabaseRaft: %v", err)
		}
	})
	t.Run("UpdateDatabaseBarrier", func(t *testing.T) {
		rt, rs, _ := newExtTransport(t)
		rs.handle(pathPrefix(http.MethodPatch, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, &transport.DatabaseInfo{})
		})
		if _, err := rt.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{Name: "db"}); err != nil {
			t.Fatalf("UpdateDatabaseBarrier: %v", err)
		}
	})
	t.Run("UpdateDatabaseConnection", func(t *testing.T) {
		rt, rs, _ := newExtTransport(t)
		rs.handle(pathPrefix(http.MethodPatch, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, &transport.DatabaseInfo{})
		})
		if _, err := rt.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{Name: "db"}); err != nil {
			t.Fatalf("UpdateDatabaseConnection: %v", err)
		}
	})
}
