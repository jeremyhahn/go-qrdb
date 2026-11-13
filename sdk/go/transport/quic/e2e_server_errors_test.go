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

// Bulk server-error path coverage. Each test installs a responder that
// returns wire.StatusError so the SDK's `if err := t.call(); err != nil`
// branch is exercised. These cover the second-to-last uncovered branch
// per method (the only remaining one being a proto.Marshal failure that
// is unreachable from well-formed Go values).

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// serverErr installs a responder that returns wire.StatusError with the
// supplied message body for the given method.
func serverErr(srv *e2eServer, method, msg string) {
	srv.SetResponder(method, func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte(msg)
	})
}

// serverNotFound makes the double answer the frame a REAL qrdb server sends
// when the addressed subject does not exist: wire.StatusNotFound carrying the
// service layer's own message.
//
// It is a separate helper from serverErr, rather than a status argument added
// to it, so that a test asking for absence cannot silently get a fault. The
// production fact it mirrors is pinned end to end by
// pkg/api/quic.TestIntegration_KVGet_NotFound -- a real server, a real frame,
// a real service.KeyNotFoundError -- and per condition by
// pkg/api/quic.TestMapServiceError_EveryCondition. Before go-qrdb#437 the wire
// had no not-found status and these doubles correctly returned StatusError;
// leaving them there after the server moved would have made the double
// narrower than production in exactly the dimension under test.
func serverNotFound(srv *e2eServer, method, msg string) {
	srv.SetResponder(method, func(_ []byte) (uint16, []byte) {
		return wire.StatusNotFound, []byte(msg)
	})
}

// ---------------------------------------------------------------------------
// MVCC
// ---------------------------------------------------------------------------

func TestE2E_PutWithLease_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "mvcc.PutWithLease", "err")
	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 0); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_GetRevision_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "mvcc.GetRevision", "err")
	if _, err := tr.GetRevision(context.Background(), "k"); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_GetAtRevision_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "mvcc.GetAtRevision", "err")
	_, err := tr.GetAtRevision(context.Background(), "k", 1)
	if err == nil {
		t.Fatal("expected err")
	}
	var compacted *transport.RevisionCompactedError
	if errors.As(err, &compacted) {
		t.Fatalf("a server fault was reported as a compaction refusal: %v", err)
	}
}

// TestE2E_GetAtRevision_Compacted: the frame a real server sends for a
// revision below the compaction floor, wire.StatusOutOfRange, comes back as
// the typed refusal carrying the revision asked for.
func TestE2E_GetAtRevision_Compacted(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.GetAtRevision", func(_ []byte) (uint16, []byte) {
		return wire.StatusOutOfRange, []byte("service: revision 3 has been compacted (compacted revision 7)")
	})
	_, err := tr.GetAtRevision(context.Background(), "k", 3)
	var compacted *transport.RevisionCompactedError
	if !errors.As(err, &compacted) {
		t.Fatalf("got %T: %v, want *transport.RevisionCompactedError", err, err)
	}
	if compacted.Revision != 3 {
		t.Errorf("Revision = %d, want 3", compacted.Revision)
	}
}

func TestE2E_Range_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "mvcc.Range", "err")
	if _, err := tr.Range(context.Background(), nil, nil, transport.RangeOptions{}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_Compact_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "mvcc.Compact", "err")
	if err := tr.Compact(context.Background(), 1); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_CurrentRevision_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "mvcc.CurrentRevision", "err")
	if _, err := tr.CurrentRevision(context.Background()); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_ExecuteTransaction_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "tx.Transaction", "err")
	if _, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_ExecuteBatch_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "batch.Batch", "err")
	if _, err := tr.ExecuteBatch(context.Background(), nil); err == nil {
		t.Fatal("expected err")
	}
}

// ---------------------------------------------------------------------------
// Lease
// ---------------------------------------------------------------------------

