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
	if _, err := tr.GetAtRevision(context.Background(), "k", 1); err == nil {
		t.Fatal("expected err")
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
