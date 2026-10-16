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

// Second coverage-uplift file for the QUIC SDK transport. Targets the
// remaining sub-91% functions by adding unmarshal-error paths, extra
// closed-path guards, and success paths for methods at ~66-88% coverage:
// CeremonyCustodian closed paths, CredentialStore/Retrieve, call/callContext
// paths, TSRichQuery, role-resolve error paths, quicCustodianToInfo nil/ts
// paths.

package quic

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// CredentialStore / CredentialRetrieve success paths (62.5% / 66.7%)
// Already have closed from transport_closed_test.go; add success+error paths.
// ---------------------------------------------------------------------------

func TestE2E_CredentialStore_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Store", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.CredentialStore(context.Background(), "key", []byte("val")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
}

func TestE2E_CredentialStore_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Store", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("store failed")
	})
	if err := tr.CredentialStore(context.Background(), "key", []byte("val")); err == nil {
		t.Fatal("expected error")
	}
}

func TestE2E_CredentialRetrieve_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Retrieve", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CredentialRetrieveResponse{Value: []byte("secret")})
	})
	val, err := tr.retrieveBytes(context.Background(), "key")
	if err != nil {
		t.Fatalf("CredentialRetrieve: %v", err)
	}
	if string(val) != "secret" {
		t.Fatalf("unexpected value: %q", string(val))
	}
}

func TestE2E_CredentialRetrieve_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Retrieve", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	_, err := tr.retrieveBytes(context.Background(), "missing")
	assertQuicRequestError(t, err)
}

func TestE2E_CredentialRetrieve_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.Retrieve", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.retrieveBytes(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error for bad proto unmarshal, got nil")
	}
	// unmarshal errors from this method are returned as *unmarshalError (not wrapped)
	var ume *unmarshalError
	if !errorAs(err, &ume) {
		t.Fatalf("expected unmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetRoleByName / ResolvePermissions (75.0%) — RPCError paths
// ---------------------------------------------------------------------------

func TestE2E_GetRoleByName_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.GetByName", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("role not found")
	})
	_, err := tr.GetRoleByName(context.Background(), &transport.GetRoleByNameRequest{TenantID: "t1", Name: "admin"})
	assertQuicRequestError(t, err)
}

func TestE2E_ResolvePermissions_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.ResolvePermissions", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("resolve failed")
	})
	_, err := tr.ResolvePermissions(context.Background(), &transport.ResolvePermissionsRequest{RoleIDs: []string{"admin"}})
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// TSRichQuery — additional RPC-error path (covers the 77.3% remaining branch)
// ---------------------------------------------------------------------------

func TestE2E_TSRichQuery_RPCError2(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.RichQuery", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("ts unavailable")
	})
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{})
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Backup unmarshal-error paths for GetBackup/VerifyBackup
// (transport_backup.go shows 83.3% for those two, meaning the unmarshal branch
// is not covered)
// ---------------------------------------------------------------------------

func TestE2E_Backup_GetBackup_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.GetBackup(context.Background(), "bk-1")
	assertQuicRequestError(t, err)
}

func TestE2E_Backup_VerifyBackup_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Verify", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.VerifyBackup(context.Background(), "bk-1")
	assertQuicRequestError(t, err)
}

func TestE2E_Backup_RestoreBackup_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Restore", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.RestoreBackup(context.Background(), &transport.RestoreRequest{BackupID: "bk-1"})
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Rebalance unmarshal-error paths
// ---------------------------------------------------------------------------

func TestE2E_Rebalance_GetShardStats_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.GetShardStats", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.GetShardStats(context.Background(), 1)
	assertQuicRequestError(t, err)
}

func TestE2E_Rebalance_CreateRebalancePlan_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.CreatePlan", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{})
	assertQuicRequestError(t, err)
}

func TestE2E_Rebalance_ExecuteRebalance_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.Execute", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.ExecuteRebalance(context.Background(), &transport.RebalancePlan{})
	assertQuicRequestError(t, err)
}

func TestE2E_Rebalance_GetImbalanceReport_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.GetImbalanceReport", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.GetImbalanceReport(context.Background())
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Embedding unmarshal-error paths
// ---------------------------------------------------------------------------

func TestE2E_Embedding_BatchEmbed_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.BatchEmbed", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{Texts: []string{"x"}})
	assertQuicRequestError(t, err)
}

func TestE2E_Embedding_ListModels_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.ListModels", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.ListEmbeddingModels(context.Background(), "openai")
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Additional closed-path guards for methods at 88.9% coverage
// (those methods have 2 uncovered statements: the closed return)
// Note: most are already covered in transport_closed_test.go; these cover
// the remainder that aren't included there.
// ---------------------------------------------------------------------------

func TestQUICExtraClosed_Auth(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ce = nil

	if err := tr.DeleteUser(context.Background(), 1); !errorsAsP(err, &ce) {
		t.Errorf("DeleteUser: %v", err)
	}
	if err := tr.EnableUser(context.Background(), 1); !errorsAsP(err, &ce) {
		t.Errorf("EnableUser: %v", err)
	}
	if err := tr.DisableUser(context.Background(), 1); !errorsAsP(err, &ce) {
		t.Errorf("DisableUser: %v", err)
	}
	if err := tr.DeleteRole(context.Background(), "r"); !errorsAsP(err, &ce) {
		t.Errorf("DeleteRole: %v", err)
	}
	if err := tr.AssignRole(context.Background(), 1, "r"); !errorsAsP(err, &ce) {
		t.Errorf("AssignRole: %v", err)
	}
	if err := tr.UnassignRole(context.Background(), 1, "r"); !errorsAsP(err, &ce) {
		t.Errorf("UnassignRole: %v", err)
	}
	if _, err := tr.RevokeCert(context.Background(), "fp"); !errorsAsP(err, &ce) {
		t.Errorf("RevokeCert: %v", err)
	}
}