func TestE2E_LeaseGrant_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.Grant", "err")
	if _, err := tr.LeaseGrant(context.Background(), 1); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LeaseRevoke_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.Revoke", "err")
	if err := tr.LeaseRevoke(context.Background(), 1); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LeaseKeepAlive_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.KeepAlive", "err")
	if _, err := tr.LeaseKeepAlive(context.Background(), 1); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LeaseTimeToLive_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.TimeToLive", "err")
	if _, err := tr.LeaseTimeToLive(context.Background(), 1); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LeaseList_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.List", "err")
	if _, err := tr.LeaseList(context.Background()); err == nil {
		t.Fatal("expected err")
	}
}

// ---------------------------------------------------------------------------
// Lock
// ---------------------------------------------------------------------------

func TestE2E_LockAcquire_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lock.Acquire", "err")
	if _, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{Key: "k"}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LockTryLock_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lock.TryLock", "err")
	if _, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{Key: "k"}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LockRelease_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lock.Release", "err")
	if err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{Key: "k"}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LockExtend_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lock.Extend", "err")
	if _, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{Key: "k"}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LockStatus_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lock.Status", "err")
	if _, err := tr.LockStatus(context.Background(), "k"); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_LockList_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lock.List", "err")
	if _, err := tr.LockList(context.Background(), ""); err == nil {
		t.Fatal("expected err")
	}
}

// ---------------------------------------------------------------------------
// Index
// ---------------------------------------------------------------------------

func TestE2E_IndexCreate_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "index.Create", "err")
	if err := tr.IndexCreate(context.Background(), "idx", "f", transport.IndexConfig{}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_IndexDrop_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "index.Drop", "err")
	if err := tr.IndexDrop(context.Background(), "idx"); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_IndexQuery_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "index.Query", "err")
	if _, err := tr.IndexQuery(context.Background(), "idx", "v"); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_IndexScan_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "index.Scan", "err")
	if _, err := tr.IndexScan(context.Background(), "idx", spIdx("a"), spIdx("z")); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_IndexList_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "index.List", "err")
	if _, err := tr.IndexList(context.Background()); err == nil {
		t.Fatal("expected err")
	}
}

// ---------------------------------------------------------------------------
// Barrier
// ---------------------------------------------------------------------------

func TestE2E_BarrierInitialize_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "barrier.Initialize", "err")
	if err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_BarrierUnseal_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "barrier.Unseal", "err")
	if err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_BarrierSeal_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "barrier.Seal", "err")
	if err := tr.BarrierSeal(context.Background()); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_BarrierStatus_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "barrier.Status", "err")
	if _, err := tr.BarrierStatus(context.Background()); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_BarrierShamirInitialize_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "barrier.ShamirInitialize", "err")
	if _, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_BarrierRecoveryGenerate_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "barrier.RecoveryGenerate", "err")
	if _, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{}); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_BarrierRecoveryDelete_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "barrier.RecoveryDelete", "err")
	if err := tr.BarrierDeleteRecoveryKeys(context.Background()); err == nil {
		t.Fatal("expected err")
	}
}

func TestE2E_BarrierRecoveryStatus_ServerErr(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "barrier.RecoveryStatus", "err")
	if _, err := tr.BarrierHasRecoveryKeys(context.Background()); err == nil {
		t.Fatal("expected err")
	}
}

// ---------------------------------------------------------------------------
// Ceremony / Credential / User / Role / Cert / AuthFactor / UserAuthFactor /
// Database / Tenant / Vector / TS / Collection — bulk server-error coverage
// ---------------------------------------------------------------------------

