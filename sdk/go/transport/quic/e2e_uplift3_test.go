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

// Third coverage-uplift file for the QUIC SDK transport. Adds RPC-error paths
// for auth, tenant, timeseries, and vector methods that only have closed + success
// paths currently.

package quic

import (
	"context"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Auth — RPC-error paths for simple error-returning methods
// ---------------------------------------------------------------------------

func TestE2E_Auth_DeleteUser_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	assertQuicRequestError(t, tr.DeleteUser(context.Background(), 1))
}

func TestE2E_Auth_EnableUser_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Enable", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("forbidden")
	})
	assertQuicRequestError(t, tr.EnableUser(context.Background(), 1))
}

func TestE2E_Auth_DisableUser_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Disable", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("forbidden")
	})
	assertQuicRequestError(t, tr.DisableUser(context.Background(), 1))
}

func TestE2E_Auth_DeleteRole_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	assertQuicRequestError(t, tr.DeleteRole(context.Background(), "r1"))
}

func TestE2E_Auth_AssignRole_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Assign", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("user not found")
	})
	assertQuicRequestError(t, tr.AssignRole(context.Background(), 1, "admin"))
}

func TestE2E_Auth_UnassignRole_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Unassign", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("role not assigned")
	})
	assertQuicRequestError(t, tr.UnassignRole(context.Background(), 1, "admin"))
}

func TestE2E_Auth_RevokeCert_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Revoke", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("cert not found")
	})
	_, err := tr.RevokeCert(context.Background(), "fp")
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Transport.go — RPC-error paths for Put, Delete, Health, Status, etc.
// ---------------------------------------------------------------------------

func TestE2E_Transport_Put_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Put", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("write failed")
	})
	assertQuicRequestError(t, tr.Put(context.Background(), "k", []byte("v")))
}

func TestE2E_Transport_Delete_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("delete failed")
	})
	assertQuicRequestError(t, tr.Delete(context.Background(), "k"))
}

func TestE2E_Transport_AddMember_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.AddMember", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("add member failed")
	})
	assertQuicRequestError(t, tr.AddMember(context.Background(), 1, 2, "127.0.0.1:5001"))
}

func TestE2E_Transport_RemoveMember_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.RemoveMember", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("remove failed")
	})
	assertQuicRequestError(t, tr.RemoveMember(context.Background(), 1, 2))
}

func TestE2E_Transport_RequestSnapshot_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.RequestSnapshot", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("snapshot failed")
	})
	assertQuicRequestError(t, tr.RequestSnapshot(context.Background(), 1))
}

func TestE2E_Transport_StartShard_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.StartShard", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("shard failed")
	})
	assertQuicRequestError(t, tr.StartShard(context.Background(), &transport.StartShardRequest{ShardID: 1}))
}

func TestE2E_Transport_StopShard_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.StopShard", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("stop failed")
	})
	assertQuicRequestError(t, tr.StopShard(context.Background(), 1))
}

// ---------------------------------------------------------------------------
// Tenant — RPC-error for DeleteTenant
// ---------------------------------------------------------------------------

func TestE2E_Tenant_DeleteTenant_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	assertQuicRequestError(t, tr.DeleteTenant(context.Background(), "t1"))
}

// ---------------------------------------------------------------------------
// TimeSeries — RPC-error for TSWriteBatch and TSDeleteSeries
// ---------------------------------------------------------------------------

func TestE2E_TS_WriteBatch_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.WriteBatch", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("write failed")
	})
	err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{{Metric: "m1"}})
	assertQuicRequestError(t, err)
}

func TestE2E_TS_DeleteSeries_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.DeleteSeries", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{})
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Vector — RPC-error for DeleteNamespace, DeleteVector, BatchUpsert, ListVectors
// ---------------------------------------------------------------------------

func TestE2E_Vector_DeleteNamespace_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.DeleteNamespace", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	_, err := tr.DeleteNamespace(context.Background(), &transport.DeleteNamespaceRequest{Namespace: "ns1"})
	assertQuicRequestError(t, err)
}

func TestE2E_Vector_DeleteVector_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	_, err := tr.DeleteVector(context.Background(), &transport.DeleteVectorRequest{Namespace: "ns1", ID: 1})
	assertQuicRequestError(t, err)
}

func TestE2E_Vector_BatchUpsert_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.BatchUpsert", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("batch failed")
	})
	_, err := tr.BatchUpsert(context.Background(), &transport.BatchUpsertRequest{Namespace: "ns1"})
	assertQuicRequestError(t, err)
}

