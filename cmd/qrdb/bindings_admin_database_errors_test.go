//go:build !integration

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestAdminBinding_PromoteMember(t *testing.T) {
	tests := []struct {
		name    string
		svcErr  error
		wantErr bool
	}{
		{name: "promoted"},
		{name: "not an observer", svcErr: &service.MemberNotObserverError{ShardID: 1, ReplicaID: 20}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotShard, gotReplica uint64
			adminSvc := &fnAdminService{
				promoteMemberFn: func(_ context.Context, shardID, replicaID uint64) error {
					gotShard, gotReplica = shardID, replicaID
					return tt.svcErr
				},
			}
			conn := newTestConnectionBinding(t, &noopKVService{}, adminSvc)
			err := NewAdminBinding(testLogger(t), conn).PromoteMember(1, 20)
			if tt.wantErr {
				var noe *service.MemberNotObserverError
				if !errors.As(err, &noe) {
					t.Fatalf("want *service.MemberNotObserverError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("PromoteMember: %v", err)
			}
			if gotShard != 1 || gotReplica != 20 {
				t.Fatalf("service got shard %d replica %d; want 1, 20", gotShard, gotReplica)
			}
		})
	}
}

func TestAdminBinding_PromoteMemberNotConnected(t *testing.T) {
	b := NewAdminBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	assertNotConnected(t, b.PromoteMember(1, 2))
}

func TestDatabaseBinding_ScaleNotConnected(t *testing.T) {
	b := NewDatabaseBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Scale("orders", 3)
	assertNotConnected(t, err)
}

func TestDatabaseBinding_ScaleServiceNotWired(t *testing.T) {
	conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService())
	_, err := NewDatabaseBinding(testLogger(t), conn).Scale("orders", 3)
	assertNilService(t, err)
}

// TestCLITypedErrors pins the message and the Unwrap chain of the CLI's
// typed errors that no command path constructs in a unit test.
func TestCLITypedErrors(t *testing.T) {
	inner := errors.New("inner")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"backup transport", &BackupTransportUnsupportedError{}, "backup: active transport does not implement the backup service; use a network transport (rest, grpc, quic, mcp, unix)"},
		{"embedding transport", &EmbeddingTransportUnsupportedError{}, "embedding: active transport does not implement the embedding service; use a network transport (rest, grpc, quic, mcp, unix)"},
		{"rebalance transport", &RebalanceTransportUnsupportedError{}, "rebalance: active transport does not implement the rebalance service; use a network transport (rest, grpc, quic, mcp, unix)"},
		{"key generation", &KeyGenerationError{Err: inner}, "qrdb: failed to generate a key and certificate signing request: inner"},
		{"missing out dir", &MissingOutDirError{Reason: "enroll writes a private key"}, "qrdb: --out is required: enroll writes a private key"},
		{"embedded audit", &EmbeddedAuditError{Path: "/a", Err: inner}, `embedded: compliance audit trail "/a" cannot be opened: inner`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q; want %q", got, tt.want)
			}
		})
	}
	for _, err := range []error{&KeyGenerationError{Err: inner}, &EmbeddedAuditError{Path: "/a", Err: inner}} {
		if !errors.Is(err, inner) {
			t.Fatalf("%T does not unwrap to its cause", err)
		}
	}
}