func TestE2E_BulkServerErr_RemainingMethods(t *testing.T) {
	// Each subtest registers a server-error responder and invokes the
	// matching SDK method to drive the `if call err != nil` branch.
	t.Run("ceremony.Initialize", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ceremony.Initialize", "err")
		if _, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ceremony.Status", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ceremony.Status", "err")
		if _, err := tr.CeremonyStatus(context.Background()); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ceremony.SignCSR", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ceremony.SignCSR", "err")
		if _, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("credential.Submit", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "credential.Submit", "err")
		if err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{Name: "k", Value: []byte("v")}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("credential.Strategy", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "credential.Strategy", "err")
		if _, err := tr.CredentialStrategy(context.Background()); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("credential.Delete", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "credential.Delete", "err")
		if err := tr.CredentialDelete(context.Background(), "k"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("credential.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "credential.List", "err")
		if _, err := tr.CredentialList(context.Background(), &transport.CredentialListRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("credential.Rotate", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "credential.Rotate", "err")
		if _, err := tr.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "k", NewValue: []byte("v")}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("credential.RetrieveVersion", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "credential.RetrieveVersion", "err")
		if _, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "k", Version: 1}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("credential.ListVersions", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "credential.ListVersions", "err")
		if _, err := tr.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "k"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("credential.GCExpired", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "credential.GCExpired", "err")
		if _, err := tr.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "k"}); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("user.Create", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "user.Create", "err")
		if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "a"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("user.Get", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "user.Get", "err")
		if _, err := tr.GetUser(context.Background(), 1); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("user.GetByUsername", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "user.GetByUsername", "err")
		if _, err := tr.GetUserByUsername(context.Background(), "t1", "alice"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("user.Update", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "user.Update", "err")
		if _, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{ID: 1}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("user.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "user.List", "err")
		if _, err := tr.ListUsers(context.Background(), &transport.ListUsersRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("user.Suspend", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "user.Suspend", "err")
		if err := tr.SuspendUser(context.Background(), 1); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("user.Unsuspend", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "user.Unsuspend", "err")
		if err := tr.UnsuspendUser(context.Background(), 1); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("role.Create", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "role.Create", "err")
		if _, err := tr.CreateRole(context.Background(), &transport.CreateRoleRequest{Name: "a"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("role.Get", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "role.Get", "err")
		if _, err := tr.GetRole(context.Background(), "r1"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("role.Update", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "role.Update", "err")
		if _, err := tr.UpdateRole(context.Background(), &transport.UpdateRoleRequest{ID: "r1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("role.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "role.List", "err")
		if _, err := tr.ListRoles(context.Background(), &transport.ListRolesRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("role.ListPermissions", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "role.ListPermissions", "err")
		if _, err := tr.ListPermissions(context.Background()); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("cert.Issue", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "cert.Issue", "err")
		if _, err := tr.IssueCert(context.Background(), &transport.IssueCertRequest{Username: "a", CSRPEM: "csr"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("cert.Enroll", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "cert.Enroll", "err")
		if _, err := tr.EnrollCert(context.Background(), &transport.EnrollCertRequest{Username: "a", CertPEM: "pem"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.TrustAnchorAdd", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.TrustAnchorAdd", "err")
		if _, err := tr.AddTrustAnchors(context.Background(), &transport.AddTrustAnchorsRequest{TenantID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.TrustAnchorList", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.TrustAnchorList", "err")
		if _, err := tr.ListTrustAnchors(context.Background(), "t1"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.TrustAnchorRemove", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.TrustAnchorRemove", "err")
		if err := tr.RemoveTrustAnchor(context.Background(), "t1", "fp1"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("cert.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "cert.List", "err")
		if _, err := tr.ListCerts(context.Background(), "t1"); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("authfactor.Enroll", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "authfactor.Enroll", "err")
		if _, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("authfactor.Challenge", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "authfactor.Challenge", "err")
		if _, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{EnrollmentID: "e"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("authfactor.Verify", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "authfactor.Verify", "err")
		if err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{ChallengeID: "c"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("authfactor.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "authfactor.List", "err")
		if _, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("authfactor.Revoke", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "authfactor.Revoke", "err")
		if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{EnrollmentID: "e"}); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("userauthfactor.Add", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "userauthfactor.Add", "err")
		if _, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{UserID: 1, Type: "mtls"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("userauthfactor.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "userauthfactor.List", "err")
		if _, err := tr.ListAuthFactors(context.Background(), &transport.AuthFactorListRequest{UserID: 1}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("userauthfactor.Get", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "userauthfactor.Get", "err")
		if _, err := tr.GetAuthFactor(context.Background(), &transport.AuthFactorGetRequest{FactorID: "f"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("userauthfactor.Remove", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "userauthfactor.Remove", "err")
		if _, err := tr.RemoveAuthFactor(context.Background(), &transport.AuthFactorRemoveRequest{FactorID: "f"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("userauthfactor.Delete", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "userauthfactor.Delete", "err")
		if _, err := tr.DeleteAuthFactor(context.Background(), &transport.AuthFactorDeleteRequest{FactorID: "f"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("userauthfactor.Rotate", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "userauthfactor.Rotate", "err")
		if _, err := tr.RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{OldFactorID: "f"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("userauthfactor.Relabel", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "userauthfactor.Relabel", "err")
		if _, err := tr.RelabelAuthFactor(context.Background(), &transport.AuthFactorRelabelRequest{FactorID: "f", Label: "l"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("userauthfactor.Login", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "userauthfactor.Login", "err")
		if _, err := tr.LoginAuthFactor(context.Background(), &transport.AuthFactorLoginRequest{Type: "mtls"}); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("db.Create", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.Create", "err")
		if _, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{Name: "db"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("db.Get", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.Get", "err")
		if _, err := tr.GetDatabase(context.Background(), "db"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("db.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.List", "err")
		if _, err := tr.ListDatabases(context.Background()); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("db.Drop", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.Drop", "err")
		if err := tr.DropDatabase(context.Background(), "db"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("db.UpdateProtocol", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.UpdateProtocol", "err")
		if _, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{Name: "db"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("db.UpdateEtcd", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.UpdateEtcd", "err")
		if _, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{Name: "db"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("db.UpdateRaft", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.UpdateRaft", "err")
		if _, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{Name: "db"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("db.UpdateBarrier", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.UpdateBarrier", "err")
		if _, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{Name: "db"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("db.UpdateConnection", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "db.UpdateConnection", "err")
		if _, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{Name: "db"}); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("tenant.Create", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.Create", "err")
		if _, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.Get", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.Get", "err")
		if _, err := tr.GetTenant(context.Background(), "t1"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.List", "err")
		if _, err := tr.ListTenants(context.Background()); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.Update", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.Update", "err")
		if _, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.UpdateQuotas", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.UpdateQuotas", "err")
		if _, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{TenantID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.BarrierInitBegin", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.BarrierInitBegin", "err")
		if _, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.BarrierInitComplete", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		srv.SetResponder("tenant.BarrierInitBegin", func(_ []byte) (uint16, []byte) {
			return wire.StatusOK, []byte(`{}`)
		})
		serverErr(srv, "tenant.BarrierInitComplete", "err")
		if _, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.BarrierUnseal", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.BarrierUnseal", "err")
		if err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.BarrierSeal", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.BarrierSeal", "err")
		if err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("tenant.BarrierStatus", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "tenant.BarrierStatus", "err")
		if _, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "t1"}); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("vector.CreateNamespace", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.CreateNamespace", "err")
		if _, err := tr.CreateNamespace(context.Background(), &transport.CreateNamespaceRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.ListNamespaces", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.ListNamespaces", "err")
		if _, err := tr.ListNamespaces(context.Background(), &transport.ListNamespacesRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.GetNamespaceInfo", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.GetNamespaceInfo", "err")
		if _, err := tr.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.Upsert", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.Upsert", "err")
		if _, err := tr.UpsertVector(context.Background(), &transport.UpsertVectorRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.Get", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.Get", "err")
		if _, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.BatchDelete", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.BatchDelete", "err")
		if _, err := tr.BatchDeleteVectors(context.Background(), &transport.BatchDeleteVectorsRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.UpdateMetadata", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.UpdateMetadata", "err")
		if _, err := tr.UpdateMetadata(context.Background(), &transport.UpdateVectorMetadataRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.SearchSimilar", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.SearchSimilar", "err")
		if _, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.SearchByMetadata", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.SearchByMetadata", "err")
		if _, err := tr.SearchByMetadata(context.Background(), &transport.SearchByMetadataRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.HybridSearch", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.HybridSearch", "err")
		if _, err := tr.HybridSearch(context.Background(), &transport.HybridSearchRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.SearchByID", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.SearchByID", "err")
		if _, err := tr.SearchByID(context.Background(), &transport.SearchByIDRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.RebuildIndex", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.RebuildIndex", "err")
		if _, err := tr.RebuildIndex(context.Background(), &transport.RebuildIndexRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.GetRebuildProgress", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.GetRebuildProgress", "err")
		if _, err := tr.GetRebuildProgress(context.Background(), &transport.GetRebuildProgressRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.GetIndexStats", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.GetIndexStats", "err")
		if _, err := tr.GetIndexStats(context.Background(), &transport.GetIndexStatsRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.CompactNamespace", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.CompactNamespace", "err")
		if _, err := tr.CompactNamespace(context.Background(), &transport.CompactNamespaceRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("vector.GetCompactionProgress", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "vector.GetCompactionProgress", "err")
		if _, err := tr.GetCompactionProgress(context.Background(), &transport.GetCompactionProgressRequest{Namespace: "ns"}); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("ts.Write", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ts.Write", "err")
		if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{Metric: "m"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ts.Query", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ts.Query", "err")
		if _, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{Metric: "m"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ts.Aggregate", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ts.Aggregate", "err")
		if _, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{Metric: "m"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ts.WritePoint", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ts.WritePoint", "err")
		if err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{Metric: "m"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ts.RichQuery", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ts.RichQuery", "err")
		if _, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{Metric: "m"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ts.GetRetention", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ts.GetRetention", "err")
		if _, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "m"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ts.SetRetention", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ts.SetRetention", "err")
		if err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{Metric: "m"}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("ts.ListMetrics", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "ts.ListMetrics", "err")
		if _, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("collection.Create", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "collection.Create", "err")
		if _, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("collection.GetInfo", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "collection.GetInfo", "err")
		if _, err := tr.GetCollectionInfo(context.Background(), "db", "c"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("collection.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "collection.List", "err")
		if _, err := tr.ListCollections(context.Background(), "db"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("collection.Drop", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "collection.Drop", "err")
		if err := tr.DropCollection(context.Background(), "db", "c"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("collection.InsertDocument", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "collection.InsertDocument", "err")
		if _, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("collection.GetDocument", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "collection.GetDocument", "err")
		if _, err := tr.GetDocument(context.Background(), "db", "c", 1); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("collection.UpdateDocument", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "collection.UpdateDocument", "err")
		if err := tr.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{}); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("collection.DeleteDocument", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "collection.DeleteDocument", "err")
		if err := tr.DeleteDocument(context.Background(), "db", "c", 1); err == nil {
			t.Fatal("expected err")
		}
	})

	t.Run("admin.GetShardState", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "admin.GetShardState", "err")
		if _, err := tr.GetShardState(context.Background(), 1); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("admin.GetTopology", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "admin.GetTopology", "err")
		if _, err := tr.GetTopology(context.Background()); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("kv.List", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "kv.List", "err")
		if _, err := tr.List(context.Background(), "p"); err == nil {
			t.Fatal("expected err")
		}
	})
	t.Run("kv.Scan", func(t *testing.T) {
		srv, tr := startE2EServer(t)
		serverErr(srv, "kv.Scan", "err")
		if _, err := tr.Scan(context.Background(), "p"); err == nil {
			t.Fatal("expected err")
		}
	})
}
