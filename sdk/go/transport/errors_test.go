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

package transport

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// RevisionCompactedError
// ---------------------------------------------------------------------------

func TestRevisionCompactedError_Message(t *testing.T) {
	err := &RevisionCompactedError{Revision: 42}
	want := "transport: revision 42 has been compacted"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// RevisionNotFoundError
// ---------------------------------------------------------------------------

func TestRevisionNotFoundError_Message(t *testing.T) {
	err := &RevisionNotFoundError{Revision: 99}
	want := "transport: revision 99 not found"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// LeaseNotFoundError
// ---------------------------------------------------------------------------

func TestLeaseNotFoundError_Message(t *testing.T) {
	err := &LeaseNotFoundError{LeaseID: 7}
	want := "transport: lease not found: 7"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// LeaseExpiredError
// ---------------------------------------------------------------------------

func TestLeaseExpiredError_Message(t *testing.T) {
	err := &LeaseExpiredError{LeaseID: 3}
	want := "transport: lease expired: 3"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// WatchCanceledError
// ---------------------------------------------------------------------------

func TestWatchCanceledError_NoReason(t *testing.T) {
	err := &WatchCanceledError{}
	want := "transport: watch canceled"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWatchCanceledError_WithReason(t *testing.T) {
	err := &WatchCanceledError{Reason: "server shutdown"}
	want := "transport: watch canceled: server shutdown"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// IndexExistsError / IndexNotFoundError
// ---------------------------------------------------------------------------

func TestIndexExistsError_Message(t *testing.T) {
	err := &IndexExistsError{Name: "idx_user"}
	want := "transport: index already exists: idx_user"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIndexNotFoundError_Message(t *testing.T) {
	err := &IndexNotFoundError{Name: "missing_idx"}
	want := "transport: index not found: missing_idx"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// TransactionFailedError
// ---------------------------------------------------------------------------

func TestTransactionFailedError_Message(t *testing.T) {
	err := &TransactionFailedError{Revision: 12}
	want := "transport: transaction preconditions failed at revision 12"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// VectorNotFoundError / NamespaceNotFoundError / NamespaceExistsError
// ---------------------------------------------------------------------------

func TestVectorNotFoundError_Message(t *testing.T) {
	err := &VectorNotFoundError{Namespace: "embeddings", ID: "vec-1"}
	want := "transport: vector not found: embeddings/vec-1"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNamespaceNotFoundError_Message(t *testing.T) {
	err := &NamespaceNotFoundError{Namespace: "mynamespace"}
	want := "transport: namespace not found: mynamespace"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNamespaceExistsError_Message(t *testing.T) {
	err := &NamespaceExistsError{Namespace: "duplicate"}
	want := "transport: namespace already exists: duplicate"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// DimensionMismatchError
// ---------------------------------------------------------------------------

func TestDimensionMismatchError_Message(t *testing.T) {
	err := &DimensionMismatchError{Expected: 768, Got: 512}
	want := "transport: dimension mismatch: expected 768, got 512"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// NotImplementedError
// ---------------------------------------------------------------------------

func TestNotImplementedError_Message(t *testing.T) {
	err := &NotImplementedError{Method: "SomeMethod"}
	want := "transport: method not implemented: SomeMethod"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// UserNotFoundError / UserExistsError
// ---------------------------------------------------------------------------

func TestUserNotFoundError_Message(t *testing.T) {
	tests := []struct {
		name string
		err  *UserNotFoundError
		want string
	}{
		{"by id", &UserNotFoundError{ID: 42}, "user: not found: 42"},
		{"by lookup", &UserNotFoundError{Lookup: "acme/alice"}, "user: not found: acme/alice"},
		{
			// Lookup wins when both are set, so a non-id lookup never renders
			// as the zero id it did not fail on.
			"lookup wins over a zero id",
			&UserNotFoundError{Lookup: "cert_spki/abc"},
			"user: not found: cert_spki/abc",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUserLookupKey(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
		key      string
		want     string
	}{
		{"tenant scoped", "acme", "alice", "acme/alice"},
		// An empty tenant means "the server's default", which the client
		// cannot name. Rendering it as "/alice" would invent a tenant whose
		// id is the empty string.
		{"no tenant", "", "alice", "alice"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UserLookupKey(tc.tenantID, tc.key); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUserExistsError_Message(t *testing.T) {
	err := &UserExistsError{Username: "alice"}
	want := "transport: user already exists: alice"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// RoleNotFoundError / RoleExistsError
// ---------------------------------------------------------------------------

func TestRoleNotFoundError_Message(t *testing.T) {
	err := &RoleNotFoundError{ID: "role-admin"}
	want := "transport: role not found: role-admin"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRoleExistsError_Message(t *testing.T) {
	err := &RoleExistsError{Name: "superuser"}
	want := "transport: role already exists: superuser"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// CertNotFoundError
// ---------------------------------------------------------------------------

func TestCertNotFoundError_Message(t *testing.T) {
	err := &CertNotFoundError{Serial: "DEADBEEF"}
	want := "transport: certificate not found: DEADBEEF"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// ShardNotFoundError
// ---------------------------------------------------------------------------

func TestShardNotFoundError_Message(t *testing.T) {
	err := &ShardNotFoundError{ShardID: 5}
	want := "transport: shard not found: 5"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// ShardStartError / ShardStopError / ShardStateError
// ---------------------------------------------------------------------------

func TestShardStartError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("raft init failed")
	err := &ShardStartError{ShardID: 3, Err: inner}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("expected shard ID in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestShardStopError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("shutdown failed")
	err := &ShardStopError{ShardID: 7, Err: inner}
	if !strings.Contains(err.Error(), "7") {
		t.Errorf("expected shard ID in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestShardStateError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("state query failed")
	err := &ShardStateError{ShardID: 9, Err: inner}
	if !strings.Contains(err.Error(), "9") {
		t.Errorf("expected shard ID in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

// ---------------------------------------------------------------------------
// TopologyError
// ---------------------------------------------------------------------------

func TestTopologyError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("rpc failed")
	err := &TopologyError{Err: inner}
	if !strings.Contains(err.Error(), "topology") {
		t.Errorf("expected 'topology' in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

// ---------------------------------------------------------------------------
// NoStateMachineFactoryError
// ---------------------------------------------------------------------------

func TestNoStateMachineFactoryError_Message(t *testing.T) {
	err := &NoStateMachineFactoryError{}
	want := "transport: no state machine factory registered for StartShard"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// TLSError
// ---------------------------------------------------------------------------

func TestTLSError_Message(t *testing.T) {
	inner := errors.New("certificate expired")
	err := &TLSError{Err: inner}
	want := "transport: TLS error: certificate expired"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

// ---------------------------------------------------------------------------
// Backup errors
// ---------------------------------------------------------------------------

func TestBackupNotFoundError_Message(t *testing.T) {
	err := &BackupNotFoundError{ID: "bk-001"}
	want := "transport: backup not found: bk-001"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBackupCreateError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("disk full")
	err := &BackupCreateError{Err: inner}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("expected inner error in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestBackupRestoreError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("corruption detected")
	err := &BackupRestoreError{BackupID: "bk-002", Err: inner}
	if !strings.Contains(err.Error(), "bk-002") {
		t.Errorf("expected backup ID in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestBackupEmptyDestinationError_Message(t *testing.T) {
	err := &BackupEmptyDestinationError{}
	want := "transport: backup destination must not be empty"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBackupEmptyIDError_Message(t *testing.T) {
	err := &BackupEmptyIDError{}
	want := "transport: backup ID must not be empty"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBackupChecksumMismatchError_Message(t *testing.T) {
	err := &BackupChecksumMismatchError{Expected: "aabbcc", Actual: "ddeeff"}
	want := "transport: backup checksum mismatch: expected aabbcc, got ddeeff"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Rebalance errors
// ---------------------------------------------------------------------------

func TestShardStatsError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("rpc timeout")
	err := &ShardStatsError{ShardID: 2, Err: inner}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("expected shard ID in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestRebalancePlanError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("not enough nodes")
	err := &RebalancePlanError{Err: inner}
	if !strings.Contains(err.Error(), "not enough nodes") {
		t.Errorf("expected inner error in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestRebalanceExecuteError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("move failed")
	err := &RebalanceExecuteError{Err: inner}
	if !strings.Contains(err.Error(), "move failed") {
		t.Errorf("expected inner error in message: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestNilRebalancePlanError_MessageVariant(t *testing.T) {
	err := &NilRebalancePlanError{}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("expected 'nil' in message, got %q", err.Error())
	}
}