// errorsAsP is a convenience wrapper matching errors.As behaviour.
func errorsAsP(err error, target **transport.ClosedError) bool {
	if err == nil {
		return false
	}
	return errorAs(err, target)
}

func TestQUICExtraClosed_Embedding(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{}); !errorAs(err, &ce) {
		t.Errorf("BatchEmbed: %v", err)
	}
	if _, err := tr.ListEmbeddingModels(context.Background(), "p"); !errorAs(err, &ce) {
		t.Errorf("ListEmbeddingModels: %v", err)
	}
}

func TestQUICExtraClosed_Rebalance(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if _, err := tr.GetShardStats(context.Background(), 1); !errorAs(err, &ce) {
		t.Errorf("GetShardStats: %v", err)
	}
	if _, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{}); !errorAs(err, &ce) {
		t.Errorf("CreateRebalancePlan: %v", err)
	}
	if _, err := tr.ExecuteRebalance(context.Background(), nil); !errorAs(err, &ce) {
		t.Errorf("ExecuteRebalance: %v", err)
	}
	if _, err := tr.GetImbalanceReport(context.Background()); !errorAs(err, &ce) {
		t.Errorf("GetImbalanceReport: %v", err)
	}
}

func TestQUICExtraClosed_DatabaseBarrier(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	if err := tr.SealDatabase(context.Background(), "db"); !errorAs(err, &ce) {
		t.Errorf("SealDatabase: %v", err)
	}
	if err := tr.UnsealDatabase(context.Background(), "db", "s"); !errorAs(err, &ce) {
		t.Errorf("UnsealDatabase: %v", err)
	}
	if _, err := tr.DatabaseBarrierStatus(context.Background(), "db"); !errorAs(err, &ce) {
		t.Errorf("DatabaseBarrierStatus: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetUserByCertThumbprint / AddCertBinding unmarshal-error paths (83.3%)
// ---------------------------------------------------------------------------

func TestE2E_Auth_GetUserByCertThumbprint_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.GetByCertThumbprint", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("this is not json")
	})
	_, err := tr.GetUserByCertThumbprint(context.Background(), "tp")
	assertQuicRequestError(t, err)
}

func TestE2E_Auth_AddCertBinding_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.AddCertBinding", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("this is not json")
	})
	_, err := tr.AddCertBinding(context.Background(), 1, "fp", 0)
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// callContext timeout path
// ---------------------------------------------------------------------------

func TestQUIC_CallContext_WithTimeout(t *testing.T) {
	_, tr := startE2EServer(t)
	// The health responder is already registered by startE2EServer.
	// Override tr's config to set a timeout so callContext creates a deadline context.
	tr.config.Timeout = 5000000000 // 5 seconds in nanoseconds

	resp, err := tr.Health(context.Background())
	if err != nil {
		t.Fatalf("Health with timeout: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("unexpected status: %q", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// AuthFactor unmarshal-error paths (VerifyFactor, ListFactors, RevokeFactor at 85-86%)
// ---------------------------------------------------------------------------

// The responder method names below are "authfactor.*", which is what
// transport_authfactor.go actually calls. They read "factor.*" until
// go-qrdb#124, so the responder never matched: the fake server fell through to
// its unregistered-method default and these tests were exercising the
// StatusUnimplemented path while claiming to exercise StatusError. That passed
// only because both statuses mapped to *transport.RequestError. Giving
// StatusUnimplemented its own typed error surfaced the mismatch, so the fix is
// to make the tests call what they say they call rather than to loosen the
// assertion.

func TestE2E_AuthFactor_VerifyFactor_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.Verify", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("verify failed")
	})
	err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{})
	assertQuicRequestError(t, err)
}

func TestE2E_AuthFactor_ListFactors_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("list failed")
	})
	_, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{})
	assertQuicRequestError(t, err)
}

func TestE2E_AuthFactor_RevokeFactor_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.Revoke", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("revoke failed")
	})
	err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{})
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Collection unmarshal-error / error paths
// ---------------------------------------------------------------------------

func TestE2E_Collection_UpdateDocument_Closed(t *testing.T) {
	// Use newClosedTransport to cover the UpdateDocument closed-path branch
	// without spinning up a QUIC server.
	tr := newClosedTransport()
	err := tr.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{})
	var ce *transport.ClosedError
	if !errorAs(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteBackup unmarshal-error path (87.5%) — the proto.Marshal error branch
// is hard to hit; test the call error path separately by using a slow server.
// Instead add closed path test that wasn't covered before.
// ---------------------------------------------------------------------------

func TestQUICExtraClosed_Remaining(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError

	// Barrier methods
	if err := tr.BarrierSeal(context.Background()); !errorAs(err, &ce) {
		t.Errorf("BarrierSeal: %v", err)
	}

	// Collection
	if err := tr.DropCollection(context.Background(), "db", "c"); !errorAs(err, &ce) {
		t.Errorf("DropCollection: %v", err)
	}

	// Auth factor
	if err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{}); !errorAs(err, &ce) {
		t.Errorf("VerifyFactor: %v", err)
	}
	if _, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{}); !errorAs(err, &ce) {
		t.Errorf("ListFactors: %v", err)
	}
	if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{}); !errorAs(err, &ce) {
		t.Errorf("RevokeFactor: %v", err)
	}
}
