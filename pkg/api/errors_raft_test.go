//go:build !integration

package api

import (
	"errors"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestRaftSimpleErrors(t *testing.T) {
	tests := []struct {
		name    string
		errFn   func() error
		wantMsg string
	}{
		{"ResourceClosed", func() error { return &ResourceClosedError{Resource: "writer"} }, "writer is closed"},
		{"Checksum", func() error { return &ChecksumError{Subject: "backup"} }, "backup checksum verification failed"},
		{"UnsupportedValue", func() error { return &UnsupportedValueError{Field: "format", Value: "xml"} }, "unsupported format: xml"},
		{"IncompleteData", func() error { return &IncompleteDataError{Subject: "header"} }, "incomplete data: header"},
		{"DeadlineRequired", func() error { return &DeadlineRequiredError{} }, "context must have deadline"},
		{"DeadlineExceeded", func() error { return &DeadlineExceededError{} }, "context deadline already exceeded"},
		{"UnknownOperation", func() error { return &UnknownOperationError{Operation: "foobar"} }, "unknown operation: foobar"},
		{"CanceledError", func() error { return &CanceledError{Operation: "write"} }, "write canceled"},
		{"ComponentNotInitialized", func() error { return &ComponentNotInitializedError{Component: "raft"} }, "raft not initialized"},
		{"WriterClosed", func() error { return &WriterClosedError{} }, "writer is closed"},
		{"AlreadyBootstrapped", func() error { return &AlreadyBootstrappedError{} }, "already bootstrapped"},
		{"InvalidNodeIDZero", func() error { return &InvalidNodeIDZeroError{} }, "peer has invalid node_id=0"},
		{"InvalidNodeIDMembers", func() error { return &InvalidNodeIDMembersError{} }, "invalid node_id=0 in members"},
		{"MTLSConfig", func() error { return &MTLSConfigError{} }, "mTLS enabled but TLS certificate files not configured"},
		{"UnsupportedConsistencyLevel", func() error { return &UnsupportedConsistencyLevelError{Level: "eventual"} }, "unsupported consistency level: eventual"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}

func TestInvalidOpcodeError(t *testing.T) {
	t.Run("WithContext", func(t *testing.T) {
		e := &InvalidOpcodeError{Opcode: 0xAB, Context: "batch"}
		want := "invalid opcode: 0xab (batch)"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutContext", func(t *testing.T) {
		e := &InvalidOpcodeError{Opcode: 0xAB}
		want := "invalid opcode: 0xab"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestShardOperationError(t *testing.T) {
	cause := errors.New("timeout")
	t.Run("WithCause", func(t *testing.T) {
		e := &ShardOperationError{ShardID: 1, Operation: "propose", Cause: cause}
		want := "propose for shard 1: timeout"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &ShardOperationError{ShardID: 2, Operation: "read"}
		want := "read for shard 2"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestNodeOperationError(t *testing.T) {
	cause := errors.New("refused")
	t.Run("Error", func(t *testing.T) {
		e := &NodeOperationError{NodeID: 3, ShardID: 1, Operation: "add", Cause: cause}
		want := "failed to add node 3 in shard 1: refused"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
}

func TestLengthMismatchError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &LengthMismatchError{FieldA: "keys", LenA: 5, FieldB: "values", LenB: 3}
		want := "keys and values length mismatch: 5 != 3"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestSnapshotOperationError(t *testing.T) {
	cause := errors.New("io error")
	t.Run("WithCause", func(t *testing.T) {
		e := &SnapshotOperationError{Operation: "save", Cause: cause}
		want := "snapshot save: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &SnapshotOperationError{Operation: "restore"}
		if got := e.Error(); got != "snapshot restore" {
			t.Errorf("got %q", got)
		}
	})
}

func TestInvalidContextTypeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &InvalidContextTypeError{Expected: "*raft.Context"}
		if got := e.Error(); got != "invalid context type, expected *raft.Context" {
			t.Errorf("got %q", got)
		}
	})
}

func TestBatchOperationError(t *testing.T) {
	cause := errors.New("fail")
	t.Run("WithCause", func(t *testing.T) {
		e := &BatchOperationError{Index: 0, Operation: "put", Cause: cause}
		want := "batch operation 0 (put) failed: fail"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap did not return cause")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &BatchOperationError{Index: 1, Operation: "delete"}
		want := "batch operation 1 has invalid type: delete"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestInvalidQueryTypeError(t *testing.T) {
	t.Run("WithActual", func(t *testing.T) {
		e := &InvalidQueryTypeError{Expected: "GetQuery", Actual: "PutQuery"}
		want := "invalid query type: got PutQuery, expected GetQuery"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutActual", func(t *testing.T) {
		e := &InvalidQueryTypeError{Expected: "GetQuery"}
		if got := e.Error(); got != "invalid query type: expected GetQuery" {
			t.Errorf("got %q", got)
		}
	})
}

func TestRaftClusterErrors(t *testing.T) {
	t.Run("RequiredFieldOperation", func(t *testing.T) {
		e := &RequiredFieldOperationError{Field: "key", Operation: "put"}
		if got := e.Error(); got != "key is required for put operation" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("InvalidEntityKey", func(t *testing.T) {
		e := &InvalidEntityKeyError{Operation: "get"}
		if got := e.Error(); got != "invalid entity key in get operation" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("BatchFormat", func(t *testing.T) {
		e := &BatchFormatError{Message: "a list of operations"}
		if got := e.Error(); got != "batch operations must be a list of operations" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("InsufficientPeers", func(t *testing.T) {
		e := &InsufficientPeersError{Required: 3, Have: 1}
		want := "insufficient peers: need 3, have 1"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("DuplicateNodeID", func(t *testing.T) {
		e := &DuplicateNodeIDError{NodeID: 5}
		if got := e.Error(); got != "duplicate node ID: 5" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("EmptyPeerAddress", func(t *testing.T) {
		e := &EmptyPeerAddressError{NodeID: 2}
		if got := e.Error(); got != "peer 2 has empty address" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("NodeNotInMembers", func(t *testing.T) {
		e := &NodeNotInMembersError{NodeID: 7}
		if got := e.Error(); got != "node_id 7 not found in initial_members" {
			t.Errorf("got %q", got)
		}
	})
}

func TestLeaderErrors(t *testing.T) {
	t.Run("LeaderNotFoundInPeersWithID", func(t *testing.T) {
		e := &LeaderNotFoundInPeersError{LeaderID: 3}
		want := "leader 3 not found in peers list"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("LeaderNotFoundInPeersNoID", func(t *testing.T) {
		e := &LeaderNotFoundInPeersError{}
		if got := e.Error(); got != "leader peer not found in peers list" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("LeaderMismatch", func(t *testing.T) {
		e := &LeaderMismatchError{Expected: 1, Observed: 2}
		want := "leader mismatch: expected 1, got 2"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("NoLeaderAvailable", func(t *testing.T) {
		e := &NoLeaderAvailableError{ShardID: 5}
		if got := e.Error(); got != "no leader available for shard 5" {
			t.Errorf("got %q", got)
		}
	})
}

func TestNoFollowersError(t *testing.T) {
	t.Run("WithContext", func(t *testing.T) {
		e := &NoFollowersError{Context: "in shard 1"}
		if got := e.Error(); got != "no followers found in shard 1" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("WithoutContext", func(t *testing.T) {
		e := &NoFollowersError{}
		if got := e.Error(); got != "no followers found" {
			t.Errorf("got %q", got)
		}
	})
}

func TestTimeoutWaitingError(t *testing.T) {
	cause := errors.New("ctx done")
	t.Run("WithCause", func(t *testing.T) {
		e := &TimeoutWaitingError{Subject: "leader", Cause: cause}
		want := "timeout waiting for leader: ctx done"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &TimeoutWaitingError{Subject: "consensus"}
		if got := e.Error(); got != "timeout waiting for consensus" {
			t.Errorf("got %q", got)
		}
	})
}

func TestContextCancelledError(t *testing.T) {
	cause := errors.New("cancelled")
	t.Run("WithCause", func(t *testing.T) {
		e := &ContextCancelledError{Operation: "propose", Cause: cause}
		want := "context cancelled while propose: cancelled"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &ContextCancelledError{Operation: "read"}
		if got := e.Error(); got != "context cancelled while read" {
			t.Errorf("got %q", got)
		}
	})
}

func TestRaftGroupErrors(t *testing.T) {
	t.Run("RaftGroupExists", func(t *testing.T) {
		e := &RaftGroupExistsError{ShardID: 1}
		if got := e.Error(); got != "raft group for shard 1 already exists" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("RaftGroupNotFound", func(t *testing.T) {
		e := &RaftGroupNotFoundError{ShardID: 2}
		if got := e.Error(); got != "raft group for shard 2 not found" {
			t.Errorf("got %q", got)
		}
	})
}

func TestConfigMultiValidationError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ConfigMultiValidationError{Errors: []string{"err1", "err2"}}
		want := "validation failed with 2 error(s):\n  - err1\n  - err2"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestInvalidWriteModeError(t *testing.T) {
	cause := errors.New("bad mode")
	t.Run("WithCause", func(t *testing.T) {
		e := &InvalidWriteModeError{Cause: cause}
		if got := e.Error(); got != "invalid write mode: bad mode" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithMode", func(t *testing.T) {
		e := &InvalidWriteModeError{Mode: "turbo"}
		if got := e.Error(); got != "invalid write mode: turbo" {
			t.Errorf("got %q", got)
		}
	})
}

func TestRaftCauseErrors(t *testing.T) {
	cause := errors.New("fail")
	tests := []struct {
		name    string
		errFn   func() error
		wantMsg string
	}{
		{"NodeHostCreation", func() error { return &NodeHostCreationError{Cause: cause} }, "failed to create node host: fail"},
		{"GroupConfigValidation", func() error { return &GroupConfigValidationError{Cause: cause} }, "invalid group config: fail"},
		{"RaftConfigCreation", func() error { return &RaftConfigCreationError{Cause: cause} }, "failed to create raft config: fail"},
		{"Session", func() error { return &SessionError{Cause: cause} }, "failed to get session: fail"},
		{"ReadOptionsValidation", func() error { return &ReadOptionsValidationError{Cause: cause} }, "invalid read options: fail"},
		{"Discovery", func() error { return &DiscoveryError{Cause: cause} }, "failed to discover peers: fail"},
		{"ClusterDetection", func() error { return &ClusterDetectionError{Cause: cause} }, "failed to detect existing cluster: fail"},
		{"RaftGroupStart", func() error { return &RaftGroupStartError{Cause: cause} }, "failed to start raft group: fail"},
		{"LeaderElection", func() error { return &LeaderElectionError{Cause: cause} }, "leader election failed: fail"},
		{"ClusterJoin", func() error { return &ClusterJoinError{Cause: cause} }, "failed to join cluster: fail"},
		{"ClientContextCanceled", func() error { return &ClientContextCanceledError{Cause: cause} }, "client context canceled: fail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			type unwrapper interface{ Unwrap() error }
			if uw, ok := e.(unwrapper); ok {
				if uw.Unwrap() != cause {
					t.Error("Unwrap failed")
				}
			}
		})
	}
}

func TestProposalResultError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ProposalResultError{ShardID: 1, Result: "rejected"}
		want := "rejected for shard 1"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestChannelNotRegisteredError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ChannelNotRegisteredError{NodeID: 42}
		if got := e.Error(); got != "no channel registered for node 42" {
			t.Errorf("got %q", got)
		}
	})
}

func TestFieldNotStringError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &FieldNotStringError{Field: "name", Actual: "int"}
		if got := e.Error(); got != "name field is not a string: got int" {
			t.Errorf("got %q", got)
		}
	})
}

func TestPeerNotFoundError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &PeerNotFoundError{NodeID: 10}
		if got := e.Error(); got != "peer with node_id 10 not found" {
			t.Errorf("got %q", got)
		}
	})
}

func TestEmptyNodeAddressError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &EmptyNodeAddressError{NodeID: 3}
		if got := e.Error(); got != "node_id 3 has empty address" {
			t.Errorf("got %q", got)
		}
	})
}

func TestRaftSimpleConstructors(t *testing.T) {
	tests := []struct {
		name     string
		newFn    func() *dberrors.DragonError
		wantCode dberrors.ErrorCode
	}{
		{"ResourceClosed", func() *dberrors.DragonError {
			return NewResourceClosedError("raft.op", "writer", dberrors.ErrUnavailable)
		}, dberrors.ErrUnavailable},
		{"Checksum", func() *dberrors.DragonError { return NewChecksumError("raft.verify", "backup") }, dberrors.ErrDataLoss},
		{"UnsupportedValue", func() *dberrors.DragonError {
			return NewUnsupportedValueError("raft.config", "format", "xml")
		}, dberrors.ErrInvalidInput},
		{"InvalidOpcode", func() *dberrors.DragonError {
			return NewInvalidOpcodeError("raft.decode", 0xAB, "batch")
		}, dberrors.ErrInvalidInput},
		{"IncompleteData", func() *dberrors.DragonError {
			return NewIncompleteDataError("raft.decode", "header")
		}, dberrors.ErrInvalidInput},
		{"DeadlineRequired", func() *dberrors.DragonError { return NewDeadlineRequiredError("raft.propose") }, dberrors.ErrInvalidInput},
		{"DeadlineExceeded", func() *dberrors.DragonError { return NewDeadlineExceededError("raft.propose") }, dberrors.ErrDeadlineExceeded},
		{"UnknownOperation", func() *dberrors.DragonError {
			return NewUnknownOperationError("raft.handle", "foobar")
		}, dberrors.ErrInvalidInput},
		{"InvalidContextType", func() *dberrors.DragonError {
			return NewInvalidContextTypeError("raft.update", "*raft.Context")
		}, dberrors.ErrInternal},
		{"Canceled", func() *dberrors.DragonError { return NewCanceledError("raft.op", "write") }, dberrors.ErrCanceled},
		{"ComponentNotInitialized", func() *dberrors.DragonError {
			return NewComponentNotInitializedError("raft.start", "raft")
		}, dberrors.ErrInternal},
		{"WriterClosed", func() *dberrors.DragonError { return NewWriterClosedError("raft.write") }, dberrors.ErrUnavailable},
		{"AlreadyBootstrapped", func() *dberrors.DragonError { return NewAlreadyBootstrappedError("raft.bootstrap") }, dberrors.ErrAlreadyExists},
		{"InvalidNodeIDZero", func() *dberrors.DragonError { return NewInvalidNodeIDZeroError("raft.validate") }, dberrors.ErrInvalidInput},
		{"InvalidNodeIDMembers", func() *dberrors.DragonError { return NewInvalidNodeIDMembersError("raft.validate") }, dberrors.ErrInvalidInput},
		{"MTLSConfig", func() *dberrors.DragonError { return NewMTLSConfigError("raft.tls") }, dberrors.ErrConfiguration},
		{"UnsupportedConsistencyLevel", func() *dberrors.DragonError {
			return NewUnsupportedConsistencyLevelError("raft.read", "eventual")
		}, dberrors.ErrInvalidInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}

func TestRaftFieldConstructors(t *testing.T) {
	cause := errors.New("fail")

	tests := []struct {
		name     string
		newFn    func() *dberrors.DragonError
		wantCode dberrors.ErrorCode
	}{
		{"ShardOperation", func() *dberrors.DragonError {
			return NewShardOperationError("raft.op", dberrors.ErrRaft, 1, "propose", cause)
		}, dberrors.ErrRaft},
		{"NodeOperation", func() *dberrors.DragonError {
			return NewNodeOperationError("raft.membership", 3, 1, "add", cause)
		}, dberrors.ErrRaft},
		{"LengthMismatch", func() *dberrors.DragonError {
			return NewLengthMismatchError("raft.batch", "keys", 5, "values", 3)
		}, dberrors.ErrInvalidInput},
		{"SnapshotOperation", func() *dberrors.DragonError {
			return NewSnapshotOperationError("raft.snapshot", "save", cause)
		}, dberrors.ErrSnapshot},
		{"BatchOperation", func() *dberrors.DragonError {
			return NewBatchOperationError("raft.batch", 0, "put", cause)
		}, dberrors.ErrInternal},
		{"InvalidQueryType", func() *dberrors.DragonError {
			return NewInvalidQueryTypeError("raft.query", "GetQuery", "PutQuery")
		}, dberrors.ErrInvalidInput},
		{"RequiredFieldOperation", func() *dberrors.DragonError {
			return NewRequiredFieldOperationError("raft.put", "key", "put")
		}, dberrors.ErrInvalidInput},
		{"InvalidEntityKey", func() *dberrors.DragonError {
			return NewInvalidEntityKeyError("raft.get", "get")
		}, dberrors.ErrInvalidInput},
		{"BatchFormat", func() *dberrors.DragonError {
			return NewBatchFormatError("raft.batch", "a list of operations")
		}, dberrors.ErrInvalidInput},
		{"InsufficientPeers", func() *dberrors.DragonError {
			return NewInsufficientPeersError("raft.bootstrap", dberrors.ErrInvalidInput, 3, 1)
		}, dberrors.ErrInvalidInput},
		{"DuplicateNodeID", func() *dberrors.DragonError {
			return NewDuplicateNodeIDError("raft.validate", 5)
		}, dberrors.ErrInvalidInput},
		{"EmptyPeerAddress", func() *dberrors.DragonError {
			return NewEmptyPeerAddressError("raft.validate", 2)
		}, dberrors.ErrInvalidInput},
		{"NodeNotInMembers", func() *dberrors.DragonError {
			return NewNodeNotInMembersError("raft.validate", 7)
		}, dberrors.ErrInvalidInput},
		{"LeaderNotFoundInPeers", func() *dberrors.DragonError {
			return NewLeaderNotFoundInPeersError("raft.forward", dberrors.ErrNotFound, 3)
		}, dberrors.ErrNotFound},
		{"LeaderMismatch", func() *dberrors.DragonError {
			return NewLeaderMismatchError("raft.verify", 1, 2)
		}, dberrors.ErrInternal},
		{"NoFollowers", func() *dberrors.DragonError {
			return NewNoFollowersError("raft.read", "in shard 1")
		}, dberrors.ErrInvalidInput},
		{"TimeoutWaiting", func() *dberrors.DragonError {
			return NewTimeoutWaitingError("raft.elect", "leader", cause)
		}, dberrors.ErrTimeout},
		{"ChannelNotRegistered", func() *dberrors.DragonError {
			return NewChannelNotRegisteredError("raft.send", 42)
		}, dberrors.ErrNotFound},
		{"ContextCancelled", func() *dberrors.DragonError {
			return NewContextCancelledError("raft.propose", "propose", cause)
		}, dberrors.ErrTimeout},
		{"ProposalResult", func() *dberrors.DragonError {
			return NewProposalResultError("raft.propose", dberrors.ErrInternal, 1, "rejected")
		}, dberrors.ErrInternal},
		{"RaftGroupExists", func() *dberrors.DragonError {
			return NewRaftGroupExistsError("raft.create", 1)
		}, dberrors.ErrAlreadyExists},
		{"RaftGroupNotFound", func() *dberrors.DragonError {
			return NewRaftGroupNotFoundError("raft.get", 2)
		}, dberrors.ErrNotFound},
		{"ConfigMultiValidation", func() *dberrors.DragonError {
			return NewConfigMultiValidationError("raft.validate", []string{"err1", "err2"})
		}, dberrors.ErrConfiguration},
		{"InvalidWriteMode", func() *dberrors.DragonError {
			return NewInvalidWriteModeError("raft.write", "turbo", cause)
		}, dberrors.ErrInvalidInput},
		{"NoLeaderAvailable", func() *dberrors.DragonError {
			return NewNoLeaderAvailableError("raft.forward", 5)
		}, dberrors.ErrLeaderNotAvailable},
		{"PeerNotFound", func() *dberrors.DragonError {
			return NewPeerNotFoundError("raft.get", 10)
		}, dberrors.ErrNotFound},
		{"EmptyNodeAddress", func() *dberrors.DragonError {
			return NewEmptyNodeAddressError("raft.validate", 3)
		}, dberrors.ErrInvalidInput},
		{"FieldNotString", func() *dberrors.DragonError {
			return NewFieldNotStringError("raft.parse", "name", "int")
		}, dberrors.ErrInvalidInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}

func TestRaftCauseConstructors(t *testing.T) {
	cause := errors.New("fail")

	tests := []struct {
		name     string
		newFn    func() *dberrors.DragonError
		wantCode dberrors.ErrorCode
	}{
		{"NodeHostCreation", func() *dberrors.DragonError { return NewNodeHostCreationError("raft.init", cause) }, dberrors.ErrRaft},
		{"MTLSCertRead", func() *dberrors.DragonError { return NewMTLSCertReadError("raft.tls", "cert.pem", cause) }, dberrors.ErrConfiguration},
		{"GroupConfigValidation", func() *dberrors.DragonError { return NewGroupConfigValidationError("raft.config", cause) }, dberrors.ErrValidation},
		{"RaftConfigCreation", func() *dberrors.DragonError { return NewRaftConfigCreationError("raft.config", cause) }, dberrors.ErrConfiguration},
		{"Session", func() *dberrors.DragonError { return NewSessionError("raft.session", cause) }, dberrors.ErrRaft},
		{"ReadOptionsValidation", func() *dberrors.DragonError { return NewReadOptionsValidationError("raft.read", cause) }, dberrors.ErrInvalidInput},
		{"Discovery", func() *dberrors.DragonError { return NewDiscoveryError("raft.discover", cause) }, dberrors.ErrUnavailable},
		{"ClusterDetection", func() *dberrors.DragonError { return NewClusterDetectionError("raft.detect", cause) }, dberrors.ErrInternal},
		{"RaftGroupStart", func() *dberrors.DragonError { return NewRaftGroupStartError("raft.start", cause) }, dberrors.ErrRaft},
		{"LeaderElection", func() *dberrors.DragonError { return NewLeaderElectionError("raft.elect", cause) }, dberrors.ErrLeaderNotAvailable},
		{"ClusterJoin", func() *dberrors.DragonError { return NewClusterJoinError("raft.join", cause) }, dberrors.ErrRaft},
		{"ClientContextCanceled", func() *dberrors.DragonError { return NewClientContextCanceledError("raft.client", cause) }, dberrors.ErrCanceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}

func TestMTLSCertReadError(t *testing.T) {
	cause := errors.New("permission denied")
	t.Run("Error", func(t *testing.T) {
		e := &MTLSCertReadError{File: "cert.pem", Cause: cause}
		want := "failed to read mTLS certificate file: cert.pem: permission denied"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &MTLSCertReadError{File: "cert.pem", Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap did not return cause")
		}
	})
}