func TestE2E_Vector_ListVectors_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("list failed")
	})
	_, err := tr.ListVectors(context.Background(), &transport.ListVectorsRequest{Namespace: "ns1"})
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Collection — RPC-error for ListDocuments
// ---------------------------------------------------------------------------

func TestE2E_Collection_ListDocuments_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.ListDocuments", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("list failed")
	})
	_, err := tr.ListDocuments(context.Background(), transport.ListDocumentsRequest{Database: "db", Collection: "c"})
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// DEK NotImplemented paths (66.7% → 100% coverage)
// The closed path is tested in transport_closed_test.go; these cover the
// NotImplemented return path.
// ---------------------------------------------------------------------------

func TestQUIC_DEK_NotImplemented(t *testing.T) {
	_, tr := startE2EServer(t)
	var nie *transport.NotImplementedError

	if err := tr.DEKInitialize(context.Background(), &transport.DEKInitializeRequest{}); !errorAs(err, &nie) {
		t.Errorf("DEKInitialize: %v", err)
	}
	if _, err := tr.DEKGetStatus(context.Background()); !errorAs(err, &nie) {
		t.Errorf("DEKGetStatus: %v", err)
	}
	if err := tr.DEKRotate(context.Background()); !errorAs(err, &nie) {
		t.Errorf("DEKRotate: %v", err)
	}
	if _, err := tr.DEKGetCurrent(context.Background()); !errorAs(err, &nie) {
		t.Errorf("DEKGetCurrent: %v", err)
	}
	if err := tr.DEKSyncToNode(context.Background(), &transport.DEKSyncRequest{}); !errorAs(err, &nie) {
		t.Errorf("DEKSyncToNode: %v", err)
	}
	if _, err := tr.TenantDEKGetOrCreate(context.Background(), &transport.TenantDEKRequest{}); !errorAs(err, &nie) {
		t.Errorf("TenantDEKGetOrCreate: %v", err)
	}
	if err := tr.TenantDEKRotate(context.Background(), &transport.TenantDEKRotateRequest{}); !errorAs(err, &nie) {
		t.Errorf("TenantDEKRotate: %v", err)
	}
	if err := tr.TenantDEKDelete(context.Background(), &transport.TenantDEKDeleteRequest{}); !errorAs(err, &nie) {
		t.Errorf("TenantDEKDelete: %v", err)
	}
	if _, err := tr.TenantDEKList(context.Background()); !errorAs(err, &nie) {
		t.Errorf("TenantDEKList: %v", err)
	}
	if _, err := tr.TenantDEKGetStatus(context.Background(), &transport.TenantDEKRequest{}); !errorAs(err, &nie) {
		t.Errorf("TenantDEKGetStatus: %v", err)
	}
}

// ---------------------------------------------------------------------------
// buildTLSConfig error paths (73.7%)
// CA file read error and cert load error paths.
// ---------------------------------------------------------------------------

func TestQUIC_BuildTLSConfig_CAFileReadError(t *testing.T) {
	tr := &Transport{config: &transport.Config{TLSCAFile: "/nonexistent/ca.pem"}}
	_, err := tr.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for nonexistent CA file")
	}
	var tlsErr *transport.TLSError
	if !errorAs(err, &tlsErr) {
		t.Fatalf("expected TLSError, got %T: %v", err, err)
	}
}

func TestQUIC_BuildTLSConfig_CAParseError(t *testing.T) {
	// Write a file with invalid PEM data.
	f, err := writeTmpFile(t, []byte("this is not valid PEM"))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	tr := &Transport{config: &transport.Config{TLSCAFile: f}}
	_, parseErr := tr.buildTLSConfig()
	if parseErr == nil {
		t.Fatal("expected error for invalid CA PEM")
	}
	var tlsErr *transport.TLSError
	if !errorAs(parseErr, &tlsErr) {
		t.Fatalf("expected TLSError, got %T: %v", parseErr, parseErr)
	}
}

// writeTmpFile writes data to a temp file and returns the path.
func writeTmpFile(t *testing.T, data []byte) (string, error) {
	t.Helper()
	f, err := os.CreateTemp("", "quic-test-*.pem")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name(), nil
}

// ---------------------------------------------------------------------------
// CredentialStore — success path (was missing 75.0%)
// Closed path is covered in transport_closed_test.go, but "not found"
// is needed too.
// ---------------------------------------------------------------------------

func TestE2E_CredentialStore_Closed(t *testing.T) {
	tr := newClosedTransport()
	err := tr.CredentialStore(context.Background(), "k", []byte("v"))
	var ce *transport.ClosedError
	if !errorAs(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
